// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// startOf is when a title first plays the cell that holds a sector, and
// whether it plays it at all.
func (f *feat) startOf(sector uint32) (float64, bool) {
	i := sort.Search(len(f.spans), func(i int) bool { return f.spans[i].first > sector })
	// Spans are in order of their first sector; cells that lie over each
	// other are rare, so the one that holds the sector is at most a few back.
	for k := i - 1; k >= 0 && k >= i-8; k-- {
		if sector <= f.spans[k].last {
			return f.spans[k].start, true
		}
	}
	return 0, false
}

// detectEpisodes finds a TV disc's episodes: several clean titles of about
// the same length (within 15% of their median), and optionally one "play
// all" title that plays their video one after another. A disc whose episodes
// have no titles of their own, only a play-all title, has that title split
// (see splitEpisodes).
func (a *analysis) detectEpisodes() {
	r := a.r
	var pool []*feat
	for _, f := range a.feats {
		c := f.c
		if isDecoy(f) || c.Flags&(FlagDuplicate|FlagPart) != 0 || len(f.seq) == 0 || f.sectors == 0 {
			continue
		}
		// An episode is authored video: it plays forwards and plays nothing
		// twice.
		if f.back > 0 || f.repeat > 0.02 {
			continue
		}
		if c.Duration < minEpisodeSec || c.Duration > maxEpisodeSec {
			continue
		}
		pool = append(pool, f)
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].c.Duration < pool[j].c.Duration })
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
	// Titles that share most of their video are cuts of one thing, not
	// episodes. Lower title numbers are kept.
	sort.SliceStable(best, func(i, j int) bool { return best[i].c.TitleNum < best[j].c.TitleNum })
	var eps []*feat
	for _, f := range best {
		dup := false
		for _, e := range eps {
			if ov, _ := a.shared(f, e); ov*2 > min(f.sectors, e.sectors) {
				dup = true
				break
			}
		}
		if !dup {
			eps = append(eps, f)
		}
	}
	// A play-all title holds (nearly) all the video of every episode and
	// lasts about as long as all of them together.
	var playAll *feat
	if len(eps) >= 2 {
		var sum float64
		in := map[*feat]bool{}
		for _, e := range eps {
			sum += e.c.Duration
			in[e] = true
		}
		for _, f := range a.feats {
			if in[f] || isDecoy(f) || len(f.seq) == 0 || f.c.Duration < sum*(1-episodeTolerant) || f.c.Duration > sum*(1+episodeTolerant) {
				continue
			}
			covers := true
			for _, e := range eps {
				if ov, _ := a.shared(e, f); e.c.VTS != f.c.VTS || ov*10 < e.sectors*9 {
					covers = false
					break
				}
			}
			if covers {
				playAll = f
				break
			}
		}
	}
	if len(eps) < 2 || (playAll == nil && len(eps) < 3) {
		a.splitEpisodes() // two titles of a length are no TV disc
		return
	}
	// Viewing order: where each episode starts inside the play-all title,
	// else disc order (title set, first sector), else title number.
	pos := map[*feat]float64{}
	if playAll != nil {
		for _, e := range eps {
			pos[e], _ = playAll.startOf(e.seq[0].first)
		}
	}
	sort.SliceStable(eps, func(i, j int) bool {
		x, y := eps[i], eps[j]
		if playAll != nil && pos[x] != pos[y] {
			return pos[x] < pos[y]
		}
		if x.c.VTS != y.c.VTS {
			return x.c.VTS < y.c.VTS
		}
		if x.seq[0].first != y.seq[0].first {
			return x.seq[0].first < y.seq[0].first
		}
		return x.c.TitleNum < y.c.TitleNum
	})
	for i, e := range eps {
		e.c.add("episode", 0, FlagEpisode, "episode %d of %d: a run of titles of about the same length", i+1, len(eps))
		r.Episodes = append(r.Episodes, Episode{Index: i + 1, Title: e.c.TitleNum, VTS: e.c.VTS, PGC: e.c.PGC, Duration: e.c.Duration, FirstChapter: 1, Source: "titles"})
	}
	if playAll != nil {
		r.PlayAll = playAll.c.TitleNum
		playAll.c.add("playAll", 0, FlagPlayAll, "plays the %d episodes one after another", len(eps))
	}
}

// minSegmentSec is the shortest stretch of a play-all title that counts as
// something of its own; shorter ones (a recap, a card between episodes) go
// with the episode they come before.
const minSegmentSec = 90

type segment struct {
	start, dur float64
	pgc        int
}

// segments cuts a title where key changes from one cell to the next, and
// folds the short pieces into their neighbours.
func segments(ts *TitleSummary, key func(TitleCell) int) []segment {
	var raw []segment
	prev := -1
	for i, tc := range ts.Cells {
		dur := ts.Duration - tc.Start
		if i+1 < len(ts.Cells) {
			dur = ts.Cells[i+1].Start - tc.Start
		}
		if k := key(tc); k != prev || len(raw) == 0 {
			raw = append(raw, segment{start: tc.Start, pgc: tc.PGCN})
			prev = k
		}
		raw[len(raw)-1].dur += dur
	}
	var out []segment
	var carry *segment
	for _, s := range raw {
		if carry != nil {
			s.start, s.dur, s.pgc = carry.start, s.dur+carry.dur, carry.pgc
			carry = nil
		}
		if s.dur < minSegmentSec {
			c := s
			carry = &c
			continue
		}
		out = append(out, s)
	}
	if carry != nil && len(out) > 0 {
		out[len(out)-1].dur += carry.dur
	}
	return out
}

func evenEpisodes(segs []segment) bool {
	if len(segs) < 3 {
		return false
	}
	ds := make([]float64, len(segs))
	for i, s := range segs {
		ds[i] = s.dur
	}
	sort.Float64s(ds)
	med := ds[len(ds)/2]
	return ds[0] >= minEpisodeSec && ds[len(ds)-1] <= maxEpisodeSec && ds[0] >= med*(1-episodeTolerant) && ds[len(ds)-1] <= med*(1+episodeTolerant)
}

// splitEpisodes looks for a play-all title that is the only way to its
// episodes: one long title made of three or more stretches of an episode's
// length, each a program chain of its own or a VOB of its own. A film is one
// chain and, but for the odd logo, one VOB, so it doesn't split.
func (a *analysis) splitEpisodes() {
	r := a.r
	for i := 0; i < len(r.Candidates) && i < 3; i++ {
		f := a.byTitle[r.Candidates[i].TitleNum]
		if f == nil || isDecoy(f) || f.c.Duration < 3*minEpisodeSec || f.c.Flags.Has(FlagDuplicate) {
			continue
		}
		source := "pgcs"
		segs := segments(f.ts, func(tc TitleCell) int { return tc.PGCN })
		if !evenEpisodes(segs) {
			source = "vobs"
			segs = segments(f.ts, func(tc TitleCell) int { return tc.Cell.VOBID })
		}
		if !evenEpisodes(segs) {
			continue
		}
		for k, s := range segs {
			chapter := 1
			for n, t := range f.ts.Chapters {
				if t <= s.start+0.5 {
					chapter = n + 1
				}
			}
			r.Episodes = append(r.Episodes, Episode{Index: k + 1, Title: f.c.TitleNum, VTS: f.c.VTS, PGC: s.pgc, Duration: s.dur, StartSec: s.start, FirstChapter: chapter, Source: source})
		}
		r.PlayAll = f.c.TitleNum
		what := "program chains"
		if source == "vobs" {
			what = "VOBs"
		}
		f.c.add("playAll", 0, FlagPlayAll, "one title made of %d %s of an episode's length: it plays every episode, and none has a title of its own", len(segs), what)
		return
	}
}

// classify gives every title a class: the main title, episodes, decoys,
// then extras by length.
func (a *analysis) classify() {
	r := a.r
	main := a.byTitle[r.Main]
	for _, f := range a.feats {
		c := f.c
		alternate := false
		if main != nil && f != main && f.sectors > 0 && c.Duration >= minFeatureSec/2 {
			ov, _ := a.shared(f, main)
			alternate = ov*10 >= f.sectors*6
		}
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
		case c.Flags.Has(FlagPart):
			c.Class = ClassPart
		case c.Flags.Has(FlagDuplicate):
			c.Class = ClassDuplicate
		case alternate:
			c.Class = ClassAlternate
			c.add("alternateCut", 0, FlagAlternate, "mostly the same video as title %d: another cut of it", r.Main)
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
		c.add("picked", 0, 0, "picked as the main title: the highest score")
	}
}

// Version is one way the main feature can be seen.
type Version struct {
	Kind  string `json:"kind"`  // angle, parental or cut
	Title int    `json:"title"` // the title that plays it
	// Angle is the camera angle (kind angle), Levels the parental levels
	// that get this cut (kind parental).
	Angle    int     `json:"angle,omitempty"`
	Levels   []int   `json:"parentalLevels,omitempty"`
	Duration float64 `json:"durationSec"`
	Cells    int     `json:"cells"`
	// Shared is how much of this version's video the main title also plays,
	// from 0 to 1 (kind cut).
	Shared float64 `json:"shared,omitempty"`
	Label  string  `json:"label"`
}

// maxVersions bounds the list of versions.
const maxVersions = 16

func pathHash(ts *TitleSummary) uint64 {
	h := fnv.New64a()
	for _, c := range ts.Cells {
		h.Write([]byte{byte(c.PGCN >> 8), byte(c.PGCN), byte(c.CellN)})
	}
	return h.Sum64()
}

// versions enumerates the ways to see the main feature: each camera angle,
// each cut the parental levels choose between (seamless branching by
// parental block), and the other titles that are mostly the same video (a
// director's cut kept as a title of its own, sharing cells with the film).
func (a *analysis) versions() {
	r := a.r
	main := a.byTitle[r.Main]
	if main == nil {
		return
	}
	read := func(ro RegisterOptions) *TitleSummary {
		if main.ts.Path != "commands" && ro.Parental == 0 {
			return a.d.TitleAngle(r.Main, max(ro.Angle, 1))
		}
		if p := a.d.playedTitle(r.Main, ro, &a.fuel); p != nil {
			return p.ts
		}
		return nil
	}
	if n := main.c.Angles; n > 1 {
		for k := 1; k <= n && k <= 9; k++ {
			if ts := read(RegisterOptions{Angle: k}); ts != nil {
				r.Versions = append(r.Versions, Version{Kind: "angle", Title: r.Main, Angle: k, Duration: ts.Duration, Cells: len(ts.Cells), Label: fmt.Sprintf("Angle %d", k)})
			}
		}
	}
	v := a.d.VTS[main.c.VTS]
	blocks := false
	if v != nil {
		for _, e := range v.PGCs {
			if e.BlockType == 1 && (e.TitleNumber == 0 || e.TitleNumber == main.c.VTSTitle) {
				blocks = true
				break
			}
		}
	}
	if blocks && len(a.d.VMG.Parental) > 0 {
		type cut struct {
			ts     *TitleSummary
			levels []int
		}
		var cuts []*cut
		at := map[uint64]*cut{}
		for level := 1; level <= 8; level++ {
			ts := read(RegisterOptions{Parental: level})
			if ts == nil || len(ts.Cells) == 0 {
				continue
			}
			h := pathHash(ts)
			if at[h] == nil {
				at[h] = &cut{ts: ts}
				cuts = append(cuts, at[h])
			}
			at[h].levels = append(at[h].levels, level)
		}
		if len(cuts) > 1 {
			for _, c := range cuts {
				label := fmt.Sprintf("Parental level %d", c.levels[0])
				if n := len(c.levels); n > 1 {
					label = fmt.Sprintf("Parental levels %d to %d", c.levels[0], c.levels[n-1])
				}
				r.Versions = append(r.Versions, Version{Kind: "parental", Title: r.Main, Levels: c.levels, Duration: c.ts.Duration, Cells: len(c.ts.Cells), Label: label})
			}
		}
	}
	for i := range r.Candidates {
		c := &r.Candidates[i]
		if !c.Flags.Has(FlagAlternate) {
			continue
		}
		f := a.byTitle[c.TitleNum]
		ov, _ := a.shared(f, main)
		share := 0.0
		if f.sectors > 0 {
			share = math.Round(100*float64(ov)/float64(f.sectors)) / 100
		}
		if len(r.Versions) >= maxVersions {
			break
		}
		label := "Another cut"
		switch {
		case c.Duration > main.c.Duration+1:
			label = "Longer cut"
		case c.Duration < main.c.Duration-1:
			label = "Shorter cut"
		}
		r.Versions = append(r.Versions, Version{Kind: "cut", Title: c.TitleNum, Duration: c.Duration, Cells: c.Cells, Shared: share, Label: fmt.Sprintf("%s (title %d)", label, c.TitleNum)})
	}
}

// Playlist is exactly what to play for a feature, in order.
type Playlist struct {
	// Titles are the titles it runs through: one, unless the disc chains
	// several into one film.
	Titles   []int   `json:"titles"`
	Duration float64 `json:"durationSec"`
	// Source says how it was found: "chapters" (the title's chapter table),
	// "commands" (a dry run of the title's commands) or "chain" (several
	// titles the disc plays in a row).
	Source   string            `json:"source"`
	Segments []PlaylistSegment `json:"segments"`
}

// PlaylistSegment is a run of consecutive cells of one program chain.
type PlaylistSegment struct {
	Title       int     `json:"title"`
	VTS         int     `json:"vts"`
	PGC         int     `json:"pgc"`
	FirstCell   int     `json:"firstCell"`
	LastCell    int     `json:"lastCell"`
	StartSec    float64 `json:"startSec"`
	Duration    float64 `json:"durationSec"`
	FirstSector uint32  `json:"firstSector"`
	LastSector  uint32  `json:"lastSector"`
}

const maxPlaylistSegments = 2048

// feature lays out what to play for the main title: its cells as runs of
// consecutive cells, leaving out cells that point at sectors that don't
// exist (a player passes over them), and running on through the titles the
// disc chains to it.
func (a *analysis) feature() {
	r := a.r
	main := a.byTitle[r.Main]
	if main == nil {
		return
	}
	pl := &Playlist{Source: main.ts.Path}
	parts := []*feat{main}
	if main.c.Continues != 0 && main.c.Flags.Has(FlagAssembled) {
		if ch := a.chain(main); len(ch) > 1 && ch[1].c.PartOf == main.c.TitleNum {
			parts, pl.Source = ch, "chain"
		}
	}
	for _, f := range parts {
		pl.Titles = append(pl.Titles, f.c.TitleNum)
		size := a.facts(f.c.VTS).size
		ts := f.ts
		for i, tc := range ts.Cells {
			if badRange(tc.Cell, size) || tc.Cell.Duration > absurdCellSec {
				continue
			}
			dur := ts.Duration - tc.Start
			if i+1 < len(ts.Cells) {
				dur = ts.Cells[i+1].Start - tc.Start
			}
			if n := len(pl.Segments); n > 0 {
				s := &pl.Segments[n-1]
				if s.Title == f.c.TitleNum && s.PGC == tc.PGCN && tc.CellN > s.LastCell && tc.CellN <= s.LastCell+9 && tc.Cell.FirstSector > s.LastSector {
					s.LastCell, s.LastSector = tc.CellN, tc.Cell.LastSector
					s.Duration += dur
					pl.Duration += dur
					continue
				}
			}
			if len(pl.Segments) >= maxPlaylistSegments {
				break
			}
			pl.Segments = append(pl.Segments, PlaylistSegment{Title: f.c.TitleNum, VTS: f.c.VTS, PGC: tc.PGCN, FirstCell: tc.CellN, LastCell: tc.CellN,
				StartSec: pl.Duration, Duration: dur, FirstSector: tc.Cell.FirstSector, LastSector: tc.Cell.LastSector})
			pl.Duration += dur
		}
	}
	r.Feature = pl
}

// Decision is one thing the engine decided, how sure it is, and why.
type Decision struct {
	// Kind is what was decided: main (which title is the feature), feature
	// (what the feature is made of, when it isn't simply one title's
	// chapters), episodes, decoys or chapters (where the main title's
	// chapter times come from).
	Kind       string  `json:"kind"`
	Titles     []int   `json:"titles,omitempty"`
	Confidence float64 `json:"confidence"` // 0 to 1
	Level      string  `json:"level"`      // high, medium or low
	Summary    string  `json:"summary"`
	// Because and Against are the evidence for and against, strongest first.
	Because []Evidence `json:"because,omitempty"`
	Against []Evidence `json:"against,omitempty"`
	// RunnerUp is the best title that is something else than the pick, and
	// Margin how far behind it is, in points (kind main).
	RunnerUp int     `json:"runnerUp,omitempty"`
	Margin   float64 `json:"margin,omitempty"`
}

func levelOf(conf float64) string {
	switch {
	case conf >= 0.8:
		return "high"
	case conf >= 0.5:
		return "medium"
	}
	return "low"
}

func clampConf(v float64) float64 {
	if math.IsNaN(v) {
		return 0.05
	}
	return math.Round(100*min(max(v, 0.05), 0.99)) / 100
}

// decide writes the decisions with their confidence.
func (a *analysis) decide() {
	r := a.r
	main := r.Candidate(r.Main)
	mf := a.byTitle[r.Main]
	if main == nil || mf == nil {
		return
	}
	// The runner-up is the best title that is a different thing: not a
	// copy or another cut of the pick, not a part of it, not a decoy, and on
	// a TV disc not another episode.
	var rival *Candidate
	for i := range r.Candidates {
		c := &r.Candidates[i]
		if c.TitleNum == r.Main || c.Class == ClassDecoy || c.Class == ClassAlternate || c.PartOf == r.Main {
			continue
		}
		if f := a.byTitle[c.TitleNum]; f != nil && c.Flags.Has(FlagDuplicate) && sameSeq(f.seq, mf.seq) {
			continue
		}
		if len(r.Episodes) > 0 && c.Flags&(FlagEpisode|FlagPlayAll) != 0 && main.Flags&(FlagEpisode|FlagPlayAll) != 0 {
			continue
		}
		rival = c
		break
	}
	margin := main.Score
	if rival != nil {
		margin = main.Score - rival.Score
	}
	// A lead of a quarter of an hour of video is about two to one.
	conf := 1 - math.Exp(-max(margin, 0)/15)
	if main.Flags&(FlagMenuPlay|FlagFirstPlay) != 0 {
		conf = max(conf, 0.85) + 0.05
	}
	if main.Flags&(FlagOutOfRange|FlagRepeats|FlagUnlisted|FlagHollow|FlagOverlap|FlagCellSpam|FlagAbsurd|FlagNoVideo|FlagShuffled) != 0 {
		conf *= 0.7
	}
	elsewhere := func(t int) bool {
		if t == 0 || t == r.Main {
			return false
		}
		c := r.Candidate(t)
		return c == nil || (c.Class != ClassAlternate && c.Class != ClassDuplicate && c.PartOf != r.Main && c.Flags&(FlagEpisode|FlagPlayAll) == 0)
	}
	if elsewhere(r.MenuPlay) || elsewhere(r.FirstPlay) {
		conf *= 0.6
	}
	if main.Distinct < 5*60 {
		conf *= 0.5
	}
	conf = clampConf(conf)
	r.Confidence, r.Level = conf, levelOf(conf)
	why := Decision{Kind: "main", Titles: []int{r.Main}, Confidence: conf, Level: r.Level, Summary: fmt.Sprintf("Title %d is the main feature because %s.", r.Main, mainBecause(main))}
	for _, e := range main.Evidence {
		switch {
		case e.Points > 0:
			why.Because = append(why.Because, e)
		case e.Points < 0:
			why.Against = append(why.Against, e)
		}
	}
	sort.SliceStable(why.Because, func(i, j int) bool { return why.Because[i].Points > why.Because[j].Points })
	sort.SliceStable(why.Against, func(i, j int) bool { return why.Against[i].Points < why.Against[j].Points })
	if rival != nil {
		why.RunnerUp, why.Margin = rival.TitleNum, round1(margin)
		why.Summary += fmt.Sprintf(" Title %d comes second, %.1f points behind.", rival.TitleNum, margin)
	}
	r.Decisions = append(r.Decisions, why)
	r.Why = &r.Decisions[0]

	if pl := r.Feature; pl != nil && pl.Source != "chapters" {
		d := Decision{Kind: "feature", Titles: pl.Titles, Confidence: 0.75}
		if pl.Source == "chain" {
			d.Summary = fmt.Sprintf("The feature runs through %d titles (%s), which the disc plays one after another by itself: %s in all.", len(pl.Titles), joinInts(pl.Titles), fmtDur(pl.Duration))
		} else {
			d.Summary = fmt.Sprintf("Title %d's cells are put together by its commands, not by its chapter table: %s in %s.", r.Main, fmtDur(pl.Duration), pluralWord(len(pl.Segments), "run of cells", "runs of cells"))
		}
		// A dry run stands for one set of register values: a menu may set
		// others before the film starts.
		if main.Flags&(FlagMenuPlay|FlagFirstPlay) != 0 {
			d.Confidence = 0.85
		}
		for _, e := range main.Evidence {
			if e.Signal == "assembled" || e.Signal == "continues" {
				d.Because = append(d.Because, e)
			}
		}
		d.Confidence = clampConf(d.Confidence)
		d.Level = levelOf(d.Confidence)
		r.Decisions = append(r.Decisions, d)
	}
	if n := len(r.Episodes); n > 0 {
		d := Decision{Kind: "episodes"}
		lo, hi := math.Inf(1), 0.0
		for _, e := range r.Episodes {
			d.Titles = append(d.Titles, e.Title)
			lo, hi = min(lo, e.Duration), max(hi, e.Duration)
		}
		d.Because = append(d.Because, Evidence{Signal: "similarLength", Detail: fmt.Sprintf("%d stretches of %s to %s", n, fmtDur(lo), fmtDur(hi))})
		switch src := r.Episodes[0].Source; {
		case src == "titles" && r.PlayAll != 0:
			d.Confidence = 0.92
			d.Because = append(d.Because, Evidence{Signal: "playAll", Detail: fmt.Sprintf("title %d plays them all, which also gives their order", r.PlayAll)})
			d.Summary = fmt.Sprintf("A TV disc: %d episodes, each a title of its own, in the order play-all title %d gives.", n, r.PlayAll)
		case src == "titles":
			d.Confidence = min(0.6+0.05*float64(n-3), 0.8)
			d.Against = append(d.Against, Evidence{Signal: "noPlayAll", Detail: "no title plays them all, so their order is the order they lie on the disc"})
			d.Summary = fmt.Sprintf("Probably a TV disc: %d titles of about the same length, in disc order.", n)
		case src == "pgcs":
			d.Confidence = 0.7
			d.Titles = []int{r.PlayAll}
			d.Summary = fmt.Sprintf("Title %d plays %d episodes, each a program chain of its own; none has a title of its own.", r.PlayAll, n)
		default:
			d.Confidence = 0.5
			d.Titles = []int{r.PlayAll}
			d.Against = append(d.Against, Evidence{Signal: "oneChain", Detail: "it is one program chain: only its VOBs tell the episodes apart"})
			d.Summary = fmt.Sprintf("Title %d may be %d episodes played in a row: it is made of %d VOBs of about the same length.", r.PlayAll, n, n)
		}
		d.Confidence = clampConf(d.Confidence)
		d.Level = levelOf(d.Confidence)
		r.Decisions = append(r.Decisions, d)
	}
	if n := len(r.Decoys); n > 0 {
		d := Decision{Kind: "decoys", Titles: r.Decoys}
		least := 99
		counts := map[string]int{}
		for _, t := range r.Decoys {
			f := a.byTitle[t]
			least = min(least, decoySigns(f))
			for _, e := range f.c.Evidence {
				if e.Points < 0 {
					counts[e.Signal]++
				}
			}
		}
		var signals []string
		for s := range counts {
			signals = append(signals, s)
		}
		sort.Slice(signals, func(i, j int) bool {
			if counts[signals[i]] != counts[signals[j]] {
				return counts[signals[i]] > counts[signals[j]]
			}
			return signals[i] < signals[j]
		})
		for _, s := range signals {
			d.Because = append(d.Because, Evidence{Signal: s, Detail: pluralWord(counts[s], "decoy shows", "decoys show") + " this"})
		}
		d.Confidence = clampConf([]float64{0.7, 0.7, 0.85, 0.95}[min(least, 3)])
		d.Level = levelOf(d.Confidence)
		d.Summary = fmt.Sprintf("%s: structures no player would show as they stand.", pluralWord(n, "title is a decoy", "titles are decoys"))
		r.Decisions = append(r.Decisions, d)
	}
	ch := Decision{Kind: "chapters", Titles: []int{r.Main}}
	switch main.ChapterSource {
	case "chapterTable":
		ch.Confidence = 0.95
		ch.Summary = fmt.Sprintf("%s, from the chapter table and the play times of the cells.", pluralWord(main.Chapters, "chapter", "chapters"))
		if main.Flags.Has(FlagBadChapters) {
			ch.Confidence = 0.7
			ch.Against = append(ch.Against, Evidence{Signal: "badChapters", Detail: "some entries of the chapter table name nothing the title plays; they keep their number"})
		}
	case "programs":
		ch.Confidence = 0.6
		ch.Summary = fmt.Sprintf("%s, one for each program: the chapter table names nothing the title plays.", pluralWord(main.Chapters, "chapter", "chapters"))
	default:
		ch.Confidence = 0.3
		ch.Summary = "No chapters could be placed."
	}
	if s := main.summary; s != nil && s.Estimated {
		ch.Confidence *= 0.7
		ch.Against = append(ch.Against, Evidence{Signal: "timesEstimated", Detail: "some cells give no play time, so chapter times are worked out from the time map or the cells' sizes"})
	}
	ch.Confidence = clampConf(ch.Confidence)
	ch.Level = levelOf(ch.Confidence)
	r.Decisions = append(r.Decisions, ch)
	r.Why = &r.Decisions[0]
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}

// warn writes the disc-wide warnings.
func (a *analysis) warn() {
	r := a.r
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
	if w := r.Why; w != nil && w.RunnerUp != 0 && w.Margin < 5 {
		if next := r.Candidate(w.RunnerUp); next != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Close call: title %d scores %.1f against title %d's %.1f. The disc's own menu is the surer guide.", main.TitleNum, main.Score, next.TitleNum, next.Score))
		}
	}
	if main.Flags.Has(FlagOutOfRange) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("The main title (title %d) has cells outside its video files: the copy may be incomplete or damaged.", main.TitleNum))
	}
	if pl := r.Feature; pl != nil && pl.Source == "chain" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("The feature is cut into %d titles (%s) that the disc plays in a row.", len(pl.Titles), joinInts(pl.Titles)))
	}
	if len(r.Episodes) > 0 {
		s := fmt.Sprintf("Looks like a TV disc: %s", pluralWord(len(r.Episodes), "episode", "episodes"))
		if r.PlayAll != 0 {
			s += fmt.Sprintf(", with title %d playing them all", r.PlayAll)
		}
		r.Warnings = append(r.Warnings, s+".")
	}
	fixed := 0
	for _, rep := range a.d.Repairs {
		if rep.Fixed {
			fixed++
		}
	}
	if fixed > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("The disc's tables disagreed in %s, mended from the tables that agree.", pluralWord(fixed, "place", "places")))
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
	if c.Flags&(FlagShuffled|FlagRepeats|FlagOutOfRange|FlagUnlisted|FlagHollow|FlagOverlap) == 0 {
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
	fmt.Fprintf(&b, "Main title: %d (confidence %.0f%%, %s)\n", r.Main, 100*r.Confidence, r.Level)
	for _, d := range r.Decisions {
		fmt.Fprintf(&b, "Decided (%s, %.0f%%): %s\n", d.Kind, 100*d.Confidence, d.Summary)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	if len(r.Episodes) > 0 {
		var list []string
		for _, e := range r.Episodes {
			if e.Source == "titles" {
				list = append(list, fmt.Sprintf("%d", e.Title))
			} else {
				list = append(list, fmt.Sprintf("%d@%s", e.Title, fmtDur(e.StartSec)))
			}
		}
		fmt.Fprintf(&b, "Episodes in order: %s\n", strings.Join(list, ", "))
	}
	for _, v := range r.Versions {
		fmt.Fprintf(&b, "Version: %s, %s\n", v.Label, fmtDur(v.Duration))
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
