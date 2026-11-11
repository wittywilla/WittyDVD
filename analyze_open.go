// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import "math"

// Analyze ranks the titles of an opened disc. On top of what the IFOs say
// (Disc.Analyze), it reads the real sizes of the VOB files and the menus'
// buttons from their NAV packs, which are never scrambled, and dry-runs the
// first menu's buttons in the disc's own VM. No video is decrypted: on a
// disc whose menus are scrambled the menu signals are simply missing.
//
// The answer is worked out once and kept: an opened disc doesn't change.
func (o *Opened) Analyze() *Ranking {
	o.rankOnce.Do(func() { o.rank = o.Disc.Analyze(o.RankOptions()) })
	return o.rank
}

// RankOptions gathers what an opened disc can add to the ranking: the sizes
// of the VOB files, the commands of the menus' buttons, and what the first
// menu's buttons play.
func (o *Opened) RankOptions() RankOptions {
	opt := RankOptions{FileSectors: map[int]uint32{}}
	for n, v := range o.Disc.VTS {
		if v == nil {
			continue
		}
		if ext, err := o.Extents(Space{Domain: DomainVTS, VTS: n}); err == nil {
			var size int64
			for _, e := range ext {
				size += e.Length
			}
			if size > 0 {
				opt.FileSectors[n] = uint32(min(size/Sector, math.MaxUint32))
			}
		}
	}
	// A copy of the index files alone has no VOBs at all: then nothing can
	// be said about which title sets have video.
	opt.FilesKnown = len(opt.FileSectors) > 0
	opt.Buttons, opt.ButtonsComplete = o.menuButtons()
	opt.MenuPlay, opt.MenuTargets = o.menuTargets()
	return opt
}

const (
	// maxButtonCells is how many menu cells are read for their buttons, and
	// maxButtonHops how many NAV packs of one cell and of all cells together.
	maxButtonCells     = 512
	maxButtonHops      = 256
	maxButtonHopsTotal = 8192
)

// menuButtons reads the button commands of every menu cell from the menu
// VOBs' NAV packs: one sector per VOBU, never the video. complete is false
// when some menu could not be read to its end (missing or scrambled VOB, a
// sector that isn't a NAV pack, more cells or VOBUs than the limits), since
// a button that wasn't seen may lead anywhere.
func (o *Opened) menuButtons() (out []ButtonCommand, complete bool) {
	defer func() {
		if recover() != nil {
			out, complete = nil, false
		}
	}()
	d := o.Disc
	complete = true
	cells, hops := 0, 0
	type place struct {
		sp          Space
		first, last uint32
	}
	type found struct {
		cmds []Command
		ok   bool
	}
	known := map[place]found{}
	scan := func(dom Domain, vts int, units []LangUnit) {
		sp := SpaceFor(dom, vts)
		var rd SectorReader
		size := d.vobSectors(dom, vts)
		for u, lu := range units {
			for i, e := range lu.PGCs {
				if e.PGC == nil {
					continue
				}
				for _, c := range e.PGC.Cells {
					if badRange(c, size) {
						continue
					}
					k := place{sp, c.FirstSector, c.LastSector}
					f, seen := known[k]
					if !seen {
						if cells >= maxButtonCells || hops >= maxButtonHopsTotal {
							complete = false
							return
						}
						cells++
						if rd == nil {
							var err error
							if rd, err = o.Reader(sp); err != nil {
								complete = false
								return
							}
						}
						var n int
						f.cmds, n, f.ok = cellButtons(rd, c.FirstSector, c.LastSector, maxButtonHops)
						hops += n
						known[k] = f
					}
					if !f.ok {
						complete = false
					}
					for _, cmd := range f.cmds {
						out = append(out, ButtonCommand{Domain: dom, VTS: vts, Unit: u, PGCN: i + 1, Cmd: cmd})
					}
				}
			}
		}
	}
	scan(DomainVMGM, 0, d.VMG.Menus)
	for n := 1; n <= maxTitleSets; n++ {
		if v := d.VTS[n]; v != nil && len(v.Menus) > 0 {
			scan(DomainVTSM, n, v.Menus)
		}
	}
	return out, complete
}

// cellButtons hops through a cell's NAV packs and collects the distinct
// commands of its buttons. ok is false when the cell could not be followed
// to its end.
func cellButtons(rd SectorReader, first, last uint32, maxHops int) (cmds []Command, hops int, ok bool) {
	seen := map[Command]bool{}
	for sector := first; sector <= last; hops++ {
		if hops >= maxHops {
			return cmds, hops, false
		}
		buf, err := rd.ReadSectors(sector, 1)
		if err != nil || len(buf) < Sector || PackScrambled(buf) {
			return cmds, hops, false
		}
		sc := ScanSectors(buf, sector)
		if len(sc.Navs) == 0 || sc.Navs[0].DSI == nil {
			return cmds, hops, false
		}
		nav := sc.Navs[0]
		if nav.PCI != nil && nav.PCI.HLStatus != 0 {
			for _, b := range nav.PCI.Buttons {
				if b.Cmd != (Command{}) && !seen[b.Cmd] {
					seen[b.Cmd] = true
					cmds = append(cmds, b.Cmd)
				}
			}
		}
		end := nav.DSI.VOBUEnd
		if end >= last-sector {
			break
		}
		sector += end + 1
	}
	return cmds, hops, true
}

// menuTargets finds the first menu with buttons that first play reaches and
// returns the title its default button plays and the titles any of its
// buttons play.
func (o *Opened) menuTargets() (play int, targets []int) {
	defer func() {
		if recover() != nil {
			play, targets = 0, nil
		}
	}()
	d := o.Disc
	fuel := int64(analysisFuel)
	length := d.titleLength()
	nav := NewNav(d, NavOptions{RegisterOptions: RegisterOptions{Rand: deterministicRand()}})
	st := nav.Start()
	nav.st.regs.fuel = &fuel
	var pic *MenuPicture
	for i := 0; i < 200 && st != nil && st.Kind != StepStop; i++ {
		if st.Kind == StepTitle && st.Title != nil && length(st.Title.TTN) >= minFeatureSec {
			return 0, nil // the disc plays the film before any menu
		}
		if st.Kind == StepMenu {
			p, err := o.Menu(SpaceFor(st.Domain, st.VTS), st.FirstSector, st.LastSector, MenuScanOptions{Palette: st.Palette, Video: d.VMG.MenuVideo, NavButton: st.DefaultButton, MaxHops: 2000})
			if err == nil && p != nil && p.Scrambled {
				return 0, nil
			}
			if err == nil && p != nil && len(p.Buttons) > 0 {
				pic = p
				break
			}
			if math.IsInf(st.StillSec, 1) {
				break
			}
		}
		st = nav.CellEnded()
	}
	if pic == nil {
		return 0, nil
	}
	seen := map[int]bool{}
	for _, b := range pic.Buttons {
		if b.Inert {
			continue
		}
		c := nav.Clone()
		cmd := b.Cmd
		ttn, _ := followToTitle(length, c, c.PressButton(b.N, &cmd), minFeatureSec/4)
		if ttn == 0 {
			continue
		}
		if b.N == pic.DefaultButton {
			play = ttn
		}
		if !seen[ttn] {
			seen[ttn] = true
			targets = append(targets, ttn)
		}
	}
	return play, targets
}
