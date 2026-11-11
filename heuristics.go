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
)

var flagNames = []string{"main", "decoy", "menuTarget", "menuPlay", "firstPlay", "intro", "episode", "playAll", "duplicate", "shuffled", "outOfRange", "repeats", "cellSpam", "loop", "absurd", "badChapters", "fromBackup", "alternate"}

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
	ClassIntro      = "intro"
	ClassStub       = "stub"
	ClassTrailer    = "trailer"
	ClassFeaturette = "featurette"
	ClassExtra      = "extra"
)

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
}

// RankOptions add what only an opened disc can tell.
type RankOptions struct {
	// MenuPlay is the title the first menu's default button plays (0 unknown).
	MenuPlay int
	// MenuTargets are titles any button of the first menu plays.
	MenuTargets []int
	// FileSectors is the real size of each title set's title VOBs in
	// sectors. It only adds reasons (an incomplete copy), never changes a
	// score, so MainTitle's pick doesn't depend on it.
	FileSectors map[int]uint32
	Weights     *Weights
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
)

type cellRange struct{ first, last uint32 }

// feat is what one title's structure says.
type feat struct {
	c       *Candidate
	seq     []cellRange // readable cells in play order
	set     map[cellRange]bool
	total   float64
	valid   float64 // play time of readable cells
	invalid int
	partial int
	pastEnd int // cells past the end of the real VOB files
	back    int
	tiny    int
	zero    int
	maxCell float64
	loop    bool
	badPTT  bool
}

func fmtDur(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

func (c *Candidate) add(points float64, flag Flag, format string, args ...any) {
	c.Score += points
	c.Flags |= flag
	msg := fmt.Sprintf(format, args...)
	if points != 0 {
		msg = fmt.Sprintf("%+.1f %s", points, msg)
	}
	c.Reasons = append(c.Reasons, msg)
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

// menuReferences statically reads every first-play and menu command (no VM
// run) and counts the titles they jump to.
func (d *Disc) menuReferences() map[int]int {
	refs := map[int]int{}
	scan := func(p *PGC, vts int) {
		if p == nil {
			return
		}
		for _, list := range [][]Command{p.Pre, p.Post, p.CellCmds} {
			for _, c := range list {
				in := decodeCached(c)
				if in.Invalid != "" || in.jump == nil {
					continue
				}
				switch j := in.jump; j.Op {
				case JumpTT:
					refs[j.TTN]++
				case JumpVTSTT, JumpVTSPTT:
					if vts != 0 {
						if g := globalTitleOf(d, vts, j.TTN); g != 0 {
							refs[g]++
						}
					}
				}
			}
		}
	}
	scan(d.VMG.FirstPlay, 0)
	for _, lu := range d.VMG.Menus {
		for _, e := range lu.PGCs {
			scan(e.PGC, 0)
		}
	}
	for n, v := range d.VTS {
		if v == nil {
			continue
		}
		for _, lu := range v.Menus {
			for _, e := range lu.PGCs {
				scan(e.PGC, n)
			}
		}
	}
	return refs
}

// followToTitle runs the navigator on from st until it plays a title at
// least minDur long, and returns it with the shorter titles passed on the
// way. It stops at a menu that waits for the viewer or loops, since what
// happens next needs a button press.
func followToTitle(d *Disc, nav *Nav, st *Step, minDur float64) (int, []int) {
	var intros []int
	seen := map[int]bool{}
	for i := 0; i < maxFollowSteps && st != nil; i++ {
		switch st.Kind {
		case StepStop:
			return 0, intros
		case StepTitle:
			if st.Title != nil && st.Title.TTN >= 1 {
				ttn := st.Title.TTN
				if ts := d.Title(ttn); ts != nil && ts.Duration >= minDur {
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

// firstPlayTarget dry-runs first play in the disc's own VM, with fixed
// random numbers, until it plays a feature-length title by itself.
func (d *Disc) firstPlayTarget() (target int, intros []int) {
	if d.VMG == nil || d.VMG.FirstPlay == nil {
		return 0, nil
	}
	defer func() {
		if recover() != nil {
			target, intros = 0, nil
		}
	}()
	nav := NewNav(d, NavOptions{RegisterOptions: RegisterOptions{Rand: deterministicRand()}})
	return followToTitle(d, nav, nav.Start(), minFeatureSec)
}

// RankTitles scores every title, best first, from the IFOs alone.
func (d *Disc) RankTitles() []Candidate { return d.Analyze(RankOptions{}).Candidates }

// Explain words the ranking for people: the pick, why, and warnings.
func (d *Disc) Explain() string { return d.Analyze(RankOptions{}).Explain() }

// Episodes lists the episodes of a TV disc in viewing order; nil when the
// disc doesn't look like one.
func (d *Disc) Episodes() []Episode { return d.Analyze(RankOptions{}).Episodes }

// Analyze scores and classifies every title.
func (d *Disc) Analyze(opt RankOptions) *Ranking {
	r := &Ranking{}
	if d == nil || d.VMG == nil {
		return r
	}
	w := DefaultWeights
	if opt.Weights != nil {
		w = *opt.Weights
	}
	titles := d.Titles()
	if len(titles) == 0 {
		r.Warnings = append(r.Warnings, "The disc lists no playable title.")
		return r
	}
	r.Candidates = make([]Candidate, len(titles))
	feats := make([]*feat, len(titles))
	for i := range titles {
		ts := &titles[i]
		c := &r.Candidates[i]
		*c = Candidate{TitleNum: ts.Number, VTS: ts.VTS, VTSTitle: ts.VTSTitle, Duration: ts.Duration, Chapters: len(ts.Chapters), Cells: len(ts.Cells), summary: ts}
		if len(ts.Cells) > 0 {
			c.PGC = ts.Cells[0].PGCN
		}
		feats[i] = d.measure(c, ts, opt)
	}
	for i, f := range feats {
		scoreTitle(d, f, &titles[i], w)
	}
	crossTitle(feats, w)
	d.discSignals(r, feats, w, opt)

	byTitle := map[int]*feat{}
	for _, f := range feats {
		byTitle[f.c.TitleNum] = f
	}
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Distinct != b.Distinct {
			return a.Distinct > b.Distinct
		}
		if a.Duration != b.Duration {
			return a.Duration > b.Duration
		}
		return a.TitleNum < b.TitleNum
	})
	r.Candidates[0].Flags |= FlagMain
	r.Main = r.Candidates[0].TitleNum
	// feats point into the candidates before sorting: re-point them.
	for i := range r.Candidates {
		byTitle[r.Candidates[i].TitleNum].c = &r.Candidates[i]
	}
	detectEpisodes(r, byTitle)
	classify(r, byTitle)
	r.warn(byTitle)
	return r
}

// measure reads one title's structure.
func (d *Disc) measure(c *Candidate, ts *TitleSummary, opt RankOptions) *feat {
	f := &feat{c: c, set: map[cellRange]bool{}}
	v := d.VTS[ts.VTS]
	var size uint32
	if v != nil {
		size = v.TitleVOBSectors
	}
	file := opt.FileSectors[ts.VTS]
	var prev uint32
	first := true
	for _, tc := range ts.Cells {
		cell := tc.Cell
		f.total += cell.Duration
		if cell.Duration < 1 {
			f.tiny++
		}
		if cell.Duration == 0 {
			f.zero++
		}
		f.maxCell = max(f.maxCell, cell.Duration)
		if cell.Duration > absurdCellSec {
			continue // a cell can't hold that much: its time isn't counted
		}
		if cell.LastSector < cell.FirstSector || (size != 0 && cell.FirstSector >= size) {
			f.invalid++
			continue
		}
		if size != 0 && cell.LastSector >= size {
			f.partial++
		}
		if file != 0 && cell.LastSector >= file {
			f.pastEnd++
		}
		k := cellRange{cell.FirstSector, cell.LastSector}
		f.seq = append(f.seq, k)
		f.valid += cell.Duration
		if !f.set[k] {
			f.set[k] = true
			c.Distinct += cell.Duration
		}
		if !first && cell.FirstSector < prev {
			f.back++
		}
		prev, first = cell.FirstSector, false
	}
	if v != nil && ts.VTSTitle >= 1 && ts.VTSTitle <= len(v.PTT) {
		ptts := v.PTT[ts.VTSTitle-1]
		if len(ptts) > 0 {
			f.loop = pgcChainLoops(v, ptts[0].PGCN)
		}
		for _, p := range ptts {
			if p.PGCN < 1 || p.PGCN > len(v.PGCs) {
				f.badPTT = true
				break
			}
			if pg := v.PGCs[p.PGCN-1].PGC; pg != nil && len(pg.Programs) > 0 && (p.PGN < 1 || p.PGN > len(pg.Programs)) {
				f.badPTT = true
				break
			}
		}
	}
	return f
}

// scoreTitle applies the signals one title carries by itself.
func scoreTitle(d *Disc, f *feat, ts *TitleSummary, w Weights) {
	c := f.c
	c.add(w.DistinctPerMinute*c.Distinct/60, 0, "distinct video: %s of unique, readable cells (plays %s)", fmtDur(c.Distinct), fmtDur(c.Duration))
	cells := len(ts.Cells)
	if f.invalid > 0 {
		share := float64(f.invalid) / float64(max(cells, 1))
		c.add(w.OutOfRangeAny+w.OutOfRangeShare*share, FlagOutOfRange, "%d of %d cells point outside the title VOB or at sectors that don't exist", f.invalid, cells)
	}
	if f.partial > 0 {
		c.add(0, FlagOutOfRange, "%d cells run past the end of the title VOB", f.partial)
	}
	if f.pastEnd > 0 {
		c.add(0, 0, "%d cells run past the end of the VOB files: the copy may be incomplete", f.pastEnd)
	}
	if f.valid > 0 {
		if rep := 1 - c.Distinct/f.valid; rep > 0.02 {
			c.add(w.Repeats*rep, FlagRepeats, "%.0f%% of its play time replays cells it already played", 100*rep)
		}
	} else if n := len(f.seq); n > 1 && len(f.set) < n {
		rep := 1 - float64(len(f.set))/float64(n)
		c.add(w.Repeats*rep, FlagRepeats, "%d of %d cells are repeats", n-len(f.set), n)
	}
	if f.back > 0 {
		share := float64(f.back) / float64(max(len(f.seq)-1, 1))
		c.add(w.BackJumpAny+w.BackJumpShare*share, 0, "plays its cells out of disc order (%d of %d steps jump back)", f.back, max(len(f.seq)-1, 1))
	}
	if f.tiny >= 10 && f.tiny*2 >= cells || f.zero >= 20 {
		c.add(w.CellSpam, FlagCellSpam, "%d of %d cells are under a second long (%d zero-length)", f.tiny, cells, f.zero)
	}
	if f.maxCell > absurdCellSec {
		c.add(w.AbsurdCell, FlagAbsurd, "a cell claims to last %s, which isn't counted", fmtDur(f.maxCell))
	}
	if f.loop {
		c.add(w.Loop, FlagLoop, "its PGC chain links back on itself")
	}
	if f.badPTT {
		c.add(w.BadChapters, FlagBadChapters, "its chapter table names PGCs or programs that don't exist")
	}
	if n := len(ts.Chapters); n >= 3 && ts.Duration > 0 {
		var lens []float64
		for i, s := range ts.Chapters {
			e := ts.Duration
			if i+1 < n {
				e = ts.Chapters[i+1]
			}
			lens = append(lens, e-s)
		}
		mean := ts.Duration / float64(n)
		var v float64
		for _, l := range lens {
			v += (l - mean) * (l - mean)
		}
		cv := math.Sqrt(v/float64(n)) / mean
		if cv < 0.8 && mean >= 60 && mean <= 20*60 {
			c.add(w.RegularChapters, 0, "%d chapters of even length (about %s each)", n, fmtDur(mean))
		}
	}
	for _, rep := range d.IFOReports {
		if rep.VTS == ts.VTS && rep.Used == "BUP" {
			c.add(0, FlagFromBackup, "its title set was read from %s: %s", rep.Name+".BUP", rep.Note)
		}
	}
}

func overlap(a, b map[cellRange]bool) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
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

// crossTitle compares titles: duplicates and shuffled copies.
func crossTitle(feats []*feat, w Weights) {
	for i, a := range feats {
		if len(a.seq) == 0 {
			continue
		}
		for j, b := range feats {
			if i == j || len(b.seq) == 0 {
				continue
			}
			if j < i && sameSeq(a.seq, b.seq) && !a.c.Flags.Has(FlagDuplicate) {
				a.c.add(w.Duplicate, FlagDuplicate, "plays the same cells in the same order as title %d", b.c.TitleNum)
				continue
			}
			if a.back == 0 || a.back <= b.back || a.c.Flags.Has(FlagShuffled) {
				continue
			}
			if ov := overlap(a.set, b.set); ov*5 >= len(a.set)*4 && ov*2 >= len(b.set) {
				a.c.add(w.Shuffled, FlagShuffled, "plays title %d's cells in a scrambled order", b.c.TitleNum)
			}
		}
	}
}

// discSignals applies what the disc as a whole says: navigation targets,
// stream counts and where the video is.
func (d *Disc) discSignals(r *Ranking, feats []*feat, w Weights, opt RankOptions) {
	byTitle := map[int]*Candidate{}
	vtsUsed := map[int]bool{}
	for _, f := range feats {
		byTitle[f.c.TitleNum] = f.c
		vtsUsed[f.c.VTS] = true
	}
	fp, intros := d.firstPlayTarget()
	r.FirstPlay = fp
	if c := byTitle[fp]; c != nil {
		c.add(w.FirstPlay, FlagFirstPlay|FlagMenuTarget, "first play reaches it by itself (a dry run of the disc's own program)")
	}
	for _, t := range intros {
		if c := byTitle[t]; c != nil {
			c.add(0, FlagIntro, "first play passes through it before anything else")
		}
	}
	for t, n := range d.menuReferences() {
		if c := byTitle[t]; c != nil && t != fp {
			c.add(w.MenuReference, FlagMenuTarget, "%s in the menus jump to it", pluralWord(n, "command", "commands"))
		}
	}
	for _, t := range opt.MenuTargets {
		if c := byTitle[t]; c != nil && !c.Flags.Has(FlagMenuTarget) {
			c.add(w.MenuReference, FlagMenuTarget, "a button of the first menu plays it")
		}
	}
	r.MenuPlay = opt.MenuPlay
	if c := byTitle[opt.MenuPlay]; c != nil {
		c.add(w.MenuPlay, FlagMenuPlay|FlagMenuTarget, "the first menu's default button plays it")
	}
	if len(vtsUsed) < 2 {
		return
	}
	var bigVTS int
	var big uint32
	maxA, maxS, minA, minS := -1, -1, 1<<30, 1<<30
	for n := range vtsUsed {
		v := d.VTS[n]
		if v == nil {
			continue
		}
		size := v.TitleVOBSectors
		if s := opt.FileSectors[n]; s != 0 {
			size = s
		}
		if size > big || (size == big && n < bigVTS) {
			big, bigVTS = size, n
		}
		maxA, minA = max(maxA, len(v.Audio)), min(minA, len(v.Audio))
		maxS, minS = max(maxS, len(v.Subp)), min(minS, len(v.Subp))
	}
	for _, f := range feats {
		c := f.c
		v := d.VTS[c.VTS]
		if v == nil {
			continue
		}
		if c.VTS == bigVTS && big > 0 {
			c.add(w.LargestVTS, 0, "title set %d holds the most video", c.VTS)
		}
		if maxA != minA && len(v.Audio) == maxA {
			c.add(w.AudioMatch, 0, "its title set has the most audio streams (%d)", maxA)
		}
		if maxS != minS && len(v.Subp) == maxS {
			c.add(w.SubpMatch, 0, "its title set has the most subtitle streams (%d)", maxS)
		}
	}
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// isDecoy says the structure gives the title away as a decoy.
func isDecoy(f *feat) bool {
	c := f.c
	if c.Flags&(FlagShuffled|FlagCellSpam|FlagAbsurd|FlagLoop) != 0 {
		return true
	}
	if f.invalid > 0 && f.invalid*2 >= len(f.seq)+f.invalid {
		return true
	}
	if f.valid > 0 && 1-c.Distinct/f.valid >= 0.5 {
		return true
	}
	return false
}
