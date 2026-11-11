// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// detectEpisodes finds a TV disc's episodes: several clean titles of about
// the same length (within 15% of their median), and optionally one "play
// all" title that plays their cells one after another.
func detectEpisodes(r *Ranking, feats map[int]*feat) {
	var pool []*feat
	for _, f := range feats {
		c := f.c
		if isDecoy(f) || c.Flags.Has(FlagDuplicate) || len(f.seq) == 0 {
			continue
		}
		if c.Duration < minEpisodeSec || c.Duration > maxEpisodeSec {
			continue
		}
		pool = append(pool, f)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].c.Duration < pool[j].c.Duration })
	// The largest window of titles within ±15% of its median.
	var best []*feat
	for i := range pool {
		for j := len(pool); j > i; j-- {
			g := pool[i:j]
			if len(g) <= len(best) {
				break
			}
			med := g[len(g)/2].c.Duration
			if g[0].c.Duration >= med*(1-episodeTolerant) && g[len(g)-1].c.Duration <= med*(1+episodeTolerant) {
				best = append([]*feat(nil), g...)
				break
			}
		}
	}
	// Titles that overlap each other a lot are cuts of one thing, not episodes.
	var eps []*feat
	for _, f := range best {
		dup := false
		for _, e := range eps {
			if overlap(f.set, e.set)*2 > min(len(f.set), len(e.set)) {
				dup = true
				break
			}
		}
		if !dup {
			eps = append(eps, f)
		}
	}
	if len(eps) < 2 {
		return
	}
	// A play-all title holds (nearly) every cell of every episode and lasts
	// about as long as all of them together.
	var sum float64
	for _, e := range eps {
		sum += e.c.Duration
	}
	var playAll *feat
	for _, f := range feats {
		if isDecoy(f) || len(f.seq) == 0 {
			continue
		}
		in := false
		for _, e := range eps {
			if e == f {
				in = true
			}
		}
		if in || f.c.Duration < sum*(1-episodeTolerant) || f.c.Duration > sum*(1+episodeTolerant) {
			continue
		}
		covers := true
		for _, e := range eps {
			if e.c.VTS != f.c.VTS || overlap(e.set, f.set)*10 < len(e.set)*9 {
				covers = false
				break
			}
		}
		if covers && (playAll == nil || f.c.TitleNum < playAll.c.TitleNum) {
			playAll = f
		}
	}
	if playAll == nil && len(eps) < 3 {
		return // two titles of a length is no TV disc
	}
	// Viewing order: where each episode starts inside the play-all title,
	// else disc order (title set, first sector), else title number.
	pos := map[*feat]int{}
	if playAll != nil {
		at := map[cellRange]int{}
		for i, k := range playAll.seq {
			if _, ok := at[k]; !ok {
				at[k] = i
			}
		}
		for _, e := range eps {
			pos[e] = at[e.seq[0]]
		}
	}
	sort.SliceStable(eps, func(i, j int) bool {
		a, b := eps[i], eps[j]
		if playAll != nil && pos[a] != pos[b] {
			return pos[a] < pos[b]
		}
		if a.c.VTS != b.c.VTS {
			return a.c.VTS < b.c.VTS
		}
		if a.seq[0].first != b.seq[0].first {
			return a.seq[0].first < b.seq[0].first
		}
		return a.c.TitleNum < b.c.TitleNum
	})
	for i, e := range eps {
		e.c.add(0, FlagEpisode, "episode %d of %d: a run of titles of about the same length", i+1, len(eps))
		r.Episodes = append(r.Episodes, Episode{Index: i + 1, Title: e.c.TitleNum, VTS: e.c.VTS, PGC: e.c.PGC, Duration: e.c.Duration})
	}
	if playAll != nil {
		r.PlayAll = playAll.c.TitleNum
		playAll.c.add(0, FlagPlayAll, "plays the %d episodes one after another", len(eps))
	}
}

// classify gives every title a class: the main title, episodes, decoys,
// then extras by length.
func classify(r *Ranking, feats map[int]*feat) {
	main := feats[r.Main]
	for _, f := range feats {
		c := f.c
		switch {
		case c.TitleNum == r.Main:
			c.Class = ClassMain
		case c.Flags.Has(FlagPlayAll):
			c.Class = ClassPlayAll
		case c.Flags.Has(FlagEpisode):
			c.Class = ClassEpisode
		case isDecoy(f):
			c.Class = ClassDecoy
			c.Flags |= FlagDecoy
			r.Decoys = append(r.Decoys, c.TitleNum)
		case c.Flags.Has(FlagDuplicate):
			c.Class = ClassDuplicate
		case main != nil && len(f.set) > 0 && overlap(f.set, main.set)*10 >= len(f.set)*6 && c.Duration >= minFeatureSec/2:
			c.Class = ClassAlternate
			c.Flags |= FlagAlternate
			c.Reasons = append(c.Reasons, fmt.Sprintf("mostly the same video as title %d: another cut of it", r.Main))
		case c.Flags.Has(FlagIntro) && c.Duration < trailerSec:
			c.Class = ClassIntro
		case c.Duration < stubSec:
			c.Class = ClassStub
		case c.Duration < trailerSec:
			c.Class = ClassTrailer
		case c.Duration < featuretteSec:
			c.Class = ClassFeaturette
		default:
			c.Class = ClassExtra
		}
	}
	sort.Ints(r.Decoys)
	if c := r.Candidate(r.Main); c != nil {
		c.Reasons = append(c.Reasons, "picked as the main title: the highest score")
	}
}

// warn writes the disc-wide warnings.
func (r *Ranking) warn(feats map[int]*feat) {
	main := r.Candidate(r.Main)
	if main == nil {
		return
	}
	if n := len(r.Decoys); n > 0 {
		kind := "Decoy titles like these are a structure-protection pattern (ARccOS, RipGuard and similar layouts) or a damaged index."
		if len(r.Candidates) >= 20 {
			kind = fmt.Sprintf("%d titles with %d decoys is the layout of structural copy protection (ARccOS, RipGuard and similar).", len(r.Candidates), n)
		}
		r.Warnings = append(r.Warnings, fmt.Sprintf("This disc has %s; picked title %d because %s. %s Nothing is decrypted: the decoys are only skipped.",
			pluralWord(n, "decoy title", "decoy titles"), main.TitleNum, mainBecause(main), kind))
	}
	if len(r.Candidates) > 1 {
		next := r.Candidates[1]
		mf, nf := feats[main.TitleNum], feats[next.TitleNum]
		same := mf != nil && nf != nil && len(nf.set) > 0 && overlap(mf.set, nf.set)*10 >= len(nf.set)*9
		if !same && main.Score-next.Score < 5 && next.Class != ClassDecoy && next.Class != ClassPlayAll && next.Class != ClassEpisode {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Close call: title %d scores %.1f against title %d's %.1f. The disc's own menu is the surer guide.", main.TitleNum, main.Score, next.TitleNum, next.Score))
		}
	}
	if main.Flags.Has(FlagOutOfRange) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("The main title (title %d) has cells outside its video files: the copy may be incomplete or damaged.", main.TitleNum))
	}
	if len(r.Episodes) > 0 {
		s := fmt.Sprintf("Looks like a TV disc: %s", pluralWord(len(r.Episodes), "episode", "episodes"))
		if r.PlayAll != 0 {
			s += fmt.Sprintf(", with title %d playing them all", r.PlayAll)
		}
		r.Warnings = append(r.Warnings, s+".")
	}
}

// mainBecause picks the strongest positive reasons for the main title.
func mainBecause(c *Candidate) string {
	var parts []string
	if c.Flags.Has(FlagMenuPlay) {
		parts = append(parts, "the menu's Play button starts it")
	}
	if c.Flags.Has(FlagFirstPlay) {
		parts = append(parts, "the disc starts it by itself")
	}
	parts = append(parts, fmt.Sprintf("it has the most distinct video (%s)", fmtDur(c.Distinct)))
	if c.Flags&(FlagShuffled|FlagRepeats|FlagOutOfRange) == 0 {
		parts = append(parts, "it plays its cells in order with nothing repeated or out of range")
	}
	return strings.Join(parts, ", ")
}

// Explain words a ranking for people: the pick, the top candidates with
// their reasons, and the warnings.
func (r *Ranking) Explain() string {
	if r == nil || len(r.Candidates) == 0 {
		return "No title to rank."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Main title: %d\n", r.Main)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	if len(r.Episodes) > 0 {
		var list []string
		for _, e := range r.Episodes {
			list = append(list, fmt.Sprintf("%d", e.Title))
		}
		fmt.Fprintf(&b, "Episodes in order: %s\n", strings.Join(list, ", "))
	}
	for i, c := range r.Candidates {
		if i >= 10 {
			fmt.Fprintf(&b, "... and %d more\n", len(r.Candidates)-i)
			break
		}
		fmt.Fprintf(&b, "%2d. title %d (VTS %d, PGC %d) %s score %.1f [%s]", i+1, c.TitleNum, c.VTS, c.PGC, fmtDur(c.Duration), round1(c.Score), c.Class)
		if fl := c.Flags.String(); fl != "" {
			fmt.Fprintf(&b, " {%s}", fl)
		}
		b.WriteByte('\n')
		for _, why := range c.Reasons {
			fmt.Fprintf(&b, "      %s\n", why)
		}
	}
	return b.String()
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
