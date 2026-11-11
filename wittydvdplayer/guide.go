// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvdplayer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wittywilla/wittydisc/session"
	"github.com/wittywilla/wittydvd"
)

// What a player can show about the engine's ranking of a disc, and the choices it can
// offer besides what the disc's own menus lead to. WittyBluray's player module answers
// the same two calls with the same shape, so a window draws one chooser for both.

// The calls this module answers on top of the session contract.
const (
	// MethodGuide returns a *Guide for the open disc. No parameters.
	MethodGuide = "guide"
	// MethodChoose plays one of the guide's choices: {"id": "..."}. It answers with a
	// presentation, like "title".
	MethodChoose = "choose"
)

// Limits on what a guide lists. A disc with ninety-nine titles isn't a menu.
const (
	maxOthers   = 24
	maxEpisodes = 64
	maxNotes    = 8
	maxRepairs  = 12
)

// Guide is the engine's reading of a disc, in a form for a window.
type Guide struct {
	Kind string `json:"kind"` // dvd
	// Feature is what the engine picked as the film, nil when the disc has no title.
	Feature *Choice `json:"feature,omitempty"`
	// Why is one sentence on why that pick, and Reasons what else was decided.
	Why     string   `json:"why,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
	// Confidence is how sure the pick is, from 0 to 1, and Level the same as high,
	// medium or low.
	Confidence float64 `json:"confidence"`
	Level      string  `json:"level"`
	// Versions are other ways to see the feature: camera angles, the cuts parental
	// levels choose, and other titles that are mostly the same video.
	Versions []Choice `json:"versions,omitempty"`
	// Episodes are a TV disc's episodes in viewing order, and PlayAll the title that
	// plays them one after another.
	Episodes []Choice `json:"episodes,omitempty"`
	PlayAll  *Choice  `json:"playAll,omitempty"`
	// Others are the titles a viewer might want instead, best first. Decoys are left
	// out; Decoys counts them, and Hidden everything that isn't listed.
	Others []Choice `json:"others,omitempty"`
	Decoys int      `json:"decoys,omitempty"`
	Hidden int      `json:"hidden,omitempty"`
	// Warnings are the engine's notes for people. Repairs says where the disc's
	// tables disagreed, and RepairCount how many such places there were in all.
	Warnings    []string `json:"warnings,omitempty"`
	Repairs     []string `json:"repairs,omitempty"`
	RepairCount int      `json:"repairCount,omitempty"`
	// PaddedSectors is how many sectors the engine couldn't read and took as empty
	// so far, and ReadRetries how many reads it tried again.
	PaddedSectors int64 `json:"paddedSectors,omitempty"`
	ReadRetries   int64 `json:"readRetries,omitempty"`
}

// Choice is one thing a viewer can ask for by its ID.
type Choice struct {
	ID string `json:"id"`
	// Kind: feature, version, episode, playAll or title.
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Title is the title that plays it, and Titles all of them, in order, when the
	// disc cuts a film into several.
	Title    int     `json:"title"`
	Titles   []int   `json:"titles,omitempty"`
	Duration float64 `json:"durationSec"`
	Chapters int     `json:"chapters,omitempty"`
	// StartSec and Chapter say where it begins inside Title: an episode of a title
	// that plays them all.
	StartSec float64 `json:"startSec,omitempty"`
	Chapter  int     `json:"chapter,omitempty"`
	// Angle and ParentalLevel are what a version sets before it plays.
	Angle         int `json:"angle,omitempty"`
	ParentalLevel int `json:"parentalLevel,omitempty"`
	// Source says how a feature was found: chapters, commands or chain.
	Source string `json:"source,omitempty"`
	// OneStream says a feature in several titles plays as one stream with one seek
	// bar. Without it the disc's own commands carry it from title to title.
	OneStream bool `json:"oneStream,omitempty"`
}

func clock(sec float64) string {
	n := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}

// classWords says a title's class in words for a menu.
var classWords = map[string]string{
	wittydvd.ClassPlayAll:    "plays all the episodes",
	wittydvd.ClassEpisode:    "episode",
	wittydvd.ClassDuplicate:  "the same as another title",
	wittydvd.ClassAlternate:  "another cut of the feature",
	wittydvd.ClassPart:       "part of the feature",
	wittydvd.ClassIntro:      "intro",
	wittydvd.ClassStub:       "a few seconds",
	wittydvd.ClassTrailer:    "trailer",
	wittydvd.ClassFeaturette: "featurette",
	wittydvd.ClassExtra:      "extra",
}

// firstSentence cuts a decision's summary down to its first sentence.
func firstSentence(s string) string {
	if at := strings.Index(s, ". "); at >= 0 {
		return s[:at+1]
	}
	return s
}

// rank is the engine's ranking of this disc, with the menus' buttons read. The engine
// works it out once for an opened disc.
func (s *dvdSession) rank() *wittydvd.Ranking { return s.opened.Analyze() }

// Guide puts the ranking in a form for a window. The lists are built once; the read
// counts are current.
func (s *dvdSession) Guide() *Guide {
	if s.guide == nil {
		s.guide = s.buildGuide()
	}
	g := *s.guide
	st := s.opened.ReadStats()
	g.PaddedSectors, g.ReadRetries = st.BadSectors, st.Retries
	return &g
}

func (s *dvdSession) buildGuide() *Guide {
	r := s.rank()
	g := &Guide{Kind: "dvd", Confidence: r.Confidence, Level: r.Level}
	if g.Level == "" {
		g.Level = "low"
	}
	main := r.Candidate(r.Main)
	inFeature := map[int]bool{}
	if main != nil {
		f := &Choice{ID: "feature", Kind: "feature", Label: fmt.Sprintf("Title %d", main.TitleNum), Title: main.TitleNum, Duration: main.Duration, Chapters: main.Chapters, Source: main.Path}
		inFeature[main.TitleNum] = true
		if pl := r.Feature; pl != nil {
			f.Duration, f.Source = pl.Duration, pl.Source
			if len(pl.Titles) > 1 {
				f.Titles = append([]int(nil), pl.Titles...)
				f.Label = "Titles " + joinInts(pl.Titles)
				f.Chapters = 0
				for _, t := range pl.Titles {
					inFeature[t] = true
					if c := r.Candidate(t); c != nil {
						f.Chapters += c.Chapters
					}
				}
			}
		}
		if fp := s.plan(); fp != nil {
			f.Chapters = len(fp.ts.Chapters)
			f.OneStream = len(f.Titles) > 1
		}
		switch f.Source {
		case "chain":
			f.Detail = fmt.Sprintf("one film in %d titles", len(f.Titles))
		case "commands":
			f.Detail = "put together by the disc's commands"
		}
		g.Feature = f
	}
	if w := r.Why; w != nil {
		g.Why = firstSentence(w.Summary)
	}
	for _, d := range r.Decisions {
		if d.Kind != "main" && len(g.Reasons) < maxNotes {
			g.Reasons = append(g.Reasons, d.Summary)
		}
	}
	for _, v := range r.Versions {
		c := Choice{Kind: "version", Label: v.Label, Title: v.Title, Duration: v.Duration}
		switch v.Kind {
		case "angle":
			c.ID, c.Angle = fmt.Sprintf("angle:%d", v.Angle), v.Angle
		case "parental":
			if len(v.Levels) == 0 {
				continue
			}
			// The highest level of a cut is the one a player set to "no limit" is nearest to.
			c.ParentalLevel = v.Levels[len(v.Levels)-1]
			c.ID = fmt.Sprintf("parental:%d", c.ParentalLevel)
		case "cut":
			c.ID = fmt.Sprintf("cut:%d", v.Title)
			if v.Shared > 0 {
				c.Detail = fmt.Sprintf("shares %.0f%% of its video with the feature", 100*v.Shared)
			}
		default:
			continue
		}
		g.Versions = append(g.Versions, c)
	}
	for _, e := range r.Episodes {
		if len(g.Episodes) >= maxEpisodes {
			break
		}
		c := Choice{ID: fmt.Sprintf("episode:%d", e.Index), Kind: "episode", Label: fmt.Sprintf("Episode %d", e.Index), Title: e.Title, Duration: e.Duration, StartSec: e.StartSec}
		if e.Source == "titles" {
			c.Detail = fmt.Sprintf("title %d", e.Title)
			if t := r.Candidate(e.Title); t != nil {
				c.Chapters = t.Chapters
			}
		} else {
			c.Chapter = e.FirstChapter
			c.Detail = fmt.Sprintf("in title %d, from %s", e.Title, clock(e.StartSec))
		}
		g.Episodes = append(g.Episodes, c)
	}
	if c := r.Candidate(r.PlayAll); c != nil && r.PlayAll != 0 {
		g.PlayAll = &Choice{ID: "playall", Kind: "playAll", Label: "Play all", Detail: fmt.Sprintf("title %d", c.TitleNum), Title: c.TitleNum, Duration: c.Duration, Chapters: c.Chapters}
	}
	g.Decoys = len(r.Decoys)
	for _, c := range r.Candidates {
		switch {
		case inFeature[c.TitleNum]:
			continue
		case c.Class == wittydvd.ClassDecoy, len(g.Others) >= maxOthers:
			g.Hidden++
			continue
		}
		g.Others = append(g.Others, Choice{ID: fmt.Sprintf("title:%d", c.TitleNum), Kind: "title", Label: fmt.Sprintf("Title %d", c.TitleNum), Detail: classWords[c.Class], Title: c.TitleNum, Duration: c.Duration, Chapters: c.Chapters})
	}
	for _, w := range r.Warnings {
		if len(g.Warnings) < maxNotes {
			g.Warnings = append(g.Warnings, w)
		}
	}
	g.RepairCount = len(s.opened.Disc.Repairs)
	for _, rep := range s.opened.Disc.Repairs {
		if len(g.Repairs) < maxRepairs {
			g.Repairs = append(g.Repairs, rep.Note)
		}
	}
	return g
}

// Choose plays one of the guide's choices.
func (s *dvdSession) Choose(id string) (*Presentation, error) {
	g := s.Guide()
	var c *Choice
	pick := func(list []Choice) {
		for i := range list {
			if list[i].ID == id {
				c = &list[i]
			}
		}
	}
	if g.Feature != nil && g.Feature.ID == id {
		c = g.Feature
	}
	if g.PlayAll != nil && g.PlayAll.ID == id {
		c = g.PlayAll
	}
	pick(g.Versions)
	pick(g.Episodes)
	pick(g.Others)
	if c == nil {
		return nil, &RefusalError{Code: session.CodeMissing, Message: "This disc has no such choice."}
	}
	regs := s.nav.Registers()
	switch {
	case c.Angle > 0:
		s.nav.SetAngle(c.Angle)
	case c.ParentalLevel > 0:
		regs.SPRM[wittydvd.SprmParental] = uint16(c.ParentalLevel)
	case c.Kind == "feature":
		// The film as the engine picked it: the first angle, and the cut the disc
		// gives a player with no parental limit set.
		s.nav.SetAngle(1)
		regs.SPRM[wittydvd.SprmParental] = s.parental
	}
	if c.StartSec > 0 {
		return s.playFrom(c.Title, c.Chapter, c.StartSec), nil
	}
	return s.PlayTitle(c.Title, max(c.Chapter, 1)), nil
}

// ---------------------------------------------------------------------------
// The feature as one stream
// ---------------------------------------------------------------------------

// featurePlan is the main feature laid out on one timeline, when that is more than its
// title's chapter table gives: a film the disc cuts into several titles and plays in a
// row, one its commands put together, or one with cells that point at nothing.
type featurePlan struct {
	// ts holds every cell of the feature; its Number is the title the feature
	// starts at.
	ts *wittydvd.TitleSummary
	// titles names the title each of ts.Cells belongs to.
	titles []int
}

// find is the place of a cell in the plan, or -1.
func (fp *featurePlan) find(ttn, pgcn, cellN int) int {
	for i, c := range fp.ts.Cells {
		if fp.titles[i] == ttn && c.PGCN == pgcn && c.CellN == cellN {
			return i
		}
	}
	return -1
}

// A cell that claims more than this is not played (the engine's own limit).
const absurdCellSec = 4 * 3600

// plan builds the feature's plan from the engine's playlist, once. It is nil when the
// feature is simply its title as the chapter table gives it, or when its parts can't
// share a stream: then the navigator plays it title by title, as the disc says.
func (s *dvdSession) plan() *featurePlan {
	if s.planned {
		return s.feature
	}
	s.planned = true
	r := s.rank()
	pl := r.Feature
	if pl == nil || len(pl.Segments) == 0 || len(pl.Titles) == 0 {
		return nil
	}
	head := r.Candidate(pl.Titles[0])
	if head == nil || head.Summary() == nil {
		return nil
	}
	hs := head.Summary()
	out := &wittydvd.TitleSummary{Number: hs.Number, VTS: hs.VTS, VTSTitle: hs.VTSTitle, Angles: hs.Angles, Audio: hs.Audio, Subp: hs.Subp, Video: hs.Video,
		ChapterSource: hs.ChapterSource, Estimated: hs.Estimated, Truncated: hs.Truncated, Path: pl.Source}
	fp := &featurePlan{ts: out}
	v := s.opened.Disc.VTS[hs.VTS]
	if v == nil {
		return nil
	}
	var first *wittydvd.PGC
	dropped := false
	for _, t := range pl.Titles {
		c := r.Candidate(t)
		if c == nil || c.Summary() == nil || c.VTS != hs.VTS {
			return nil // parts in different title sets have different streams
		}
		ts := c.Summary()
		// Where each cell of this part lands on the feature's timeline, for its
		// chapters; -1 for a cell that isn't played.
		at := make([]float64, len(ts.Cells))
		for i, tc := range ts.Cells {
			at[i] = -1
			if !inSegments(pl.Segments, t, tc) {
				dropped = true
				continue
			}
			if tc.PGCN < 1 || tc.PGCN > len(v.PGCs) {
				return nil
			}
			p := v.PGCs[tc.PGCN-1].PGC
			if first == nil {
				first = p
			} else if !sameStreams(first, p) {
				return nil
			}
			dur := ts.Duration - tc.Start
			if i+1 < len(ts.Cells) {
				dur = ts.Cells[i+1].Start - tc.Start
			}
			if dur < 0 {
				dur = 0
			}
			at[i] = out.Duration
			tc.Start = out.Duration
			tc.Cell.Duration = dur
			out.Cells = append(out.Cells, tc)
			fp.titles = append(fp.titles, t)
			out.Duration += dur
		}
		for _, ct := range ts.Chapters {
			k := -1
			for i, tc := range ts.Cells {
				if tc.Start <= ct+0.01 {
					k = i
				}
			}
			if k < 0 {
				continue
			}
			into := ct - ts.Cells[k].Start
			for k < len(at) && at[k] < 0 {
				k, into = k+1, 0 // the chapter's own cell isn't played: it starts at the next that is
			}
			if k >= len(at) {
				continue
			}
			when := at[k] + into
			if n := len(out.Chapters); n == 0 || when > out.Chapters[n-1]+0.001 {
				out.Chapters = append(out.Chapters, when)
			}
		}
	}
	if len(out.Cells) == 0 {
		return nil
	}
	if len(pl.Titles) == 1 && hs.Path != "commands" && !dropped {
		return nil // the title's own chapter table already says all of it
	}
	s.feature = fp
	return fp
}

// inSegments says whether the engine's playlist plays a cell of a title.
func inSegments(segs []wittydvd.PlaylistSegment, title int, tc wittydvd.TitleCell) bool {
	c := tc.Cell
	if c.LastSector < c.FirstSector || c.Duration > absurdCellSec {
		return false
	}
	for _, sg := range segs {
		if sg.Title == title && sg.PGC == tc.PGCN && tc.CellN >= sg.FirstCell && tc.CellN <= sg.LastCell && c.FirstSector >= sg.FirstSector && c.LastSector <= sg.LastSector {
			return true
		}
	}
	return false
}

// sameStreams says two program chains map the title set's audio and subtitle streams
// the same way and colour subtitles alike, so their cells can share one stream.
func sameStreams(a, b *wittydvd.PGC) bool {
	if a == b {
		return true
	}
	if a.AudioControl != b.AudioControl || a.Palette != b.Palette {
		return false
	}
	for i := range a.SubpControl {
		x, y := a.SubpControl[i], b.SubpControl[i]
		if (x == nil) != (y == nil) || (x != nil && *x != *y) {
			return false
		}
	}
	return true
}

// playFrom plays a title from a place inside it: an episode of a title that plays them
// all. chapter is where the disc's own chapter table puts that place.
func (s *dvdSession) playFrom(ttn, chapter int, sec float64) *Presentation {
	p := s.show(s.nav.PlayTitle(ttn, max(chapter, 1)))
	if p == nil || p.Kind != "title" || s.playing == nil || sec <= 0 || sec >= s.playing.ts.Duration {
		return p
	}
	return s.jump(sec)
}

// jump restarts the stream being played at a position, and tells the navigator which
// cell that is.
func (s *dvdSession) jump(sec float64) *Presentation {
	s.syncPosition(sec)
	s.seekTo = sec
	return s.present(s.nav.Current(), true)
}

// ---------------------------------------------------------------------------
// The calls
// ---------------------------------------------------------------------------

// Host answers a player's calls about one disc at a time: the session contract's, and
// this module's own two. A player's built-in copy and the helper program both use it,
// so they answer alike.
type Host struct {
	base session.Host
	cur  *dvdSession
}

// NewHost makes a host that opens discs with Open.
func NewHost() *Host {
	h := &Host{}
	h.base.Open = func(path string, drive bool, prefs Prefs) (session.Session, error) {
		h.cur = nil
		s, err := Open(path, drive, prefs)
		if err != nil {
			return nil, err
		}
		h.cur, _ = s.(*dvdSession)
		return s, nil
	}
	return h
}

// Close ends the session in progress.
func (h *Host) Close() {
	h.cur = nil
	h.base.Close()
}

// Handle answers one call with a result to send as JSON, and an optional blob.
func (h *Host) Handle(method string, raw json.RawMessage) (any, []byte, error) {
	switch method {
	case "close":
		h.cur = nil
	case MethodGuide, MethodChoose:
		if h.cur == nil {
			return nil, nil, fmt.Errorf("no disc is open")
		}
		if method == MethodGuide {
			return h.cur.Guide(), nil, nil
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, nil, err
		}
		p, err := h.cur.Choose(in.ID)
		if err != nil {
			return nil, nil, err
		}
		return p, nil, nil
	}
	return h.base.Handle(method, raw)
}
