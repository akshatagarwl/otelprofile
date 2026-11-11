// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Location building adapted from Go's runtime/pprof/proto.go
// (appendLocsForStack, pcDeck, allFrames, parseProcSelfMaps), with the
// runtime-internal helpers replaced by public APIs and the caches kept across
// profiles.

package rtprof

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// symbolizeFlag keeps track of symbolization result.
//
//	0                  : no symbol lookup was performed
//	1<<0 (lookupTried) : symbol lookup was performed
//	1<<1 (lookupFailed): symbol lookup was performed but failed
type symbolizeFlag uint8

const (
	lookupTried  symbolizeFlag = 1 << iota
	lookupFailed symbolizeFlag = 1 << iota
)

type memMap struct {
	start   uintptr
	end     uintptr
	offset  uint64
	file    string
	buildID string
	funcs   symbolizeFlag
	fake    bool // /proc/self/maps was not available
}

type line struct {
	fn   int32 // index into symbols.funcs
	line int64
}

type location struct {
	id      uint64
	npcs    int // stack entries this location covers (inlined frames)
	addr    uint64
	mapping int // index into symbols.mem
	lines   []line
	// The allFrames result for the leaf-most PC, to re-try it in a deck.
	firstFrames []runtime.Frame
	firstResult symbolizeFlag
	gen         uint32
}

type function struct {
	name, file string
	gen        uint32
}

// symbols turns stacks into pprof locations and functions. Everything it
// learns is kept for the life of the Profiler: a PC that was symbolized
// once is never symbolized again.
type symbols struct {
	mem     []memMap
	locs    map[uintptr]*location
	locList []*location // by ID-1
	funcIdx map[string]int32
	funcs   []function
	deck    pcDeck
	expand  []uintptr
	out     []uint64
	gen     uint32 // incremented per written profile, for "used" marks
}

func newSymbols() *symbols {
	return &symbols{
		mem:     readMapping(),
		locs:    map[uintptr]*location{},
		funcIdx: map[string]int32{},
	}
}

func (s *symbols) byID(id uint64) *location { return s.locList[id-1] }

// allFrames returns all of the frames that addr expands to, as
// runtime/pprof's allFrames.
func allFrames(addr uintptr) ([]runtime.Frame, symbolizeFlag) {
	frames := runtime.CallersFrames([]uintptr{addr})
	frame, more := frames.Next()
	if frame.Function == "runtime.goexit" {
		return nil, 0
	}
	symbolizeResult := lookupTried
	if frame.PC == 0 || frame.Function == "" || frame.File == "" || frame.Line == 0 {
		symbolizeResult |= lookupFailed
	}
	if frame.PC == 0 {
		// If we failed to resolve the frame, at least make up
		// a reasonable call PC. This mostly happens in tests.
		frame.PC = addr - 1
	}
	ret := []runtime.Frame{frame}
	for frame.Function != "runtime.goexit" && more {
		frame, more = frames.Next()
		ret = append(ret, frame)
	}
	return ret, symbolizeResult
}

// noFunc ends an inline expansion in expandFinalInlineFrame: it has no
// function, so runtime.CallersFrames emits the virtual PCs of every frame
// inlined at the PC before it, and then stops.
const noFunc = 1

// expandFinalInlineFrame does what runtime/pprof's runtime-internal
// runtime_expandFinalInlineFrame does: if the last PC of a truncated stack
// is inside an inlined call, append the virtual PCs of the frames it was
// inlined into, so the stack ends on a whole location. It uses
// runtime.CallersFrames, which inserts those virtual PCs itself when the
// next PC in its input is not one of them.
func (s *symbols) expandFinalInlineFrame(stk []uintptr) []uintptr {
	if len(stk) == 0 {
		return stk
	}
	pc := stk[len(stk)-1]
	frames := runtime.CallersFrames([]uintptr{pc, noFunc})
	first, more := frames.Next()
	if first.Func != nil || first.Entry == 0 || !more {
		return stk // not inlined, or not a Go function
	}
	s.expand = append(s.expand[:0], stk[:len(stk)-1]...)
	s.expand = append(s.expand, pc)
	for more {
		var f runtime.Frame
		f, more = frames.Next()
		if f.Entry != first.Entry {
			break
		}
		s.expand = append(s.expand, f.PC+1)
		if f.Func != nil {
			break // the physical function the others were inlined into
		}
	}
	return s.expand
}

// locsForStack returns the location IDs of a stack of return PCs (or 1 + the
// PC of an inline mark), as runtime/pprof's appendLocsForStack. The result
// is only valid until the next call.
func (s *symbols) locsForStack(stk []uintptr) []uint64 {
	locs := s.out[:0]
	s.deck.reset()
	stk = s.expandFinalInlineFrame(stk)
	for len(stk) > 0 {
		addr := stk[0]
		if l, ok := s.locs[addr]; ok {
			// addr may be a "fake" PC of an inline mark that a leaf
			// location absorbs: try the deck first.
			if len(s.deck.pcs) > 0 {
				if added := s.deck.tryAdd(addr, l.firstFrames, l.firstResult); added {
					stk = stk[1:]
					continue
				}
			}
			if id := s.emitLocation(); id > 0 {
				locs = append(locs, id)
			}
			locs = append(locs, l.id)
			stk = stk[min(l.npcs, len(stk)):]
			continue
		}
		frames, symbolizeResult := allFrames(addr)
		if len(frames) == 0 { // runtime.goexit
			if id := s.emitLocation(); id > 0 {
				locs = append(locs, id)
			}
			stk = stk[1:]
			continue
		}
		if added := s.deck.tryAdd(addr, frames, symbolizeResult); added {
			stk = stk[1:]
			continue
		}
		// addr is not inlined with the PCs in the deck: flush the deck
		// and retry this PC.
		if id := s.emitLocation(); id > 0 {
			locs = append(locs, id)
		}
		if l, ok := s.locs[addr]; ok {
			locs = append(locs, l.id)
			stk = stk[min(l.npcs, len(stk)):]
		} else {
			s.deck.tryAdd(addr, frames, symbolizeResult) // must succeed
			stk = stk[1:]
		}
	}
	if id := s.emitLocation(); id > 0 {
		locs = append(locs, id)
	}
	s.out = locs
	return locs
}

// pcDeck collects the PCs of one physical location: a leaf PC and the fake
// PCs of the inline marks of the functions it was inlined into. See the
// comment on pcDeck in runtime/pprof/proto.go.
type pcDeck struct {
	pcs             []uintptr
	frames          []runtime.Frame
	symbolizeResult symbolizeFlag
	firstPCFrames   int
	firstPCResult   symbolizeFlag
}

func (d *pcDeck) reset() {
	d.pcs = d.pcs[:0]
	d.frames = d.frames[:0]
	d.symbolizeResult = 0
	d.firstPCFrames = 0
	d.firstPCResult = 0
}

func (d *pcDeck) tryAdd(pc uintptr, frames []runtime.Frame, symbolizeResult symbolizeFlag) bool {
	if existing := len(d.frames); existing > 0 {
		newFrame := frames[0]
		last := d.frames[existing-1]
		if last.Func != nil { // the last frame can't be inlined
			return false
		}
		if last.Entry == 0 || newFrame.Entry == 0 { // possibly not a Go function
			return false
		}
		if last.Entry != newFrame.Entry { // a different function
			return false
		}
		// runtime/pprof compares linker symbol names here; the printed
		// name differs only between instantiations of one generic
		// function, which cannot be inlined into each other.
		if last.Function == newFrame.Function { // maybe recursion
			return false
		}
	}
	d.pcs = append(d.pcs, pc)
	d.frames = append(d.frames, frames...)
	d.symbolizeResult |= symbolizeResult
	if len(d.pcs) == 1 {
		d.firstPCFrames = len(d.frames)
		d.firstPCResult = symbolizeResult
	}
	return true
}

// emitLocation turns the deck into a cached location and returns its ID.
func (s *symbols) emitLocation() uint64 {
	if len(s.deck.pcs) == 0 {
		return 0
	}
	defer s.deck.reset()
	addr := s.deck.pcs[0]
	l := &location{
		id:          uint64(len(s.locList)) + 1,
		npcs:        len(s.deck.pcs),
		addr:        uint64(s.deck.frames[0].PC),
		mapping:     -1,
		lines:       make([]line, 0, len(s.deck.frames)),
		firstFrames: append([]runtime.Frame(nil), s.deck.frames[:s.deck.firstPCFrames]...),
		firstResult: s.deck.firstPCResult,
	}
	for k := range s.deck.frames {
		frame := &s.deck.frames[k]
		fi, ok := s.funcIdx[frame.Function]
		if !ok {
			fi = int32(len(s.funcs))
			s.funcs = append(s.funcs, function{name: frame.Function, file: frame.File})
			s.funcIdx[frame.Function] = fi
		}
		l.lines = append(l.lines, line{fn: fi, line: int64(frame.Line)})
	}
	for i := range s.mem {
		if s.mem[i].start <= addr && addr < s.mem[i].end || s.mem[i].fake {
			l.mapping = i
			s.mem[i].funcs |= s.deck.symbolizeResult
			break
		}
	}
	s.locs[addr] = l
	s.locList = append(s.locList, l)
	return l.id
}

func readMapping() []memMap {
	data, _ := os.ReadFile("/proc/self/maps")
	var mem []memMap
	parseProcSelfMaps(data, func(lo, hi, offset uint64, file, buildID string) {
		mem = append(mem, memMap{start: uintptr(lo), end: uintptr(hi), offset: offset, file: file, buildID: buildID})
	})
	if len(mem) == 0 { // pprof expects a map entry, so fake one
		mem = []memMap{{fake: true}}
	}
	return mem
}

var (
	space   = []byte(" ")
	newline = []byte("\n")
)

// parseProcSelfMaps is runtime/pprof's, unchanged.
func parseProcSelfMaps(data []byte, addMapping func(lo, hi, offset uint64, file, buildID string)) {
	var line []byte
	// next removes and returns the next field in the line.
	// It also removes from line any spaces following the field.
	next := func() []byte {
		var f []byte
		f, line, _ = bytes.Cut(line, space)
		line = bytes.TrimLeft(line, " ")
		return f
	}
	for len(data) > 0 {
		line, data, _ = bytes.Cut(data, newline)
		addr := next()
		loStr, hiStr, ok := strings.Cut(string(addr), "-")
		if !ok {
			continue
		}
		lo, err := strconv.ParseUint(loStr, 16, 64)
		if err != nil {
			continue
		}
		hi, err := strconv.ParseUint(hiStr, 16, 64)
		if err != nil {
			continue
		}
		perm := next()
		if len(perm) < 4 || perm[2] != 'x' {
			// Only interested in executable mappings.
			continue
		}
		offset, err := strconv.ParseUint(string(next()), 16, 64)
		if err != nil {
			continue
		}
		next()          // dev
		inode := next() // inode
		if line == nil {
			continue
		}
		file := string(line)

		// Trim deleted file marker.
		file = strings.TrimSuffix(file, " (deleted)")

		if len(inode) == 1 && inode[0] == '0' && file == "" {
			// Huge-page text mappings list the initial fragment of
			// mapped but unpopulated memory as being inode 0.
			// Don't report that part.
			// But [vdso] and [vsyscall] are inode 0, so let non-empty file names through.
			continue
		}
		buildID, _ := elfBuildID(file)
		addMapping(lo, hi, offset, file, buildID)
	}
}
