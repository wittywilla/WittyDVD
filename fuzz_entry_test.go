// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// One fuzz target for each way bytes from a disc get into the engine. The
// seeds are the synthetic discs of scenarios_test.go, so the fuzzer starts
// from index files that parse and mutates its way out from there; they run
// as ordinary tests with every `go test`. To explore:
//
//	go test -run '^$' -fuzz '^FuzzOpenDisc$' -fuzztime 1m .
//
// What every target holds the engine to: no panic, no endless loop, memory
// bounded by the size of the input (see the budget in ifo.go), and answers
// that can be written as JSON (no NaN, no infinity).

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"testing/fstest"
)

func seedDiscs() []fxDisc {
	var out []fxDisc
	for _, sc := range scenarios {
		if sc.name == "five hundred fake program chains" || sc.name == "99 titles, 98 decoys" {
			continue // big seeds slow the fuzzer down and add no new shapes
		}
		out = append(out, sc.disc())
	}
	return out
}

func checkRanking(t *testing.T, r *Ranking) {
	t.Helper()
	if len(r.Candidates) == 0 {
		if r.Main != 0 {
			t.Fatal("a main title and no candidates")
		}
		return
	}
	main := r.Candidate(r.Main)
	if main == nil || r.Why == nil || r.Feature == nil {
		t.Fatalf("main %d: candidate %v, why %v, feature %v", r.Main, main != nil, r.Why != nil, r.Feature != nil)
	}
	if !(r.Confidence > 0 && r.Confidence < 1) {
		t.Fatalf("confidence %v", r.Confidence)
	}
	for _, c := range r.Candidates {
		if math.IsNaN(c.Score) || math.IsInf(c.Score, 0) || c.Distinct < 0 || c.Class == "" {
			t.Fatalf("title %d: score %v, distinct %v, class %q", c.TitleNum, c.Score, c.Distinct, c.Class)
		}
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatalf("the ranking can't be written as JSON: %v", err)
	}
	_ = r.Explain()
}

func FuzzParseVMG(f *testing.F) {
	f.Add(testVMG())
	f.Add(append([]byte("DVDVIDEO-VMG"), make([]byte, 0x200)...))
	for _, d := range seedDiscs() {
		f.Add(encodeVMG(d))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := ParseVMG(b)
		if err != nil {
			return
		}
		d := &Disc{VMG: v, VTS: map[int]*VTS{}}
		_ = d.CrossCheck()
		_ = d.Region()
		_ = d.ParentalMask(0x5553, 8, 1)
		checkRanking(t, d.Analyze(RankOptions{}))
	})
}

func FuzzParseVTS(f *testing.F) {
	f.Add(testVTS([3]int{2, 1, 1<<2 | 1}, [3]int{1, 2, 3<<2 | 1}))
	f.Add(append([]byte("DVDVIDEO-VTS"), make([]byte, 0x300)...))
	for _, d := range seedDiscs() {
		f.Add(encodeVTS(d.vts[0]))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := ParseVTS(b, 1)
		if err != nil {
			return
		}
		for _, e := range v.PGCs {
			if e.PGC == nil {
				t.Fatal("a row of the PGCIT without a program chain")
			}
		}
		// As title set 1 of a disc whose every title is in it.
		vmg := &VMG{NumTitleSets: 1}
		for i := 0; i < 99 && i < max(len(v.PTT), 3); i++ {
			vmg.Titles = append(vmg.Titles, TitleSearch{Number: i + 1, VTS: 1, VTSTitle: i + 1, Angles: 1})
		}
		d := &Disc{VMG: vmg, VTS: map[int]*VTS{1: v}}
		_ = d.CrossCheck()
		if again := d.CrossCheck(); len(again) > 0 {
			for _, r := range again {
				if r.Fixed && r.Table != tblTTSRPT {
					t.Fatalf("mended twice: %+v", r)
				}
			}
		}
		for _, ts := range d.Titles() {
			if len(ts.Cells) > maxTitleCells || math.IsNaN(ts.Duration) {
				t.Fatalf("title %d: %d cells, %v long", ts.Number, len(ts.Cells), ts.Duration)
			}
			for i := 1; i <= 3; i++ {
				_ = d.TitleAngle(ts.Number, i)
			}
		}
		checkRanking(t, d.Analyze(RankOptions{FilesKnown: true, FileSectors: map[int]uint32{1: 5000}, ButtonsComplete: true}))
	})
}

// FuzzOpenDisc is a whole disc: both copies of both index files, as Open
// reads them, merged table by table, cross-checked, ranked and navigated.
func FuzzOpenDisc(f *testing.F) {
	for _, d := range seedDiscs() {
		vmg, vts := encodeVMG(d), encodeVTS(d.vts[0])
		f.Add(vmg, vmg, vts, vts)
		// One table gone from each copy.
		a, b := append([]byte(nil), vts...), append([]byte(nil), vts...)
		wipeTable(a, 0xcc)
		wipeTable(b, 0xc8)
		f.Add(vmg, vmg, a, b)
	}
	f.Fuzz(func(t *testing.T, vmgIFO, vmgBUP, vtsIFO, vtsBUP []byte) {
		files := fstest.MapFS{
			"VIDEO_TS/VIDEO_TS.IFO": {Data: vmgIFO},
			"VIDEO_TS/VIDEO_TS.BUP": {Data: vmgBUP},
			"VIDEO_TS/VTS_01_0.IFO": {Data: vtsIFO},
			"VIDEO_TS/VTS_01_0.BUP": {Data: vtsBUP},
			"VIDEO_TS/VTS_02_0.IFO": {Data: vtsBUP},
		}
		o, err := openSource(fsSource{files}, "fuzz", "folder")
		if err != nil {
			return
		}
		for _, rep := range o.Disc.IFOReports {
			for _, tb := range rep.Tables {
				if tb.From != "IFO" && tb.From != "BUP" {
					t.Fatalf("%s %s read from %q", rep.Name, tb.Name, tb.From)
				}
			}
		}
		r := o.Analyze()
		checkRanking(t, r)
		if m := o.Disc.MainTitle(); (m == nil) != (len(r.Candidates) == 0) {
			t.Fatal("MainTitle and the ranking disagree about whether there is a title")
		}
		n := NewNav(o.Disc, NavOptions{RegisterOptions: RegisterOptions{Rand: deterministicRand()}})
		fuel := int64(200000)
		st := n.Start()
		n.st.regs.fuel = &fuel
		for i := 0; i < 50 && st != nil && st.Kind != StepStop; i++ {
			st = n.CellEnded()
		}
		n.PlayTitle(r.Main, 1)
	})
}

func FuzzNavPack(f *testing.F) {
	f.Add(navAt{sector: 5, end: 99, vob: 2, id: 3, buttons: []Command{jumpTT(1), jumpTitleMenu()}}.pack())
	f.Add(make([]byte, Sector))
	f.Add(make([]byte, 0x3d4))
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = ParsePCI(b)
		_ = ParseDSI(b)
		_ = PackScrambled(b)
		sc := ScanSectors(b, 7)
		for _, n := range sc.Navs {
			if n.PCI != nil && len(n.PCI.Buttons) > 3*maxButtons {
				t.Fatalf("%d buttons from one NAV pack", len(n.PCI.Buttons))
			}
		}
		// The same bytes fed in pieces must not crash the scanner either.
		ps := NewPackScanner()
		for at := 0; at < len(b); at += 3 * Sector {
			ps.Feed(b[at:min(at+3*Sector, len(b))], uint32(at/Sector))
		}
	})
}

// byteSectors is a VOB held in memory.
type byteSectors []byte

func (s byteSectors) ReadSectors(first uint32, count int) ([]byte, error) {
	a := int64(first) * Sector
	if a >= int64(len(s)) || count <= 0 {
		return nil, nil
	}
	return s[a:min(a+int64(count)*Sector, int64(len(s)))], nil
}

// FuzzVOB is everything that walks a VOB from NAV pack to NAV pack: the menu
// scan, the button reader, the interleaved-cell reader and its resync.
func FuzzVOB(f *testing.F) {
	d := guardedFeature()
	menus, _ := layoutMenus(d.menus)
	var vob []byte
	for _, n := range navsOf(menus, idsFor(menus, 0), 0) {
		for len(vob) < int(n.sector)*Sector {
			vob = append(vob, make([]byte, Sector)...)
		}
		vob = append(vob, n.pack()...)
		vob = append(vob, make([]byte, 3*Sector)...)
	}
	f.Add(vob, uint32(0), uint32(3))
	f.Add(make([]byte, 4*Sector), uint32(1), uint32(900))
	f.Add([]byte("0"), uint32(84), uint32(2)) // a cell that ends before it starts
	f.Fuzz(func(t *testing.T, b []byte, first, last uint32) {
		if len(b) > 64*Sector {
			return
		}
		rd := byteSectors(b)
		if pic, err := ScanMenu(rd, first, last, MenuScanOptions{MaxHops: 64, ReadAhead: 16}); err == nil && pic != nil {
			_ = pic.WithDefault(1)
		}
		if _, hops, _ := cellButtons(rd, first, last, 64); hops > 64 {
			t.Fatalf("%d hops, limit 64", hops)
		}
		if next, ok := resyncVOBU(rd, nil, first, last); ok && (next <= first || next > last) {
			t.Fatalf("resync from %d to %d, cell ends at %d", first, next, last)
		}
		if next, ok := resyncVOBU(rd, []uint32{2, 9, 40}, first, last); ok && (next <= first || next > last) {
			t.Fatalf("resync from %d to %d, cell ends at %d", first, next, last)
		}
		o := &Opened{Disc: &Disc{VMG: &VMG{}, VTS: map[int]*VTS{}}, spaces: map[Space][]FileExtent{}, readers: map[Space]*spaceReader{}}
		o.vobReader = func(Space) (SectorReader, error) { return rd, nil }
		c := Cell{FirstSector: first, LastSector: last, Interleaved: true, VOBID: 1, CellID: 1}
		// A cell that ends before it starts comes back as it is: it holds
		// nothing, and RangeExtents gives it no bytes.
		if rs, err := o.CellRanges(Space{Domain: DomainVTS, VTS: 1}, c); err == nil && last >= first {
			for _, r := range rs {
				if r.Last < r.First || r.First < first || r.Last > last {
					t.Fatalf("range %v outside cell %d..%d", r, first, last)
				}
			}
		}
		_ = navPTM(rd, first)
		_ = scanLead(b, first)
	})
}

func FuzzSPU(f *testing.F) {
	f.Add([]byte{0x00, 0x20, 0x00, 0x10, 0x00, 0x00, 0x00, 0x10, 0x01, 0xff})
	f.Add(spuUnit(spuPix, append([]byte{0, 0}, spuSetup(0x07, 0x00, 16, 0x00, 0x14, 0x10, 0x15, 0x00, 0x0c, 0x00, 0x30, 0x00, 0x80, 0x0f, 0xff, 0xff, 0xff, 0x01, 0xff)...), []byte{0, 9, 0x02, 0xff}))
	f.Fuzz(func(t *testing.T, b []byte) {
		var pal [16]uint32
		if s, err := DecodeSPU(b); err == nil && s != nil {
			_ = s.Render(pal, nil).Crop()
		}
		frames, _ := DecodeSPUFrames(b)
		for _, s := range frames {
			_ = s.Render(pal, &Look{}).Opaque()
		}
	})
}

// FuzzVM is the command decoder and the virtual machine: any eight bytes are
// a command, and any list of them must come back.
func FuzzVM(f *testing.F) {
	var seed []byte
	for _, c := range []Command{jumpTT(3), jumpVTSTT(2), jumpRootMenu(), linkPGCN(7), linkCN(2), setGPRM(1, 5), ifGPRMNeLinkPGCN(1, 5, 9)} {
		seed = append(seed, c[:]...)
	}
	f.Add(seed, uint8(0))
	f.Add([]byte{0, 1, 0, 0, 0, 0, 0, 1}, uint8(1)) // Goto 1: a loop
	f.Fuzz(func(t *testing.T, b []byte, start uint8) {
		var cmds []Command
		for at := 0; at+8 <= len(b) && len(cmds) < 256; at += 8 {
			var c Command
			copy(c[:], b[at:])
			in := Decode(c)
			_ = in.String()
			cmds = append(cmds, c)
		}
		r := NewRegisters(RegisterOptions{Rand: rand.New(rand.NewSource(1)).Float64})
		res := r.Run(cmds, RunOptions{Start: int(start)})
		if res.Steps > maxVMSteps {
			t.Fatalf("%d steps", res.Steps)
		}
		_ = r.Run(cmds, RunOptions{Start: int(start), Once: true})
		(&PGC{Pre: cmds}).SetsHighlight()
		_ = fallsThrough(cmds)
		// The static reading of the same commands as a first-play program.
		d := &Disc{VMG: &VMG{FirstPlay: &PGC{Pre: cmds, Post: cmds}, Titles: []TitleSearch{{Number: 1, VTS: 1, VTSTitle: 1}}}, VTS: map[int]*VTS{}}
		_ = d.reachable([]ButtonCommand{{Domain: DomainVMGM, PGCN: 1, Cmd: Command(cmds0(cmds))}})
	})
}

func cmds0(c []Command) Command {
	if len(c) == 0 {
		return Command{}
	}
	return c[0]
}

// A thousand random scratches through the index files of every synthetic
// disc: each must open (or be refused) and rank without a crash. This is the
// fuzzers' work in miniature, for the runs that don't fuzz.
func TestScratchedIndexFiles(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	rounds := 40
	if testing.Short() {
		rounds = 5
	}
	// WITTYDVD_SCRATCH_ROUNDS=5000 makes a soak test of it.
	if n, err := strconv.Atoi(os.Getenv("WITTYDVD_SCRATCH_ROUNDS")); err == nil && n > 0 {
		rounds = n
	}
	for _, d := range seedDiscs() {
		good := d.files()
		for i := 0; i < rounds; i++ {
			files := fstest.MapFS{}
			for name, f := range good {
				b := append([]byte(nil), f.Data...)
				switch rng.Intn(4) {
				case 0: // a burst of noise
					at := rng.Intn(len(b))
					for k := at; k < len(b) && k < at+rng.Intn(600); k++ {
						b[k] = byte(rng.Intn(256))
					}
				case 1: // a zeroed sector
					at := rng.Intn(len(b)/Sector) * Sector
					clear(b[at : at+Sector])
				case 2: // single flipped bytes
					for k := 0; k < 8; k++ {
						b[rng.Intn(len(b))] ^= byte(1 << rng.Intn(8))
					}
				}
				files[name] = &fstest.MapFile{Data: b}
			}
			o, err := openSource(fsSource{files}, "scratched", "folder")
			if err != nil {
				continue
			}
			checkRanking(t, o.Analyze())
			n := NewNav(o.Disc, NavOptions{})
			n.Start()
			n.CellEnded()
		}
	}
}
