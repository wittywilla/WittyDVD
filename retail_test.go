// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// What ordinary retail film discs do that the ranking once took for
// something else. Each disc here is synthetic: it has the structure that
// images of physical discs showed the engine getting wrong, and nothing of
// theirs.

import (
	"strings"
	"testing"
)

// animatedShorts: a film, and four titles of ten to twenty-three cells of
// about half a second each, which the menus jump to: frame-by-frame
// animations, as a menu's transitions and games are authored.
func animatedShorts() fxDisc {
	pgcs := []fxPGC{{title: 1, cells: cellsAt(0, 21, 253, 1500)}}
	at := uint32(40000)
	for i, n := range []int{23, 10, 23, 10} {
		pgcs = append(pgcs, fxPGC{title: i + 2, cells: cellsAt(at, n, 0.5, 20)})
		at += 1000
	}
	return fxDisc{titles: titlesFor(1, 5), firstPlay: []Command{jumpTT(1)}, vts: []fxVTS{{titleSectors: 50000, audio: 2, pgcs: pgcs}}}
}

// An animation of very short cells is not cell spam, and a disc with some
// is not called copy-protected.
func TestShortAnimationsAreNotDecoys(t *testing.T) {
	r := openFS(t, animatedShorts().files()).Disc.Analyze(RankOptions{})
	if r.Main != 1 || len(r.Decoys) != 0 {
		t.Fatalf("main %d with decoys %v, want title 1 and none\n%s", r.Main, r.Decoys, r.Explain())
	}
	for n := 2; n <= 5; n++ {
		if c := r.Candidate(n); c == nil || c.Flags&(FlagCellSpam|FlagDecoy) != 0 {
			t.Errorf("title %d: %s", n, flagsOf(c))
		}
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "decoy") || strings.Contains(w, "protection") {
			t.Errorf("warned: %s", w)
		}
	}
	// Spam is still spam: cells of no length, or short cells padding a long title.
	sp := openFS(t, spamDisc().files()).Disc.Analyze(RankOptions{})
	if c := sp.Candidate(1); c == nil || !c.Flags.Has(FlagCellSpam|FlagDecoy) {
		t.Errorf("the padded title is no longer spam: %s", flagsOf(c))
	}
}

// filmWithFeaturettes: a film of an hour and a half and four featurettes of
// nine to eleven minutes, each in a title set of its own.
func filmWithFeaturettes() fxDisc {
	d := fxDisc{firstPlay: []Command{jumpTT(1)}}
	add := func(cells []fxCell, sectors uint32) {
		d.vts = append(d.vts, fxVTS{titleSectors: sectors, audio: 1, pgcs: []fxPGC{{title: 1, cells: cells}}})
		d.titles = append(d.titles, fxTitle{len(d.vts), 1})
	}
	add(cellsAt(0, 19, 281, 2000), 40000)
	for _, sec := range []float64{641, 595, 564, 536} {
		add(cellsAt(0, 4, sec/4, 1500), 7000)
	}
	return d
}

// Featurettes of about one length beside a film are extras, not a TV
// disc's episodes; the same titles without the film still are episodes.
func TestFeaturettesBesideAFilmAreNotEpisodes(t *testing.T) {
	d := filmWithFeaturettes()
	r := openFS(t, d.files()).Disc.Analyze(RankOptions{})
	if r.Main != 1 || len(r.Episodes) != 0 || r.PlayAll != 0 {
		t.Fatalf("main %d, episodes %+v, play all %d: want the film and no episodes\n%s", r.Main, r.Episodes, r.PlayAll, r.Explain())
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "TV disc") {
			t.Errorf("warned: %s", w)
		}
	}
	for n := 2; n <= 5; n++ {
		if c := r.Candidate(n); c == nil || c.Flags.Has(FlagEpisode) || c.Class == ClassEpisode {
			t.Errorf("title %d is called an episode: %+v", n, c)
		}
	}
	d.vts, d.titles, d.firstPlay = d.vts[1:], d.titles[:4], nil
	tv := openFS(t, d.files()).Disc.Analyze(RankOptions{})
	if len(tv.Episodes) != 4 {
		t.Errorf("without the film, %d episodes, want 4\n%s", len(tv.Episodes), tv.Explain())
	}
}
