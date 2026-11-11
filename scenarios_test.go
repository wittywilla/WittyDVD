// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// One synthetic disc for every obfuscation and oddity the engine claims to
// cope with, and what it must make of each. No physical disc is involved:
// the discs are written by the generator in fixture_test.go and
// synth_test.go, and read back through the same code a real one goes
// through.

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

func feature28() []fxCell { return cellsAt(0, 28, 4*min_, 1000) } // 1:52:00

// unreadableZone: besides the film, titles that claim hours of video in the
// zone of the disc that was pressed unreadable. The sectors are inside the
// VOB, but the title set's cell address table doesn't know them.
func unreadableZone() fxDisc {
	bad := func(at uint32, n int) []fxCell {
		c := cellsAt(at, n, 6*min_, 400)
		for i := range c {
			c[i].unlisted = true
		}
		return c
	}
	return fxDisc{titles: titlesFor(1, 4), vts: []fxVTS{{titleSectors: 80000, audio: 2, pgcs: []fxPGC{
		{title: 1, cells: bad(40000, 30)}, // 3:00:00 of nothing
		{title: 2, cells: feature28()},
		{title: 3, cells: bad(60000, 25)},
		{title: 4, cells: cellsAt(30000, 2, 2*min_, 1000)},
	}}}}
}

// fakePGCs: one feature, and five hundred program chains of junk around it,
// with every title but one pointing at a slice of them.
func fakePGCs() fxDisc {
	rng := rand.New(rand.NewSource(7))
	feature := feature28()
	var pgcs []fxPGC
	const real = 61
	for t := 1; t <= 99; t++ {
		if t == real {
			pgcs = append(pgcs, fxPGC{title: t, cells: feature})
			continue
		}
		// A decoy title: a chain of five program chains of the film's cells,
		// drawn at random, some of them over and over.
		for k := 0; k < 5; k++ {
			var cells []fxCell
			for i := 0; i < 12; i++ {
				cells = append(cells, feature[rng.Intn(len(feature))])
			}
			p := fxPGC{cells: cells}
			if k == 0 {
				p.title = t
			}
			if k < 4 {
				p.next = len(pgcs) + 2
			}
			pgcs = append(pgcs, p)
		}
	}
	return fxDisc{titles: titlesFor(1, 99), vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: pgcs}}}
}

// overlappingCells: a title made of cells that lie over each other, so its
// play time adds up to more than the film's from the same video.
func overlappingCells() fxDisc {
	var over []fxCell
	for i := 0; i < 60; i++ {
		f := uint32(i) * 450
		over = append(over, fxCell{first: f, last: f + 999, dur: 4 * min_, vob: 9, id: i + 1, unlisted: false})
	}
	return fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 30000, audio: 1, noCADT: true, noADMAP: true, pgcs: []fxPGC{
		{title: 1, cells: over}, // 4:00:00 claimed
		{title: 2, cells: feature28()},
	}}}}
}

// hollowCells: a title whose cells claim two hours and are a few sectors
// each.
func hollowCells() fxDisc {
	return fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: []fxPGC{
		{title: 1, cells: cellsAt(28500, 40, 5*min_, 8)}, // 3:20:00 in 320 sectors
		{title: 2, cells: feature28()},
	}}}}
}

// fakeTitleSets: five title sets, of which only the third has video files.
// The others are an index each, claiming a longer film.
func fakeTitleSets() fxDisc {
	d := fxDisc{}
	for n := 1; n <= 5; n++ {
		v := fxVTS{titleSectors: 60000, audio: 1, noVOB: n != 3, pgcs: []fxPGC{{title: 1, cells: cellsAt(0, 36, 5*min_, 1000)}}}
		if n == 3 {
			v.titleSectors, v.pgcs = 30000, []fxPGC{{title: 1, cells: feature28()}}
		}
		d.vts = append(d.vts, v)
		d.titles = append(d.titles, fxTitle{n, 1})
	}
	return d
}

// linkedFeature: title 1's chapter table names a program chain of ten
// seconds. Its commands link on, out of order, through the chains that hold
// the film; nothing but the commands says so.
func linkedFeature() fxDisc {
	f := feature28()
	return fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 40000, audio: 1, pgcs: []fxPGC{
		{title: 1, cells: cellsAt(30000, 1, 10, 200), post: []Command{linkPGCN(4)}},
		{title: 2, cells: cellsAt(31000, 6, 5*min_, 1000)},                      // a featurette
		{title: 1, noEntry: true, cells: f[14:], post: nil},                     // PGC 3: second half
		{title: 1, noEntry: true, cells: f[:7], post: []Command{linkPGCN(5)}},   // PGC 4: first quarter
		{title: 1, noEntry: true, cells: f[7:14], post: []Command{linkPGCN(3)}}, // PGC 5: second quarter
	}, ptt: map[int][]PTT{1: {{1, 1}}, 2: {{2, 1}}}}}}
}

// chainedTitles: the film is cut into four titles of uneven length that
// play on into each other, among decoys longer than any one part.
func chainedTitles() fxDisc {
	f := cellsAt(0, 30, 4*min_, 1000) // 2:00:00
	parts := map[int][]fxCell{7: f[:6], 3: f[6:16], 9: f[16:21], 2: f[21:]}
	next := map[int]int{7: 3, 3: 9, 9: 2}
	var pgcs []fxPGC
	for t := 1; t <= 10; t++ {
		p := fxPGC{title: t}
		switch cells, ok := parts[t]; {
		case ok:
			p.cells = cells
			if n := next[t]; n != 0 {
				p.post = []Command{jumpTT(n)}
			}
		default: // 50 minutes of the film's cells, shuffled
			for i := 0; i < 12; i++ {
				p.cells = append(p.cells, f[(i*7+t)%len(f)])
			}
			p.cells = append(p.cells, f[0])
		}
		pgcs = append(pgcs, p)
	}
	return fxDisc{titles: titlesFor(1, 10), firstPlay: []Command{jumpTT(7)}, vts: []fxVTS{{titleSectors: 40000, audio: 1, pgcs: pgcs}}}
}

// playAllOnly: six episodes as six program chains of one title; no episode
// has a title of its own.
func playAllOnly() fxDisc {
	var pgcs []fxPGC
	for e := 0; e < 6; e++ {
		p := fxPGC{title: 1, noEntry: e > 0, cells: cellsAt(uint32(e)*6000, 6, (3.6+0.05*float64(e))*min_, 1000)}
		if e < 5 {
			p.next = e + 2
		}
		pgcs = append(pgcs, p)
	}
	pgcs = append(pgcs, fxPGC{title: 2, cells: cellsAt(40000, 1, 90, 500)})
	return fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 50000, audio: 1, pgcs: pgcs}}}
}

// playAllOneChain: four episodes in one program chain, each a VOB.
func playAllOneChain() fxDisc {
	var cells []fxCell
	var progs []int
	for e := 0; e < 4; e++ {
		c := cellsAt(uint32(e)*8000, 5, 5*min_, 1500)
		for i := range c {
			c[i].vob, c[i].id = e+1, i+1
		}
		progs = append(progs, len(cells)+1)
		cells = append(cells, c...)
	}
	return fxDisc{titles: titlesFor(1, 1), vts: []fxVTS{{titleSectors: 40000, audio: 1, pgcs: []fxPGC{{title: 1, cells: cells, programs: progs}}}}}
}

// multiAngle: a film whose middle scene has three camera angles, stored
// interleaved. Each angle's scene is a different length here, so the test
// can tell which one was taken.
func multiAngle() fxDisc {
	f := cellsAt(0, 20, 5*min_, 1000)
	var cells []fxCell
	cells = append(cells, f[:8]...)
	for a := 0; a < 3; a++ {
		c := fxCell{first: 8000, last: 10999, dur: (4 + float64(a)) * min_, typ: 1, mode: []int{1, 2, 3}[a], interleaved: true, vob: 2, id: a + 1}
		for u := uint32(0); u < 10; u++ {
			s := 8000 + u*300 + uint32(a)*100
			c.pieces = append(c.pieces, cellRange{s, s + 99})
		}
		cells = append(cells, c)
	}
	cells = append(cells, f[11:]...)
	progs := []int{1, 5, 9, 12}
	return fxDisc{titles: titlesFor(1, 1), vts: []fxVTS{{titleSectors: 30000, audio: 2, pgcs: []fxPGC{{title: 1, cells: cells, programs: progs}}}}}
}

// twoCuts: a theatrical cut and a longer one as two titles that share most
// of their cells (seamless branching), and a trailer.
func twoCuts() fxDisc {
	f := cellsAt(0, 24, 5*min_, 1000)
	extra := cellsAt(24000, 3, 5*min_, 1000)
	long := append(append(append([]fxCell{}, f[:10]...), extra...), f[10:]...)
	return fxDisc{titles: titlesFor(1, 3), vts: []fxVTS{{titleSectors: 40000, audio: 2, pgcs: []fxPGC{
		{title: 1, cells: f}, // 2:00:00
		{title: 2, cells: cellsAt(30000, 1, 2*min_, 1000)},  // trailer
		{title: 3, cells: long, programs: []int{1, 11, 14}}, // 2:15:00
	}}}}
}

// parentalCuts: one title, three program chains; the middle one comes in a
// tame and an uncut version, chosen by the player's parental level.
func parentalCuts() fxDisc {
	f := cellsAt(0, 24, 5*min_, 1000)
	return fxDisc{
		titles:   titlesFor(1, 1),
		parental: []uint16{1, 1, 1, 1, 1, 2, 2, 2}, // levels 1 to 5 get mask 1, 6 to 8 mask 2
		vts: []fxVTS{{titleSectors: 40000, audio: 1, pgcs: []fxPGC{
			{title: 1, cells: f[:8], next: 2},
			{title: 1, noEntry: true, cells: f[8:12], next: 4, blockType: 1, blockMode: 1, mask: 1}, // tame: 20 minutes
			{title: 1, noEntry: true, cells: f[8:16], next: 4, blockType: 1, blockMode: 3, mask: 2}, // uncut: 40 minutes
			{title: 1, noEntry: true, cells: f[16:]},
		}, ptt: map[int][]PTT{1: {{1, 1}, {3, 1}, {4, 1}}}}},
	}
}

// guardedFeature: the film bounces a player that didn't come through the
// menu's Play button (which sets a register) to a decoy chain; a longer,
// unguarded title is the bait.
func guardedFeature() fxDisc {
	bait := cellsAt(0, 28, 4*min_, 1000)
	for i, j := 0, len(bait)-1; i < j; i, j = i+1, j-1 {
		bait[i], bait[j] = bait[j], bait[i]
	}
	bait = append(bait, cellsAt(29000, 3, 4*min_, 300)...)
	return fxDisc{
		titles:    titlesFor(1, 2),
		firstPlay: []Command{jumpTitleMenu()},
		menus:     []fxPGC{{menu: 2, buttons: []Command{jumpTT(2), jumpTT(1)}}},
		vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: []fxPGC{
			{title: 1, cells: bait},
			{title: 2, cells: feature28()},
		}}},
	}
}

// brokenChapters: the film's chapter table names program chains that don't
// exist, in both copies of the index.
func brokenChapters() fxDisc {
	d := simpleMovie()
	d.vts[0].pgcs[0].programs = []int{1, 6, 11, 16, 21, 26}
	d.vts[0].ptt = map[int][]PTT{1: {{40, 1}, {41, 1}, {42, 1}}}
	return d
}

// tvNoPlayAll: five episodes as five titles, numbered against their order
// on the disc, and no title that plays them all.
func tvNoPlayAll() fxDisc {
	order := []int{3, 1, 5, 2, 4} // title numbers, in disc order
	pgcs := make([]fxPGC, 6)
	for i, t := range order {
		pgcs[t-1] = fxPGC{title: t, cells: cellsAt(uint32(i)*9000, 8, (5.3+0.1*float64(i))*min_, 1100)}
	}
	pgcs[5] = fxPGC{title: 6, cells: cellsAt(46000, 1, 100, 800)}
	return fxDisc{titles: titlesFor(1, 6), vts: []fxVTS{{titleSectors: 50000, audio: 1, pgcs: pgcs}}}
}

// tvOneCellPlayAll: the play-all title plays each episode as one long cell,
// where the episodes' own titles have six cells each.
func tvOneCellPlayAll() fxDisc {
	d := tvDisc()
	var long []fxCell
	for _, t := range []int{5, 2, 3, 4} {
		c := d.vts[0].pgcs[t-1].cells
		var dur float64
		for _, x := range c {
			dur += x.dur
		}
		long = append(long, fxCell{first: c[0].first, last: c[len(c)-1].last, dur: dur, vob: 50, id: t})
	}
	d.vts[0].pgcs[0] = fxPGC{title: 1, cells: long}
	return d
}

// linkedAmongDecoys: the film that only its commands put together, on a
// disc with forty decoys, whose IFO has lost its chapter table and whose BUP
// has lost its cell address table.
func linkedAmongDecoys() fxDisc {
	return addDecoys(linkedFeature(), rand.New(rand.NewSource(11)), 40)
}

// chainedAmongFakes: the film cut into chained titles, in the only title
// set with video; three more title sets are an index each, claiming a film
// longer than all the parts together.
func chainedAmongFakes() fxDisc {
	d := chainedTitles()
	for n := 2; n <= 4; n++ {
		d.vts = append(d.vts, fxVTS{titleSectors: 90000, audio: 3, subp: 8, noVOB: true, pgcs: []fxPGC{{title: 1, cells: cellsAt(0, 50, 4*min_, 1500)}}})
		d.titles = append(d.titles, fxTitle{n, 1})
	}
	return d
}

// tvAmongDecoys: the TV disc with sixty decoys thrown in.
func tvAmongDecoys() fxDisc {
	return addDecoys(tvDisc(), rand.New(rand.NewSource(5)), 60)
}

type scenario struct {
	name string
	disc func() fxDisc
	// opened says the scenario needs the VOB files: it is then read from a
	// folder, and Opened.Analyze is what is checked.
	opened bool
	// damage scratches the index files before they are read.
	damage   func(files fstest.MapFS)
	main     int
	decoys   int // at least this many
	class    map[int]string
	flags    map[int]Flag
	without  map[int]Flag
	episodes []int
	starts   []float64 // where each episode starts, when one title holds them
	playAll  int
	feature  []int   // the titles the feature runs through
	length   float64 // of the feature, in minutes
	chapters int     // of the main title
	versions []string
	level    string
}

var scenarios = []scenario{
	{name: "simple movie", disc: simpleMovie, main: 1, length: 120, chapters: 30, level: "high",
		class: map[int]string{1: ClassMain, 2: ClassTrailer, 3: ClassFeaturette}},
	{name: "99 titles, 98 decoys", disc: decoy99, main: 37, decoys: 98, length: 112,
		flags: map[int]Flag{5: FlagShuffled, 6: FlagShuffled, 7: FlagOutOfRange, 8: FlagRepeats, 9: FlagCellSpam}},
	{name: "shuffled-cell decoy", disc: shuffledDecoy, main: 2, decoys: 1,
		flags: map[int]Flag{1: FlagShuffled | FlagDecoy}},
	{name: "TV disc with play all", disc: tvDisc, main: 1, episodes: []int{5, 2, 3, 4}, playAll: 1,
		class: map[int]string{2: ClassEpisode, 5: ClassEpisode, 6: ClassTrailer}},
	{name: "first play jumps to a non-obvious title", disc: func() fxDisc { return menuDisc(3) }, main: 3,
		flags: map[int]Flag{3: FlagFirstPlay}, class: map[int]string{1: ClassExtra}},
	{name: "zero-length cell spam", disc: spamDisc, main: 3, decoys: 2,
		flags: map[int]Flag{1: FlagCellSpam | FlagDecoy, 2: FlagAbsurd | FlagDecoy}},
	{name: "titles in the unreadable zone", disc: unreadableZone, main: 2, decoys: 2, length: 112,
		flags: map[int]Flag{1: FlagUnlisted | FlagDecoy, 3: FlagUnlisted | FlagDecoy}, without: map[int]Flag{2: FlagUnlisted}},
	{name: "five hundred fake program chains", disc: fakePGCs, main: 61, decoys: 90, length: 112},
	{name: "overlapping cells", disc: overlappingCells, main: 2, decoys: 1,
		flags: map[int]Flag{1: FlagOverlap | FlagDecoy}},
	{name: "hollow cells", disc: hollowCells, main: 2, decoys: 1, flags: map[int]Flag{1: FlagHollow | FlagDecoy}},
	{name: "fake title sets", disc: fakeTitleSets, opened: true, main: 3, decoys: 4,
		flags: map[int]Flag{1: FlagNoVideo | FlagDecoy, 5: FlagNoVideo}},
	{name: "feature linked together by commands", disc: linkedFeature, main: 1, length: 112.0 + 10.0/60, feature: []int{1},
		flags: map[int]Flag{1: FlagAssembled}, class: map[int]string{2: ClassFeaturette}},
	{name: "feature cut into chained titles", disc: chainedTitles, main: 7, feature: []int{7, 3, 9, 2}, length: 120,
		flags: map[int]Flag{7: FlagAssembled | FlagFirstPlay, 3: FlagPart, 9: FlagPart, 2: FlagPart},
		class: map[int]string{3: ClassPart, 2: ClassPart}, decoys: 6},
	{name: "play all only, a chain per episode", disc: playAllOnly, main: 1, playAll: 1, episodes: []int{1, 1, 1, 1, 1, 1},
		starts: []float64{0, 21.6, 43.5, 65.7, 88.2, 111}},
	{name: "play all only, one chain", disc: playAllOneChain, main: 1, playAll: 1, episodes: []int{1, 1, 1, 1},
		starts: []float64{0, 25, 50, 75}},
	{name: "three camera angles", disc: multiAngle, main: 1, length: 89, chapters: 4,
		versions: []string{"angle 1 89", "angle 2 90", "angle 3 91"}},
	{name: "two cuts sharing cells", disc: twoCuts, main: 3, length: 135, chapters: 3,
		class: map[int]string{1: ClassAlternate, 2: ClassTrailer}, versions: []string{"cut 1 120"}},
	{name: "parental block", disc: parentalCuts, main: 1, versions: []string{"parental 1-5 100", "parental 6-8 120"}},
	{name: "guarded feature behind the Play button", disc: guardedFeature, opened: true, main: 2, level: "high",
		flags: map[int]Flag{2: FlagMenuPlay}},
	{name: "chapter table points nowhere", disc: brokenChapters, main: 1, chapters: 6, length: 120},
	{name: "TV disc without play all", disc: tvNoPlayAll, main: 4, episodes: []int{3, 1, 5, 2, 4},
		class: map[int]string{3: ClassEpisode, 6: ClassTrailer}},
	{name: "play all of one cell per episode", disc: tvOneCellPlayAll, main: 1, episodes: []int{5, 2, 3, 4}, playAll: 1},

	// Adversarial combinations.
	{name: "linked film, forty decoys, a table gone from each copy", disc: linkedAmongDecoys, main: 1, feature: []int{1}, length: 112.0 + 10.0/60, decoys: 30,
		flags: map[int]Flag{1: FlagAssembled},
		damage: func(files fstest.MapFS) {
			wipeTable(files[vtsIFO].Data, 0xc8)
			wipeTable(files[vtsBUP].Data, 0xe0)
		}},
	{name: "chained film among fake title sets", disc: chainedAmongFakes, opened: true, main: 7, feature: []int{7, 3, 9, 2}, length: 120, decoys: 9,
		flags: map[int]Flag{11: FlagNoVideo | FlagDecoy, 7: FlagAssembled | FlagFirstPlay}},
	{name: "TV disc among sixty decoys", disc: tvAmongDecoys, main: 1, episodes: []int{5, 2, 3, 4}, playAll: 1, decoys: 40},
}

func (sc scenario) run(t *testing.T, d fxDisc) *Ranking {
	t.Helper()
	var o *Opened
	var r *Ranking
	if sc.opened {
		var err error
		if o, err = Open(d.writeFolder(t)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { o.Close() })
		r = o.Analyze()
	} else {
		files := d.files()
		if sc.damage != nil {
			sc.damage(files)
		}
		o = openFS(t, files)
		r = o.Disc.Analyze(RankOptions{})
		if m := o.Disc.MainTitle(); m == nil || m.Number != r.Main {
			t.Fatalf("MainTitle disagrees with the ranking: %+v", m)
		}
	}
	return r
}

func (sc scenario) check(t *testing.T, r *Ranking) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("%s\n%s", fmt.Sprintf(format, a...), r.Explain())
	}
	if r.Main != sc.main {
		fail("main title %d, want %d", r.Main, sc.main)
	}
	main := r.Candidate(r.Main)
	if !main.Flags.Has(FlagMain) || len(main.Reasons) == 0 || len(main.Evidence) == 0 {
		fail("main candidate unexplained: %+v", main)
	}
	if r.Why == nil || r.Why.Kind != "main" || len(r.Why.Because) == 0 || r.Why.Summary == "" || r.Confidence <= 0 || r.Confidence >= 1 {
		fail("no usable explanation of the pick: %+v, confidence %v", r.Why, r.Confidence)
	}
	if sc.level != "" && r.Level != sc.level {
		fail("confidence %v (%s), want %s", r.Confidence, r.Level, sc.level)
	}
	if len(r.Decoys) < sc.decoys {
		fail("%d decoys, want at least %d: %v", len(r.Decoys), sc.decoys, r.Decoys)
	}
	for n, want := range sc.class {
		if c := r.Candidate(n); c == nil || c.Class != want {
			t.Errorf("title %d class %q, want %q", n, c.Class, want)
		}
	}
	for n, want := range sc.flags {
		if c := r.Candidate(n); c == nil || !c.Flags.Has(want) {
			t.Errorf("title %d flags %s, want %s", n, flagsOf(c), want)
		}
	}
	for n, not := range sc.without {
		if c := r.Candidate(n); c == nil || c.Flags&not != 0 {
			t.Errorf("title %d flags %s, want none of %s", n, flagsOf(c), not)
		}
	}
	var eps []int
	for _, e := range r.Episodes {
		eps = append(eps, e.Title)
	}
	if !equalInts(eps, sc.episodes) || r.PlayAll != sc.playAll {
		fail("episodes %v with play all %d, want %v with %d", eps, r.PlayAll, sc.episodes, sc.playAll)
	}
	for i, want := range sc.starts {
		if got := r.Episodes[i].StartSec / 60; math.Abs(got-want) > 0.05 {
			fail("episode %d starts at %.2f min, want %.2f", i+1, got, want)
		}
	}
	if r.Feature == nil || len(r.Feature.Segments) == 0 {
		fail("no feature playlist")
	}
	if sc.feature != nil && !equalInts(r.Feature.Titles, sc.feature) {
		fail("the feature runs through titles %v, want %v", r.Feature.Titles, sc.feature)
	}
	if sc.length != 0 && math.Abs(r.Feature.Duration/60-sc.length) > 0.05 {
		fail("the feature lasts %.2f min, want %.2f", r.Feature.Duration/60, sc.length)
	}
	if sc.chapters != 0 && main.Chapters != sc.chapters {
		fail("%d chapters, want %d", main.Chapters, sc.chapters)
	}
	if sc.versions != nil {
		var got []string
		for _, v := range r.Versions {
			switch v.Kind {
			case "angle":
				got = append(got, fmt.Sprintf("angle %d %.0f", v.Angle, v.Duration/60))
			case "parental":
				got = append(got, fmt.Sprintf("parental %d-%d %.0f", v.Levels[0], v.Levels[len(v.Levels)-1], v.Duration/60))
			default:
				got = append(got, fmt.Sprintf("%s %d %.0f", v.Kind, v.Title, v.Duration/60))
			}
		}
		if strings.Join(got, "; ") != strings.Join(sc.versions, "; ") {
			fail("versions %q, want %q", got, sc.versions)
		}
	}
	if !strings.Contains(r.Explain(), "Main title: ") {
		t.Fatal("Explain says nothing")
	}
}

func TestScenarios(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { sc.check(t, sc.run(t, sc.disc())) })
	}
}

// contentOf names a title by what it plays, not by its number: its title
// set and the sectors of its cells in order.
func contentOf(r *Ranking, title int) string {
	c := r.Candidate(title)
	if c == nil || c.Summary() == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "vts%d", c.VTS)
	for _, tc := range c.Summary().Cells {
		fmt.Fprintf(&b, " %d-%d", tc.Cell.FirstSector, tc.Cell.LastSector)
	}
	return b.String()
}

func episodeContent(r *Ranking) []string {
	var out []string
	for _, e := range r.Episodes {
		out = append(out, fmt.Sprintf("%s@%.0f", contentOf(r, e.Title), e.StartSec))
	}
	return out
}

// renumber gives the titles of a disc new numbers: perm[i] is the old index
// of the title that becomes title i+1. Jumps by title number in first play,
// menus and post-commands are rewritten to match.
func renumber(d fxDisc, perm []int) fxDisc {
	newNum := map[int]int{}
	out := d
	out.titles = make([]fxTitle, len(perm))
	for i, old := range perm {
		out.titles[i] = d.titles[old]
		newNum[old+1] = i + 1
	}
	fix := func(cmds []Command) []Command {
		var res []Command
		for _, c := range cmds {
			if c[0] == 0x30 && c[1]&0x0f == 0x02 {
				c[5] = byte(newNum[int(c[5])])
			}
			res = append(res, c)
		}
		return res
	}
	fixPGCs := func(pgcs []fxPGC) []fxPGC {
		res := append([]fxPGC(nil), pgcs...)
		for i := range res {
			res[i].pre, res[i].post, res[i].cellCmds, res[i].buttons = fix(res[i].pre), fix(res[i].post), fix(res[i].cellCmds), fix(res[i].buttons)
		}
		return res
	}
	if d.firstPlay != nil {
		out.firstPlay = fix(d.firstPlay)
	}
	out.menus = fixPGCs(d.menus)
	out.vts = append([]fxVTS(nil), d.vts...)
	for i := range out.vts {
		out.vts[i].pgcs = fixPGCs(out.vts[i].pgcs)
		out.vts[i].menus = fixPGCs(out.vts[i].menus)
	}
	return out
}

// addDecoys adds n decoy titles to title set 1 of a disc, of the kinds
// structure protection uses, drawn at random.
func addDecoys(d fxDisc, rng *rand.Rand, n int) fxDisc {
	out := d
	out.vts = append([]fxVTS(nil), d.vts...)
	v := out.vts[0]
	v.pgcs = append([]fxPGC(nil), v.pgcs...)
	var pool []fxCell
	for _, p := range v.pgcs {
		pool = append(pool, p.cells...)
	}
	vtsTitles := 0
	for _, p := range v.pgcs {
		vtsTitles = max(vtsTitles, p.title)
	}
	ptt := map[int][]PTT{}
	for k, l := range v.ptt {
		ptt[k] = l
	}
	if len(ptt) > 0 {
		v.ptt = ptt
	}
	for i := 0; i < n && len(out.titles) < 99 && len(pool) > 0; i++ {
		var cells []fxCell
		switch rng.Intn(5) {
		case 0: // the disc's cells, shuffled
			for _, k := range rng.Perm(len(pool)) {
				cells = append(cells, pool[k])
			}
		case 1: // a few cells over and over
			for k := 0; k < 60; k++ {
				cells = append(cells, pool[(i+k%3)%len(pool)])
			}
		case 2: // hours outside the VOB
			cells = cellsAt(v.titleSectors+uint32(rng.Intn(100000)), 40, 6*min_, 100)
		case 3: // zero-length spam
			cells = cellsAt(uint32(rng.Intn(1000)), 200, 0, 1)
			for k := range cells {
				cells[k].unlisted = true
			}
		default: // hollow cells that claim hours
			cells = cellsAt(v.titleSectors-400+uint32(rng.Intn(50)), 30, 8*min_, 4)
			for k := range cells {
				cells[k].unlisted = rng.Intn(2) == 0
			}
		}
		vtsTitles++
		v.pgcs = append(v.pgcs, fxPGC{title: vtsTitles, cells: cells})
		if v.ptt != nil {
			v.ptt[vtsTitles] = []PTT{{len(v.pgcs), 1}}
		}
		out.titles = append(append([]fxTitle(nil), out.titles...), fxTitle{1, vtsTitles})
	}
	out.vts[0] = v
	return out
}

// However the titles are numbered, and however many decoys are thrown in,
// the engine picks the same film, the same episodes and the same feature.
func TestRankingInvariants(t *testing.T) {
	rounds := 12
	if testing.Short() {
		rounds = 3
	}
	for _, sc := range scenarios {
		if sc.opened {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			base := sc.disc()
			r0 := openFS(t, base.files()).Disc.Analyze(RankOptions{})
			want, wantEps := contentOf(r0, r0.Main), episodeContent(r0)
			rng := rand.New(rand.NewSource(int64(len(sc.name)) * 7919))
			for i := 0; i < rounds; i++ {
				d := base
				what := "renumbered"
				if i%2 == 1 && len(base.vts) == 1 {
					d = addDecoys(d, rng, 1+rng.Intn(30))
					what = "with decoys added"
				}
				d = renumber(d, rng.Perm(len(d.titles)))
				r := openFS(t, d.files()).Disc.Analyze(RankOptions{})
				if got := contentOf(r, r.Main); got != want {
					t.Fatalf("%s (round %d): the pick changed\n got %.80s\nwant %.80s\n%s", what, i, got, want, r.Explain())
				}
				got := episodeContent(r)
				if len(sc.starts) == 0 {
					// Episodes that are titles keep their viewing order.
					if strings.Join(got, "|") != strings.Join(wantEps, "|") {
						t.Fatalf("%s (round %d): the episodes changed\n%s", what, i, r.Explain())
					}
				} else {
					sort.Strings(got)
					w := append([]string(nil), wantEps...)
					sort.Strings(w)
					if strings.Join(got, "|") != strings.Join(w, "|") {
						t.Fatalf("%s (round %d): the episodes changed\n%s", what, i, r.Explain())
					}
				}
				if (r0.Feature == nil) != (r.Feature == nil) || (r.Feature != nil && math.Abs(r.Feature.Duration-r0.Feature.Duration) > 0.5) {
					t.Fatalf("%s (round %d): the feature changed: %+v\n%s", what, i, r.Feature, r.Explain())
				}
			}
		})
	}
}
