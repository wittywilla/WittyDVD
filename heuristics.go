// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Title mapping heuristics: which title is the feature, which are episodes,
// decoys or extras. Everything here works from the unencrypted structure of
// the disc (IFO/BUP tables, PGC chains, cell timings and sector addresses,
// VOB sizes) and the disc's own navigation commands. Nothing is decrypted and
// nothing here reads, derives or guesses CSS keys: a scrambled disc is still
// refused elsewhere, whatever these scores say.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"strings"
)

// Flag marks what the engine found out about a title.
type Flag uint32

const (
	FlagMain        Flag = 1 << iota // the engine's pick
	FlagDecoy                        // structure-protection decoy or junk
	FlagMenuTarget                   // a menu or first-play command jumps here
	FlagMenuPlay                     // the first menu's default ("Play") button plays this
	FlagFirstPlay                    // first play reaches this long title by itself
	FlagIntro                        // a short title first play passes on its way
	FlagEpisode                      // one episode of a TV disc
	FlagPlayAll                      // plays the episodes one after another
	FlagDuplicate                    // the same cells in the same order as a lower-numbered title
	FlagShuffled                     // another title's cells in a scrambled order
	FlagOutOfRange                   // cells outside the title VOB
	FlagRepeats                      // replays the same cells many times
	FlagCellSpam                     // many zero or sub-second cells
	FlagLoop                         // its PGC chain links back on itself
	FlagAbsurd                       // impossible cell durations
	FlagBadChapters                  // chapter table names PGCs or programs that don't exist
	FlagFromBackup                   // its title set was read from the .BUP copy
	FlagAlternate                    // mostly the main title's video (another cut)
	FlagUnlisted                     // cells the cell address table doesn't hold
	FlagHollow                       // cells that claim far more time than their sectors can hold
	FlagOverlap                      // cells that lie over each other on the disc
	FlagUnreachable                  // no command or button on the disc leads to it
	FlagAssembled                    // its cells are put together by commands, not its chapter table
	FlagPart                         // one part of a feature that runs on through several titles
	FlagNoVideo                      // its title set has no video files
	FlagTruncated                    // lists more cells than were read
)

var flagNames = []string{"main", "decoy", "menuTarget", "menuPlay", "firstPlay", "intro", "episode", "playAll", "duplicate", "shuffled", "outOfRange", "repeats", "cellSpam", "loop", "absurd", "badChapters", "fromBackup", "alternate", "unlisted", "hollow", "overlap", "unreachable", "assembled", "part", "noVideo", "truncated"}

// Has reports whether every flag in g is set.
func (f Flag) Has(g Flag) bool { return f&g == g }

// Names lists the flags set, in a fixed order.
func (f Flag) Names() []string {
	out := []string{}
	for i, n := range flagNames {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

func (f Flag) String() string { return strings.Join(f.Names(), ",") }

// MarshalJSON writes the flags as a list of names.
func (f Flag) MarshalJSON() ([]byte, error) { return json.Marshal(f.Names()) }

// UnmarshalJSON reads a list of flag names; unknown names are ignored.
func (f *Flag) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	*f = 0
	for _, n := range names {
		for i, k := range flagNames {
			if k == n {
				*f |= 1 << i
			}
		}
	}
	return nil
}

// Title classes.
const (
	ClassMain       = "main"
	ClassPlayAll    = "playAll"
	ClassEpisode    = "episode"
	ClassDecoy      = "decoy"
	ClassDuplicate  = "duplicate"
	ClassAlternate  = "alternate"
	ClassPart       = "part"
	ClassIntro      = "intro"
	ClassStub       = "stub"
	ClassTrailer    = "trailer"
	ClassFeaturette = "featurette"
	ClassExtra      = "extra"
)

// Evidence is one thing the engine weighed for a title: which signal, how
// many points it was worth (in minutes of video; 0 for a remark) and, in
// words, what was seen. Signal names are stable, for a server to key on.
type Evidence struct {
	Signal string  `json:"signal"`
	Points float64 `json:"points"`
	Detail string  `json:"detail"`
}

// Candidate is one title as the engine scored it.
type Candidate struct {
	TitleNum int      `json:"title"`
	VTS      int      `json:"vts"`
	VTSTitle int      `json:"vtsTitle"`
	PGC      int      `json:"pgc"` // the title's first PGC
	Duration float64  `json:"durationSec"`
	Distinct float64  `json:"distinctSec"` // each readable cell counted once
	Chapters int      `json:"chapters"`
	Cells    int      `json:"cells"`
	Score    float64  `json:"score"`
	Class    string   `json:"class"`
	Flags    Flag     `json:"flags"`
	Reasons  []string `json:"reasons"`
	// Evidence is Reasons in a form a program can read: one entry per
	// signal, with its points.
	Evidence []Evidence `json:"evidence,omitempty"`
	// Path says how the title's cells were found: "chapters" (its chapter
	// table and next-PGC links) or "commands" (a dry run of its own
	// commands, where that shows more of the film).
	Path string `json:"path,omitempty"`
	// ChapterSource says where the chapter times come from (see
	// TitleSummary.ChapterSource).
	ChapterSource string `json:"chapterSource,omitempty"`
	// Angles is how many camera angles the title has.
	Angles int `json:"angles,omitempty"`
	// Continues is the title the disc goes on to by itself when this one
	// ends, and PartOf the title a feature in several parts starts at.
	Continues int `json:"continues,omitempty"`
	PartOf    int `json:"partOf,omitempty"`

	summary *TitleSummary
}

// Summary is the flattened title the candidate was scored from.
func (c Candidate) Summary() *TitleSummary { return c.summary }

// Episode is one episode of a TV disc, in viewing order.
type Episode struct {
	Index    int     `json:"index"` // 1-based viewing order
	Title    int     `json:"title"`
	VTS      int     `json:"vts"`
	PGC      int     `json:"pgc"`
	Duration float64 `json:"durationSec"`
	// StartSec and FirstChapter say where the episode begins inside Title.
	// They are 0 and 1 for a title that is one episode; a play-all title
	// that is the only way to the episodes is split, and they say where.
	StartSec     float64 `json:"startSec"`
	FirstChapter int     `json:"firstChapter"`
	// Source says how the episode was found: "titles" (a title of its own),
	// "pgcs" (one program chain of a play-all title) or "vobs" (one VOB of a
	// play-all title).
	Source string `json:"source"`
}

// Ranking is the engine's full answer for a disc.
type Ranking struct {
	Candidates []Candidate `json:"candidates"` // best first
	Main       int         `json:"main"`       // 0 when the disc has no title
	PlayAll    int         `json:"playAll,omitempty"`
	Episodes   []Episode   `json:"episodes,omitempty"`
	Decoys     []int       `json:"decoys,omitempty"`
	FirstPlay  int         `json:"firstPlay,omitempty"` // long title first play reaches
	MenuPlay   int         `json:"menuPlay,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
	// Confidence is how sure the pick of the main title is, from 0 to 1, and
	// Level the same in a word: high, medium or low.
	Confidence float64 `json:"confidence"`
	Level      string  `json:"level,omitempty"`
	// Why is the pick of the main title explained: what spoke for it, what
	// against, and who came second. Decisions holds it together with the
	// other things decided (episodes, decoys, chapters, the feature's parts).
	Why       *Decision  `json:"why,omitempty"`
	Decisions []Decision `json:"decisions,omitempty"`
	// Feature is exactly what to play for the main feature: its cells in
	// order, across titles when the disc chains several into one film.
	Feature *Playlist `json:"feature,omitempty"`
	// Versions lists the ways the main feature can be seen: camera angles,
	// the cuts parental levels choose, and other titles that are mostly the
	// same video (another cut).
	Versions []Version `json:"versions,omitempty"`
	// Reachable lists the titles the disc's own commands can get to from
	// first play, by a static reading of every command. ButtonsRead says the
	// menus' button commands were part of that reading; without them a
	// title may be missing here only because a button names it.
	Reachable   []int `json:"reachable,omitempty"`
	ButtonsRead bool  `json:"buttonsRead,omitempty"`
}

// Candidate returns the scored title by number, or nil.
func (r *Ranking) Candidate(title int) *Candidate {
	for i := range r.Candidates {
		if r.Candidates[i].TitleNum == title {
			return &r.Candidates[i]
		}
	}
	return nil
}

// Weights tune the scoring. Scores are in "minutes of distinct video": a
// weight of 15 is worth as much as a quarter of an hour of unique cells.
type Weights struct {
	DistinctPerMinute float64 // per minute of distinct, readable video
	OutOfRangeAny     float64 // any cell starts outside the title VOB
	OutOfRangeShare   float64 // times the share of such cells
	Repeats           float64 // times the share of play time spent on repeats
	BackJumpAny       float64 // any jump back on the disc between cells
	BackJumpShare     float64 // times the share of steps that jump back
	Shuffled          float64 // another title's cells in scrambled order
	Duplicate         float64 // identical to a lower-numbered title (tie breaker)
	CellSpam          float64 // most cells are zero or under a second long
	AbsurdCell        float64 // a cell claims more than four hours
	Loop              float64 // the PGC chain links back on itself
	BadChapters       float64 // the chapter table points nowhere
	RegularChapters   float64 // three or more chapters of sensible, even length
	AudioMatch        float64 // its title set has the most audio streams
	SubpMatch         float64 // its title set has the most subtitle streams
	LargestVTS        float64 // its title set holds the most video
	FirstPlay         float64 // first play reaches it by itself
	MenuReference     float64 // a menu or first-play command jumps to it
	MenuPlay          float64 // the first menu's default button plays it
	UnlistedAny       float64 // any cell is missing from the cell address table
	UnlistedShare     float64 // times the share of such cells
	OffVOBUShare      float64 // times the share of cells that don't start at a VOBU
	HollowShare       float64 // times the share of play time in cells too small to hold it
	Overlap           float64 // cells lie over each other on the disc
	Unreachable       float64 // nothing on the disc leads to it (buttons read)
	FirstMenuJump     float64 // the first title jump of the first menu names it
	TimeMismatch      float64 // a program chain's play time isn't the sum of its cells
	NoVideo           float64 // its title set has no video files
	// What the disc does by itself is worth more the more video it leads
	// to: these are added per minute of the title's distinct video.
	FirstPlayPerMinute float64
	MenuPlayPerMinute  float64
	Scrambled          float64 // a quarter or more of its steps jump back on the disc
	NoAudio            float64 // its program chain turns on no audio stream, where others of its title set do
}

// DefaultWeights are the weights MainTitle uses.
var DefaultWeights = Weights{
	DistinctPerMinute: 1,
	OutOfRangeAny:     -10,
	OutOfRangeShare:   -20,
	Repeats:           -30,
	BackJumpAny:       -5,
	BackJumpShare:     -35,
	Shuffled:          -25,
	Duplicate:         -0.5,
	CellSpam:          -30,
	AbsurdCell:        -20,
	Loop:              -15,
	BadChapters:       -10,
	RegularChapters:   3,
	AudioMatch:        2,
	SubpMatch:         1,
	LargestVTS:        5,
	FirstPlay:         15,
	MenuReference:     4,
	MenuPlay:          25,
	UnlistedAny:       -8,
	UnlistedShare:     -25,
	OffVOBUShare:      -10,
	HollowShare:       -20,
	Overlap:           -15,
	Unreachable:       -25,
	FirstMenuJump:     6,
	TimeMismatch:      -3,
	NoVideo:           -60,

	FirstPlayPerMinute: 0.25,
	MenuPlayPerMinute:  0.35,
	Scrambled:          -25,
	NoAudio:            -20,
}

// RankOptions add what only an opened disc can tell.
type RankOptions struct {
	// MenuPlay is the title the first menu's default button plays (0 unknown).
	MenuPlay int
	// MenuTargets are titles any button of the first menu plays.
	MenuTargets []int
	// FileSectors is the real size of each title set's title VOBs in
	// sectors. By itself it only adds reasons (an incomplete copy), never
	// changes a score, so MainTitle's pick doesn't depend on it.
	FileSectors map[int]uint32
	// FilesKnown says FileSectors is the whole truth about the VOB files: a
	// title set missing from it has no video at all, and a cell that starts
	// past the end of its files can't be played. Those then count against a
	// title, as they should when a disc carries title sets that are only an
	// index.
	FilesKnown bool
	// Buttons are the commands of the menus' buttons, read from the menu
	// VOBs' NAV packs. ButtonsComplete says every menu cell was read, so a
	// title no command and no button leads to really is unreachable.
	Buttons         []ButtonCommand
	ButtonsComplete bool
	Weights         *Weights
}

// Durations that steer the classification.
const (
	minFeatureSec   = 20 * 60 // first play reaching a title this long has found the film
	stubSec         = 15
	trailerSec      = 4 * 60
	featuretteSec   = 45 * 60
	maxFollowSteps  = 200
	absurdCellSec   = 4 * 3600
	minEpisodeSec   = 8 * 60
	maxEpisodeSec   = 75 * 60
	episodeTolerant = 0.15
	// hollowMinSec and hollowSectorsPerSec say when a cell is hollow: it
	// claims at least a minute and holds under half a sector (8 kbit) for
	// each second of it. The thinnest real stream, a still with mono sound,
	// is several times that.
	hollowMinSec        = 60
	hollowSectorsPerSec = 0.5
	// analysisFuel is how many VM commands and navigator hops all the dry
	// runs of one analysis may take together, and analysisWork how many
	// steps the comparisons between titles may take.
	analysisFuel = 1_000_000
	analysisWork = 20_000_000
)

type cellRange struct{ first, last uint32 }

// span is a stretch of sectors a title plays, and when.
type span struct {
	cellRange
	start float64
}

// feat is what one title's structure says.
type feat struct {
	c        *Candidate
	ts       *TitleSummary
	seq      []cellRange // readable cells in play order
	seqHash  uint64
	iv       []cellRange // the sectors it is credited with, merged, ascending
	sectors  uint64
	spans    []span // credited cells by first sector, with their first start time
	total    float64
	valid    float64 // play time of readable cells
	invalid  int
	partial  int
	pastEnd  int // cells past the end of the real VOB files
	back     int
	tiny     int
	zero     int
	unlisted int
	offVOBU  int
	overlaps int
	hollow   float64 // play time of hollow cells
	mismatch int     // program chains whose play time isn't the sum of their cells
	maxCell  float64
	loop     bool
	badPTT   bool
	repeat   float64 // share of its play time that replays cells
	next     int     // the title the disc goes on to by itself
	whole    bool
	refs     int // commands and buttons outside titles that name it
	audio    int // audio streams its first program chain turns on

	borrowedDone bool
}

func fmtDur(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

func (c *Candidate) add(signal string, points float64, flag Flag, format string, args ...any) {
	if math.IsNaN(points) || math.IsInf(points, 0) {
		points = 0
	}
	c.Score += points
	c.Flags |= flag
	detail := fmt.Sprintf(format, args...)
	c.Evidence = append(c.Evidence, Evidence{Signal: signal, Points: round1(points), Detail: detail})
	if points != 0 {
		detail = fmt.Sprintf("%+.1f %s", points, detail)
	}
	c.Reasons = append(c.Reasons, detail)
}

func deterministicRand() func() float64 { return rand.New(rand.NewSource(1)).Float64 }

// pgcChainLoops says whether following next-PGC links from start comes back.
func pgcChainLoops(v *VTS, start int) bool {
	seen := map[int]bool{}
	for p := start; p >= 1 && p <= len(v.PGCs); {
		if seen[p] {
			return true
		}
		seen[p] = true
		if v.PGCs[p-1].PGC == nil {
			return false
		}
		p = v.PGCs[p-1].PGC.NextPGCN
	}
	return false
}

func globalTitleOf(d *Disc, vts, vtsTTN int) int {
	for _, t := range d.VMG.Titles {
		if t.VTS == vts && t.VTSTitle == vtsTTN {
			return t.Number
		}
	}
	return 0
}

// followToTitle runs the navigator on from st until it plays a title at
// least minDur long, and returns it with the shorter titles passed on the
// way. It stops at a menu that waits for the viewer or loops, since what
// happens next needs a button press. length says how long a title plays.
func followToTitle(length func(int) float64, nav *Nav, st *Step, minDur float64) (int, []int) {
	var intros []int
	seen := map[int]bool{}
	for i := 0; i < maxFollowSteps && st != nil; i++ {
		switch st.Kind {
		case StepStop:
			return 0, intros
		case StepTitle:
			if st.Title != nil && st.Title.TTN >= 1 {
				ttn := st.Title.TTN
				if length(ttn) >= minDur {
					return ttn, intros
				}
				if !seen[ttn] {
					seen[ttn] = true
					intros = append(intros, ttn)
				}
			}
		case StepMenu:
			if st.Loop || math.IsInf(st.StillSec, 1) {
				return 0, intros
			}
		}
		st = nav.CellEnded()
	}
	return 0, intros
}

// titleLength answers how long a title plays, from its chapter table.
func (d *Disc) titleLength() func(int) float64 {
	known := map[int]float64{}
	return func(ttn int) float64 {
		if v, ok := known[ttn]; ok {
			return v
		}
		var dur float64
		if ts := d.Title(ttn); ts != nil {
			dur = ts.Duration
		}
		known[ttn] = dur
		return dur
	}
}

// firstPlayTarget dry-runs first play in the disc's own VM, with fixed
// random numbers, until it plays a feature-length title by itself.
func (d *Disc) firstPlayTarget(length func(int) float64, fuel *int64) (target int, intros []int) {
	if d.VMG == nil || d.VMG.FirstPlay == nil {
		return 0, nil
	}
	defer func() {
		if recover() != nil {
			target, intros = 0, nil
		}
	}()
	nav := NewNav(d, NavOptions{RegisterOptions: RegisterOptions{Rand: deterministicRand()}})
	nav.lean = true
	nav.st.regs.fuel = fuel
	st := nav.Start()
	nav.st.regs.fuel = fuel // Start makes a fresh register file
	return followToTitle(length, nav, st, minFeatureSec)
}

// RankTitles scores every title, best first, from the IFOs alone.
func (d *Disc) RankTitles() []Candidate { return d.Analyze(RankOptions{}).Candidates }

// Explain words the ranking for people: the pick, why, and warnings.
func (d *Disc) Explain() string { return d.Analyze(RankOptions{}).Explain() }

// Episodes lists the episodes of a TV disc in viewing order; nil when the
// disc doesn't look like one.
func (d *Disc) Episodes() []Episode { return d.Analyze(RankOptions{}).Episodes }

// vtsFacts is what a title set's tables say about where video really is.
type vtsFacts struct {
	v     *VTS
	size  uint32     // of the title VOBs, by the IFO
	file  uint32     // of the title VOB files (0 unknown)
	ix    *cellIndex // the cell address table, when it can be trusted
	vobus []uint32   // the VOBU map, when it can be trusted
}

// analysis is one run of the engine.
type analysis struct {
	d       *Disc
	opt     RankOptions
	w       Weights
	r       *Ranking
	fuel    int64
	work    int64
	vts     map[int]*vtsFacts
	feats   []*feat
	byTitle map[int]*feat
	reach   *reach
	length  func(int) float64
	clean   map[int]*feat
}

// facts reads a title set's address tables once. A table earns trust by
// agreeing with at least one cell of one program chain: one that agrees with
// nothing is itself what is wrong, and says nothing about the titles.
func (a *analysis) facts(n int) *vtsFacts {
	if f, ok := a.vts[n]; ok {
		return f
	}
	f := &vtsFacts{v: a.d.VTS[n], file: a.opt.FileSectors[n]}
	a.vts[n] = f
	v := f.v
	if v == nil {
		return f
	}
	f.size = v.TitleVOBSectors
	var ix *cellIndex
	if v.tableUsable(tblCADT) {
		ix = newCellIndex(v.CellAddrs)
	}
	vobus := v.vobus
	if !v.tableUsable(tblADMAP) {
		vobus = nil
	}
	if ix == nil && vobus == nil {
		return f
	}
	done := map[*PGC]bool{}
	for _, e := range v.PGCs {
		if e.PGC == nil || done[e.PGC] {
			continue
		}
		done[e.PGC] = true
		for _, c := range e.PGC.Cells {
			if badRange(c, f.size) {
				continue
			}
			if f.ix == nil && ix != nil && ix.listed(c) {
				f.ix = ix
			}
			if f.vobus == nil && vobus != nil && isVOBUStart(vobus, c.FirstSector) {
				f.vobus = vobus
			}
		}
		if (f.ix != nil || ix == nil) && (f.vobus != nil || vobus == nil) {
			break
		}
	}
	return f
}

// Analyze scores and classifies every title.
func (d *Disc) Analyze(opt RankOptions) *Ranking {
	r := &Ranking{}
	if d == nil || d.VMG == nil {
		return r
	}
	a := &analysis{d: d, opt: opt, w: DefaultWeights, r: r, fuel: analysisFuel, work: analysisWork, vts: map[int]*vtsFacts{}, byTitle: map[int]*feat{}}
	if opt.Weights != nil {
		a.w = *opt.Weights
	}
	for _, t := range d.VMG.Titles {
		if f := a.title(t.Number); f != nil {
			a.feats = append(a.feats, f)
			a.byTitle[t.Number] = f
		}
	}
	if len(a.feats) == 0 {
		r.Warnings = append(r.Warnings, "The disc lists no playable title.")
		return r
	}
	a.length = func(ttn int) float64 {
		if f := a.byTitle[ttn]; f != nil {
			return f.c.Duration
		}
		return 0
	}
	a.crossTitle()
	a.discSignals()
	a.chains()

	r.Candidates = make([]Candidate, len(a.feats))
	for i, f := range a.feats {
		r.Candidates[i] = *f.c
	}
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		x, y := r.Candidates[i], r.Candidates[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Distinct != y.Distinct {
			return x.Distinct > y.Distinct
		}
		if x.Duration != y.Duration {
			return x.Duration > y.Duration
		}
		return x.TitleNum < y.TitleNum
	})
	r.Candidates[0].Flags |= FlagMain
	r.Main = r.Candidates[0].TitleNum
	// The candidates were copied to be sorted: point the features at them.
	for i := range r.Candidates {
		a.byTitle[r.Candidates[i].TitleNum].c = &r.Candidates[i]
	}
	a.detectEpisodes()
	a.classify()
	a.versions()
	a.feature()
	a.decide()
	a.warn()
	for i := range r.Candidates {
		c := &r.Candidates[i]
		c.Score = round1(c.Score)
	}
	return r
}

// title reads one title both ways (see paths.go), scores each and keeps the
// one that shows more of what is really there.
func (a *analysis) title(number int) *feat {
	ts := a.d.Title(number)
	if ts == nil {
		return nil
	}
	f := a.measure(ts)
	a.scoreTitle(f)
	if !a.hasCommands(ts) {
		f.whole = true
		return f
	}
	p := a.d.playedTitle(number, RegisterOptions{}, &a.fuel, len(ts.Cells)+8)
	if p == nil {
		return f
	}
	if samePath(ts, p.ts) || len(p.ts.Cells) == 0 {
		f.next, f.whole = p.next, p.whole
		return f
	}
	// A run that was cut off (the budget, the cell limit) says nothing
	// about what the title plays.
	if !p.whole && !p.loop {
		return f
	}
	g := a.measure(p.ts)
	a.scoreTitle(g)
	g.next, g.whole = p.next, p.whole
	if p.loop {
		g.c.add("commandLoop", 0, 0, "its own commands play it round in a circle")
	}
	if g.c.Score > f.c.Score {
		g.c.add("assembled", 0, FlagAssembled, "its own commands play %s through %s, where its chapter table gives %s: the commands are followed",
			fmtDur(g.c.Duration), pluralWord(pgcCount(p.ts), "program chain", "program chains"), fmtDur(f.c.Duration))
		return g
	}
	if g.c.Duration < f.c.Duration*0.9 {
		f.c.add("guarded", 0, 0, "started cold, its own commands leave it after %s: the disc sets something first, in a menu", fmtDur(g.c.Duration))
	} else {
		f.next, f.whole = p.next, p.whole
	}
	return f
}

func pgcCount(ts *TitleSummary) int {
	seen := map[int]bool{}
	for _, c := range ts.Cells {
		seen[c.PGCN] = true
	}
	return len(seen)
}

// hasCommands says a dry run could show something the chapter table doesn't:
// some program chain of the title set carries a command.
func (a *analysis) hasCommands(ts *TitleSummary) bool {
	v := a.d.VTS[ts.VTS]
	if v == nil {
		return false
	}
	seen := map[int]bool{}
	check := func(pgcn int) bool {
		if pgcn < 1 || pgcn > len(v.PGCs) || seen[pgcn] {
			return false
		}
		seen[pgcn] = true
		p := v.PGCs[pgcn-1].PGC
		return p != nil && len(p.Pre)+len(p.Post)+len(p.CellCmds) > 0
	}
	for _, c := range ts.Cells {
		if check(c.PGCN) {
			return true
		}
	}
	if ts.VTSTitle >= 1 && ts.VTSTitle <= len(v.PTT) {
		for _, x := range v.PTT[ts.VTSTitle-1] {
			if check(x.PGCN) {
				return true
			}
		}
	}
	for i, e := range v.PGCs {
		if e.Entry && e.TitleNumber == ts.VTSTitle && check(i+1) {
			return true
		}
	}
	return false
}

// measure reads one title's structure.
func (a *analysis) measure(ts *TitleSummary) *feat {
	c := &Candidate{TitleNum: ts.Number, VTS: ts.VTS, VTSTitle: ts.VTSTitle, Duration: ts.Duration, Chapters: len(ts.Chapters), Cells: len(ts.Cells),
		Path: ts.Path, ChapterSource: ts.ChapterSource, Angles: max(ts.Angles, 1), summary: ts}
	if len(ts.Cells) > 0 {
		c.PGC = ts.Cells[0].PGCN
	}
	f := &feat{c: c, ts: ts}
	facts := a.facts(ts.VTS)
	v, size := facts.v, facts.size
	type credit struct {
		span
		dur   float64
		loose bool // interleaved: its sectors are shared by design
	}
	var credits []credit
	seen := map[cellRange]bool{}
	var prev uint32
	first := true
	h := fnv.New64a()
	var hb [8]byte
	for i, tc := range ts.Cells {
		cell := tc.Cell
		dur := ts.Duration - tc.Start
		if i+1 < len(ts.Cells) {
			dur = ts.Cells[i+1].Start - tc.Start
		}
		f.total += dur
		if dur < 1 {
			f.tiny++
		}
		if dur == 0 {
			f.zero++
		}
		f.maxCell = max(f.maxCell, cell.Duration)
		if cell.Duration > absurdCellSec {
			continue // a cell can't hold that much: its time isn't counted
		}
		// On an opened disc a cell that starts past the end of the video
		// files, or in a title set that has none, has nothing to play.
		if badRange(cell, size) || (a.opt.FilesKnown && cell.FirstSector >= facts.file) {
			f.invalid++
			continue
		}
		if size != 0 && cell.LastSector >= size {
			f.partial++
		}
		if facts.file != 0 && cell.LastSector >= facts.file {
			f.pastEnd++
		}
		k := cellRange{cell.FirstSector, cell.LastSector}
		f.seq = append(f.seq, k)
		hb = [8]byte{byte(k.first >> 24), byte(k.first >> 16), byte(k.first >> 8), byte(k.first), byte(k.last >> 24), byte(k.last >> 16), byte(k.last >> 8), byte(k.last)}
		h.Write(hb[:])
		f.valid += dur
		if !first && cell.FirstSector < prev {
			f.back++
		}
		prev, first = cell.FirstSector, false
		if facts.ix != nil && !facts.ix.listed(cell) {
			f.unlisted++
			continue // not among the cells the title set holds: no credit
		}
		if facts.vobus != nil && !isVOBUStart(facts.vobus, cell.FirstSector) {
			f.offVOBU++
		}
		if dur >= hollowMinSec && float64(uint64(k.last-k.first)+1) < dur*hollowSectorsPerSec {
			f.hollow += dur
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		credits = append(credits, credit{span: span{k, tc.Start}, dur: dur, loose: cell.Interleaved || cell.BlockType == 1})
	}
	f.seqHash = h.Sum64()
	// Each sector is credited once: a cell that lies over another one only
	// counts for the part that is new.
	sort.Slice(credits, func(i, j int) bool {
		if credits[i].first != credits[j].first {
			return credits[i].first < credits[j].first
		}
		return credits[i].last < credits[j].last
	})
	covered := int64(-1)
	for _, cr := range credits {
		n := int64(cr.last) - int64(cr.first) + 1
		fresh := n
		if !cr.loose {
			from := max(int64(cr.first), covered+1)
			fresh = max(int64(cr.last)-from+1, 0)
			if fresh < n {
				f.overlaps++
			}
		}
		c.Distinct += cr.dur * float64(fresh) / float64(n)
		covered = max(covered, int64(cr.last))
		f.spans = append(f.spans, cr.span)
		if k := len(f.iv); k > 0 && int64(cr.first) <= int64(f.iv[k-1].last)+1 {
			f.iv[k-1].last = max(f.iv[k-1].last, cr.last)
		} else {
			f.iv = append(f.iv, cr.cellRange)
		}
	}
	for _, r := range f.iv {
		f.sectors += uint64(r.last-r.first) + 1
	}
	if v != nil && c.PGC >= 1 && c.PGC <= len(v.PGCs) {
		for _, stream := range v.PGCs[c.PGC-1].PGC.AudioControl {
			if stream >= 0 {
				f.audio++
			}
		}
	}
	if v != nil && ts.VTSTitle >= 1 && ts.VTSTitle <= len(v.PTT) {
		ptts := v.PTT[ts.VTSTitle-1]
		if len(ptts) > 0 {
			f.loop = pgcChainLoops(v, ptts[0].PGCN)
		}
		for _, p := range ptts {
			if !pttValid(v, p) {
				f.badPTT = true
				break
			}
		}
		// A chain's play time is the sum of its cells' (one angle's worth).
		done := map[int]bool{}
		for _, tc := range ts.Cells {
			if done[tc.PGCN] || tc.PGCN < 1 || tc.PGCN > len(v.PGCs) {
				continue
			}
			done[tc.PGCN] = true
			p := v.PGCs[tc.PGCN-1].PGC
			var sum float64
			for n := 1; n >= 1 && n <= len(p.Cells); n = nextCellN(p, n) {
				sum += p.Cells[n-1].Duration
			}
			if p.Duration > 0 && sum > 0 && math.Abs(p.Duration-sum) > 5+0.1*max(p.Duration, sum) {
				f.mismatch++
			}
		}
	}
	return f
}

// scoreTitle applies the signals one title carries by itself.
func (a *analysis) scoreTitle(f *feat) {
	c, ts, w := f.c, f.ts, a.w
	c.add("distinctVideo", w.DistinctPerMinute*c.Distinct/60, 0, "distinct video: %s of unique, readable cells (plays %s)", fmtDur(c.Distinct), fmtDur(c.Duration))
	cells := len(ts.Cells)
	if ts.Truncated {
		c.add("truncated", 0, FlagTruncated, "it lists more cells than any real title: only the first %d were read", cells)
	}
	if ts.Estimated {
		c.add("timesEstimated", 0, 0, "some cells give no play time: theirs was worked out from the time map or their share of the sectors")
	}
	if f.invalid > 0 {
		share := float64(f.invalid) / float64(max(cells, 1))
		c.add("outOfRange", w.OutOfRangeAny+w.OutOfRangeShare*share, FlagOutOfRange, "%d of %d cells point outside the title VOB or at sectors that don't exist", f.invalid, cells)
	}
	if f.partial > 0 {
		c.add("pastVOBEnd", 0, FlagOutOfRange, "%d cells run past the end of the title VOB", f.partial)
	}
	if f.pastEnd > 0 {
		c.add("pastFileEnd", 0, 0, "%d cells run past the end of the VOB files: the copy may be incomplete", f.pastEnd)
	}
	if f.unlisted > 0 {
		share := float64(f.unlisted) / float64(max(len(f.seq), 1))
		c.add("unlistedCells", w.UnlistedAny+w.UnlistedShare*share, FlagUnlisted, "%d of %d cells aren't in the title set's cell address table: there is no such video", f.unlisted, len(f.seq))
	}
	if f.offVOBU > 0 {
		share := float64(f.offVOBU) / float64(max(len(f.seq), 1))
		c.add("offVOBU", w.OffVOBUShare*share, 0, "%d of %d cells don't start where the VOBU map says a VOBU starts", f.offVOBU, len(f.seq))
	}
	if f.hollow > 0 && f.total > 0 {
		c.add("hollowCells", w.HollowShare*f.hollow/f.total, FlagHollow, "%s of its play time is in cells too small to hold it", fmtDur(f.hollow))
	}
	if f.overlaps >= 2 && f.overlaps*10 >= len(f.spans) {
		c.add("overlappingCells", w.Overlap, FlagOverlap, "%d of its %d distinct cells lie over other cells on the disc", f.overlaps, len(f.spans))
	}
	if f.valid > 0 {
		var once float64
		seen := map[cellRange]bool{}
		for i, tc := range ts.Cells {
			k := cellRange{tc.Cell.FirstSector, tc.Cell.LastSector}
			if seen[k] || badRange(tc.Cell, a.facts(ts.VTS).size) || tc.Cell.Duration > absurdCellSec {
				continue
			}
			seen[k] = true
			if i+1 < len(ts.Cells) {
				once += ts.Cells[i+1].Start - tc.Start
			} else {
				once += ts.Duration - tc.Start
			}
		}
		if rep := 1 - once/f.valid; rep > 0.02 {
			f.repeat = rep
			c.add("repeats", w.Repeats*rep, FlagRepeats, "%.0f%% of its play time replays cells it already played", 100*rep)
		}
	} else if n := len(f.seq); n > 1 {
		seen := map[cellRange]bool{}
		for _, k := range f.seq {
			seen[k] = true
		}
		if len(seen) < n {
			rep := 1 - float64(len(seen))/float64(n)
			f.repeat = rep
			c.add("repeats", w.Repeats*rep, FlagRepeats, "%d of %d cells are repeats", n-len(seen), n)
		}
	}
	if f.back > 0 {
		share := float64(f.back) / float64(max(len(f.seq)-1, 1))
		c.add("outOfOrder", w.BackJumpAny+w.BackJumpShare*share, 0, "plays its cells out of disc order (%d of %d steps jump back)", f.back, max(len(f.seq)-1, 1))
		// Authored video plays forwards through the disc, but for the odd
		// shared scene. A title that keeps jumping back was put together
		// from other titles' cells.
		if f.back >= 4 && share >= 0.25 {
			c.add("scrambled", w.Scrambled, FlagShuffled, "a quarter or more of its steps jump back on the disc: it is put together from cells in no order")
		}
	}
	if f.tiny >= 10 && f.tiny*2 >= cells || f.zero >= 20 {
		c.add("cellSpam", w.CellSpam, FlagCellSpam, "%d of %d cells are under a second long (%d zero-length)", f.tiny, cells, f.zero)
	}
	if f.maxCell > absurdCellSec {
		c.add("absurdCell", w.AbsurdCell, FlagAbsurd, "a cell claims to last %s, which isn't counted", fmtDur(f.maxCell))
	}
	if f.loop {
		c.add("loop", w.Loop, FlagLoop, "its PGC chain links back on itself")
	}
	if f.badPTT {
		c.add("badChapters", w.BadChapters, FlagBadChapters, "its chapter table names PGCs or programs that don't exist")
	}
	if f.mismatch > 0 {
		c.add("timeMismatch", w.TimeMismatch, 0, "%s a play time that isn't the sum of its cells'", pluralWord(f.mismatch, "program chain gives", "program chains give"))
	}
	if ts.ChapterSource == "programs" {
		c.add("chaptersFromPrograms", 0, 0, "its chapter table names nothing it plays: each program is taken as a chapter")
	}
	if n := len(ts.Chapters); n >= 3 && ts.Duration > 0 {
		mean := ts.Duration / float64(n)
		var v float64
		for i, s := range ts.Chapters {
			e := ts.Duration
			if i+1 < n {
				e = ts.Chapters[i+1]
			}
			v += (e - s - mean) * (e - s - mean)
		}
		cv := math.Sqrt(v/float64(n)) / mean
		if cv < 0.8 && mean >= 60 && mean <= 20*60 {
			c.add("evenChapters", w.RegularChapters, 0, "%d chapters of even length (about %s each)", n, fmtDur(mean))
		}
	}
	for _, rep := range a.d.IFOReports {
		if rep.VTS == ts.VTS && rep.Used == "BUP" {
			c.add("fromBackup", 0, FlagFromBackup, "its title set was read from %s: %s", rep.Name+".BUP", rep.Note)
		}
	}
	if a.opt.FilesKnown && len(a.opt.FileSectors) > 0 && a.opt.FileSectors[ts.VTS] == 0 {
		c.add("noVideoFiles", w.NoVideo, FlagNoVideo, "title set %d has an index and no video files", ts.VTS)
	}
}

// shared is how many sectors two titles are both credited with. It costs
// the comparison budget; with none left it answers 0 and says so.
func (a *analysis) shared(x, y *feat) (uint64, bool) {
	if x.c.VTS != y.c.VTS || len(x.iv) == 0 || len(y.iv) == 0 {
		return 0, true
	}
	a.work -= int64(len(x.iv) + len(y.iv))
	if a.work < 0 {
		return 0, false
	}
	var n uint64
	i, j := 0, 0
	for i < len(x.iv) && j < len(y.iv) {
		lo, hi := max(x.iv[i].first, y.iv[j].first), min(x.iv[i].last, y.iv[j].last)
		if lo <= hi {
			n += uint64(hi-lo) + 1
		}
		if x.iv[i].last < y.iv[j].last {
			i++
		} else {
			j++
		}
	}
	return n, true
}

func sameSeq(a, b []cellRange) bool {
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

// scrambles says f plays video it shares with o in another order than o
// does. A longer cut of o plays o's cells in o's order with more in between,
// wherever on the disc the extra scenes lie; a decoy made of o's cells does
// not keep the order.
func (f *feat) scrambles(o *feat) bool {
	last := -1.0
	for _, k := range f.seq {
		if t, ok := o.startOf(k.first); ok {
			if t < last {
				return true
			}
			last = t
		}
	}
	return false
}

// maxShuffleRefs is how many clean titles of a title set a scrambled one is
// compared with: a decoy copies the film, and the film is among the titles
// with the most video played in order.
const maxShuffleRefs = 16

// crossTitle compares titles: duplicates and shuffled copies. Titles are
// first grouped by what they play, so the hundred identical decoys of a
// protected disc cost one comparison, not five thousand.
func (a *analysis) crossTitle() {
	type key struct {
		vts  int
		hash uint64
		n    int
	}
	firsts := map[key]*feat{}
	var originals []*feat
	for _, f := range a.feats {
		if len(f.seq) == 0 {
			continue
		}
		k := key{f.c.VTS, f.seqHash, len(f.seq)}
		if o := firsts[k]; o != nil && sameSeq(o.seq, f.seq) {
			f.c.add("duplicate", a.w.Duplicate, FlagDuplicate, "plays the same cells in the same order as title %d", o.c.TitleNum)
			continue
		}
		if firsts[k] == nil {
			firsts[k] = f
		}
		originals = append(originals, f)
	}
	// References: per title set, the titles that play in order, most video
	// first.
	refs := map[int][]*feat{}
	byVideo := append([]*feat(nil), originals...)
	sort.SliceStable(byVideo, func(i, j int) bool {
		if byVideo[i].back != byVideo[j].back {
			return byVideo[i].back < byVideo[j].back
		}
		return byVideo[i].sectors > byVideo[j].sectors
	})
	for _, f := range byVideo {
		if len(refs[f.c.VTS]) < maxShuffleRefs {
			refs[f.c.VTS] = append(refs[f.c.VTS], f)
		}
	}
	spent := false
	for _, f := range a.feats {
		if f.back == 0 || f.sectors == 0 || f.c.Flags.Has(FlagShuffled) {
			continue
		}
		for _, o := range refs[f.c.VTS] {
			if o == f || o.back >= f.back || sameSeq(o.seq, f.seq) {
				continue
			}
			ov, ok := a.shared(f, o)
			if !ok {
				spent = true
				break
			}
			if ov*5 >= f.sectors*4 && ov*2 >= o.sectors && f.scrambles(o) {
				f.c.add("shuffled", a.w.Shuffled, FlagShuffled, "plays title %d's cells in a scrambled order", o.c.TitleNum)
				break
			}
		}
	}
	if spent {
		a.r.Warnings = append(a.r.Warnings, "This disc lists so many cells that not every title could be compared with every other.")
	}
	for _, f := range a.feats {
		a.borrowed(f)
	}
}

// inOrder is, per title set, the video of the titles that play forwards
// with nothing wrong with them, as one stretch of sectors.
func (a *analysis) inOrder(vts int) *feat {
	if u, ok := a.clean[vts]; ok {
		return u
	}
	u := &feat{c: &Candidate{VTS: vts}}
	var all []cellRange
	for _, f := range a.feats {
		if f.c.VTS == vts && f.back == 0 && f.sectors > 0 && !isDecoy(f) {
			all = append(all, f.iv...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].first < all[j].first })
	for _, r := range all {
		if k := len(u.iv); k > 0 && uint64(r.first) <= uint64(u.iv[k-1].last)+1 {
			u.iv[k-1].last = max(u.iv[k-1].last, r.last)
		} else {
			u.iv = append(u.iv, r)
		}
	}
	if a.clean == nil {
		a.clean = map[int]*feat{}
	}
	a.clean[vts] = u
	return u
}

// borrowed takes back the credit a scrambled title got for video that other
// titles play in order: that video is theirs. A real film with an odd
// layout keeps what only it plays.
func (a *analysis) borrowed(f *feat) {
	if !f.c.Flags.Has(FlagShuffled) || f.borrowedDone || f.sectors == 0 {
		return
	}
	f.borrowedDone = true
	ov, ok := a.shared(f, a.inOrder(f.c.VTS))
	if !ok || ov == 0 {
		return
	}
	share := float64(ov) / float64(f.sectors)
	f.c.add("borrowedVideo", -a.w.DistinctPerMinute*f.c.Distinct/60*share, 0, "%.0f%% of its video is what other titles play in order: that isn't counted for it", 100*share)
}

// discSignals applies what the disc as a whole says: navigation targets,
// stream counts and where the video is.
func (a *analysis) discSignals() {
	d, r, w, opt := a.d, a.r, a.w, a.opt
	vtsUsed := map[int]bool{}
	cand := func(t int) *Candidate {
		if f := a.byTitle[t]; f != nil {
			return f.c
		}
		return nil
	}
	for _, f := range a.feats {
		vtsUsed[f.c.VTS] = true
	}
	fp, intros := d.firstPlayTarget(a.length, &a.fuel)
	r.FirstPlay = fp
	if c := cand(fp); c != nil {
		c.add("firstPlay", w.FirstPlay+w.FirstPlayPerMinute*c.Distinct/60, FlagFirstPlay|FlagMenuTarget, "first play reaches it by itself (a dry run of the disc's own program)")
	}
	for _, t := range intros {
		if c := cand(t); c != nil {
			c.add("intro", 0, FlagIntro, "first play passes through it before anything else")
		}
	}
	a.reach = d.reachable(opt.Buttons)
	r.ButtonsRead = len(opt.Buttons) > 0
	for t := range a.reach.depth {
		r.Reachable = append(r.Reachable, t)
	}
	sort.Ints(r.Reachable)
	var named []int
	for t := range a.reach.direct {
		named = append(named, t)
	}
	sort.Ints(named)
	for _, t := range named {
		if f := a.byTitle[t]; f != nil {
			f.refs = a.reach.direct[t]
			if t != fp {
				f.c.add("menuReference", w.MenuReference, FlagMenuTarget, "%s in the menus jump to it", pluralWord(f.refs, "command", "commands"))
			}
		}
	}
	if c := cand(a.reach.firstJump); c != nil && a.reach.firstJump != fp {
		c.add("firstMenuJump", w.FirstMenuJump, FlagMenuTarget, "it is the first title the disc's first menu jumps to")
	}
	for _, t := range opt.MenuTargets {
		if c := cand(t); c != nil && !c.Flags.Has(FlagMenuTarget) {
			c.add("menuButton", w.MenuReference, FlagMenuTarget, "a button of the first menu plays it")
		}
	}
	r.MenuPlay = opt.MenuPlay
	if c := cand(opt.MenuPlay); c != nil {
		c.add("menuPlay", w.MenuPlay+w.MenuPlayPerMinute*c.Distinct/60, FlagMenuPlay|FlagMenuTarget, "the first menu's default button plays it")
	}
	// Nothing leads to it: only said when the buttons were read, and when
	// the search found its way to some title at all.
	if opt.ButtonsComplete && a.reach.complete && len(opt.Buttons) > 0 && len(a.reach.depth) > 0 {
		for _, f := range a.feats {
			if _, ok := a.reach.depth[f.c.TitleNum]; !ok && !f.c.Flags.Has(FlagMenuTarget) {
				f.c.add("unreachable", w.Unreachable, FlagUnreachable, "no command and no menu button on the disc leads to it")
			}
		}
	}
	// A film's program chain turns its soundtracks on (the audio stream
	// control of the PGC). A long title that turns none on, in a title set
	// where other titles do, plays in silence on a player.
	most := map[int]int{}
	for _, f := range a.feats {
		most[f.c.VTS] = max(most[f.c.VTS], f.audio)
	}
	for _, f := range a.feats {
		if f.audio == 0 && most[f.c.VTS] > 0 && f.c.Duration >= trailerSec {
			f.c.add("noAudioStreams", w.NoAudio, 0, "its program chain turns on no audio stream, where other titles of title set %d do", f.c.VTS)
		}
	}
	if len(vtsUsed) < 2 {
		return
	}
	var bigVTS int
	var big uint32
	maxA, maxS, minA, minS := -1, -1, 1<<30, 1<<30
	for n := 1; n <= maxTitleSets; n++ {
		v := d.VTS[n]
		if v == nil || !vtsUsed[n] {
			continue
		}
		size := v.TitleVOBSectors
		if s := opt.FileSectors[n]; s != 0 {
			size = s
		}
		if size > big {
			big, bigVTS = size, n
		}
		maxA, minA = max(maxA, len(v.Audio)), min(minA, len(v.Audio))
		maxS, minS = max(maxS, len(v.Subp)), min(minS, len(v.Subp))
	}
	for _, f := range a.feats {
		c := f.c
		v := d.VTS[c.VTS]
		if v == nil {
			continue
		}
		if c.VTS == bigVTS && big > 0 {
			c.add("largestTitleSet", w.LargestVTS, 0, "title set %d holds the most video", c.VTS)
		}
		if maxA != minA && len(v.Audio) == maxA {
			c.add("audioStreams", w.AudioMatch, 0, "its title set has the most audio streams (%d)", maxA)
		}
		if maxS != minS && len(v.Subp) == maxS {
			c.add("subtitleStreams", w.SubpMatch, 0, "its title set has the most subtitle streams (%d)", maxS)
		}
	}
}

// maxChain is how many titles a feature in parts may run through.
const maxChain = 32

// chain lists the titles the disc plays one after another from a title, by
// itself: each ends with a jump to the next.
func (a *analysis) chain(head *feat) []*feat {
	out := []*feat{head}
	seen := map[int]bool{head.c.TitleNum: true}
	for f := head; len(out) < maxChain; {
		n := a.byTitle[f.next]
		if n == nil || seen[n.c.TitleNum] || !f.whole || isDecoy(n) {
			break
		}
		seen[n.c.TitleNum] = true
		out = append(out, n)
		f = n
	}
	return out
}

// episodeLike says the titles could be a TV disc's episodes: three or more,
// each of an episode's length and all about as long as each other.
func episodeLike(fs []*feat) bool {
	if len(fs) < 3 {
		return false
	}
	ds := make([]float64, len(fs))
	for i, f := range fs {
		ds[i] = f.c.Duration
	}
	sort.Float64s(ds)
	med := ds[len(ds)/2]
	return ds[0] >= minEpisodeSec && ds[len(ds)-1] <= maxEpisodeSec && ds[0] >= med*(1-episodeTolerant) && ds[len(ds)-1] <= med*(1+episodeTolerant)
}

// chains finds features the disc plays as several titles in a row, each
// jumping to the next when it ends: structure protection cuts a film up this
// way so that no single title is the film. The first title is credited with
// the video of the rest. Episodes that play on into the next one look the
// same from here; they are told apart by their even lengths.
func (a *analysis) chains() {
	target := map[int]bool{}
	for _, f := range a.feats {
		if f.next != 0 && f.whole {
			target[f.next] = true
		}
	}
	for _, f := range a.feats {
		if f.next == 0 || target[f.c.TitleNum] || isDecoy(f) {
			continue
		}
		ch := a.chain(f)
		if len(ch) < 2 {
			continue
		}
		f.c.Continues = f.next
		if episodeLike(ch) {
			continue
		}
		var extra float64
		var nums []string
		for _, m := range ch[1:] {
			extra += m.c.Distinct
			nums = append(nums, fmt.Sprint(m.c.TitleNum))
			m.c.PartOf = f.c.TitleNum
			m.c.Continues = m.next
			m.c.add("partOfFeature", 0, FlagPart, "the disc plays it straight after title %d, as one feature that starts at title %d", ch[0].c.TitleNum, f.c.TitleNum)
		}
		f.c.add("continues", a.w.DistinctPerMinute*extra/60, FlagAssembled, "when it ends the disc plays on into %s %s by itself: one feature in %d parts, %s more of distinct video",
			pluralNoun(len(nums), "title", "titles"), strings.Join(nums, ", "), len(ch), fmtDur(extra))
		// A decoy can scramble the cells of the whole feature, which no one
		// part of it holds: compare with all the parts together.
		whole := &feat{c: &Candidate{VTS: f.c.VTS}}
		var all []cellRange
		var before float64
		for _, m := range ch {
			if m.c.VTS == f.c.VTS {
				all = append(all, m.iv...)
				for _, sp := range m.spans {
					whole.spans = append(whole.spans, span{sp.cellRange, before + sp.start})
				}
			}
			before += m.c.Duration
		}
		sort.Slice(whole.spans, func(i, j int) bool { return whole.spans[i].first < whole.spans[j].first })
		sort.Slice(all, func(i, j int) bool { return all[i].first < all[j].first })
		for _, r := range all {
			if k := len(whole.iv); k > 0 && uint64(r.first) <= uint64(whole.iv[k-1].last)+1 {
				whole.iv[k-1].last = max(whole.iv[k-1].last, r.last)
			} else {
				whole.iv = append(whole.iv, r)
			}
		}
		part := map[*feat]bool{}
		for _, m := range ch {
			part[m] = true
		}
		for _, g := range a.feats {
			if part[g] || g.back == 0 || g.sectors == 0 || g.c.Flags.Has(FlagShuffled) {
				continue
			}
			if ov, ok := a.shared(g, whole); ok && ov*5 >= g.sectors*4 && g.scrambles(whole) {
				g.c.add("shuffled", a.w.Shuffled, FlagShuffled, "plays cells of the feature that starts at title %d in a scrambled order", f.c.TitleNum)
				a.borrowed(g)
			}
		}
	}
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pluralNoun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// decoySigns counts the independent signs that a title is a decoy.
func decoySigns(f *feat) int {
	c := f.c
	n := 0
	for _, g := range []Flag{FlagShuffled, FlagCellSpam, FlagAbsurd, FlagLoop, FlagNoVideo} {
		if c.Flags.Has(g) {
			n++
		}
	}
	if f.invalid > 0 && f.invalid*2 >= len(f.seq)+f.invalid {
		n++
	}
	if f.unlisted > 0 && f.unlisted*2 >= len(f.seq) {
		n++
	}
	if f.total > 0 && f.hollow*2 >= f.total {
		n++
	}
	if f.overlaps > 0 && f.overlaps*2 >= len(f.spans) && c.Flags.Has(FlagOverlap) {
		n++
	}
	if f.repeat >= 0.5 {
		n++
	}
	return n
}

// isDecoy says the structure gives the title away as a decoy.
func isDecoy(f *feat) bool { return decoySigns(f) > 0 }
