// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import "math"

// Analyze ranks the titles of an opened disc. On top of what the IFOs say
// (Disc.Analyze), it reads the real sizes of the VOB files and the first
// menu's buttons from its NAV packs, which are never scrambled, and dry-runs
// the default ("Play") button in the disc's own VM. No video is decrypted:
// on a disc whose menus are scrambled the menu signals are simply missing.
func (o *Opened) Analyze() *Ranking {
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
				opt.FileSectors[n] = uint32(size / Sector)
			}
		}
	}
	opt.MenuPlay, opt.MenuTargets = o.menuTargets()
	return o.Disc.Analyze(opt)
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
	nav := NewNav(d, NavOptions{RegisterOptions: RegisterOptions{Rand: deterministicRand()}})
	st := nav.Start()
	var pic *MenuPicture
	for i := 0; i < 200 && st != nil && st.Kind != StepStop; i++ {
		if st.Kind == StepTitle && st.Title != nil {
			if ts := d.Title(st.Title.TTN); ts != nil && ts.Duration >= minFeatureSec {
				return 0, nil // the disc plays the film before any menu
			}
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
		ttn, _ := followToTitle(d, c, c.PressButton(b.N, &cmd), minFeatureSec/4)
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
