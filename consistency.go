// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Cross-checks between the tables of a disc. A DVD says most things twice:
// TT_SRPT counts the chapters VTS_PTT_SRPT lists, the chapter table names
// program chains the PGCIT holds, every cell of a chain appears in the cell
// address table, and every cell starts at a VOBU the address map lists. When
// two tables disagree and one of them can't be right, the other is used to
// mend it; when it can't be told which is wrong, the disagreement is only
// reported. Nothing here reads video or decrypts anything.

import "fmt"

// Repair is one disagreement between tables, and what was done about it.
type Repair struct {
	VTS   int    `json:"vts"`             // 0 for the VMG
	Title int    `json:"title,omitempty"` // the title concerned, when it is one
	Table string `json:"table"`           // the table that is wrong
	From  string `json:"from,omitempty"`  // the table it was mended from
	Fixed bool   `json:"fixed"`           // false: reported only
	Note  string `json:"note"`
}

// CrossCheck compares the tables of the disc with each other, mends what one
// table can mend in another, and returns what it found. Open has already
// called it (see Disc.Repairs); calling it again finds nothing left to mend.
func (d *Disc) CrossCheck() []Repair {
	if d == nil || d.VMG == nil {
		return nil
	}
	var out []Repair
	for n := 1; n <= maxTitleSets; n++ {
		if v := d.VTS[n]; v != nil {
			out = append(out, checkVTS(v, n)...)
		}
	}
	for i := range d.VMG.Titles {
		out = append(out, d.checkTitle(&d.VMG.Titles[i])...)
	}
	return out
}

// tableUsable says a table may be trusted as evidence: it read cleanly, or
// the title set was built by hand and has no diagnosis.
func (v *VTS) tableUsable(name string) bool { return v.diag == nil || v.diag.usable(name) }

func badRange(c Cell, size uint32) bool {
	return c.LastSector < c.FirstSector || (size != 0 && c.FirstSector >= size)
}

func checkVTS(v *VTS, n int) []Repair {
	var out []Repair
	note := func(table, from string, fixed bool, format string, a ...any) {
		out = append(out, Repair{VTS: n, Table: table, From: from, Fixed: fixed, Note: fmt.Sprintf(format, a...)})
	}
	var ix *cellIndex
	if v.tableUsable(tblCADT) {
		ix = newCellIndex(v.CellAddrs)
	}
	vobus := v.vobus
	if !v.tableUsable(tblADMAP) {
		vobus = nil
	}
	// The size of the title VOBs: the header says it, and so do the last
	// rows of the address tables.
	if ix != nil {
		last := ix.last()
		switch {
		case v.TitleVOBSectors == 0:
			v.TitleVOBSectors = last + 1
			note("VTS header", tblCADT, true, "the header gives no size for the title VOBs: %d sectors, from the cell address table", last+1)
		case last >= v.TitleVOBSectors && len(vobus) > 0 && vobus[len(vobus)-1] >= v.TitleVOBSectors:
			note("VTS header", tblCADT, true, "the header says the title VOBs are %d sectors, but the cell address table and the VOBU map both reach sector %d: their size is used", v.TitleVOBSectors, last)
			v.TitleVOBSectors = last + 1
		}
	}
	if v.MenuVOBSectors == 0 && (v.diag == nil || v.diag.usable(tblVTSMCADT)) {
		if mx := newCellIndex(v.MenuCellAddrs); mx != nil {
			v.MenuVOBSectors = mx.last() + 1
		}
	}
	size := v.TitleVOBSectors
	// Program chains against the cell address table.
	done := map[*PGC]bool{}
	mended, named, noProg, badProg := 0, 0, 0, 0
	for _, e := range v.PGCs {
		p := e.PGC
		if p == nil || done[p] {
			continue
		}
		done[p] = true
		if ix != nil {
			mended += mendCells(p, ix, size)
			for i := range p.Cells {
				c := &p.Cells[i]
				if !badRange(*c, size) && c.VOBID == 0 && c.CellID == 0 {
					if a, ok := ix.startsPiece(c.FirstSector); ok && a.Last == c.LastSector {
						c.VOBID, c.CellID = a.VOBID, a.CellID
						named++
					}
				}
			}
		}
		if len(p.Cells) == 0 {
			continue
		}
		valid := 0
		for _, entry := range p.Programs {
			if entry >= 1 && entry <= len(p.Cells) {
				valid++
			}
		}
		switch {
		case valid == 0:
			p.Programs = []int{1}
			noProg++
		case valid < len(p.Programs):
			badProg++
		}
	}
	if mended > 0 {
		note(tblPGCIT, tblCADT, true, "%s pointed at sectors that don't exist: their place was taken from the cell address table", pluralWord(mended, "cell", "cells"))
	}
	if named > 0 {
		note(tblPGCIT, tblCADT, true, "%s had no VOB and cell id: taken from the cell address table", pluralWord(named, "cell", "cells"))
	}
	if noProg > 0 {
		note(tblPGCIT, "", true, "%s had cells but no usable program map: each is read as one program", pluralWord(noProg, "program chain", "program chains"))
	}
	if badProg > 0 {
		note(tblPGCIT, "", false, "%s have programs that start at cells they don't have", pluralWord(badProg, "program chain", "program chains"))
	}
	// The cell address table against the VOBU map.
	if ix != nil && len(vobus) > 0 {
		off := 0
		for _, a := range ix.rows {
			if !isVOBUStart(vobus, a.First) {
				off++
			}
		}
		if off > 0 {
			note(tblCADT, "", false, "%d of %d rows of the cell address table don't start at a VOBU the address map lists", off, len(ix.rows))
		}
	}
	if v.tableUsable(tblTMAPTI) && len(v.timeMaps) > 0 && len(v.timeMaps) != len(v.PGCs) && v.tableUsable(tblPGCIT) {
		note(tblTMAPTI, "", false, "%d time maps for %d program chains", len(v.timeMaps), len(v.PGCs))
	}
	return out
}

// mendCells gives the cells of a chain that point at sectors that can't
// exist the place the cell address table has for their VOB and cell ids. It
// is all or nothing: every such cell must be in the table, and the chain must
// then play forwards through the VOB without a cell lying over another, as an
// authored chain does. A decoy chain that borrows real ids in a scrambled
// order is left as it is. It returns how many cells were mended.
func mendCells(p *PGC, ix *cellIndex, size uint32) int {
	// Where each cell would lie: as it says, or as the table says.
	place := make([]cellRange, len(p.Cells))
	fixes := 0
	for i, c := range p.Cells {
		place[i] = cellRange{c.FirstSector, c.LastSector}
		if !badRange(c, size) {
			continue
		}
		r, ok := ix.byID[cellKey(c.VOBID, c.CellID)]
		if !ok || c.VOBID == 0 || (size != 0 && r.first >= size) {
			return 0
		}
		place[i] = r
		fixes++
	}
	if fixes == 0 {
		return 0
	}
	for i, c := range p.Cells {
		// Cells of an angle block lie interleaved, over each other.
		if i > 0 && c.BlockType != 1 && !c.Interleaved && place[i].first <= place[i-1].last {
			return 0
		}
	}
	for i := range p.Cells {
		c := &p.Cells[i]
		if r := place[i]; r.first != c.FirstSector || r.last != c.LastSector {
			c.FirstSector, c.LastSector = r.first, r.last
			if c.LastVOBUStart < r.first || c.LastVOBUStart > r.last {
				c.LastVOBUStart = r.first
			}
		}
	}
	return fixes
}

// rebuildPTT makes a title's chapter list from its program chains: every
// program of its entry chain and of the chains its next links lead to.
func rebuildPTT(v *VTS, vtsTTN int) []PTT {
	entry := 0
	for i, e := range v.PGCs {
		if e.Entry && e.TitleNumber == vtsTTN && e.PGC != nil && len(e.PGC.Cells) > 0 {
			entry = i + 1
			break
		}
	}
	if entry == 0 {
		return nil
	}
	var list []PTT
	seen := map[int]bool{}
	for k := entry; k >= 1 && k <= len(v.PGCs) && !seen[k] && len(list) < maxPTTEntries; k = v.PGCs[k-1].PGC.NextPGCN {
		seen[k] = true
		e := v.PGCs[k-1]
		if e.PGC == nil || (k != entry && e.TitleNumber != 0 && e.TitleNumber != vtsTTN) {
			break
		}
		for pg := range max(len(e.PGC.Programs), 1) {
			list = append(list, PTT{PGCN: k, PGN: pg + 1})
		}
	}
	return list
}

func pttValid(v *VTS, p PTT) bool {
	if p.PGCN < 1 || p.PGCN > len(v.PGCs) {
		return false
	}
	pg := v.PGCs[p.PGCN-1].PGC
	return pg != nil && (len(pg.Programs) == 0 || (p.PGN >= 1 && p.PGN <= len(pg.Programs)))
}

// angleCount is the largest angle block among a title's program chains.
func angleCount(v *VTS, list []PTT) int {
	best := 1
	seen := map[int]bool{}
	for _, x := range list {
		if x.PGCN < 1 || x.PGCN > len(v.PGCs) || seen[x.PGCN] {
			continue
		}
		seen[x.PGCN] = true
		n := 0
		for _, c := range v.PGCs[x.PGCN-1].PGC.Cells {
			if c.BlockType != 1 {
				n = 0
				continue
			}
			if c.BlockMode == 1 {
				n = 0
			}
			n++
			best = max(best, n)
		}
	}
	return min(best, 9)
}

func (d *Disc) checkTitle(t *TitleSearch) []Repair {
	var out []Repair
	note := func(table, from string, fixed bool, format string, a ...any) {
		out = append(out, Repair{VTS: t.VTS, Title: t.Number, Table: table, From: from, Fixed: fixed, Note: fmt.Sprintf(format, a...)})
	}
	v := d.VTS[t.VTS]
	if v == nil {
		note(tblTTSRPT, "", false, "title %d is in title set %d, which can't be read", t.Number, t.VTS)
		return out
	}
	if t.VTSTitle < 1 || t.VTSTitle > 99 {
		note(tblTTSRPT, "", false, "title %d has no number inside its title set", t.Number)
		return out
	}
	var list []PTT
	if t.VTSTitle <= len(v.PTT) {
		list = v.PTT[t.VTSTitle-1]
	}
	if len(list) == 0 || !pttValid(v, list[0]) {
		if fresh := rebuildPTT(v, t.VTSTitle); len(fresh) > 0 {
			for len(v.PTT) < t.VTSTitle {
				v.PTT = append(v.PTT, nil)
			}
			v.PTT[t.VTSTitle-1] = fresh
			what := "has no chapter list"
			if len(list) > 0 {
				what = "has a chapter list that starts at a program chain that doesn't exist"
			}
			note(tblPTT, tblPGCIT, true, "title %d %s: rebuilt from its program chains (%s)", t.Number, what, pluralWord(len(fresh), "chapter", "chapters"))
			list = fresh
		} else if len(list) == 0 {
			note(tblPTT, "", false, "title %d has no chapter list and no entry program chain", t.Number)
			return out
		}
	}
	bad := 0
	for _, p := range list {
		if !pttValid(v, p) {
			bad++
		}
	}
	if bad > 0 {
		note(tblPTT, "", false, "%d of title %d's %d chapters name a program chain or program that doesn't exist", bad, t.Number, len(list))
	}
	if t.Chapters != len(list) {
		note(tblTTSRPT, tblPTT, true, "TT_SRPT counts %d chapters for title %d and the chapter table lists %d", t.Chapters, t.Number, len(list))
		t.Chapters = len(list)
	}
	if n := angleCount(v, list); t.Angles != n && (t.Angles < 1 || t.Angles > 9 || n > 1) {
		note(tblTTSRPT, tblPGCIT, true, "TT_SRPT says title %d has %d angles and its angle blocks hold %d", t.Number, t.Angles, n)
		t.Angles = n
	}
	return out
}
