package goresharp_test

import (
	"bytes"
	"context"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	re2 "github.com/wasilibs/go-re2"

	"github.com/ieviev/go-resharp/compiler"
)

var benchVocabulary = strings.Fields(`
the of and to in a is that for it as was with be by on not he
have this are or his from at which but not have had were they
we her she an will would there their what so up out if about who
get which go me when make can like time no just him know take
person into year your good some could them see other than then
now look only come its over think also back after use two how
our work first well way even new want because any these give day
computer network system software hardware database internet server
protocol interface algorithm function variable structure repository
sherlock holmes watson moriarty adler baskerville hound reichenbach
elementary deduction mystery detective london baker street pipe
violin telegram inspector lestrade scotland yard mycroft irene
adventure scandal bohemia speckled band engineer thumb blue carbuncle
`)

var benchURLs = []string{
	"https://example.com/path?x=1&y=2",
	"http://sub.example.org/a/b/c#frag",
	"https://docs.example.net/reference/index.html",
	"http://api.example.io/v1/resource?id=42",
}

func benchCorpus(n int) []byte {
	r := rand.New(rand.NewSource(1))
	phrases := []string{"Sherlock Holmes", "Doctor Watson", "Irene Adler", "Professor Moriarty"}
	var b bytes.Buffer
	for b.Len() < n {
		switch r.Intn(25) {
		case 0:
			b.WriteString(phrases[r.Intn(len(phrases))])
		case 1:
			b.WriteString(benchURLs[r.Intn(len(benchURLs))])
		default:
			b.WriteString(benchVocabulary[r.Intn(len(benchVocabulary))])
		}
		if r.Intn(15) == 0 {
			b.WriteByte('\n')
		} else {
			b.WriteByte(' ')
		}
	}
	return b.Bytes()
}

func benchDictionaryPattern(n int) string {
	seen := make(map[string]bool, n)
	var words []string
	for _, w := range benchVocabulary {
		if seen[w] {
			continue
		}
		seen[w] = true
		words = append(words, w)
		if len(words) == n {
			break
		}
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = compiler.QuoteMeta(w)
	}
	return strings.Join(quoted, "|")
}

const benchCorpusSize = 4 << 20

var (
	benchLiteralPattern     = "Sherlock Holmes"
	benchAlternationPattern = "Sherlock|Holmes|Watson|Irene|Adler"
	benchBoundedRepeatPat   = `[A-Za-z]{8,13}`
	benchURLPattern         = `https?://[a-zA-Z0-9./_?&=#%-]+`
	benchAPIKeyPattern      = `AKIA[0-9A-Z]{16}`
)

var benchSecretTokens = []string{
	"AKIAABCDEF1234567890", "AKIAZZZZZZZZZZZZZZZZ", "AKIA0123456789ABCDEF",
}

func benchAPIKeyCorpus(n int) []byte {
	r := rand.New(rand.NewSource(2))
	var b bytes.Buffer
	for b.Len() < n {
		switch r.Intn(20) {
		case 0:
			b.WriteString(benchSecretTokens[r.Intn(len(benchSecretTokens))])
		default:
			b.WriteString(benchVocabulary[r.Intn(len(benchVocabulary))])
		}
		b.WriteByte(' ')
	}
	return b.Bytes()
}

type engine struct {
	name    string
	compile func(pattern string) (findAll func(data []byte) int, err error)
}

var engines = []engine{
	{
		name: "GoResharp",
		compile: func(pattern string) (func([]byte) int, error) {
			dfa, err := compiler.CompileDFA(context.Background(), pattern, compiler.CompileOptions{})
			if err != nil {
				return nil, err
			}
			return func(data []byte) int { return len(dfa.FindAll(data)) }, nil
		},
	},
	{
		name: "GoResharpWASM",
		compile: func(pattern string) (func([]byte) int, error) {
			re, err := compiler.NewRegexShared(context.Background(), pattern, compiler.CompileOptions{})
			if err != nil {
				return nil, err
			}
			return func(data []byte) int {
				matches, err := re.FindAll(data)
				if err != nil {
					panic(err)
				}
				return len(matches)
			}, nil
		},
	},
	{
		name: "RE2WASM",
		compile: func(pattern string) (func([]byte) int, error) {
			re, err := re2.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return func(data []byte) int { return len(re.FindAllIndex(data, -1)) }, nil
		},
	},
	{
		name: "StdlibRegexp",
		compile: func(pattern string) (func([]byte) int, error) {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			return func(data []byte) int { return len(re.FindAllIndex(data, -1)) }, nil
		},
	},
}

func benchmarkPattern(b *testing.B, pattern string, data []byte) {
	for _, eng := range engines {
		b.Run(eng.name, func(b *testing.B) {
			findAll, err := eng.compile(pattern)
			if err != nil {
				b.Fatalf("%s: compile %q: %v", eng.name, pattern, err)
			}
			b.ResetTimer()
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				_ = findAll(data)
			}
		})
	}
}

func BenchmarkLiteral(b *testing.B) {
	benchmarkPattern(b, benchLiteralPattern, benchCorpus(benchCorpusSize))
}

func BenchmarkLiteralAlternation(b *testing.B) {
	benchmarkPattern(b, benchAlternationPattern, benchCorpus(benchCorpusSize))
}

func BenchmarkBoundedRepeat(b *testing.B) {
	benchmarkPattern(b, benchBoundedRepeatPat, benchCorpus(benchCorpusSize))
}

func BenchmarkURL(b *testing.B) {
	benchmarkPattern(b, benchURLPattern, benchCorpus(benchCorpusSize))
}

func BenchmarkDictionary100(b *testing.B) {
	benchmarkPattern(b, benchDictionaryPattern(100), benchCorpus(benchCorpusSize))
}

func BenchmarkAPIKey(b *testing.B) {
	benchmarkPattern(b, benchAPIKeyPattern, benchAPIKeyCorpus(benchCorpusSize))
}
