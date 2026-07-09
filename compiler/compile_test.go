package compiler

import (
	"context"
	"testing"

	goresharp "github.com/ieviev/go-resharp"
)

func TestCompileDFA(t *testing.T) {
	dfa, err := CompileDFA(context.Background(), `api(?s:.){0,}secret`, CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFA: %v", err)
	}
	data := []byte("x api y secret z")
	got := dfa.FindAll(data)
	want := []goresharp.Match{{Start: 2, End: 14}}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestCompileDFAShared(t *testing.T) {
	dfa, err := CompileDFAShared(context.Background(), `api(?s:.){0,}secret`, CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFAShared: %v", err)
	}
	got := dfa.FindAll([]byte("x api y secret z"))
	want := []goresharp.Match{{Start: 2, End: 14}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %#v, want %#v", got, want)
	}

	dfa2, err := CompileDFAShared(context.Background(), `foo(?s:.){0,}bar`, CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFAShared (2nd call): %v", err)
	}
	got2 := dfa2.FindAll([]byte("x foo y bar z"))
	if len(got2) != 1 {
		t.Fatalf("2nd call: got %#v", got2)
	}
}

func TestNewRegexReusesCompiledPattern(t *testing.T) {
	re, err := NewRegexShared(context.Background(), `api(?s:.){0,}secret`, CompileOptions{})
	if err != nil {
		t.Fatalf("NewRegexShared: %v", err)
	}
	defer re.Close()

	for i, data := range [][]byte{
		[]byte("x api y secret z"),
		[]byte("no match here"),
		[]byte("api a secret, api b secret"),
	} {
		got, err := re.FindAll(data)
		if err != nil {
			t.Fatalf("call %d: FindAll: %v", i, err)
		}
		if i == 2 && len(got) != 1 {
			t.Fatalf("call %d: got %#v, want 1 match (greedy .{0,} spans to the last secret)", i, got)
		}
	}

	if err := re.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := re.FindAll([]byte("x")); err == nil {
		t.Fatal("expected error calling FindAll on a closed Regex")
	}
}

func TestCompileRejectsEmptyPattern(t *testing.T) {
	_, err := Compile(context.Background(), ``, CompileOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFindAllMatchesDFA(t *testing.T) {
	pat := `api(?s:.){0,}secret`
	data := []byte("x api y secret z")

	dfa, err := CompileDFAShared(context.Background(), pat, CompileOptions{})
	if err != nil {
		t.Fatalf("CompileDFAShared: %v", err)
	}
	want := dfa.FindAll(data)

	got, err := FindAllShared(context.Background(), pat, CompileOptions{}, data)
	if err != nil {
		t.Fatalf("FindAllShared: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestFindAllHandlesPatternsCompileRejects(t *testing.T) {
	pat := `(?<!secret)api[a-z]+`
	data := []byte("secretapikey and apikey and apix")

	if _, err := CompileDFAShared(context.Background(), pat, CompileOptions{}); err == nil {
		t.Fatal("expected CompileDFAShared to reject a negative lookbehind pattern")
	}

	got, err := FindAllShared(context.Background(), pat, CompileOptions{}, data)
	if err != nil {
		t.Fatalf("FindAllShared: %v", err)
	}
	want := []goresharp.Match{{Start: 17, End: 23}, {Start: 28, End: 32}}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
