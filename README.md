# go-resharp

> **Note:** Experimental. Open an issue or PR if you hit something.

Go bindings for [resharp](https://github.com/ieviev/resharp), a fast
automata-based regex engine written in Rust.

Two packages:

- **`github.com/ieviev/go-resharp`**: pure Go, matches against an
  already-compiled regex. No WASM, no wazero, no cgo.
- **`github.com/ieviev/go-resharp/compiler`**: compiles a regex pattern
  by calling resharp through WASM ([wazero](https://github.com/tetratelabs/wazero)).
  Needed either for matching directly in WASM, or for a build step
  that precompiles a pattern ahead of time. See "Precompile and embed
  a DFA" below.

## Usage

```go
import (
    "context"
    "log"

    "github.com/ieviev/go-resharp/compiler"
)

dfa, err := compiler.CompileDFA(context.Background(), `api(?s:.){0,}secret`, compiler.CompileOptions{})
if err != nil {
    log.Fatal(err)
}
matches := dfa.FindAll([]byte("x api y secret z"))
```

`CompileOptions{}` is the default: ASCII mode, RE# pattern syntax. We
also support a subset of RE2 and Rust-regex syntax, so patterns don't
need to be hand-ported to RE#. Some options to know about:

- `Unicode: true`: full Unicode character classes instead of ASCII.
- `Flavor: compiler.FlavorRE2`: write the pattern in RE2 syntax, the
  same syntax Go's own `regexp` package uses.
- `Flavor: compiler.FlavorRustRegex`: write the pattern in the Rust
  `regex` crate's syntax.

See "Pattern syntax flavors" below.

Spinning up a new WASM instance per call is expensive; for hot paths,
reuse a `compiler.Runtime` (`NewRuntime`/`Compile`/`CompileDFA`) or
the process-wide shared one (`CompileShared`/`CompileDFAShared`), and
reuse a `goresharp.Scratch` across `FindAllInto`/
`FindAllOverlappingInto` calls to avoid allocating per search.

## Precompile and embed a DFA (zero dependencies)

Compilation and matching are separate packages, so a binary that only
imports the root package links zero wazero/WASM code. That gives you
a fast, dependency-free regex matcher with no cgo and no embedded WASM
runtime, at the cost of compiling patterns ahead of time as a build
step.

Compile a pattern to a `.dfa` blob with the `go-resharp-dump` CLI:

```sh
go run ./cmd/go-resharp-dump --out akia.dfa 'AKIA[0-9A-Z]{16}'
```

Then embed and load it with only the root package:

```go
package main

import (
    _ "embed"

    goresharp "github.com/ieviev/go-resharp"
)

//go:embed akia.dfa
var akiaBlob []byte

func main() {
    dfa, err := goresharp.Load(akiaBlob)
    if err != nil {
        panic(err)
    }
    matches := dfa.FindAll([]byte("AKIA of dogs and AKIAABCDEFGHIJKLMNOP cats"))
    _ = matches
}
```

This binary has no `compiler` import, so `go build` never touches
wazero or the embedded exporter WASM module at all.

`DFA.Tables()`/`DFA.LiteralPrefix()` also expose the decoded fwd/rev
tables (or literal-prefix data) of an already-loaded DFA, for
generating Go code with the tables baked in as an alternative to
shipping a `.dfa` file.

## Pattern syntax flavors

resharp's own syntax (RE#, see
[resharp/docs/syntax.md](https://github.com/ieviev/resharp/blob/main/docs/syntax.md))
is standard regex syntax, including lookarounds compiled
directly into the automaton, plus three extra meta characters: `&`
(intersection), `~` (complement), and `_` (any-byte wildcard).
To write a pattern in another syntax without hand-porting it to RE#,
set `CompileOptions.Flavor`:

- `compiler.FlavorResharp` (default): pattern is already RE# syntax.
- `compiler.FlavorRE2`: pattern is RE2 syntax, the same syntax Go's
  own `regexp` package implements, so anything that already compiles
  with `regexp.MustCompile` compiles here unchanged. No manual
  escaping of `_`/`&`/`~` needed.
- `compiler.FlavorRustRegex`: pattern is Rust `regex`-crate syntax
  (very close to RE2 but not byte-identical).

The translators (`internal/exporter/src/flavors/{translate,re2}.rs`)
rewrite the pattern text before compiling: escaping RE#-only meta
characters that are ordinary literals in the other grammars, expanding
`\Q...\E` literal spans, and rewriting octal escapes to resharp's
`\x{...}` hex form. Lazy quantifiers (`*?`, `+?`, `??`) are a hard
compile error under these flavors: resharp is a leftmost-longest
engine with no shortest-match mode, so there's no greedy equivalent
to lower them to. Rewrite with RE#'s complement (`~`) instead:
`<div>.*?</div>` becomes `<div>~(_*</div>_*)</div>`.
See [resharp/docs/syntax.md](https://github.com/ieviev/resharp/blob/main/docs/syntax.md).

If you don't need flavor translation, `compiler.QuoteMeta` escapes a
literal for safe inclusion in a resharp pattern (e.g. to build an
alternation of literals with `strings.Join`).

## Matching via WASM (reference matcher)

`compiler.NewRegex`/`NewRegexShared` compile a pattern once inside the
WASM sandbox and return a `Regex` whose `FindAll` calls resharp's own
`find_all` directly. This is mainly useful for two things:
cross-checking the compiled-DFA fast path in tests, and matching
patterns the Go DFA export can't handle:

```go
re, err := compiler.NewRegexShared(ctx, `(?<!secret)api[a-z]+`, compiler.CompileOptions{})
// ...
matches, err := re.FindAll(haystack)
```

`compiler.CompileDFA` on the same pattern returns an error instead:
this repo's Go-side DFA matcher (`dfa.go`) only implements a subset
of resharp's `find_all` strategies - the plain forward/reverse DFA
and the literal-prefix case - and a lookbehind-driven pattern makes
resharp's own compile step pick a different strategy that this
subset doesn't cover. `compiler.FindAllShared` is a shorthand for
one-off matches: it wraps `NewRegexShared`, one `FindAll` call, and
`Close`, so it recompiles the pattern on every call.

## Benchmarks

`go test -bench . -benchtime=1s ./...` on an AMD Ryzen 7 5800X,
comparing go-resharp against [RE2](https://github.com/wasilibs/go-re2)
(no-cgo WASM build) and Go's stdlib `regexp`. Compile time is excluded
for all engines (each compiles once, then the timer resets). See
[`dfa_bench_test.go`](dfa_bench_test.go) for the exact patterns, test
data, and engine setup.

| Benchmark          | go-resharp            | go-resharp (WASM)  | RE2 (WASM)          | stdlib regexp        |
|--------------------|----------------------:|--------------------:|---------------------:|---------------------:|
| Literal            | **18991 MB/s (1x)**   | 4060 MB/s (4.7x)     | 870 MB/s (21.8x)      | 1659 MB/s (11.4x)     |
| LiteralAlternation  | **439 MB/s (1x)**     | 1130 MB/s (0.4x)     | 148 MB/s (3.0x)       | 17.0 MB/s (25.7x)     |
| BoundedRepeat       | **268 MB/s (1x)**     | 150 MB/s (1.8x)      | 57.3 MB/s (4.7x)      | 21.6 MB/s (12.4x)     |
| URL                 | **969 MB/s (1x)**     | 657 MB/s (1.5x)      | 184 MB/s (5.3x)       | 137 MB/s (7.1x)       |
| Dictionary100       | **101 MB/s (1x)**     | 55.6 MB/s (1.8x)     | 13.0 MB/s (7.8x)      | 0.64 MB/s (157x)      |
| APIKey              | **1788 MB/s (1x)**    | 710 MB/s (2.5x)      | 192 MB/s (9.3x)       | 210 MB/s (8.5x)       |

go-resharp (WASM) beats go-resharp itself on `LiteralAlternation`: for
this pattern resharp picks a SIMD multi-substring prefix, but Go's
runtime only ports the plain literal-prefix case, so it walks the
DFA byte by byte.

## Building the WASM exporter

`compiler/internal/wasm/dfa_export.wasm` is checked in prebuilt. Rebuild it
from `internal/exporter` with `scripts/build_wasm.sh` (requires a
`wasm32-unknown-unknown` Rust toolchain), or by hand:

```sh
cd internal/exporter
RUSTFLAGS='-C target-feature=+simd128' cargo build --release --target wasm32-unknown-unknown
cp target/wasm32-unknown-unknown/release/go_resharp_exporter.wasm ../../compiler/internal/wasm/dfa_export.wasm
```

The `+simd128` target feature is required: resharp's WASM SIMD
substring search only compiles for `wasm32-unknown-unknown` behind
`target_feature = "simd128"`, and the exporter enables prefix
acceleration so literal-prefixed patterns use it.
[wazero](https://github.com/tetratelabs/wazero) runs the resulting
SIMD opcodes fine.

Go only implements a subset of resharp's runtime acceleration
mechanisms: string literal prefixes. Patterns that need one of
resharp's other mechanisms fall back to a fresh recompile with prefix
acceleration disabled, using the plain forward+reverse DFA format. See
`internal/exporter/src/lib.rs` for how the exporter decides which case
applies.
