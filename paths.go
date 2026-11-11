// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// What a title plays. There are two ways to find out, and discs that guard
// their film make them disagree:
//
//   - the chapter table: start at the program chain the first chapter names
//     and follow next-PGC links (TitleAngle);
//   - the commands: start the title in the disc's own virtual machine and
//     note every cell it shows until it leaves the title (playedTitle). A film
//     put together by LinkPGCN commands only shows up this way.
//
// Both are bounded: a title is never read past maxTitleCells cells or
// maxTitlePGCs chains, and dry runs draw on a shared fuel budget.

const (
	maxTitleCells = 4096
	maxTitlePGCs  = 512
	// minEstimateSectors is how much video a cell with no play time must
	// hold before a time is worked out for it.
	minEstimateSectors = 256
)

// TitleAngle flattens a title for one camera angle (1-based); cells inside
// angle blocks are replaced by that angle's cell.
func (d *Disc) TitleAngle(number, angle int) *TitleSummary {
	if d == nil || d.VMG == nil || number < 1 || number > len(d.VMG.Titles) {
		return nil
	}
	t := d.VMG.Titles[number-1]
	v := d.VTS[t.VTS]
	if v == nil || t.VTSTitle < 1 || t.VTSTitle > len(v.PTT) {
		return nil
	}
	ts := &TitleSummary{Number: number, VTS: t.VTS, VTSTitle: t.VTSTitle, Angles: t.Angles, Audio: v.Audio, Subp: v.Subp, Video: v.Video, Path: "chapters"}
	ptts := v.PTT[t.VTSTitle-1]
	if len(ptts) == 0 {
		return ts
	}
	// Walk PGCs starting from the first chapter's PGC, following next_pgcn
	// (or, without one, the next PGC the chapter table names).
	var chain []int
	cells := 0
	seen := map[int]bool{}
	for pgcn := ptts[0].PGCN; pgcn >= 1 && pgcn <= len(v.PGCs) && !seen[pgcn]; pgcn = nextTitlePGC(v, t.VTSTitle, pgcn) {
		if len(chain) >= maxTitlePGCs {
			ts.Truncated = true
			break
		}
		seen[pgcn] = true
		chain = append(chain, pgcn)
		cells += len(v.PGCs[pgcn-1].PGC.Cells)
	}
	ts.Cells = make([]TitleCell, 0, min(cells, maxTitleCells))
	for _, pgcn := range chain {
		p := v.PGCs[pgcn-1].PGC
		for n := 1; n >= 1 && n <= len(p.Cells); n = nextCellN(p, n) {
			if len(ts.Cells) >= maxTitleCells {
				ts.Truncated = true
				break
			}
			k := angleCell(p, n, angle)
			ts.Cells = append(ts.Cells, TitleCell{PGCN: pgcn, CellN: k, Cell: p.Cells[k-1]})
		}
		if ts.Truncated {
			break
		}
	}
	finishSummary(ts, v, ptts)
	return ts
}

// finishSummary gives the cells of a title their start times and works out
// the chapter times.
//
// A chapter starts where the program its chapter table entry names starts.
// Entries that name nothing the title plays keep their number (the time of
// the chapter before them) unless they come last, so chapter N here is
// chapter N to the navigator. When no entry can be placed at all, each
// program the title plays becomes a chapter.
func finishSummary(ts *TitleSummary, v *VTS, ptts []PTT) {
	ct := cellTimer{v: v}
	type prog struct{ pgcn, pgn int }
	at := map[prog]float64{}
	var starts []float64
	var t float64
	prev := prog{}
	for i := range ts.Cells {
		tc := &ts.Cells[i]
		var p *PGC
		if tc.PGCN >= 1 && tc.PGCN <= len(v.PGCs) {
			p = v.PGCs[tc.PGCN-1].PGC
		}
		dur := tc.Cell.Duration
		if p != nil {
			cur := prog{tc.PGCN, programOf(p, tc.CellN)}
			if cur != prev {
				if _, ok := at[cur]; !ok {
					at[cur] = t
				}
				starts = append(starts, t)
				prev = cur
			}
			var est bool
			if dur, est = ct.duration(tc.PGCN, p, tc.CellN-1); est {
				ts.Estimated = true
			}
		}
		tc.Start = t
		t += dur
	}
	ts.Duration = t
	ts.Chapters, ts.ChapterSource = nil, ""
	last := -1
	var times []float64
	for i, x := range ptts {
		s, ok := at[prog{x.PGCN, x.PGN}]
		if ok {
			last = i
		} else if n := len(times); n > 0 {
			s = times[n-1]
		}
		times = append(times, s)
	}
	switch {
	case last >= 0:
		ts.Chapters, ts.ChapterSource = times[:last+1], "chapterTable"
	case len(starts) > 0:
		ts.Chapters, ts.ChapterSource = starts, "programs"
	}
}

// cellTimer answers how long a cell plays. That is the cell's own play time
// whenever it has a believable one. A cell that holds video but says zero
// gets its time from the chain's time map, or else its share, by sectors, of
// what the chain's play time leaves over.
type cellTimer struct {
	v     *VTS
	cache map[*PGC][]float64
}

func needsEstimate(c Cell) bool {
	return c.Duration == 0 && c.LastSector >= c.FirstSector && c.LastSector-c.FirstSector >= minEstimateSectors
}

func (ct *cellTimer) duration(pgcn int, p *PGC, i int) (float64, bool) {
	if i < 0 || i >= len(p.Cells) {
		return 0, false
	}
	c := p.Cells[i]
	if c.Duration > absurdCellSec {
		return 0, false // a claim no disc can hold: not counted
	}
	if !needsEstimate(c) {
		return c.Duration, false
	}
	est, ok := ct.cache[p]
	if !ok {
		est = ct.estimate(pgcn, p)
		if ct.cache == nil {
			ct.cache = map[*PGC][]float64{}
		}
		ct.cache[p] = est
	}
	if est == nil || est[i] <= 0 {
		return 0, false
	}
	return est[i], true
}

func (ct *cellTimer) estimate(pgcn int, p *PGC) []float64 {
	out := make([]float64, len(p.Cells))
	var tm timeMap
	if ct.v != nil && pgcn >= 1 && pgcn <= len(ct.v.timeMaps) && ct.v.tableUsable(tblTMAPTI) {
		tm = ct.v.timeMaps[pgcn-1]
	}
	var known float64
	var need uint64
	for _, c := range p.Cells {
		switch {
		case needsEstimate(c):
			need += uint64(c.LastSector-c.FirstSector) + 1
		case c.Duration <= absurdCellSec:
			known += c.Duration
		}
	}
	left := p.Duration - known
	if p.Duration > 3*absurdCellSec {
		left = 0
	}
	any := false
	for i, c := range p.Cells {
		if !needsEstimate(c) {
			continue
		}
		t0, ok0 := tm.at(uint64(c.FirstSector))
		t1, ok1 := tm.at(uint64(c.LastSector) + 1)
		switch {
		case ok0 && ok1 && t1 > t0:
			out[i] = t1 - t0
		case left > 0 && need > 0:
			out[i] = left * float64(uint64(c.LastSector-c.FirstSector)+1) / float64(need)
		}
		any = any || out[i] > 0
	}
	if !any {
		return nil
	}
	return out
}

// played is what a dry run of one title found.
type played struct {
	ts *TitleSummary
	// next is the title the disc went on to by itself when this one ended
	// (0: it stopped, or went to a menu).
	next int
	// whole says the run reached the title's own end rather than being cut
	// off by a loop, the step limit or the fuel.
	whole bool
	loop  bool
}

// playedTitle starts a title in the disc's own virtual machine, as a viewer
// who picked it would, and notes every cell shown until the disc leaves the
// title. Random numbers are fixed, so the answer is the same every time.
// Nothing is read from the VOBs.
func (d *Disc) playedTitle(number int, ro RegisterOptions, fuel *int64, hint int) (out *played) {
	if number < 1 || number > len(d.VMG.Titles) {
		return nil
	}
	t := d.VMG.Titles[number-1]
	v := d.VTS[t.VTS]
	if v == nil {
		return nil
	}
	defer func() {
		// The navigator is fuzzed, but an analysis must never take the
		// disc down with it.
		if recover() != nil {
			out = nil
		}
	}()
	if ro.Rand == nil {
		ro.Rand = deterministicRand()
	}
	nav := NewNav(d, NavOptions{RegisterOptions: ro})
	nav.lean = true
	nav.st.regs.fuel = fuel
	ts := &TitleSummary{Number: number, VTS: t.VTS, VTSTitle: t.VTSTitle, Angles: t.Angles, Audio: v.Audio, Subp: v.Subp, Video: v.Video, Path: "commands"}
	res := &played{ts: ts}
	type at struct{ pgcn, cell int }
	seen := map[at]bool{}
	ts.Cells = make([]TitleCell, 0, min(hint, maxTitleCells))
	st := nav.PlayTitle(number, 1)
	for {
		if st == nil || st.Kind != StepTitle || st.Title == nil || st.VTS != t.VTS {
			res.whole = st != nil && st.Reason != errFuel
			break
		}
		if st.Title.TTN != number {
			res.next, res.whole = st.Title.TTN, true
			break
		}
		k := at{st.PGCN, st.CellN}
		if seen[k] {
			res.loop = true
			break
		}
		seen[k] = true
		if len(ts.Cells) >= maxTitleCells {
			ts.Truncated = true
			break
		}
		p := nav.pgcOf(nav.st, DomainVTS, st.VTS, st.PGCN)
		if p == nil || st.CellN < 1 || st.CellN > len(p.Cells) {
			break
		}
		ts.Cells = append(ts.Cells, TitleCell{PGCN: st.PGCN, CellN: st.CellN, Cell: p.Cells[st.CellN-1]})
		st = nav.CellEnded()
	}
	var ptts []PTT
	if t.VTSTitle >= 1 && t.VTSTitle <= len(v.PTT) {
		ptts = v.PTT[t.VTSTitle-1]
	}
	finishSummary(ts, v, ptts)
	return res
}

// samePath says two flattened titles play the same cells in the same order.
func samePath(a, b *TitleSummary) bool {
	if len(a.Cells) != len(b.Cells) {
		return false
	}
	for i := range a.Cells {
		if a.Cells[i].PGCN != b.Cells[i].PGCN || a.Cells[i].CellN != b.Cells[i].CellN {
			return false
		}
	}
	return true
}
