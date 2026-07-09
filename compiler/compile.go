// Package compiler compiles patterns into goresharp DFA blobs.
package compiler

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	goresharp "github.com/ieviev/go-resharp"
)

//go:embed internal/wasm/dfa_export.wasm
var exporterWASM []byte

// Flavor selects the input regex syntax.
type Flavor string

const (
	// FlavorResharp is RE# syntax.
	FlavorResharp Flavor = ""
	// FlavorRE2 is Go regexp syntax.
	FlavorRE2 Flavor = "re2"
	// FlavorRustRegex is Rust regex-crate syntax.
	FlavorRustRegex Flavor = "rust"
)

// CompileOptions controls compilation.
type CompileOptions struct {
	Unicode bool
	Flavor  Flavor
}

type compileRequest struct {
	Pattern string `json:"pattern"`
	ASCII   bool   `json:"ascii"`
	Flavor  Flavor `json:"flavor,omitempty"`
}

type compileError struct {
	Message string `json:"message"`
}

type Runtime struct {
	ctx          context.Context
	rt           wazero.Runtime
	mod          api.Module
	alloc        api.Function
	dealloc      api.Function
	compile      api.Function
	regexNew     api.Function
	regexFindAll api.Function
	regexFree    api.Function
	outPtr       api.Function
	outLen       api.Function
	errPtr       api.Function
	errLen       api.Function
}

func NewRuntime(ctx context.Context) (*Runtime, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	mod, err := rt.Instantiate(ctx, exporterWASM)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("instantiate go-resharp exporter wasm: %w", err)
	}
	r := &Runtime{
		ctx:          ctx,
		rt:           rt,
		mod:          mod,
		alloc:        mod.ExportedFunction("go_resharp_alloc"),
		dealloc:      mod.ExportedFunction("go_resharp_dealloc"),
		compile:      mod.ExportedFunction("go_resharp_compile"),
		regexNew:     mod.ExportedFunction("go_resharp_regex_new"),
		regexFindAll: mod.ExportedFunction("go_resharp_regex_find_all"),
		regexFree:    mod.ExportedFunction("go_resharp_regex_free"),
		outPtr:       mod.ExportedFunction("go_resharp_out_ptr"),
		outLen:       mod.ExportedFunction("go_resharp_out_len"),
		errPtr:       mod.ExportedFunction("go_resharp_error_ptr"),
		errLen:       mod.ExportedFunction("go_resharp_error_len"),
	}
	for name, fn := range map[string]api.Function{
		"go_resharp_alloc":          r.alloc,
		"go_resharp_dealloc":        r.dealloc,
		"go_resharp_compile":        r.compile,
		"go_resharp_regex_new":      r.regexNew,
		"go_resharp_regex_find_all": r.regexFindAll,
		"go_resharp_regex_free":     r.regexFree,
		"go_resharp_out_ptr":        r.outPtr,
		"go_resharp_out_len":        r.outLen,
		"go_resharp_error_ptr":      r.errPtr,
		"go_resharp_error_len":      r.errLen,
	} {
		if fn == nil {
			r.Close()
			return nil, fmt.Errorf("go-resharp exporter wasm missing export %q", name)
		}
	}
	return r, nil
}

func (r *Runtime) Close() {
	r.rt.Close(r.ctx)
}

func (r *Runtime) lastError() string {
	pr, err := r.errPtr.Call(r.ctx)
	if err != nil {
		return fmt.Sprintf("failed to read error pointer: %v", err)
	}
	lr, err := r.errLen.Call(r.ctx)
	if err != nil {
		return fmt.Sprintf("failed to read error length: %v", err)
	}
	ptr, ln := uint32(pr[0]), uint32(lr[0])
	if ln == 0 {
		return "unknown error"
	}
	buf, ok := r.mod.Memory().Read(ptr, ln)
	if !ok {
		return fmt.Sprintf("failed to read %d error bytes at %#x", ln, ptr)
	}
	var e compileError
	if err := json.Unmarshal(buf, &e); err != nil {
		return fmt.Sprintf("malformed exporter error %q: %v", string(buf), err)
	}
	if e.Message == "" {
		return fmt.Sprintf("empty exporter error %q", string(buf))
	}
	return e.Message
}

func (r *Runtime) Compile(pattern string, opts CompileOptions) ([]byte, error) {
	req, err := json.Marshal(compileRequest{Pattern: pattern, ASCII: !opts.Unicode, Flavor: opts.Flavor})
	if err != nil {
		return nil, fmt.Errorf("marshal compile request: %w", err)
	}
	return r.callBuf(r.compile, "go_resharp_compile", req)
}

func (r *Runtime) writeInput(buf []byte) (ptr uint32, free func(), err error) {
	inRes, err := r.alloc.Call(r.ctx, uint64(len(buf)))
	if err != nil {
		return 0, func() {}, fmt.Errorf("go_resharp_alloc: %w", err)
	}
	inPtr := uint32(inRes[0])
	if inPtr == 0 && len(buf) > 0 {
		return 0, func() {}, fmt.Errorf("go_resharp_alloc: out of memory")
	}
	free = func() {
		if inPtr != 0 {
			_, _ = r.dealloc.Call(r.ctx, uint64(inPtr), uint64(len(buf)))
		}
	}
	if !r.mod.Memory().Write(inPtr, buf) {
		free()
		return 0, func() {}, fmt.Errorf("go-resharp exporter: failed to write %d bytes at %#x", len(buf), inPtr)
	}
	return inPtr, free, nil
}

func (r *Runtime) readOut() ([]byte, error) {
	outPtrRes, err := r.outPtr.Call(r.ctx)
	if err != nil {
		return nil, fmt.Errorf("go_resharp_out_ptr: %w", err)
	}
	outLenRes, err := r.outLen.Call(r.ctx)
	if err != nil {
		return nil, fmt.Errorf("go_resharp_out_len: %w", err)
	}
	outPtr, outLen := uint32(outPtrRes[0]), uint32(outLenRes[0])
	out, ok := r.mod.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, fmt.Errorf("go-resharp exporter: failed to read %d output bytes at %#x", outLen, outPtr)
	}
	copied := make([]byte, len(out))
	copy(copied, out)
	return copied, nil
}

func (r *Runtime) callBuf(fn api.Function, fnName string, buf []byte) ([]byte, error) {
	inPtr, free, err := r.writeInput(buf)
	if err != nil {
		return nil, err
	}
	defer free()

	res, err := fn.Call(r.ctx, uint64(inPtr), uint64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fnName, err)
	}
	if int32(res[0]) != 0 {
		return nil, fmt.Errorf("go-resharp exporter: %s", r.lastError())
	}
	return r.readOut()
}

func (r *Runtime) CompileDFA(pattern string, opts CompileOptions) (*goresharp.DFA, error) {
	buf, err := r.Compile(pattern, opts)
	if err != nil {
		return nil, err
	}
	dfa, err := goresharp.Load(buf)
	if err != nil {
		return nil, fmt.Errorf("load compiled DFA: %w", err)
	}
	return dfa, nil
}

// FindAll compiles pattern and matches haystack inside the WASM runtime.
func (r *Runtime) FindAll(pattern string, opts CompileOptions, haystack []byte) ([]goresharp.Match, error) {
	re, err := r.NewRegex(pattern, opts)
	if err != nil {
		return nil, err
	}
	defer re.Close()
	return re.FindAll(haystack)
}

// Regex is a pattern compiled inside the WASM runtime.
type Regex struct {
	r      *Runtime
	handle int32
	closed bool
}

// NewRegex compiles pattern once for repeated matching.
func (r *Runtime) NewRegex(pattern string, opts CompileOptions) (*Regex, error) {
	header, err := json.Marshal(compileRequest{Pattern: pattern, ASCII: !opts.Unicode, Flavor: opts.Flavor})
	if err != nil {
		return nil, fmt.Errorf("marshal regex_new request: %w", err)
	}
	inPtr, free, err := r.writeInput(header)
	if err != nil {
		return nil, err
	}
	defer free()

	res, err := r.regexNew.Call(r.ctx, uint64(inPtr), uint64(len(header)))
	if err != nil {
		return nil, fmt.Errorf("go_resharp_regex_new: %w", err)
	}
	handle := int32(res[0])
	if handle < 0 {
		return nil, fmt.Errorf("go-resharp exporter: %s", r.lastError())
	}
	return &Regex{r: r, handle: handle}, nil
}

// FindAll matches haystack against the compiled pattern.
func (g *Regex) FindAll(haystack []byte) ([]goresharp.Match, error) {
	if g.closed {
		return nil, fmt.Errorf("go-resharp exporter: FindAll called on a closed Regex")
	}
	inPtr, free, err := g.r.writeInput(haystack)
	if err != nil {
		return nil, err
	}
	defer free()

	res, err := g.r.regexFindAll.Call(g.r.ctx, uint64(uint32(g.handle)), uint64(inPtr), uint64(len(haystack)))
	if err != nil {
		return nil, fmt.Errorf("go_resharp_regex_find_all: %w", err)
	}
	if int32(res[0]) != 0 {
		return nil, fmt.Errorf("go-resharp exporter: %s", g.r.lastError())
	}
	out, err := g.r.readOut()
	if err != nil {
		return nil, err
	}
	return decodeMatches(out)
}

// Close frees the compiled pattern.
func (g *Regex) Close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	res, err := g.r.regexFree.Call(g.r.ctx, uint64(uint32(g.handle)))
	if err != nil {
		return fmt.Errorf("go_resharp_regex_free: %w", err)
	}
	if int32(res[0]) != 0 {
		return fmt.Errorf("go-resharp exporter: %s", g.r.lastError())
	}
	return nil
}

func decodeMatches(out []byte) ([]goresharp.Match, error) {
	if len(out) < 4 {
		return nil, fmt.Errorf("go-resharp exporter: find_all output shorter than count prefix")
	}
	count := binary.LittleEndian.Uint32(out[:4])
	body := out[4:]
	if uint64(len(body)) != uint64(count)*8 {
		return nil, fmt.Errorf("go-resharp exporter: find_all output has %d bytes for %d matches", len(body), count)
	}
	matches := make([]goresharp.Match, count)
	for i := range matches {
		matches[i].Start = int(binary.LittleEndian.Uint32(body[i*8:]))
		matches[i].End = int(binary.LittleEndian.Uint32(body[i*8+4:]))
	}
	return matches, nil
}

func Compile(ctx context.Context, pattern string, opts CompileOptions) ([]byte, error) {
	r, err := NewRuntime(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Compile(pattern, opts)
}

func CompileDFA(ctx context.Context, pattern string, opts CompileOptions) (*goresharp.DFA, error) {
	r, err := NewRuntime(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.CompileDFA(pattern, opts)
}

var sharedRuntime = struct {
	sync.Mutex
	runtime *Runtime
}{}

func sharedRuntimeLocked(ctx context.Context) (*Runtime, error) {
	if sharedRuntime.runtime == nil {
		r, err := NewRuntime(ctx)
		if err != nil {
			return nil, err
		}
		sharedRuntime.runtime = r
	}
	return sharedRuntime.runtime, nil
}

func CompileDFAShared(ctx context.Context, pattern string, opts CompileOptions) (*goresharp.DFA, error) {
	sharedRuntime.Lock()
	defer sharedRuntime.Unlock()
	r, err := sharedRuntimeLocked(ctx)
	if err != nil {
		return nil, err
	}
	return r.CompileDFA(pattern, opts)
}

func FindAllShared(ctx context.Context, pattern string, opts CompileOptions, haystack []byte) ([]goresharp.Match, error) {
	sharedRuntime.Lock()
	defer sharedRuntime.Unlock()
	r, err := sharedRuntimeLocked(ctx)
	if err != nil {
		return nil, err
	}
	return r.FindAll(pattern, opts, haystack)
}

// NewRegexShared compiles pattern once with the shared runtime.
func NewRegexShared(ctx context.Context, pattern string, opts CompileOptions) (*Regex, error) {
	sharedRuntime.Lock()
	defer sharedRuntime.Unlock()
	r, err := sharedRuntimeLocked(ctx)
	if err != nil {
		return nil, err
	}
	return r.NewRegex(pattern, opts)
}
