// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

const min_ = 60.0

// simpleMovie: a 2-hour feature, a trailer and a featurette.
func simpleMovie() fxDisc {
	return fxDisc{
		titles: titlesFor(1, 3),
		vts: []fxVTS{{titleSectors: 60000, audio: 2, subp: 3, pgcs: []fxPGC{
			{title: 1, cells: cellsAt(0, 30, 4*min_, 1000)},
			{title: 2, cells: cellsAt(40000, 1, 2*min_, 1000)},
			{title: 3, cells: cellsAt(41000, 4, 5*min_, 1000)},
		}}},
	}
}

// decoy99: 99 titles, one real one (title 37) and 98 decoys of the kinds
// structure protection uses: shuffled and rotated copies of the feature,
// cells far outside the VOB, the same few cells replayed for hours, and
// zero-length cell spam.
func decoy99() fxDisc {
	const real = 37
	feature := cellsAt(0, 28, 4*min_, 1000) // 1:52:00
	var pgcs []fxPGC
	for t := 1; t <= 99; t++ {
		var cells []fxCell
		switch {
		case t == real:
			cells = feature
		case t%5 == 0: // reversed feature
			for i := len(feature) - 1; i >= 0; i-- {
				cells = append(cells, feature[i])
			}
		case t%5 == 1: // rotated feature: a single jump back
			k := 1 + t%27
			cells = append(append(cells, feature[k:]...), feature[:k]...)
		case t%5 == 2: // longer than the feature, all outside the VOB
			cells = cellsAt(500000+uint32(t)*100, 24, 5*min_, 50)
		case t%5 == 3: // three cells replayed thirty times
			for i := 0; i < 30; i++ {
				cells = append(cells, feature[t%25], feature[t%25+1], feature[t%25+2])
			}
		default: // zero-length spam
			cells = cellsAt(uint32(t)*10, 120, 0, 1)
		}
		pgcs = append(pgcs, fxPGC{title: t, cells: cells})
	}
	return fxDisc{titles: titlesFor(1, 99), vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: pgcs}}}
}

// shuffledDecoy: title 1 plays the feature's cells (title 2) in scrambled
// order plus a few minutes more, so it has more distinct video than the
// feature itself.
func shuffledDecoy() fxDisc {
	feature := cellsAt(0, 20, 5*min_, 1000)
	order := []int{7, 2, 15, 0, 11, 19, 4, 9, 13, 1, 17, 6, 3, 18, 10, 5, 14, 8, 16, 12}
	var decoy []fxCell
	for _, i := range order {
		decoy = append(decoy, feature[i])
	}
	decoy = append(decoy, cellsAt(20000, 2, 2*min_, 1000)...)
	return fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 30000, audio: 1, pgcs: []fxPGC{
		{title: 1, cells: decoy},
		{title: 2, cells: feature},
	}}}}
}

// tvDisc: a play-all title and four episodes. The episode titles aren't
// numbered in viewing order: title 5 is the first episode on the disc.
func tvDisc() fxDisc {
	ep5 := cellsAt(0, 6, 3.5*min_, 1000)     // 21:00
	ep2 := cellsAt(6000, 6, 3.7*min_, 1000)  // 22:12
	ep3 := cellsAt(12000, 6, 3.9*min_, 1000) // 23:24
	ep4 := cellsAt(18000, 6, 4*min_, 1000)   // 24:00
	var all []fxCell
	for _, e := range [][]fxCell{ep5, ep2, ep3, ep4} {
		all = append(all, e...)
	}
	return fxDisc{titles: titlesFor(1, 6), vts: []fxVTS{{titleSectors: 30000, audio: 1, subp: 1, pgcs: []fxPGC{
		{title: 1, cells: all, programs: []int{1, 7, 13, 19}},
		{title: 2, cells: ep2},
		{title: 3, cells: ep3},
		{title: 4, cells: ep4},
		{title: 5, cells: ep5},
		{title: 6, cells: cellsAt(25000, 1, 1.5*min_, 1000)}, // a trailer
	}}}}
}

// menuDisc: title 1 is a long, clean title, but first play goes to the
// root menu whose only button plays title 3.
func menuDisc(fpTitle int) fxDisc {
	d := fxDisc{
		titles: titlesFor(1, 3),
		vts: []fxVTS{{titleSectors: 60000, audio: 1, pgcs: []fxPGC{
			{title: 1, cells: cellsAt(0, 25, 4*min_, 1000)},     // 1:40:00
			{title: 2, cells: cellsAt(26000, 1, 3*min_, 1000)},  // trailer
			{title: 3, cells: cellsAt(30000, 18, 5*min_, 1000)}, // 1:30:00
		}}},
		firstPlay: []Command{jumpRootMenu()},
		menus:     []fxPGC{{menu: 3, cells: []fxCell{{first: 0, last: 0, dur: 10, still: 255}}}},
	}
	if fpTitle != 0 {
		d.firstPlay = []Command{jumpTT(fpTitle)}
	}
	return d
}

// spamDisc: title 1 hides ten long cells among 240 sub-second ones; title 2
// claims a nine-hour cell; title 3 is the feature.
func spamDisc() fxDisc {
	spam := cellsAt(40000, 240, 0.4, 2)
	spam = append(spam, cellsAt(41000, 10, 12*min_, 500)...)
	return fxDisc{titles: titlesFor(1, 3), vts: []fxVTS{{titleSectors: 60000, audio: 1, pgcs: []fxPGC{
		{title: 1, cells: spam},
		{title: 2, cells: []fxCell{{first: 50000, last: 50999, dur: 9 * 3600}}},
		{title: 3, cells: cellsAt(0, 25, 4*min_, 1000)},
	}}}}
}

func TestRankScenarios(t *testing.T) {
	cases := []struct {
		name     string
		disc     fxDisc
		main     int
		decoys   int // at least this many
		class    map[int]string
		flags    map[int]Flag
		episodes []int
		playAll  int
	}{
		{name: "simple movie", disc: simpleMovie(), main: 1,
			class: map[int]string{1: ClassMain, 2: ClassTrailer, 3: ClassFeaturette}},
		{name: "99-title decoy disc", disc: decoy99(), main: 37, decoys: 98,
			flags: map[int]Flag{5: FlagShuffled, 6: FlagShuffled, 7: FlagOutOfRange, 8: FlagRepeats, 9: FlagCellSpam}},
		{name: "shuffled-cell decoy", disc: shuffledDecoy(), main: 2, decoys: 1,
			flags: map[int]Flag{1: FlagShuffled | FlagDecoy}},
		{name: "TV disc", disc: tvDisc(), main: 1, episodes: []int{5, 2, 3, 4}, playAll: 1,
			class: map[int]string{2: ClassEpisode, 5: ClassEpisode, 6: ClassTrailer}},
		{name: "first play jumps to a non-obvious title", disc: menuDisc(3), main: 3,
			flags: map[int]Flag{3: FlagFirstPlay}, class: map[int]string{1: ClassExtra}},
		{name: "zero-length cell spam", disc: spamDisc(), main: 3, decoys: 2,
			flags: map[int]Flag{1: FlagCellSpam | FlagDecoy, 2: FlagAbsurd | FlagDecoy}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := openFS(t, tc.disc.files())
			r := o.Disc.Analyze(RankOptions{})
			if r.Main != tc.main {
				t.Fatalf("main title %d, want %d\n%s", r.Main, tc.main, r.Explain())
			}
			if m := o.Disc.MainTitle(); m == nil || m.Number != tc.main {
				t.Fatalf("MainTitle disagrees with the ranking: %+v", m)
			}
			if len(r.Decoys) < tc.decoys {
				t.Fatalf("%d decoys, want at least %d: %v\n%s", len(r.Decoys), tc.decoys, r.Decoys, r.Explain())
			}
			for n, want := range tc.class {
				if c := r.Candidate(n); c == nil || c.Class != want {
					t.Errorf("title %d class %q, want %q", n, c.Class, want)
				}
			}
			for n, want := range tc.flags {
				if c := r.Candidate(n); c == nil || !c.Flags.Has(want) {
					t.Errorf("title %d flags %s, want %s", n, flagsOf(c), want)
				}
			}
			if tc.episodes != nil {
				var got []int
				for _, e := range o.Disc.Episodes() {
					got = append(got, e.Title)
				}
				if !equalInts(got, tc.episodes) {
					t.Fatalf("episodes %v, want %v\n%s", got, tc.episodes, r.Explain())
				}
				if r.PlayAll != tc.playAll {
					t.Fatalf("play all %d, want %d", r.PlayAll, tc.playAll)
				}
			} else if len(r.Episodes) > 0 {
				t.Fatalf("not a TV disc, but found episodes %+v", r.Episodes)
			}
			main := r.Candidate(r.Main)
			if !main.Flags.Has(FlagMain) || len(main.Reasons) == 0 {
				t.Fatalf("main candidate unexplained: %+v", main)
			}
			if !strings.Contains(o.Disc.Explain(), "Main title: ") {
				t.Fatal("Explain says nothing")
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDecoyWarning(t *testing.T) {
	r := openFS(t, decoy99().files()).Disc.Analyze(RankOptions{})
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "98 decoy titles") && strings.Contains(w, "picked title 37 because") && strings.Contains(w, "Nothing is decrypted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no decoy warning: %q", r.Warnings)
	}
}

// The menu's default button plays title 3: only an opened disc can see it,
// from the NAV pack of the menu VOB. The IFOs alone still pick title 1.
func TestMenuPlayButton(t *testing.T) {
	d := menuDisc(0)
	d.menus = []fxPGC{{menu: 3, buttons: []Command{jumpTT(3)}}}
	o, err := Open(d.writeFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if m := o.Disc.MainTitle(); m == nil || m.Number != 1 {
		t.Fatalf("from the IFOs alone the longest clean title wins: %+v", m)
	}
	r := o.Analyze()
	if r.MenuPlay != 3 || r.Main != 3 {
		t.Fatalf("menu play %d, main %d\n%s", r.MenuPlay, r.Main, r.Explain())
	}
	if c := r.Candidate(3); !c.Flags.Has(FlagMenuPlay | FlagMenuTarget) {
		t.Fatalf("flags %s", flagsOf(c))
	}
	if !r.ButtonsRead || r.Level != "high" {
		t.Fatalf("buttons read %v, confidence %v %s", r.ButtonsRead, r.Confidence, r.Level)
	}
	// Titles 1 and 2 are on the disc, and nothing leads to them.
	for _, n := range []int{1, 2} {
		if c := r.Candidate(n); !c.Flags.Has(FlagUnreachable) {
			t.Errorf("title %d flags %s, want unreachable", n, flagsOf(c))
		}
	}
}

func TestCorruptIFOGoodBUP(t *testing.T) {
	good := simpleMovie()
	bad := simpleMovie()
	// The IFO parses, but the feature's cells point far outside the VOB.
	bad.vts[0].pgcs[0].cells = cellsAt(900000, 30, 4*min_, 1000)
	files := good.files()
	files["VIDEO_TS/VTS_01_0.IFO"] = &fstest.MapFile{Data: encodeVTS(bad.vts[0])}
	o := openFS(t, files)
	var rep IFOReport
	for _, x := range o.Disc.IFOReports {
		if x.VTS == 1 {
			rep = x
		}
	}
	if rep.Used != "BUP" || !rep.Mismatch {
		t.Fatalf("report %+v", rep)
	}
	r := o.Disc.Analyze(RankOptions{})
	if r.Main != 1 || r.Candidates[0].Flags&FlagFromBackup == 0 || r.Candidates[0].Flags&FlagOutOfRange != 0 {
		t.Fatalf("main %d %s\n%s", r.Main, flagsOf(&r.Candidates[0]), r.Explain())
	}

	// A wrecked header: only the backup parses.
	files["VIDEO_TS/VTS_01_0.IFO"] = &fstest.MapFile{Data: make([]byte, 4096)}
	o = openFS(t, files)
	if m := o.Disc.MainTitle(); m == nil || m.Number != 1 || m.Duration != 30*4*min_ {
		t.Fatalf("main from the backup: %+v", m)
	}
	// Identical copies: the IFO, no mismatch.
	o = openFS(t, good.files())
	for _, x := range o.Disc.IFOReports {
		if x.Used != "IFO" || x.Mismatch {
			t.Fatalf("clean disc report %+v", x)
		}
	}
}

func TestLoopingChainAndBadChapters(t *testing.T) {
	d := &Disc{VMG: &VMG{Titles: []TitleSearch{{Number: 1, VTS: 1, VTSTitle: 1}, {Number: 2, VTS: 1, VTSTitle: 2}}}, VTS: map[int]*VTS{1: {
		TitleVOBSectors: 100000,
		PGCs: []PGCEntry{
			{Entry: true, TitleNumber: 1, PGC: &PGC{NextPGCN: 2, Cells: []Cell{{FirstSector: 0, LastSector: 999, Duration: 3000}}}},
			{PGC: &PGC{NextPGCN: 1, Cells: []Cell{{FirstSector: 1000, LastSector: 1999, Duration: 3000}}}},
			{Entry: true, TitleNumber: 2, PGC: &PGC{Cells: []Cell{{FirstSector: 5000, LastSector: 9999, Duration: 5400}}}},
		},
		PTT: [][]PTT{{{1, 1}, {9, 1}}, {{3, 1}}},
	}}}
	r := d.Analyze(RankOptions{})
	c := r.Candidate(1)
	if !c.Flags.Has(FlagLoop|FlagBadChapters) || c.Class != ClassDecoy || r.Main != 2 {
		t.Fatalf("main %d, title 1 %s %s\n%s", r.Main, c.Class, flagsOf(c), r.Explain())
	}
}

func TestFlagJSON(t *testing.T) {
	b, err := json.Marshal(Candidate{TitleNum: 1, Flags: FlagMain | FlagDecoy})
	if err != nil || !strings.Contains(string(b), `"flags":["main","decoy"]`) {
		t.Fatalf("%s %v", b, err)
	}
	var c Candidate
	if err := json.Unmarshal(b, &c); err != nil || c.Flags != FlagMain|FlagDecoy {
		t.Fatalf("%v %v", c.Flags, err)
	}
}

func TestRankEmpty(t *testing.T) {
	var nilDisc *Disc
	if r := nilDisc.Analyze(RankOptions{}); len(r.Candidates) != 0 {
		t.Fatal("candidates from nothing")
	}
	d := &Disc{VMG: &VMG{}, VTS: map[int]*VTS{}}
	if d.MainTitle() != nil || d.RankTitles() != nil || d.Episodes() != nil {
		t.Fatal("an empty disc has no main title")
	}
}

// The scorer reads whatever the index files say, so no input may crash it
// or keep it from coming back.
func FuzzRankTitles(f *testing.F) {
	for _, d := range []fxDisc{simpleMovie(), shuffledDecoy(), tvDisc(), menuDisc(3), spamDisc()} {
		m := d.files()
		f.Add(m["VIDEO_TS/VIDEO_TS.IFO"].Data, m["VIDEO_TS/VTS_01_0.IFO"].Data)
	}
	f.Add(testVMG(), testVTS([3]int{2, 1, 1<<2 | 1}, [3]int{1, 2, 3<<2 | 1}))
	f.Fuzz(func(t *testing.T, vmgB, vtsB []byte) {
		vmg, err := ParseVMG(vmgB)
		if err != nil {
			return
		}
		d := &Disc{VMG: vmg, VTS: map[int]*VTS{}}
		if v, err := ParseVTS(vtsB, 1); err == nil {
			d.VTS[1] = v
			cp := append([]byte(nil), vtsB...)
			if len(cp) > 0x10 {
				binary.BigEndian.PutUint32(cp[0x0c:], 0) // also with no VOB size
			}
			if v2, err := ParseVTS(cp, 2); err == nil {
				d.VTS[2] = v2
			}
		}
		r := d.Analyze(RankOptions{MenuPlay: 1, MenuTargets: []int{1, 2, 99}, FileSectors: map[int]uint32{1: 10}})
		_ = r.Explain()
		_ = d.MainTitle()
		_ = d.Episodes()
		if len(r.Candidates) > 0 && r.Candidate(r.Main) == nil {
			t.Fatal("main title not among the candidates")
		}
	})
}
