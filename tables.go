// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// The address tables of an index file: where the cells and VOBUs of a VOB
// really lie (C_ADT, VOBU_ADMAP) and where playback is at each step of time
// (TMAPTI). A player needs none of them to play a healthy disc. They are read
// here because they say the same things as the program chains a second time,
// which is what lets a wrong table be told from a right one.

import (
	"fmt"
	"sort"
)

// CellAddr is one row of a cell address table: a piece of a cell and the
// sectors it occupies in its VOB. An interleaved cell has several rows.
type CellAddr struct {
	VOBID  int    `json:"vobId"`
	CellID int    `json:"cellId"`
	First  uint32 `json:"firstSector"`
	Last   uint32 `json:"lastSector"`
}

// timeMap is one program chain's time map: Entries[i] is the VOBU playing
// (i+1)*Unit seconds into the chain.
type timeMap struct {
	unit    int
	entries []uint32
}

type tableState uint8

const (
	tableMissing tableState = iota
	tableOK
	tableDamaged
)

func (s tableState) String() string {
	return [...]string{"missing", "ok", "damaged"}[s]
}

// tableDiag is how one table of an index file read.
type tableDiag struct {
	state tableState
	note  string
}

func damaged(note string) tableDiag { return tableDiag{state: tableDamaged, note: note} }

// Table names, as the public references spell them.
const (
	tblTTSRPT     = "TT_SRPT"
	tblFirstPlay  = "FP_PGC"
	tblPTLMAIT    = "PTL_MAIT"
	tblVMGMPGCIUT = "VMGM_PGCI_UT"
	tblVMGMCADT   = "VMGM_C_ADT"
	tblVMGMADMAP  = "VMGM_VOBU_ADMAP"
	tblPTT        = "VTS_PTT_SRPT"
	tblPGCIT      = "VTS_PGCIT"
	tblVTSMPGCIUT = "VTSM_PGCI_UT"
	tblTMAPTI     = "VTS_TMAPTI"
	tblVTSMCADT   = "VTSM_C_ADT"
	tblVTSMADMAP  = "VTSM_VOBU_ADMAP"
	tblCADT       = "VTS_C_ADT"
	tblADMAP      = "VTS_VOBU_ADMAP"
)

var (
	vmgTables = []string{tblTTSRPT, tblFirstPlay, tblPTLMAIT, tblVMGMPGCIUT, tblVMGMCADT, tblVMGMADMAP}
	vtsTables = []string{tblPTT, tblPGCIT, tblVTSMPGCIUT, tblTMAPTI, tblVTSMCADT, tblVTSMADMAP, tblCADT, tblADMAP}
)

// ifoDiag is how each table of one index file read; a table the file does
// not point at is absent (missing).
type ifoDiag map[string]tableDiag

func (d ifoDiag) set(name string, ok bool, note string) {
	if ok {
		d[name] = tableDiag{state: tableOK}
		return
	}
	d[name] = damaged(note)
}

// usable says the table was there and read without a fault.
func (d ifoDiag) usable(name string) bool { return d[name].state == tableOK }

// TableReport is how one table of an index file read, and which copy of the
// file it was taken from.
type TableReport struct {
	Name  string `json:"name"`
	State string `json:"state"` // ok, missing or damaged
	From  string `json:"from"`  // IFO or BUP
	Note  string `json:"note,omitempty"`
}

// parseCADT reads a cell address table.
func parseCADT(r reader, base int) ([]CellAddr, tableDiag) {
	if !r.ok(base, 8) {
		return nil, damaged("outside the file")
	}
	end := r.rel(base, r.u32(base+4))
	if end < 0 {
		end = len(r.b) - 1
	}
	n := (end + 1 - base - 8) / 12
	diag := tableDiag{state: tableOK}
	if n <= 0 {
		return nil, damaged("lists no cell")
	}
	if n > maxCellAddrs {
		n, diag = maxCellAddrs, damaged(fmt.Sprintf("lists more than %d cells", maxCellAddrs))
	}
	out := make([]CellAddr, 0, n)
	bad := 0
	for i := 0; i < n; i++ {
		o := base + 8 + i*12
		if !r.ok(o, 12) {
			diag = damaged("cut short")
			break
		}
		a := CellAddr{VOBID: r.u16(o), CellID: r.u8(o + 2), First: r.u32(o + 4), Last: r.u32(o + 8)}
		if a.Last < a.First || a.VOBID == 0 || a.CellID == 0 {
			bad++
			continue
		}
		out = append(out, a)
	}
	if bad > 0 && diag.state == tableOK {
		diag = damaged(fmt.Sprintf("%d of %d rows make no sense", bad, n))
	}
	if len(out) == 0 && diag.state == tableOK {
		diag = damaged("lists no cell")
	}
	return out, diag
}

// parseADMAP reads a VOBU address map: the first sector of every VOBU, in
// order. Rows out of order are dropped and reported.
func parseADMAP(r reader, base int) ([]uint32, tableDiag) {
	if !r.ok(base, 4) {
		return nil, damaged("outside the file")
	}
	end := r.rel(base, r.u32(base))
	if end < 0 {
		end = len(r.b) - 1
	}
	n := (end + 1 - base - 4) / 4
	diag := tableDiag{state: tableOK}
	if n <= 0 {
		return nil, damaged("lists no VOBU")
	}
	if n > maxVOBUs {
		n, diag = maxVOBUs, damaged("lists more VOBUs than a disc can hold")
	}
	out := make([]uint32, 0, n)
	bad := 0
	for i := 0; i < n; i++ {
		o := base + 4 + i*4
		if !r.ok(o, 4) {
			diag = damaged("cut short")
			break
		}
		s := r.u32(o)
		if k := len(out); k > 0 && s <= out[k-1] {
			bad++
			continue
		}
		out = append(out, s)
	}
	if bad > 0 && diag.state == tableOK {
		diag = damaged(fmt.Sprintf("%d of %d rows are out of order", bad, n))
	}
	return out, diag
}

// parseTMAPTI reads the time maps, one per program chain.
func parseTMAPTI(r reader, base int) ([]timeMap, tableDiag) {
	if !r.ok(base, 8) {
		return nil, damaged("outside the file")
	}
	n := r.u16(base)
	if n == 0 {
		return nil, damaged("lists no time map")
	}
	diag := tableDiag{state: tableOK}
	if n > maxTimeMaps {
		n, diag = maxTimeMaps, damaged("lists too many time maps")
	}
	// Every map's entries are four bytes of the file, so the file's size
	// bounds them however many maps point at the same place.
	left := len(r.b) / 4
	out := make([]timeMap, 0, min(n, (len(r.b)-base)/4))
	for i := 0; i < n; i++ {
		o := base + 8 + i*4
		if !r.ok(o, 4) {
			diag = damaged("cut short")
			break
		}
		at := r.rel(base, r.u32(o))
		var m timeMap
		if r.ok(at, 4) {
			m.unit = r.u8(at)
			cnt := min(r.u16(at+2), maxTimeEntries, left)
			for k := 0; k < cnt; k++ {
				e := at + 4 + k*4
				if !r.ok(e, 4) {
					diag = damaged(fmt.Sprintf("time map %d cut short", i+1))
					break
				}
				m.entries = append(m.entries, r.u32(e)&0x7fffffff)
			}
			left -= len(m.entries)
		} else {
			diag = damaged(fmt.Sprintf("time map %d outside the file", i+1))
		}
		out = append(out, m)
	}
	return out, diag
}

// at is how far into its program chain the VOBU at a sector plays, in
// seconds, and whether the map can say. It only holds for chains that play
// their sectors in order.
func (m timeMap) at(sector uint32) (float64, bool) {
	if m.unit <= 0 || len(m.entries) == 0 {
		return 0, false
	}
	if !sort.SliceIsSorted(m.entries, func(i, j int) bool { return m.entries[i] < m.entries[j] }) {
		return 0, false
	}
	k := sort.Search(len(m.entries), func(i int) bool { return m.entries[i] > sector })
	return float64(k * m.unit), true
}

// cellIndex answers "where does the address table put this cell" quickly.
type cellIndex struct {
	byID map[uint32]cellRange // (VOB id, cell id) -> the sectors of all its pieces
	rows []CellAddr           // sorted by first sector
}

func cellKey(vob, cell int) uint32 { return uint32(vob)<<8 | uint32(cell&0xff) }

func newCellIndex(addrs []CellAddr) *cellIndex {
	if len(addrs) == 0 {
		return nil
	}
	ix := &cellIndex{byID: make(map[uint32]cellRange, len(addrs)), rows: append([]CellAddr(nil), addrs...)}
	for _, a := range addrs {
		k := cellKey(a.VOBID, a.CellID)
		if r, ok := ix.byID[k]; ok {
			ix.byID[k] = cellRange{min(r.first, a.First), max(r.last, a.Last)}
		} else {
			ix.byID[k] = cellRange{a.First, a.Last}
		}
	}
	sort.Slice(ix.rows, func(i, j int) bool { return ix.rows[i].First < ix.rows[j].First })
	return ix
}

// listed says the table holds a cell with these ids whose sectors take in
// the cell's first sector: what a program chain says agrees with what is on
// the disc.
func (ix *cellIndex) listed(c Cell) bool {
	r, ok := ix.byID[cellKey(c.VOBID, c.CellID)]
	return ok && c.FirstSector >= r.first && c.FirstSector <= r.last
}

// startsPiece says some row of the table starts at this sector.
func (ix *cellIndex) startsPiece(sector uint32) (CellAddr, bool) {
	i := sort.Search(len(ix.rows), func(i int) bool { return ix.rows[i].First >= sector })
	if i < len(ix.rows) && ix.rows[i].First == sector {
		return ix.rows[i], true
	}
	return CellAddr{}, false
}

// last is the highest sector any row reaches.
func (ix *cellIndex) last() uint32 {
	var m uint32
	for _, a := range ix.rows {
		m = max(m, a.Last)
	}
	return m
}

// isVOBUStart says the address map lists a VOBU starting at the sector.
func isVOBUStart(vobus []uint32, sector uint32) bool {
	i := sort.Search(len(vobus), func(i int) bool { return vobus[i] >= sector })
	return i < len(vobus) && vobus[i] == sector
}

// nextVOBU is the first VOBU start after sector, or false.
func nextVOBU(vobus []uint32, sector uint32) (uint32, bool) {
	i := sort.Search(len(vobus), func(i int) bool { return vobus[i] > sector })
	if i < len(vobus) {
		return vobus[i], true
	}
	return 0, false
}
