// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// A static reading of the disc's commands: which titles can the disc's own
// program get to at all, starting from first play?
//
// Nothing is run. Every command of every program chain is decoded, its
// compare is ignored and its link or jump becomes an edge, so the answer
// takes in every path any register values could choose. That makes it sound
// in one direction: a title this search can't reach is a title no player
// would ever show. The other direction needs the menus' button commands,
// which live in the menu VOB's NAV packs and not in the IFO; without them
// (ButtonCommands) a title can look unreachable only because a button names
// it, so "unreachable" is only ever said when the buttons were read.

// ButtonCommand is the command of one menu button, with the program chain
// whose cell shows it.
type ButtonCommand struct {
	Domain Domain  `json:"domain"` // DomainVMGM or DomainVTSM
	VTS    int     `json:"vts"`    // 0 for the VMGM
	Unit   int     `json:"unit"`   // language unit, 0-based
	PGCN   int     `json:"pgcn"`
	Cmd    Command `json:"cmd"`
}

type pgcNode struct {
	dom  Domain
	vts  int
	unit int
	pgcn int
}

// reach is what the search found.
type reach struct {
	// depth is, per reachable title, how many program chains lie between
	// first play and it.
	depth map[int]int
	// direct counts the jump commands in first play, the menus and their
	// buttons that name each title outright.
	direct map[int]int
	// firstJump is the first title the first menu on the way jumps to: on
	// most discs, what its first button ("Play") does.
	firstJump int
	nodes     int
	// complete is false when the search ran out of its budget: what it
	// didn't reach may still be reachable.
	complete bool
}

// maxReachWork bounds the walks through chapter tables the search makes.
const maxReachWork = 4_000_000

// maxReachNodes bounds the search: every program chain of a disc is a node,
// and a disc's budget is maxDiscPGCs of them.
const maxReachNodes = 2 * maxDiscPGCs

func (d *Disc) reachable(buttons []ButtonCommand) *reach {
	r := &reach{depth: map[int]int{}, direct: map[int]int{}, complete: true}
	if d == nil || d.VMG == nil {
		return r
	}
	work := maxReachWork
	var sets []int // title sets in order, so that the search is the same every time
	for n := 1; n <= maxTitleSets; n++ {
		if d.VTS[n] != nil {
			sets = append(sets, n)
		}
	}
	byNode := map[pgcNode][]Command{}
	for _, b := range buttons {
		k := pgcNode{b.Domain, b.VTS, b.Unit, b.PGCN}
		byNode[k] = append(byNode[k], b.Cmd)
	}
	// Which titles a title-domain chain belongs to.
	type vp struct{ vts, pgcn int }
	owners := map[vp][]int{}
	for _, t := range d.VMG.Titles {
		v := d.VTS[t.VTS]
		if v == nil {
			continue
		}
		add := func(pgcn int) {
			k := vp{t.VTS, pgcn}
			for _, x := range owners[k] {
				if x == t.Number {
					return
				}
			}
			owners[k] = append(owners[k], t.Number)
		}
		if t.VTSTitle >= 1 && t.VTSTitle <= len(v.PTT) {
			for _, x := range v.PTT[t.VTSTitle-1] {
				add(x.PGCN)
			}
		}
		for i, e := range v.PGCs {
			if e.Entry && e.TitleNumber == t.VTSTitle {
				add(i + 1)
			}
		}
	}
	pgcOf := func(n pgcNode) *PGC {
		var list []PGCEntry
		switch n.dom {
		case DomainFP:
			return d.VMG.FirstPlay
		case DomainVMGM:
			if n.unit < len(d.VMG.Menus) {
				list = d.VMG.Menus[n.unit].PGCs
			}
		case DomainVTSM:
			if v := d.VTS[n.vts]; v != nil && n.unit < len(v.Menus) {
				list = v.Menus[n.unit].PGCs
			}
		case DomainVTS:
			if v := d.VTS[n.vts]; v != nil {
				list = v.PGCs
			}
		}
		if n.pgcn < 1 || n.pgcn > len(list) {
			return nil
		}
		return list[n.pgcn-1].PGC
	}
	depth := map[pgcNode]int{}
	var queue []pgcNode
	push := func(n pgcNode, dep int) {
		if _, ok := depth[n]; ok {
			return
		}
		if len(depth) >= maxReachNodes {
			r.complete = false
			return
		}
		depth[n] = dep
		queue = append(queue, n)
	}
	menus := func(dom Domain, vts, typ, dep int) {
		units := d.VMG.Menus
		if dom == DomainVTSM {
			v := d.VTS[vts]
			if v == nil {
				return
			}
			units = v.Menus
		}
		for u, lu := range units {
			for i, e := range lu.PGCs {
				if e.Entry && (typ == 0 || e.MenuType == typ) {
					push(pgcNode{dom, vts, u, i + 1}, dep)
				}
			}
		}
	}
	title := func(ttn, dep int) {
		if ttn < 1 || ttn > len(d.VMG.Titles) {
			return
		}
		if _, ok := r.depth[ttn]; !ok {
			r.depth[ttn] = dep
		}
		t := d.VMG.Titles[ttn-1]
		v := d.VTS[t.VTS]
		if v == nil {
			return
		}
		if t.VTSTitle >= 1 && t.VTSTitle <= len(v.PTT) {
			for _, x := range v.PTT[t.VTSTitle-1] {
				push(pgcNode{DomainVTS, t.VTS, 0, x.PGCN}, dep)
			}
		}
		for i, e := range v.PGCs {
			if e.Entry && e.TitleNumber == t.VTSTitle {
				push(pgcNode{DomainVTS, t.VTS, 0, i + 1}, dep)
			}
		}
		// The viewer's MENU key, while it plays.
		menus(DomainVTSM, t.VTS, 0, dep+1)
	}
	vtsTitle := func(vts, vtsTTN, dep int) {
		if g := globalTitleOf(d, vts, vtsTTN); g != 0 {
			title(g, dep)
		}
	}
	if d.VMG.FirstPlay != nil {
		push(pgcNode{dom: DomainFP}, 0)
	} else {
		title(1, 0)
	}
	// The viewer's TITLE key works from anywhere.
	menus(DomainVMGM, 0, 0, 1)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		p := pgcOf(n)
		if p == nil {
			continue
		}
		r.nodes++
		dep := depth[n]
		if n.dom == DomainVTS {
			for _, ttn := range owners[vp{n.vts, n.pgcn}] {
				if _, ok := r.depth[ttn]; !ok {
					title(ttn, dep)
				}
			}
		}
		same := func(pgcn int) {
			if pgcn != 0 && n.dom != DomainFP {
				push(pgcNode{n.dom, n.vts, n.unit, pgcn}, dep+1)
			}
		}
		menu := n.dom == DomainFP || n.dom == DomainVMGM || n.dom == DomainVTSM
		sawJump := false
		for _, list := range [][]Command{p.Pre, p.Post, p.CellCmds, byNode[n]} {
			for _, c := range list {
				in := decodeCached(c)
				if in.Invalid != "" {
					continue
				}
				if l := in.link; l != nil {
					switch l.Op {
					case LinkPGCN:
						same(l.N)
					case LinkNextPGC:
						same(p.NextPGCN)
					case LinkPrevPGC:
						same(p.PrevPGCN)
					case LinkGoUpPGC:
						same(p.GoUpPGCN)
					case LinkPTTN:
						// Within the title: its chains are already nodes.
					}
				}
				j := in.jump
				if j == nil {
					continue
				}
				switch j.Op {
				case JumpTT:
					title(j.TTN, dep+1)
					if menu {
						r.direct[j.TTN]++
						if !sawJump && r.firstJump == 0 && n.dom != DomainFP {
							r.firstJump = j.TTN
						}
						sawJump = true
					}
				case JumpVTSTT, JumpVTSPTT:
					// From first play or the VMGM the title set is whichever
					// was last played, which a static reading can't know.
					if n.vts != 0 {
						vtsTitle(n.vts, j.TTN, dep+1)
						if menu {
							if g := globalTitleOf(d, n.vts, j.TTN); g != 0 {
								r.direct[g]++
							}
						}
					} else {
						for _, vts := range sets {
							vtsTitle(vts, j.TTN, dep+1)
						}
					}
				case JumpSS, CallSS:
					switch {
					case j.Domain == DomainFP:
						push(pgcNode{dom: DomainFP}, dep+1)
					case j.Domain == DomainVMGM && j.HasPGCN:
						for u := range d.VMG.Menus {
							push(pgcNode{DomainVMGM, 0, u, j.PGCN}, dep+1)
						}
					case j.Domain == DomainVMGM:
						menus(DomainVMGM, 0, j.Menu, dep+1)
					default:
						vts := j.VTS
						if vts == 0 {
							vts = n.vts
						}
						if vts != 0 {
							menus(DomainVTSM, vts, j.Menu, dep+1)
							if j.TTN != 0 {
								vtsTitle(vts, j.TTN, dep+1)
							}
						} else {
							for _, k := range sets {
								menus(DomainVTSM, k, j.Menu, dep+1)
							}
						}
					}
				}
			}
		}
		// What happens when the chain simply ends, if it can.
		if !fallsThrough(p.Pre) || !fallsThrough(p.Post) {
			continue
		}
		switch n.dom {
		case DomainFP:
			title(1, dep+1)
		case DomainVTS:
			same(p.NextPGCN)
			if p.NextPGCN != 0 {
				break
			}
			// Without a next link the title goes on with the next chain
			// its chapter table names: a walk through that table.
			for _, ttn := range owners[vp{n.vts, n.pgcn}] {
				t := d.VMG.Titles[ttn-1]
				v := d.VTS[n.vts]
				if t.VTSTitle >= 1 && t.VTSTitle <= len(v.PTT) {
					work -= len(v.PTT[t.VTSTitle-1])
				}
				if work < 0 {
					r.complete = false
					break
				}
				same(nextTitlePGC(v, t.VTSTitle, n.pgcn))
			}
		}
	}
	return r
}

// fallsThrough says a command list can run off its end: it has no command
// that always jumps away. A list with a Goto is taken to fall through, since
// the Goto may step over anything.
func fallsThrough(cmds []Command) bool {
	for _, c := range cmds {
		if decodeCached(c).sp == spGoto {
			return true
		}
	}
	for _, c := range cmds {
		in := decodeCached(c)
		if in.Invalid != "" {
			return false // the VM stops here
		}
		if in.sp == spBreak && in.cmp == cmpNone {
			return true
		}
		if in.cmp != cmpNone {
			continue
		}
		if in.jump != nil {
			return false
		}
		if l := in.link; l != nil {
			switch l.Op {
			case LinkPGCN, LinkNextPGC, LinkPrevPGC, LinkGoUpPGC, LinkTopPGC, LinkRSM:
				return false
			}
			return true // a link inside the chain: it plays on to its end
		}
	}
	return true
}
