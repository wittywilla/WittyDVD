// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Opening the worst disc there is must stay quick and small on a small
// machine: 99 titles, thousands of program chains, every one a decoy but
// one, with commands that send a dry run round in circles.

import (
	"math/rand"
	"runtime"
	"testing"
	"time"
)

// stressDisc is 99 titles over pgcsPerTitle program chains each (2970
// chains at 30): every title but one chains its way through junk made of
// the film's cells, and every chain carries commands.
func stressDisc(pgcsPerTitle int) fxDisc {
	rng := rand.New(rand.NewSource(3))
	feature := cellsAt(0, 60, 2*min_, 500) // 2:00:00
	const real = 42
	var pgcs []fxPGC
	for t := 1; t <= 99; t++ {
		if t == real {
			pgcs = append(pgcs, fxPGC{title: t, cells: feature, post: []Command{jumpTitleMenu()}})
			continue
		}
		first := len(pgcs) + 1
		for k := 0; k < pgcsPerTitle; k++ {
			var cells []fxCell
			for i := 0; i < 40; i++ {
				cells = append(cells, feature[rng.Intn(len(feature))])
			}
			p := fxPGC{cells: cells, pre: []Command{setGPRM(k%16, k)}, cellCmds: []Command{linkCN(1 + k%40)}}
			if k == 0 {
				p.title = t
			}
			p.next = len(pgcs) + 2
			if k == pgcsPerTitle-1 {
				p.next = first // round in a circle
				p.post = []Command{linkPGCN(first)}
			}
			if k%7 == 3 {
				p.cells[5].cmd = 1 // a cell command that jumps back
			}
			pgcs = append(pgcs, p)
		}
	}
	return fxDisc{
		titles:    titlesFor(1, 99),
		firstPlay: []Command{jumpTitleMenu()},
		menus:     []fxPGC{{menu: 2, buttons: []Command{jumpTT(real), jumpTT(1), jumpTT(99)}}},
		vts:       []fxVTS{{titleSectors: 30000, audio: 2, subp: 4, pgcs: pgcs, noTMAP: true}},
	}
}

func TestStressDiscIsBounded(t *testing.T) {
	d := stressDisc(30)
	files := d.files()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	o := openFS(t, files)
	opened := time.Since(start)
	r := o.Disc.Analyze(RankOptions{})
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if n := len(o.Disc.VTS[1].PGCs); n != 98*30+1 {
		t.Fatalf("%d program chains", n)
	}
	if r.Main != 42 || len(r.Decoys) != 98 {
		t.Fatalf("main %d, %d decoys\n%s", r.Main, len(r.Decoys), r.Explain())
	}
	alloc := (after.TotalAlloc - before.TotalAlloc) >> 20
	t.Logf("opened in %v, ranked in %v, %d MiB allocated in all, index files %d KiB", opened, took-opened, alloc, (len(files[vtsIFO].Data)+len(files["VIDEO_TS/VIDEO_TS.IFO"].Data))>>10)
	// The time is only a guard against the analysis going quadratic: a
	// laptop takes about a tenth of a second, a Raspberry Pi 3 some twenty
	// times that. A busy machine or the race detector says nothing.
	limit := 15 * time.Second
	if raceOn {
		limit = 5 * time.Minute
	}
	if took > limit {
		t.Errorf("opening and ranking took %v", took)
	}
	if alloc > 192 {
		t.Errorf("%d MiB allocated", alloc)
	}
	// Nearly three times the chains: the work is bounded by budgets, not by
	// what the disc claims, so the answer still comes, and is the same.
	big := stressDisc(80).files()
	start = time.Now()
	ob := openFS(t, big)
	rb := ob.Disc.Analyze(RankOptions{})
	if took := time.Since(start); took > 4*limit || rb.Main != 42 || len(rb.Decoys) != 98 {
		t.Errorf("7841 chains: %v, main %d, %d decoys", took, rb.Main, len(rb.Decoys))
	}
}

// What the analysis may spend is fixed, whatever the disc holds: a disc
// whose every title walks a chapter table of thousands of entries at every
// cell runs the dry runs out of fuel, and is ranked from its tables.
func TestDryRunsAreBounded(t *testing.T) {
	var cells []fxCell
	for i := 0; i < 250; i++ {
		cells = append(cells, fxCell{first: uint32(i) * 100, last: uint32(i)*100 + 99, dur: 30})
	}
	var pgcs []fxPGC
	ptt := map[int][]PTT{}
	for t := 1; t <= 99; t++ {
		first := len(pgcs) + 1
		for k := 0; k < 8; k++ {
			p := fxPGC{cells: cells, pre: []Command{setGPRM(1, k)}, next: len(pgcs) + 2}
			if k == 0 {
				p.title = t
			}
			if k == 7 {
				p.next = 0
			}
			pgcs = append(pgcs, p)
			for pg := 1; pg <= 250; pg++ {
				ptt[t] = append(ptt[t], PTT{first + k, pg})
			}
		}
	}
	d := fxDisc{titles: titlesFor(1, 99), vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: pgcs, ptt: ptt, noTMAP: true}}}
	o := openFS(t, d.files())
	a := &analysis{d: o.Disc, w: DefaultWeights, r: &Ranking{}, fuel: analysisFuel, work: analysisWork, vts: map[int]*vtsFacts{}, byTitle: map[int]*feat{}}
	for _, tt := range o.Disc.VMG.Titles {
		if f := a.title(tt.Number); f == nil || len(f.ts.Cells) != 2000 {
			t.Fatalf("title %d: %d cells by %s, fuel %d", tt.Number, len(f.ts.Cells), f.ts.Path, a.fuel)
		}
	}
	if a.fuel > 0 {
		t.Fatalf("%d of %d fuel left: the walk through the chapter table isn't charged", a.fuel, analysisFuel)
	}
	if r := o.Disc.Analyze(RankOptions{}); r.Main == 0 || len(r.Candidates) != 99 {
		t.Fatalf("main %d of %d", r.Main, len(r.Candidates))
	}
}

func BenchmarkOpenStressDisc(b *testing.B) {
	files := stressDisc(30).files()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openSource(fsSource{files}, "stress", "folder"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeStressDisc(b *testing.B) {
	o := openFS(b, stressDisc(30).files())
	b.ReportAllocs()
	for b.Loop() {
		if r := o.Disc.Analyze(RankOptions{}); r.Main != 42 {
			b.Fatal(r.Main)
		}
	}
}

func BenchmarkAnalyzeMovie(b *testing.B) {
	o := openFS(b, simpleMovie().files())
	b.ReportAllocs()
	for b.Loop() {
		_ = o.Disc.Analyze(RankOptions{})
	}
}

func BenchmarkAnalyzeDecoy99(b *testing.B) {
	o := openFS(b, decoy99().files())
	b.ReportAllocs()
	for b.Loop() {
		_ = o.Disc.Analyze(RankOptions{})
	}
}
