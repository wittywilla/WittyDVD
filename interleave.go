// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Interleaved cells.
//
// Multi-angle blocks (e.g. the same intro with English or Dutch on-screen
// text) and seamless branching store their cells interleaved on the disc:
// a run of VOBUs of one cell, then a run of another, and so on. Reading a
// cell's sector range straight through would play every angle alternately.
// A player instead follows only the VOBUs whose NAV pack names the cell
// (VOB id + cell id), which is what CellRanges does.

// SectorRange is an inclusive run of sectors in a VOB address space.
type SectorRange struct{ First, Last uint32 }

type rangesKey struct {
	sp          Space
	first, last uint32
	vob, cell   int
}

// CellRanges returns the sectors that belong to one cell. Ordinary cells are
// one contiguous range; interleaved cells are the VOBUs carrying their ids.
func (o *Opened) CellRanges(sp Space, c Cell) ([]SectorRange, error) {
	whole := []SectorRange{{c.FirstSector, c.LastSector}}
	if !(c.Interleaved || c.BlockType == 1) || c.LastSector < c.FirstSector {
		return whole, nil
	}
	k := rangesKey{sp, c.FirstSector, c.LastSector, c.VOBID, c.CellID}
	o.mu.Lock()
	if o.ranges == nil {
		o.ranges = map[rangesKey][]SectorRange{}
	}
	if r, ok := o.ranges[k]; ok {
		o.mu.Unlock()
		return r, nil
	}
	o.mu.Unlock()
	rd, err := o.Reader(sp)
	if err != nil {
		return nil, err
	}
	var out []SectorRange
	vobus := o.vobuMap(sp)
	for sector, hops := c.FirstSector, 0; sector <= c.LastSector && hops < 200000; hops++ {
		buf, err := rd.ReadSectors(sector, 1)
		if err != nil || len(buf) < Sector {
			break
		}
		sc := ScanSectors(buf, sector)
		if len(sc.Navs) == 0 || sc.Navs[0].DSI == nil {
			// Not a VOBU start: an unreadable sector handed on as zeros, or
			// junk. Carry on from the next VOBU instead of losing the rest
			// of the cell; what lies in between is left out.
			next, ok := resyncVOBU(rd, vobus, sector, c.LastSector)
			if !ok {
				break
			}
			sector = next
			continue
		}
		d := sc.Navs[0].DSI
		end := c.LastSector
		if d.VOBUEnd < c.LastSector-sector {
			end = sector + d.VOBUEnd
		}
		if d.VOBID == c.VOBID && d.CellID == c.CellID {
			if n := len(out); n > 0 && out[n-1].Last+1 == sector {
				out[n-1].Last = end
			} else {
				out = append(out, SectorRange{sector, end})
			}
		}
		if end == c.LastSector {
			break
		}
		sector = end + 1
	}
	if len(out) == 0 {
		out = whole // no NAV information: fall back to the plain range
	}
	o.mu.Lock()
	o.ranges[k] = out
	o.mu.Unlock()
	return out, nil
}

// CellExtents maps a cell (following interleaving) onto file byte extents.
func (o *Opened) CellExtents(sp Space, c Cell) ([]FileExtent, error) {
	rs, err := o.CellRanges(sp, c)
	if err != nil {
		return nil, err
	}
	var out []FileExtent
	for _, r := range rs {
		ext, err := o.RangeExtents(sp, r.First, r.Last)
		if err != nil {
			return nil, err
		}
		for _, e := range ext {
			if n := len(out); n > 0 && out[n-1].File == e.File && out[n-1].Offset+out[n-1].Length == e.Offset {
				out[n-1].Length += e.Length
			} else {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// vobuMap is the VOBU address map of a VOB space, when its IFO has one.
func (o *Opened) vobuMap(sp Space) []uint32 {
	switch sp.Domain {
	case DomainVTS:
		if v := o.Disc.VTS[sp.VTS]; v != nil {
			return v.vobus
		}
	case DomainVTSM:
		if v := o.Disc.VTS[sp.VTS]; v != nil {
			return v.menuVOBUs
		}
	default:
		return o.Disc.VMG.menuVOBUs
	}
	return nil
}

// resyncSectors is how far past a sector that isn't a NAV pack the next one
// is looked for when the IFO has no VOBU map: a VOBU is at most about a
// second of video, which is well under this at any DVD bit rate.
const resyncSectors = 2048

// resyncVOBU finds the next VOBU start after a sector that should have been
// one and isn't: from the IFO's VOBU address map when there is one, else by
// reading on for a NAV pack.
func resyncVOBU(rd SectorReader, vobus []uint32, sector, last uint32) (uint32, bool) {
	if len(vobus) > 0 {
		next, ok := nextVOBU(vobus, sector)
		return next, ok && next <= last
	}
	const chunk = 64
	for at := uint64(sector) + 1; at <= uint64(last) && at <= uint64(sector)+resyncSectors; at += chunk {
		n := int(min(chunk, uint64(last)-at+1))
		buf, err := rd.ReadSectors(uint32(at), n)
		if err != nil || len(buf) < Sector {
			return 0, false
		}
		for p := 0; p+Sector <= len(buf); p += Sector {
			if sc := ScanSectors(buf[p:p+Sector], uint32(at)+uint32(p/Sector)); len(sc.Navs) > 0 && sc.Navs[0].DSI != nil {
				return uint32(at) + uint32(p/Sector), true
			}
		}
	}
	return 0, false
}
