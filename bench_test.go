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
	// Generous bounds: a Raspberry Pi 3 is some twenty times slower than
	// the machines tests run on, and must still open this in a few seconds.
	if took > 3*time.Second {
		t.Errorf("opening and ranking took %v", took)
	}
	if alloc > 192 {
		t.Errorf("%d MiB allocated", alloc)
	}
	// Ten times the chains must not take a hundred times as long.
	big := stressDisc(80).files()
	start = time.Now()
	ob := openFS(t, big)
	rb := ob.Disc.Analyze(RankOptions{})
	if took := time.Since(start); took > 10*time.Second || rb.Main != 42 {
		t.Errorf("7841 chains: %v, main %d", took, rb.Main)
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
