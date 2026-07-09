package goresharp_test

import (
	"context"
	"math/rand"
	"regexp"
	"testing"
	"time"

	goresharp "github.com/ieviev/go-resharp"
	"github.com/ieviev/go-resharp/compiler"
)

func TestLiteralPrefixExact(t *testing.T) {
	dfa, err := compiler.CompileDFAShared(context.Background(), "hello world", compiler.CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFAShared: %v", err)
	}
	if dfa.LiteralPrefix() == nil || !dfa.LiteralPrefix().Exact {
		t.Fatalf("expected an exact literal-prefix DFA, got %#v", dfa.LiteralPrefix())
	}
	data := []byte("say hello world to hello world again")
	got := dfa.FindAll(data)
	want := []goresharp.Match{{Start: 4, End: 15}, {Start: 19, End: 30}}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestLiteralPrefixOverlappingUnsupported(t *testing.T) {
	dfa, err := compiler.CompileDFAShared(context.Background(), "AKIA[0-9A-Z]{16}", compiler.CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFAShared: %v", err)
	}
	if dfa.LiteralPrefix() == nil {
		t.Fatalf("expected a literal-prefix DFA")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected FindAllOverlapping to panic on a literal-prefix DFA")
		}
	}()
	dfa.FindAllOverlapping([]byte("AKIAABCDEFGHIJKLMNOP"))
}

func TestLiteralPrefixMatchesStdlib(t *testing.T) {
	cases := []string{
		"apisecret1234567890",
		"AKIA[0-9A-Z]{16}",
		"ghp_[0-9a-zA-Z]{36}",
		"-----BEGIN (RSA|EC|DSA|OPENSSH) PRIVATE KEY-----",
		"https?://[a-zA-Z0-9./_?&=#%-]+",
	}
	vocab := []string{
		"AKIAABCDEF1234567890", "ghp_", "abcdefghijklmnopqrstuvwxyz0123456789",
		"http://example.com/path?a=1&b=2", "https://x.y/z",
		"-----BEGIN RSA PRIVATE KEY-----", "apisecret1234567890",
		" some filler text around ", "AKIAZZZZZZZZZZZZZZZZ", "\n\n",
	}
	runLiteralPrefixFuzz(t, cases, vocab, true)
}

func TestRealWorldSecretPatterns(t *testing.T) {
	cases := []string{
		`AIza[0-9A-Za-z\-_]{35}`,
		`xox[baprs]-([0-9a-zA-Z]{10,48})`,
		`sk_live_[0-9a-zA-Z]{24,}`,
		`[a-f0-9]{32}`,
		`[a-f0-9]{64}`,
		`(?i)password\s*=\s*['"][^'"]{4,}['"]`,
		`[\w.+-]+@[\w-]+\.[a-zA-Z]{2,}`,                     // Dfa/AnchoredRev
		`\b(?:\d{1,3}\.){3}\d{1,3}\b`,                       // FwdLbPrefix/Teddy
		`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`, // JWT, Dfa/AnchoredRev
		`postgres://[^\s]+`,
		`mongodb(\+srv)?://[^\s]+`,
		`-----BEGIN CERTIFICATE-----`,
		`glpat-[0-9a-zA-Z\-_]{20}`,
		`npm_[0-9a-zA-Z]{36}`,
		`xoxb-[0-9]{10,12}-[0-9]{10,12}-[0-9a-zA-Z]{24}`,
	}
	vocab := []string{
		"AIzaSyABCDEFGHIJKLMNOPQRSTUVWXYZ0123456", "xoxb-1234567890-1234567890-abcdefghijklmnopqrstuvwx",
		"sk_live_ABCDEFGHIJKLMNOPQRSTUVWX", "deadbeefdeadbeefdeadbeefdeadbeef",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"password = 'sup3rSecret!'", `PASSWORD="another one"`,
		"user@example.com", "192.168.0.1", "postgres://user:pass@host:5432/db",
		"mongodb+srv://user:pass@cluster.mongodb.net/db", "mongodb://localhost:27017",
		"-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
		"glpat-ABCDEFGHIJKLMNOPQRST", "npm_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ab",
		" filler text around things ", "\n\n", "12.34.56.78 is an ip",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	}
	runLiteralPrefixFuzz(t, cases, vocab, false)
}

func TestBoundedRepeatCompilesFast(t *testing.T) {
	cases := []string{
		`\w{3}`,
		`[A-Za-z]{8,13}`,
		`[0-9]{3}-[0-9]{2}-[0-9]{4}`,
		`[0-9a-zA-Z]{20,64}`,
	}
	for _, pat := range cases {
		pat := pat
		t.Run(pat, func(t *testing.T) {
			start := time.Now()
			dfa, err := compiler.CompileDFAShared(context.Background(), pat, compiler.CompileOptions{})
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("compile took %v, want well under a second (regressed to the pre-fix BDFA blowup?)", elapsed)
			}
			if err != nil {
				t.Fatalf("CompileDFAShared: %v", err)
			}
			if dfa.LiteralPrefix() != nil {
				t.Fatalf("pattern %q unexpectedly selected the literal-prefix path", pat)
			}
		})
	}
}

func runLiteralPrefixFuzz(t *testing.T, cases, vocab []string, requireLiteralPrefix bool) {
	rng := rand.New(rand.NewSource(42))
	for _, pat := range cases {
		pat := pat
		t.Run(pat, func(t *testing.T) {
			dfa, err := compiler.CompileDFAShared(context.Background(), pat, compiler.CompileOptions{})
			if err != nil {
				t.Fatalf("CompileDFAShared: %v", err)
			}
			if requireLiteralPrefix && dfa.LiteralPrefix() == nil {
				t.Fatalf("pattern %q did not select the literal-prefix path (test no longer exercises it)", pat)
			}
			re := regexp.MustCompile(pat)
			for trial := 0; trial < 200; trial++ {
				n := 3 + rng.Intn(6)
				var data []byte
				for i := 0; i < n; i++ {
					data = append(data, vocab[rng.Intn(len(vocab))]...)
				}
				got := dfa.FindAll(data)
				want := re.FindAllIndex(data, -1)
				if len(got) != len(want) {
					t.Fatalf("trial %d: got %d matches %v, want %d %v, data=%q", trial, len(got), got, len(want), want, data)
				}
				for i := range want {
					if got[i].Start != want[i][0] || got[i].End != want[i][1] {
						t.Fatalf("trial %d match %d: got %#v, want [%d,%d), data=%q", trial, i, got[i], want[i][0], want[i][1], data)
					}
				}
			}
		})
	}
}
