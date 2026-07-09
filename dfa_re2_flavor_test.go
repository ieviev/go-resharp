package goresharp_test

import (
	"context"
	"math/rand"
	"regexp"
	"testing"

	"github.com/ieviev/go-resharp/compiler"
)

func TestRE2FlavorMatchesStdlib(t *testing.T) {
	cases := []string{
		`my_var[a-z]+`,
		`a&b|c~d`,
		`user_[0-9]+_id`,
		`\Qa.b*c\E[0-9]+`,
		`(?P<foo_bar>[a-z]+)_[0-9]+`,
	}
	vocab := []string{
		"my_varabc", "a&b", "c~d", "user_123_id", "a.b*c99", "foo_x", "bar_1",
		" filler ", "\n",
	}
	rng := rand.New(rand.NewSource(7))
	for _, pat := range cases {
		pat := pat
		t.Run(pat, func(t *testing.T) {
			dfa, err := compiler.CompileDFAShared(context.Background(), pat, compiler.CompileOptions{Flavor: compiler.FlavorRE2})
			if err != nil {
				t.Fatalf("CompileDFAShared: %v", err)
			}
			re := regexp.MustCompile(pat)
			for trial := 0; trial < 200; trial++ {
				n := 3 + rng.Intn(5)
				var data []byte
				for i := 0; i < n; i++ {
					data = append(data, vocab[rng.Intn(len(vocab))]...)
					data = append(data, ' ')
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

func TestRE2FlavorRejectsLazyQuantifiers(t *testing.T) {
	_, err := compiler.CompileDFAShared(context.Background(), `a*?`, compiler.CompileOptions{Flavor: compiler.FlavorRE2})
	if err == nil {
		t.Fatal("expected an error for a lazy quantifier under FlavorRE2")
	}
}

func TestRustRegexFlavorMatchesStdlib(t *testing.T) {
	pat := `my_var[a-z]+_[0-9]+`
	dfa, err := compiler.CompileDFAShared(context.Background(), pat, compiler.CompileOptions{Flavor: compiler.FlavorRustRegex})
	if err != nil {
		t.Fatalf("CompileDFAShared: %v", err)
	}
	re := regexp.MustCompile(pat)
	data := []byte("see my_varabc_123 there")
	got := dfa.FindAll(data)
	want := re.FindAllIndex(data, -1)
	if len(got) != len(want) || got[0].Start != want[0][0] || got[0].End != want[0][1] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
