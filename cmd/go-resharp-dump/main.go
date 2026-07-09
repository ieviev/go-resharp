package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ieviev/go-resharp/compiler"
)

func main() {
	unicode := flag.Bool("unicode", false, "compile with full Unicode character classes instead of the default ASCII mode")
	flavor := flag.String("flavor", "", `pattern syntax: "" (RE#, default), "re2" (RE2/Go stdlib regexp), or "rust" (Rust regex-crate)`)
	out := flag.String("out", "", "output DFA blob path")
	flag.Parse()
	if flag.NArg() != 1 {
		panic("usage: go-resharp-dump [--unicode] [--flavor re2|rust] --out <path> <pattern>")
	}
	if *out == "" {
		panic("--out is required")
	}
	buf, err := compiler.Compile(context.Background(), flag.Arg(0), compiler.CompileOptions{Unicode: *unicode, Flavor: compiler.Flavor(*flavor)})
	if err != nil {
		panic(fmt.Sprintf("compile: %v", err))
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		panic(fmt.Sprintf("write %s: %v", *out, err))
	}
}
