// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// A synthetic disc generator. It writes real VIDEO_TS.IFO and VTS_nn_0.IFO
// byte layouts (TT_SRPT, PTL_MAIT, VMGM and VTSM PGCI_UT, the first-play PGC,
// VTS_PTT_SRPT, PGCIT with program maps, cell playback and cell position
// tables and commands, VTS_TMAPTI, C_ADT and VOBU_ADMAP) so the engine is
// tested through the same parsers a real disc goes through, without any
// physical media. synth_test.go adds the VOBs (sparse files that hold only
// NAV packs) and lays the whole thing out as a folder or a disc image.

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

type fxCell struct {
	first, last uint32
	dur         float64
	still       int
	vob, id     int  // VOB and cell id; 0: given by the generator
	mode, typ   int  // angle block: mode 1 first, 2 middle, 3 last; typ 1
	interleaved bool // stored interleaved with other cells
	seamless    bool
	cmd         int         // 1-based cell command
	unlisted    bool        // kept out of the cell address table
	pieces      []cellRange // interleaved units; nil: the cell is one piece
}

type fxPGC struct {
	title    int  // VTS title number in the category
	noEntry  bool // belongs to the title without being its entry PGC
	menu     int  // menu type of a menu PGC (3 root)
	cells    []fxCell
	programs []int // entry cell per program; nil = one per cell
	next     int
	prev, up int
	pre      []Command
	post     []Command
	cellCmds []Command
	buttons  []Command // a menu's button commands, the default first
	// Parental block (alternative versions of a stretch of a title).
	blockMode, blockType, mask int
	dur                        *float64 // play time, when it isn't the sum of the cells'
	playback                   int
}

type fxVTS struct {
	titleSectors            uint32
	audio, subp             int
	pgcs                    []fxPGC
	menus                   []fxPGC       // VTSM menus, language "en"
	ptt                     map[int][]PTT // chapter lists that aren't the PGCs' programs
	noCADT, noADMAP, noTMAP bool
	noVOB                   bool // an index with no video files
}

type fxTitle struct{ vts, vtsTitle int }

type fxDisc struct {
	titles    []fxTitle
	vts       []fxVTS // index 0 is VTS 1
	firstPlay []Command
	menus     []fxPGC // VMGM menus, language "en"
	// parental holds, per level 1 to 8, the parental ID mask every title
	// set gets (PTL_MAIT, country US).
	parental []uint16
	// split is how many sectors one title VOB file holds (0: the usual
	// gigabyte).
	split uint32
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

// cellIDs gives every cell of a VOB its VOB and cell id. Cells that share a
// sector range share an id, as the cells of real program chains do.
type cellIDs map[cellRange][2]int

func (c fxCell) listed(size uint32) bool {
	return !c.unlisted && c.last >= c.first && (size == 0 || c.first < size)
}

func idsFor(pgcs []fxPGC, size uint32) cellIDs {
	ids := cellIDs{}
	var auto []cellRange
	maxVOB := 0
	for _, p := range pgcs {
		for _, c := range p.cells {
			k := cellRange{c.first, c.last}
			if c.vob != 0 {
				ids[k] = [2]int{c.vob, c.id}
				maxVOB = max(maxVOB, c.vob)
			} else if _, ok := ids[k]; !ok && c.listed(size) {
				ids[k] = [2]int{}
				auto = append(auto, k)
			}
		}
	}
	sort.Slice(auto, func(i, j int) bool { return auto[i].first < auto[j].first })
	n := 0
	for _, k := range auto {
		if ids[k] != ([2]int{}) {
			continue
		}
		ids[k] = [2]int{maxVOB + 1 + n/255, n%255 + 1}
		n++
	}
	return ids
}

// of is the id of a cell: the table's, or a made-up one for cells the table
// leaves out.
func (ids cellIDs) of(c fxCell, n int) (vob, id int) {
	if c.vob != 0 {
		return c.vob, c.id
	}
	if v, ok := ids[cellRange{c.first, c.last}]; ok && !c.unlisted {
		return v[0], v[1]
	}
	return 200, n%255 + 1
}

func encodePGC(p fxPGC, ids cellIDs) []byte {
	progs := p.progs()
	n, np := len(p.cells), len(progs)
	progOff := 0xec
	cellPb := progOff + (np+1)&^1
	cellPos := cellPb + 24*n
	cmdOff := cellPos + 4*n
	size := cmdOff
	nCmd := len(p.pre) + len(p.post) + len(p.cellCmds)
	if nCmd > 0 {
		size += 8 + 8*nCmd
	}
	b := buf(make([]byte, size))
	b[2], b[3] = byte(np), byte(n)
	var total float64
	block := false
	for _, c := range p.cells {
		// One angle's worth: the other cells of an angle block play instead.
		if c.typ == 1 && c.mode != 1 && block {
			continue
		}
		block = c.typ == 1
		total += c.dur
	}
	if p.dur != nil {
		total = *p.dur
	}
	t := bcdTime(total)
	copy(b[4:], t[:])
	b.u16(0x9c, p.next)
	b.u16(0x9e, p.prev)
	b.u16(0xa0, p.up)
	b[0xa3] = byte(p.playback)
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
		cat := c.mode<<6 | c.typ<<4
		if c.seamless {
			cat |= 8
		}
		if c.interleaved {
			cat |= 4
		}
		b[o] = byte(cat)
		b[o+2] = byte(c.still)
		b[o+3] = byte(c.cmd)
		t := bcdTime(c.dur)
		copy(b[o+4:], t[:])
		b.u32(o+8, int(c.first))
		b.u32(o+16, int(c.last))
		b.u32(o+20, int(c.last))
		vob, id := ids.of(c, i)
		b.u16(cellPos+4*i, vob)
		b[cellPos+4*i+3] = byte(id)
	}
	if nCmd > 0 {
		b.u16(0xe4, cmdOff)
		b.u16(cmdOff, len(p.pre))
		b.u16(cmdOff+2, len(p.post))
		b.u16(cmdOff+4, len(p.cellCmds))
		b.u16(cmdOff+6, 8+8*nCmd-1)
		at := cmdOff + 8
		for _, list := range [][]Command{p.pre, p.post, p.cellCmds} {
			for _, c := range list {
				copy(b[at:], c[:])
				at += 8
			}
		}
	}
	return b
}

// encodePGCIT lays out a PGC information table; cat gives each row's
// category word.
func encodePGCIT(pgcs []fxPGC, ids cellIDs, cat func(fxPGC) int) []byte {
	head := 8 + 8*len(pgcs)
	var blobs [][]byte
	size := head
	for _, p := range pgcs {
		e := encodePGC(p, ids)
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

// encodePGCIUT wraps menus in a one-language (English) menu unit table.
func encodePGCIUT(menus []fxPGC, ids cellIDs) []byte {
	lu := encodePGCIT(menus, ids, func(p fxPGC) int { return (0x80 | p.menu) << 24 })
	u := buf(make([]byte, 16+len(lu)))
	u.u16(0, 1)
	u.u32(4, len(u)-1)
	copy(u[8:], "en")
	u[11] = 0x80
	u.u32(12, 16)
	copy(u[16:], lu)
	return u
}

func sectorsFor(n int) int { return (n + Sector - 1) / Sector }

// menuSectors is how long a menu VOB the menus need, and layoutMenus gives a
// menu with buttons and no cells a cell of its own that waits for the viewer.
const menuCellSectors = 4

func layoutMenus(menus []fxPGC) ([]fxPGC, uint32) {
	out := append([]fxPGC(nil), menus...)
	var end uint32
	for _, p := range out {
		for _, c := range p.cells {
			end = max(end, c.last+1)
		}
	}
	for i := range out {
		if len(out[i].cells) == 0 && len(out[i].buttons) > 0 {
			out[i].cells = []fxCell{{first: end, last: end + menuCellSectors - 1, dur: 10, still: 0xff}}
			end += menuCellSectors
		}
	}
	return out, end
}

// ptts builds a VTS's chapter table: per title, every program of its entry
// PGC and of the PGCs its next links chain to.
func (v fxVTS) ptts() [][]PTT {
	maxT := 0
	for _, p := range v.pgcs {
		maxT = max(maxT, p.title)
	}
	for t := range v.ptt {
		maxT = max(maxT, t)
	}
	out := make([][]PTT, maxT)
	for t := 1; t <= maxT; t++ {
		if l, ok := v.ptt[t]; ok {
			out[t-1] = l
			continue
		}
		for i, p := range v.pgcs {
			if p.title != t || p.noEntry {
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

// cadt lays out a cell address table: one row per piece of each listed cell.
func cadt(pgcs []fxPGC, ids cellIDs, size uint32) ([]byte, []uint32) {
	type row struct {
		vob, id     int
		first, last uint32
	}
	var rows []row
	seen := map[cellRange]bool{}
	for _, p := range pgcs {
		for i, c := range p.cells {
			k := cellRange{c.first, c.last}
			if seen[k] || !c.listed(size) {
				continue
			}
			seen[k] = true
			vob, id := ids.of(c, i)
			pieces := c.pieces
			if pieces == nil {
				pieces = []cellRange{k}
			}
			for _, pc := range pieces {
				rows = append(rows, row{vob, id, pc.first, pc.last})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].first < rows[j].first })
	b := buf(make([]byte, 8+12*len(rows)))
	vobs := map[int]bool{}
	var starts []uint32
	for i, r := range rows {
		vobs[r.vob] = true
		b.u16(8+12*i, r.vob)
		b[8+12*i+2] = byte(r.id)
		b.u32(8+12*i+4, int(r.first))
		b.u32(8+12*i+8, int(r.last))
		if n := len(starts); n == 0 || starts[n-1] != r.first {
			starts = append(starts, r.first)
		}
	}
	b.u16(0, len(vobs))
	b.u32(4, len(b)-1)
	return b, starts
}

// admap lays out a VOBU address map.
func admap(starts []uint32) []byte {
	b := buf(make([]byte, 4+4*len(starts)))
	b.u32(0, len(b)-1)
	for i, s := range starts {
		b.u32(4+4*i, int(s))
	}
	return b
}

// tmapti lays out the time maps: one per PGC, an entry every few seconds
// naming the VOBU (here: the cell) that plays then.
func tmapti(pgcs []fxPGC) []byte {
	var maps [][]byte
	for _, p := range pgcs {
		var total float64
		for _, c := range p.cells {
			total += c.dur
		}
		unit := max(4, int(math.Ceil(total/2000)))
		var entries []uint32
		var t float64
		k := 1
		for _, c := range p.cells {
			for ; float64(k*unit) <= t+c.dur && len(entries) < 2048; k++ {
				entries = append(entries, c.first)
			}
			t += c.dur
		}
		m := buf(make([]byte, 4+4*len(entries)))
		m[0] = byte(min(unit, 255))
		m.u16(2, len(entries))
		for i, e := range entries {
			m.u32(4+4*i, int(e))
		}
		maps = append(maps, m)
	}
	size := 8 + 4*len(maps)
	for _, m := range maps {
		size += len(m)
	}
	b := buf(make([]byte, size))
	b.u16(0, len(maps))
	b.u32(4, size-1)
	at := 8 + 4*len(maps)
	for i, m := range maps {
		b.u32(8+4*i, at)
		copy(b[at:], m)
		at += len(m)
	}
	return b
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
	ids := idsFor(v.pgcs, v.titleSectors)
	pgcit := encodePGCIT(v.pgcs, ids, func(p fxPGC) int {
		cat := p.blockMode<<22 | p.blockType<<20 | p.mask&0xffff
		if p.title == 0 {
			return cat
		}
		if p.noEntry {
			return cat | p.title<<24
		}
		return cat | (0x80|p.title)<<24
	})
	menus, menuSectors := layoutMenus(v.menus)
	menuIDs := idsFor(menus, 0)

	type table struct {
		ptr  int
		data []byte
	}
	tables := []table{{0xc8, ptt}, {0xcc, pgcit}}
	if len(menus) > 0 {
		c, starts := cadt(menus, menuIDs, 0)
		tables = append(tables, table{0xd0, encodePGCIUT(menus, menuIDs)}, table{0xd8, c}, table{0xdc, admap(starts)})
	}
	if !v.noTMAP {
		tables = append(tables, table{0xd4, tmapti(v.pgcs)})
	}
	c, starts := cadt(v.pgcs, ids, v.titleSectors)
	if !v.noCADT {
		tables = append(tables, table{0xe0, c})
	}
	if !v.noADMAP {
		tables = append(tables, table{0xe4, admap(starts)})
	}
	ifoSectors := 1
	for _, t := range tables {
		ifoSectors += sectorsFor(len(t.data))
	}
	b := buf(make([]byte, ifoSectors*Sector))
	copy(b, "DVDVIDEO-VTS")
	sec := 1
	for _, t := range tables {
		b.u32(t.ptr, sec)
		copy(b[sec*Sector:], t.data)
		sec += sectorsFor(len(t.data))
	}
	ifoLast := ifoSectors - 1
	titleVOB := ifoLast + 1 + int(menuSectors)
	b.u32(0x0c, titleVOB+int(v.titleSectors)+ifoLast) // IFO, menu VOB, title VOBs, BUP
	b.u32(0x1c, ifoLast)
	if menuSectors > 0 {
		b.u32(0xc0, ifoLast+1)
	}
	b.u32(0xc4, titleVOB)
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
	return b
}

func encodeVMG(d fxDisc) []byte {
	tt := buf(make([]byte, 8+12*len(d.titles)))
	tt.u16(0, len(d.titles))
	tt.u32(4, len(tt)-1)
	for i, t := range d.titles {
		o := 8 + 12*i
		tt[o], tt[o+1] = 0x3c, 1
		if t.vts >= 1 && t.vts <= len(d.vts) {
			v := d.vts[t.vts-1]
			p := v.ptts()
			if t.vtsTitle >= 1 && t.vtsTitle <= len(p) {
				tt.u16(o+2, len(p[t.vtsTitle-1]))
			}
			// The angles of the title's largest angle block.
			for _, pg := range v.pgcs {
				n := 0
				for _, c := range pg.cells {
					if c.typ != 1 {
						n = 0
						continue
					}
					if c.mode == 1 {
						n = 0
					}
					n++
					if pg.title == t.vtsTitle {
						tt[o+1] = max(tt[o+1], byte(n))
					}
				}
			}
		}
		tt[o+6], tt[o+7] = byte(t.vts), byte(t.vtsTitle)
	}
	menus, menuSectors := layoutMenus(d.menus)
	menuIDs := idsFor(menus, 0)
	type table struct {
		ptr  int
		data []byte
	}
	tables := []table{{0xc4, tt}}
	if len(menus) > 0 {
		c, starts := cadt(menus, menuIDs, 0)
		tables = append(tables, table{0xc8, encodePGCIUT(menus, menuIDs)}, table{0xd8, c}, table{0xdc, admap(starts)})
	}
	if d.parental != nil {
		sets := len(d.vts)
		p := buf(make([]byte, 16+16*(sets+1)))
		p.u16(0, 1)
		p.u16(2, sets)
		p.u32(4, len(p)-1)
		copy(p[8:], "US")
		p.u16(12, 16)
		for level := 8; level >= 1; level-- {
			for k := 0; k <= sets; k++ {
				p.u16(16+2*(8-level)*(sets+1)+2*k, int(d.parental[level-1]))
			}
		}
		tables = append(tables, table{0xcc, p})
	}
	var fp []byte
	if d.firstPlay != nil {
		fp = encodePGC(fxPGC{pre: d.firstPlay}, nil)
	}
	ifoSectors := 1
	for _, t := range tables {
		ifoSectors += sectorsFor(len(t.data))
	}
	b := buf(make([]byte, ifoSectors*Sector))
	copy(b, "DVDVIDEO-VMG")
	sec := 1
	for _, t := range tables {
		b.u32(t.ptr, sec)
		copy(b[sec*Sector:], t.data)
		sec += sectorsFor(len(t.data))
	}
	b.u32(0x0c, 2*ifoSectors+int(menuSectors)-1)
	b.u32(0x1c, ifoSectors-1)
	b.u16(0x3e, len(d.vts))
	if menuSectors > 0 {
		b.u32(0xc0, ifoSectors)
	}
	if fp != nil {
		b.u32(0x84, 0x400)
		copy(b[0x400:], fp)
	}
	return b
}

// files lays the disc's index files out as a VIDEO_TS folder in memory, BUPs
// included. It has no VOBs (see vfiles for those).
func (d fxDisc) files() fstest.MapFS {
	m := fstest.MapFS{}
	for i, v := range d.vts {
		b := encodeVTS(v)
		name := fmt.Sprintf("VIDEO_TS/VTS_%02d_0", i+1)
		m[name+".IFO"] = &fstest.MapFile{Data: b}
		m[name+".BUP"] = &fstest.MapFile{Data: append([]byte(nil), b...)}
	}
	vmg := encodeVMG(d)
	m["VIDEO_TS/VIDEO_TS.IFO"] = &fstest.MapFile{Data: vmg}
	m["VIDEO_TS/VIDEO_TS.BUP"] = &fstest.MapFile{Data: append([]byte(nil), vmg...)}
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
func writeDir(t testing.TB, m fstest.MapFS) string {
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

// Commands, as the VM decodes them.
func jumpTT(n int) Command    { return Command{0x30, 0x02, 0, 0, 0, byte(n), 0, 0} }
func jumpVTSTT(n int) Command { return Command{0x30, 0x03, 0, 0, 0, byte(n), 0, 0} }
func jumpRootMenu() Command   { return Command{0x30, 0x06, 0, 0, 0, 0x43, 0, 0} }
func jumpTitleMenu() Command  { return Command{0x30, 0x06, 0, 0, 0, 0x42, 0, 0} }
func linkPGCN(n int) Command  { return Command{0x20, 0x04, 0, 0, 0, 0, byte(n >> 8), byte(n)} }
func linkCN(n int) Command    { return Command{0x20, 0x07, 0, 0, 0, 0, 0, byte(n)} }

// setGPRM is "gN = v", and ifGPRMEqJumpTT "if (gN == v) JumpTT t".
func setGPRM(reg, v int) Command {
	return Command{0x71, 0, 0, byte(reg), byte(v >> 8), byte(v), 0, 0}
}

// ifGPRMNeLinkPGCN is "if (gN != v) LinkPGCN p": the guard a feature's
// pre-commands use to bounce a player that didn't come through the menu.
func ifGPRMNeLinkPGCN(reg, v, pgcn int) Command {
	return Command{0x20, 0xb4, 0, byte(reg), byte(v >> 8), byte(v), byte(pgcn >> 8), byte(pgcn)}
}

func flagsOf(c *Candidate) string {
	if c == nil {
		return "<none>"
	}
	return strings.Join(c.Flags.Names(), ",")
}

// tableSpan is where a table of an index file lies, from its sector pointer
// in the header: the bytes from its first sector to its end address.
func tableSpan(ifo []byte, ptr int, endAt int) (int, int) {
	base := int(binary.BigEndian.Uint32(ifo[ptr:])) * Sector
	end := base + int(binary.BigEndian.Uint32(ifo[base+endAt:])) + 1
	return base, min(end, len(ifo))
}

// wipeTable zeroes a table, as a scratch through its sectors would.
func wipeTable(ifo []byte, ptr int) {
	endAt := 4
	if ptr == 0xdc || ptr == 0xe4 {
		endAt = 0 // a VOBU map starts with its end address
	}
	a, b := tableSpan(ifo, ptr, endAt)
	clear(ifo[a:b])
}
