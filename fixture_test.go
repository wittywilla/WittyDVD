// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// A builder for synthetic DVD index files. It writes real VIDEO_TS.IFO and
// VTS_nn_0.IFO byte layouts (TT_SRPT, PTT_SRPT, PGCIT with program maps,
// cell playback and cell position tables, C_ADT, VMGM PGCI_UT, first-play
// PGC) so the scoring engine is tested through the same parsers a real disc
// goes through, without any physical media.

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type fxCell struct {
	first, last uint32
	dur         float64
	still       int
}

type fxPGC struct {
	title    int // VTS title number of an entry PGC (0: not an entry)
	menu     int // menu type of a menu PGC (3 root)
	cells    []fxCell
	programs []int // entry cell per program; nil = one per cell
	next     int
	pre      []Command
}

type fxVTS struct {
	titleSectors uint32
	audio, subp  int
	pgcs         []fxPGC
}

type fxTitle struct{ vts, vtsTitle int }

type fxDisc struct {
	titles    []fxTitle
	vts       []fxVTS // index 0 is VTS 1
	firstPlay []Command
	menus     []fxPGC // VMGM menus, language "en"
}

// cellsAt makes n cells of dur seconds, stride sectors each, from sector at.
func cellsAt(at uint32, n int, dur float64, stride uint32) []fxCell {
	out := make([]fxCell, n)
	for i := range out {
		f := at + uint32(i)*stride
		out[i] = fxCell{first: f, last: f + stride - 1, dur: dur}
	}
	return out
}

func toBCD(v int) byte { return byte(v/10<<4 | v%10) }

// bcdTime encodes seconds as a DVD time at 25 fps.
func bcdTime(sec float64) [4]byte {
	whole := int(sec)
	frames := int(math.Round((sec - float64(whole)) * 25))
	if frames >= 25 {
		whole, frames = whole+1, 0
	}
	h := min(whole/3600, 99)
	return [4]byte{toBCD(h), toBCD(whole / 60 % 60), toBCD(whole % 60), 0x40 | toBCD(frames)}
}

type buf []byte

func (b buf) u16(off, v int)     { binary.BigEndian.PutUint16(b[off:], uint16(v)) }
func (b buf) u32(off int, v int) { binary.BigEndian.PutUint32(b[off:], uint32(v)) }

func (p fxPGC) progs() []int {
	if p.programs != nil {
		return p.programs
	}
	out := make([]int, len(p.cells))
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func encodePGC(p fxPGC) []byte {
	progs := p.progs()
	n, np := len(p.cells), len(progs)
	progOff := 0xec
	cellPb := progOff + (np+1)&^1
	cellPos := cellPb + 24*n
	cmdOff := cellPos + 4*n
	size := cmdOff
	if len(p.pre) > 0 {
		size += 8 + 8*len(p.pre)
	}
	b := buf(make([]byte, size))
	b[2], b[3] = byte(np), byte(n)
	var total float64
	for _, c := range p.cells {
		total += c.dur
	}
	t := bcdTime(total)
	copy(b[4:], t[:])
	b.u16(0x9c, p.next)
	if np > 0 {
		b.u16(0xe6, progOff)
	}
	if n > 0 {
		b.u16(0xe8, cellPb)
		b.u16(0xea, cellPos)
	}
	for i, e := range progs {
		b[progOff+i] = byte(e)
	}
	for i, c := range p.cells {
		o := cellPb + 24*i
		b[o+2] = byte(c.still)
		t := bcdTime(c.dur)
		copy(b[o+4:], t[:])
		b.u32(o+8, int(c.first))
		b.u32(o+16, int(c.last))
		b.u32(o+20, int(c.last))
		b.u16(cellPos+4*i, 1)
		b[cellPos+4*i+3] = byte(i + 1)
	}
	if len(p.pre) > 0 {
		b.u16(0xe4, cmdOff)
		b.u16(cmdOff, len(p.pre))
		b.u16(cmdOff+6, 8+8*len(p.pre)-1)
		for i, c := range p.pre {
			copy(b[cmdOff+8+8*i:], c[:])
		}
	}
	return b
}

// encodePGCIT lays out a PGC information table; cat gives each row's
// category word.
func encodePGCIT(pgcs []fxPGC, cat func(fxPGC) int) []byte {
	head := 8 + 8*len(pgcs)
	var blobs [][]byte
	size := head
	for _, p := range pgcs {
		e := encodePGC(p)
		blobs = append(blobs, e)
		size += len(e)
	}
	b := buf(make([]byte, size))
	b.u16(0, len(pgcs))
	b.u32(4, size-1)
	at := head
	for i, p := range pgcs {
		b.u32(8+8*i, cat(p))
		b.u32(12+8*i, at)
		copy(b[at:], blobs[i])
		at += len(blobs[i])
	}
	return b
}

func sectorsFor(n int) int { return (n + Sector - 1) / Sector }

// ptts builds a VTS's chapter table: per title, every program of its entry
// PGC and of the PGCs its next links chain to.
func (v fxVTS) ptts() [][]PTT {
	maxT := 0
	for _, p := range v.pgcs {
		maxT = max(maxT, p.title)
	}
	out := make([][]PTT, maxT)
	for t := 1; t <= maxT; t++ {
		for i, p := range v.pgcs {
			if p.title != t {
				continue
			}
			seen := map[int]bool{}
			for k := i + 1; k >= 1 && k <= len(v.pgcs) && !seen[k]; k = v.pgcs[k-1].next {
				seen[k] = true
				for pg := range v.pgcs[k-1].progs() {
					out[t-1] = append(out[t-1], PTT{PGCN: k, PGN: pg + 1})
				}
			}
			break
		}
	}
	return out
}

func encodeVTS(v fxVTS) []byte {
	ptts := v.ptts()
	pttSize := 8 + 4*len(ptts)
	for _, l := range ptts {
		pttSize += 4 * len(l)
	}
	ptt := buf(make([]byte, pttSize))
	ptt.u16(0, len(ptts))
	ptt.u32(4, pttSize-1)
	at := 8 + 4*len(ptts)
	for i, l := range ptts {
		ptt.u32(8+4*i, at)
		for _, e := range l {
			ptt.u16(at, e.PGCN)
			ptt.u16(at+2, e.PGN)
			at += 4
		}
	}
	pgcit := encodePGCIT(v.pgcs, func(p fxPGC) int {
		if p.title == 0 {
			return 0
		}
		return (0x80 | p.title) << 24
	})
	// C_ADT: one row per distinct cell (VOB 1).
	type row struct{ first, last uint32 }
	var rows []row
	seen := map[row]bool{}
	for _, p := range v.pgcs {
		for _, c := range p.cells {
			r := row{c.first, c.last}
			if !seen[r] {
				seen[r] = true
				rows = append(rows, r)
			}
		}
	}
	cadt := buf(make([]byte, 8+12*len(rows)))
	cadt.u16(0, 1)
	cadt.u32(4, len(cadt)-1)
	for i, r := range rows {
		cadt.u16(8+12*i, 1)
		cadt[8+12*i+2] = byte(i%255 + 1)
		cadt.u32(8+12*i+4, int(r.first))
		cadt.u32(8+12*i+8, int(r.last))
	}
	pttSec := 1
	pgcitSec := pttSec + sectorsFor(len(ptt))
	cadtSec := pgcitSec + sectorsFor(len(pgcit))
	ifoSectors := cadtSec + sectorsFor(len(cadt))
	b := buf(make([]byte, ifoSectors*Sector))
	copy(b, "DVDVIDEO-VTS")
	ifoLast := ifoSectors - 1
	titleVOB := ifoLast + 1
	b.u32(0x0c, titleVOB+int(v.titleSectors)+ifoLast) // IFO, title VOBs, BUP
	b.u32(0x1c, ifoLast)
	b.u32(0xc4, titleVOB)
	b.u32(0xc8, pttSec)
	b.u32(0xcc, pgcitSec)
	b.u32(0xe0, cadtSec)
	b.u16(0x200, 0x4000|3<<10) // MPEG-2, NTSC, 16:9
	b.u16(0x202, v.audio)
	for i := 0; i < v.audio && i < 8; i++ {
		o := 0x204 + 8*i
		b[o], b[o+1] = 0x04, 5 // AC-3 with a language, 6 channels
		copy(b[o+2:], "en")
	}
	b.u16(0x254, v.subp)
	for i := 0; i < v.subp && i < 32; i++ {
		o := 0x256 + 6*i
		b[o] = 1
		copy(b[o+2:], "en")
	}
	copy(b[pttSec*Sector:], ptt)
	copy(b[pgcitSec*Sector:], pgcit)
	copy(b[cadtSec*Sector:], cadt)
	return b
}

func encodeVMG(d fxDisc, vtsBytes [][]byte) []byte {
	tt := buf(make([]byte, 8+12*len(d.titles)))
	tt.u16(0, len(d.titles))
	tt.u32(4, len(tt)-1)
	for i, t := range d.titles {
		o := 8 + 12*i
		tt[o], tt[o+1] = 0x3c, 1
		if t.vts >= 1 && t.vts <= len(d.vts) {
			p := d.vts[t.vts-1].ptts()
			if t.vtsTitle >= 1 && t.vtsTitle <= len(p) {
				tt.u16(o+2, len(p[t.vtsTitle-1]))
			}
		}
		tt[o+6], tt[o+7] = byte(t.vts), byte(t.vtsTitle)
	}
	var ut []byte
	if len(d.menus) > 0 {
		lu := encodePGCIT(d.menus, func(p fxPGC) int { return (0x80 | p.menu) << 24 })
		u := buf(make([]byte, 16+len(lu)))
		u.u16(0, 1)
		u.u32(4, len(u)-1)
		copy(u[8:], "en")
		u[11] = 0x80
		u.u32(12, 16)
		copy(u[16:], lu)
		ut = u
	}
	var fp []byte
	if d.firstPlay != nil {
		fp = encodePGC(fxPGC{pre: d.firstPlay})
	}
	ttSec := 1
	utSec := ttSec + sectorsFor(len(tt))
	ifoSectors := utSec + sectorsFor(len(ut))
	b := buf(make([]byte, ifoSectors*Sector))
	copy(b, "DVDVIDEO-VMG")
	b.u32(0x0c, 2*ifoSectors-1)
	b.u32(0x1c, ifoSectors-1)
	b.u16(0x3e, len(d.vts))
	b.u32(0xc4, ttSec)
	if ut != nil {
		b.u32(0xc8, utSec)
		copy(b[utSec*Sector:], ut)
	}
	if fp != nil {
		b.u32(0x84, 0x400)
		copy(b[0x400:], fp)
	}
	copy(b[ttSec*Sector:], tt)
	return b
}

// files lays the disc out as a VIDEO_TS folder in memory, BUPs included.
func (d fxDisc) files() fstest.MapFS {
	m := fstest.MapFS{}
	var vts [][]byte
	for i, v := range d.vts {
		b := encodeVTS(v)
		vts = append(vts, b)
		name := fmt.Sprintf("VIDEO_TS/VTS_%02d_0", i+1)
		m[name+".IFO"] = &fstest.MapFile{Data: b}
		m[name+".BUP"] = &fstest.MapFile{Data: b}
	}
	vmg := encodeVMG(d, vts)
	m["VIDEO_TS/VIDEO_TS.IFO"] = &fstest.MapFile{Data: vmg}
	m["VIDEO_TS/VIDEO_TS.BUP"] = &fstest.MapFile{Data: vmg}
	return m
}

// fsSource is a Source over an fs.FS holding a VIDEO_TS folder. It has no
// VOBs: everything it says comes from the index files.
type fsSource struct{ fsys fs.FS }

func (s fsSource) ReadIFO(name string) ([]byte, error) {
	return fs.ReadFile(s.fsys, "VIDEO_TS/"+name)
}
func (s fsSource) VOBExtents(Space) ([]FileExtent, error) { return nil, nil }
func (s fsSource) Close() error                           { return nil }
func (s fsSource) Describe() string                       { return "memory" }

func openFS(t testing.TB, fsys fs.FS) *Opened {
	t.Helper()
	o, err := openSource(fsSource{fsys}, "fixture", "folder")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// writeDir writes files (and any extra ones) into a temporary VIDEO_TS.
func writeDir(t *testing.T, m fstest.MapFS) string {
	t.Helper()
	root := t.TempDir()
	for name, f := range m {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func titlesFor(vts int, n int) []fxTitle {
	out := make([]fxTitle, n)
	for i := range out {
		out[i] = fxTitle{vts, i + 1}
	}
	return out
}

func jumpTT(n int) Command  { return Command{0x30, 0x02, 0, 0, 0, byte(n), 0, 0} }
func jumpRootMenu() Command { return Command{0x30, 0x06, 0, 0, 0, 0x43, 0, 0} }
func flagsOf(c *Candidate) string {
	if c == nil {
		return "<none>"
	}
	return strings.Join(c.Flags.Names(), ",")
}
