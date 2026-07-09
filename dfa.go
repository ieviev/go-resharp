// Package goresharp reads and matches resharp DFA blobs.
package goresharp

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	dfaDead = 1
	noMatch = -1
)

const (
	formatPlain   = 0
	formatLiteral = 1
)

const (
	maskCenter = 0b001
	maskEnd    = 0b100
)

type Effect struct {
	Mask byte
	Rel  uint32
}

type LDFA struct {
	MtLog    uint32
	MtLookup [256]byte
	Begin    []uint16
	Center   []uint16
	Effects  [][]Effect
}

func (l *LDFA) mt(b byte) uint32 {
	return uint32(l.MtLookup[b])
}

func (l *LDFA) delta(state uint16, mt uint32) uint32 {
	return (uint32(state) << l.MtLog) | mt
}

func readUint16Slice(buf []byte, label string) ([]uint16, []byte, error) {
	if len(buf) < 4 {
		return nil, nil, fmt.Errorf("truncated: %s len", label)
	}
	n := binary.LittleEndian.Uint32(buf)
	buf = buf[4:]
	if uint32(len(buf)) < 2*n {
		return nil, nil, fmt.Errorf("truncated: %s", label)
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(buf)
		buf = buf[2:]
	}
	return out, buf, nil
}

func readBytes(buf []byte, label string) ([]byte, []byte, error) {
	if len(buf) < 4 {
		return nil, nil, fmt.Errorf("truncated: %s len", label)
	}
	n := binary.LittleEndian.Uint32(buf)
	buf = buf[4:]
	if uint32(len(buf)) < n {
		return nil, nil, fmt.Errorf("truncated: %s", label)
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out, buf[n:], nil
}

func readLDFA(buf []byte) (*LDFA, []byte, error) {
	if len(buf) < 4 {
		return nil, nil, fmt.Errorf("truncated: mt_log")
	}
	l := &LDFA{}
	l.MtLog = binary.LittleEndian.Uint32(buf)
	buf = buf[4:]

	if len(buf) < 256 {
		return nil, nil, fmt.Errorf("truncated: mt_lookup")
	}
	copy(l.MtLookup[:], buf[:256])
	buf = buf[256:]

	var err error
	l.Begin, buf, err = readUint16Slice(buf, "begin_table")
	if err != nil {
		return nil, nil, err
	}

	l.Center, buf, err = readUint16Slice(buf, "center_table")
	if err != nil {
		return nil, nil, err
	}

	if len(buf) < 4 {
		return nil, nil, fmt.Errorf("truncated: effects len")
	}
	numStates := binary.LittleEndian.Uint32(buf)
	buf = buf[4:]
	l.Effects = make([][]Effect, numStates)
	for i := uint32(0); i < numStates; i++ {
		if len(buf) < 2 {
			return nil, nil, fmt.Errorf("truncated: effects[%d] count", i)
		}
		count := binary.LittleEndian.Uint16(buf)
		buf = buf[2:]
		if count == 0 {
			continue
		}
		effs := make([]Effect, count)
		for j := range effs {
			if len(buf) < 5 {
				return nil, nil, fmt.Errorf("truncated: effects[%d][%d]", i, j)
			}
			effs[j].Mask = buf[0]
			effs[j].Rel = binary.LittleEndian.Uint32(buf[1:])
			buf = buf[5:]
		}
		l.Effects[i] = effs
	}

	return l, buf, nil
}

// LiteralPrefix is forward-prefix acceleration data.
type LiteralPrefix struct {
	// Needle is the required literal prefix bytes.
	Needle []byte
	// Exact reports whether the whole pattern reduces to Needle.
	Exact bool
}

type DFA struct {
	fwd *LDFA
	rev *LDFA
	lit *LiteralPrefix
}

// Tables returns the decoded fwd/rev tables.
func (d *DFA) Tables() (fwd, rev *LDFA) {
	return d.fwd, d.rev
}

// LiteralPrefix returns the DFA literal prefix, if present.
func (d *DFA) LiteralPrefix() *LiteralPrefix {
	return d.lit
}

func Load(buf []byte) (*DFA, error) {
	if len(buf) < 1 {
		return nil, fmt.Errorf("empty blob")
	}
	tag := buf[0]
	buf = buf[1:]
	switch tag {
	case formatPlain:
		fwd, rest, err := readLDFA(buf)
		if err != nil {
			return nil, fmt.Errorf("read fwd: %w", err)
		}
		rev, rest, err := readLDFA(rest)
		if err != nil {
			return nil, fmt.Errorf("read rev: %w", err)
		}
		if len(rest) != 0 {
			return nil, fmt.Errorf("%d trailing bytes after decoding both DFAs", len(rest))
		}
		return &DFA{fwd: fwd, rev: rev}, nil
	case formatLiteral:
		if len(buf) < 1 {
			return nil, fmt.Errorf("truncated: literal-prefix exact flag")
		}
		exact := buf[0] != 0
		buf = buf[1:]
		needle, rest, err := readBytes(buf, "literal-prefix needle")
		if err != nil {
			return nil, err
		}
		fwd, rest, err := readLDFA(rest)
		if err != nil {
			return nil, fmt.Errorf("read fwd: %w", err)
		}
		if len(rest) != 0 {
			return nil, fmt.Errorf("%d trailing bytes after decoding literal-prefix DFA", len(rest))
		}
		return &DFA{fwd: fwd, lit: &LiteralPrefix{Needle: needle, Exact: exact}}, nil
	default:
		return nil, fmt.Errorf("unknown wire format tag %d", tag)
	}
}

type Match struct {
	Start int
	End   int
}

func collectNulls(l *LDFA, state uint32, pos int, query byte, dst []int) []int {
	for _, e := range l.Effects[state] {
		if e.Mask&query != 0 {
			dst = append(dst, pos+int(e.Rel))
		}
	}
	return dst
}

func fwdUpdate(l *LDFA, state uint32, pos int, query byte, maxEnd int) int {
	found := false
	var bestRel uint32
	for _, e := range l.Effects[state] {
		if e.Mask&query != 0 && (!found || e.Rel < bestRel) {
			found = true
			bestRel = e.Rel
		}
	}
	if !found {
		return maxEnd
	}
	cand := pos - int(bestRel)
	if maxEnd == noMatch || cand > maxEnd {
		return cand
	}
	return maxEnd
}

func collectStarts(rev *LDFA, data []byte, dst []int) []int {
	n := len(data)
	pos := n - 1
	curr := uint32(rev.Begin[rev.mt(data[pos])])
	if n == 1 {
		return collectNulls(rev, curr, 0, maskEnd, dst)
	}
	dst = collectNulls(rev, curr, pos, maskCenter, dst)
	for pos > 0 {
		pos--
		mt := rev.mt(data[pos])
		curr = uint32(rev.Center[rev.delta(uint16(curr), mt)])
		if curr <= dfaDead {
			break
		}
		query := byte(maskCenter)
		if pos == 0 {
			query = maskEnd
		}
		dst = collectNulls(rev, curr, pos, query, dst)
	}
	return dst
}

func scanFwdFrom(fwd *LDFA, data []byte, curr uint32, pos int) int {
	n := len(data)
	maxEnd := noMatch
	for {
		query := byte(maskCenter)
		if pos == n {
			query = maskEnd
		}
		maxEnd = fwdUpdate(fwd, curr, pos, query, maxEnd)
		if curr <= dfaDead || pos >= n {
			break
		}
		mt := fwd.mt(data[pos])
		curr = uint32(fwd.Center[fwd.delta(uint16(curr), mt)])
		pos++
	}
	return maxEnd
}

func longestMatchFrom(fwd *LDFA, data []byte, begin int) int {
	curr := uint32(fwd.Begin[fwd.mt(data[begin])])
	return scanFwdFrom(fwd, data, curr, begin+1)
}

func walkPrefix(fwd *LDFA, data []byte, pos, length int) uint32 {
	state := uint32(fwd.Begin[fwd.mt(data[pos])])
	if state <= dfaDead {
		return 0
	}
	for i := 1; i < length; i++ {
		mt := fwd.mt(data[pos+i])
		state = uint32(fwd.Center[fwd.delta(uint16(state), mt)])
		if state <= dfaDead {
			return 0
		}
	}
	return state
}

func findAllLiteralExact(data, needle []byte, dst []Match) []Match {
	pos := 0
	n := len(needle)
	for {
		idx := bytes.Index(data[pos:], needle)
		if idx < 0 {
			break
		}
		start := pos + idx
		dst = append(dst, Match{Start: start, End: start + n})
		pos = start + n
	}
	return dst
}

func findAllLiteralPrefixInto(d *DFA, data []byte, s *Scratch) []Match {
	lit := d.lit
	s.matches = s.matches[:0]
	if lit.Exact {
		s.matches = findAllLiteralExact(data, lit.Needle, s.matches)
		return s.matches
	}

	fwd := d.fwd
	needle := lit.Needle
	nlen := len(needle)
	searchStart := 0

	if state := uint32(fwd.Begin[fwd.mt(data[0])]); state > dfaDead {
		if end := scanFwdFrom(fwd, data, state, 1); end > 0 {
			s.matches = append(s.matches, Match{Start: 0, End: end})
			searchStart = end
		}
	}

	for {
		idx := bytes.Index(data[searchStart:], needle)
		if idx < 0 {
			break
		}
		candidate := searchStart + idx
		if state := walkPrefix(fwd, data, candidate, nlen); state > dfaDead {
			if end := scanFwdFrom(fwd, data, state, candidate+nlen); end > candidate {
				s.matches = append(s.matches, Match{Start: candidate, End: end})
				searchStart = end
				continue
			}
		}
		searchStart = candidate + 1
	}
	return s.matches
}

func allEndsFrom(fwd *LDFA, data []byte, begin int, dst []int) []int {
	n := len(data)
	pos := begin
	curr := uint32(fwd.Begin[fwd.mt(data[pos])])
	pos++
	for {
		query := byte(maskCenter)
		if pos == n {
			query = maskEnd
		}
		dst = collectNulls(fwd, curr, pos, query, dst)
		if curr <= dfaDead || pos >= n {
			break
		}
		mt := fwd.mt(data[pos])
		curr = uint32(fwd.Center[fwd.delta(uint16(curr), mt)])
		pos++
	}
	return dst
}

// Scratch holds buffers reused across *Into calls.
type Scratch struct {
	starts  []int
	matches []Match
	ends    []int
}

// FindAll returns non-overlapping, leftmost-longest matches.
func (d *DFA) FindAll(data []byte) []Match {
	return append([]Match(nil), d.FindAllInto(data, &Scratch{})...)
}

// FindAllInto is FindAll using s's buffers.
func (d *DFA) FindAllInto(data []byte, s *Scratch) []Match {
	if len(data) == 0 {
		return nil
	}
	if d.lit != nil {
		return findAllLiteralPrefixInto(d, data, s)
	}
	s.starts = collectStarts(d.rev, data, s.starts[:0])
	s.matches = s.matches[:0]
	pos := 0
	lastStart := -1
	for i := len(s.starts) - 1; i >= 0; i-- {
		begin := s.starts[i]
		if begin == lastStart {
			continue
		}
		lastStart = begin
		if pos > begin {
			continue
		}
		end := longestMatchFrom(d.fwd, data, begin)
		if end < 0 {
			continue
		}
		s.matches = append(s.matches, Match{Start: begin, End: end})
		pos = end
	}
	return s.matches
}

// FindAllOverlapping returns overlapping matches.
func (d *DFA) FindAllOverlapping(data []byte) []Match {
	return append([]Match(nil), d.FindAllOverlappingInto(data, &Scratch{})...)
}

// FindAllOverlappingInto is FindAllOverlapping using s's buffers.
func (d *DFA) FindAllOverlappingInto(data []byte, s *Scratch) []Match {
	if d.lit != nil {
		panic("goresharp: FindAllOverlapping is not supported on a literal-forward-prefix DFA (no reverse DFA is loaded for it)")
	}
	if len(data) == 0 {
		return nil
	}
	s.starts = collectStarts(d.rev, data, s.starts[:0])
	s.matches = s.matches[:0]
	lastStart := -1
	for i := len(s.starts) - 1; i >= 0; i-- {
		begin := s.starts[i]
		if begin == lastStart {
			continue
		}
		lastStart = begin
		s.ends = allEndsFrom(d.fwd, data, begin, s.ends[:0])
		for _, end := range s.ends {
			s.matches = append(s.matches, Match{Start: begin, End: end})
		}
	}
	return s.matches
}
