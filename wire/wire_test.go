// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wire

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/wittywilla/wittydvd"
)

func testPGC() *wittydvd.PGC {
	p := &wittydvd.PGC{Duration: 12.5, Programs: []int{1, 2}, Cells: []wittydvd.Cell{{FirstSector: 5, LastSector: 9, Duration: 12.5}}, Pre: []wittydvd.Command{{1, 2, 3}}}
	p.SubpControl[3] = &wittydvd.SubpControl{Narrow: 3, Wide: 4}
	p.SubpControl[4] = &wittydvd.SubpControl{} // stream 0 in every mode: not the same as absent
	p.AudioControl[0] = -1
	return p
}

// A missing sub-picture stream is a nil in an array, which gob refuses on its own. It
// must arrive as nil, a stream 0 must not, and the disc must be left as it was.
func TestDiscRoundTrip(t *testing.T) {
	d := &wittydvd.Disc{
		VMG: &wittydvd.VMG{FirstPlay: testPGC(), Titles: []wittydvd.TitleSearch{{Number: 1, VTS: 1, VTSTitle: 1, VTSSector: 7}},
			Menus: []wittydvd.LangUnit{{Language: "en", PGCs: []wittydvd.PGCEntry{{PGC: testPGC()}}}}},
		VTS: map[int]*wittydvd.VTS{1: {Number: 1, PGCs: []wittydvd.PGCEntry{{Entry: true, PGC: testPGC()}}, Menus: []wittydvd.LangUnit{{PGCs: []wittydvd.PGCEntry{{PGC: testPGC()}}}},
			PTT: [][]wittydvd.PTT{{{PGCN: 1, PGN: 1}}}}},
	}
	b, err := EncodeDisc(d)
	if err != nil {
		t.Fatal(err)
	}
	if d.VMG.FirstPlay.SubpControl[0] != nil {
		t.Fatal("the helper's own disc was left marked")
	}
	got, err := DecodeDisc(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*wittydvd.PGC{got.VMG.FirstPlay, got.VMG.Menus[0].PGCs[0].PGC, got.VTS[1].PGCs[0].PGC, got.VTS[1].Menus[0].PGCs[0].PGC} {
		if p.SubpControl[0] != nil || p.SubpControl[31] != nil {
			t.Fatalf("an absent stream arrived: %+v", p.SubpControl[0])
		}
		if p.SubpControl[3] == nil || p.SubpControl[3].Wide != 4 || p.SubpControl[4] == nil {
			t.Fatalf("a stream was lost: %+v", p.SubpControl)
		}
		if p.AudioControl[0] != -1 || len(p.Cells) != 1 || p.Pre[0][2] != 3 {
			t.Fatalf("%+v", p)
		}
	}
	if got.VMG.Titles[0].VTSSector != 7 || got.VTS[1].PTT[0][0].PGCN != 1 {
		t.Fatalf("fields without a JSON form must survive: %+v", got.VMG.Titles[0])
	}
}

func TestStepRoundTrip(t *testing.T) {
	st := &wittydvd.Step{Kind: wittydvd.StepMenu, Domain: wittydvd.DomainVTSM, StillSec: math.Inf(1), Serial: 9, Title: &wittydvd.TitleInfo{Cells: []wittydvd.Cell{{LastSector: 4}}}}
	regs := wittydvd.NewRegisters(wittydvd.RegisterOptions{})
	regs.GPRM[3] = 77
	b, err := Encode(&NavAnswer{Step: st, Regs: *regs})
	if err != nil {
		t.Fatal(err)
	}
	a, err := DecodeNav(b)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(a.Step.StillSec, 1) || a.Step.Domain != wittydvd.DomainVTSM || a.Step.Title.Cells[0].LastSector != 4 || a.Regs.GPRM[3] != 77 {
		t.Fatalf("%+v", a)
	}
	// A navigator with nothing to say says so.
	b, _ = Encode(&NavAnswer{Regs: *regs})
	if a, err = DecodeNav(b); err != nil || a.Step != nil {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestMenuRoundTrip(t *testing.T) {
	pix := func(w, h int, v byte) []byte {
		b := make([]byte, w*h*4)
		for i := range b {
			b[i] = v
		}
		return b
	}
	p := &wittydvd.MenuPicture{FrameW: 720, FrameH: 480, Aspect: 4.0 / 3, DefaultButton: 2,
		Normal: &wittydvd.RGBA{X: 1, Y: 2, Width: 3, Height: 2, Pix: pix(3, 2, 7)},
		Buttons: []wittydvd.MenuButton{
			{N: 1, Cmd: wittydvd.Command{9}, CmdText: "x", Select: &wittydvd.RGBA{Width: 1, Height: 1, Pix: pix(1, 1, 8)}, Action: &wittydvd.RGBA{X: 5}},
			{N: 2},
		}}
	b, err := EncodeMenu(p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Normal.Pix == nil || p.Buttons[0].Select.Pix == nil {
		t.Fatal("the helper's cached picture lost its pixels")
	}
	got, err := DecodeMenu(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Normal.X != 1 || got.Normal.Pix[23] != 7 || len(got.Normal.Pix) != 24 || got.Buttons[0].Select.Pix[3] != 8 || got.Buttons[0].Cmd[0] != 9 {
		t.Fatalf("%+v", got)
	}
	if got.Buttons[0].Action == nil || got.Buttons[0].Action.X != 5 || got.Buttons[1].Select != nil || got.DefaultButton != 2 {
		t.Fatalf("%+v", got.Buttons)
	}
}

// What a fork might send: every one is an error, not a panic or a large allocation.
func TestMenuLies(t *testing.T) {
	p := &wittydvd.MenuPicture{Normal: &wittydvd.RGBA{Width: 2, Height: 2, Pix: make([]byte, 16)}}
	good, err := EncodeMenu(p)
	if err != nil {
		t.Fatal(err)
	}
	head, err := Encode(&wittydvd.MenuPicture{Normal: &wittydvd.RGBA{Width: 1 << 30, Height: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	huge := append(binary.BigEndian.AppendUint32(nil, uint32(len(head))), head...)
	long := append(append([]byte{}, good...), 1)
	for name, b := range map[string][]byte{
		"empty":          {},
		"short":          good[:2],
		"cut":            good[:len(good)-1],
		"left over":      long,
		"huge picture":   huge,
		"huge header":    {0xff, 0xff, 0xff, 0xff, 1, 2, 3},
		"not gob":        {0, 0, 0, 3, 1, 2, 3},
		"past the bytes": {0, 0, 1, 0, 1, 2, 3},
	} {
		if _, err := DecodeMenu(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A size that doesn't match the pixels isn't sent at all.
	if _, err := EncodeMenu(&wittydvd.MenuPicture{Normal: &wittydvd.RGBA{Width: 2, Height: 2, Pix: make([]byte, 3)}}); err == nil {
		t.Error("a broken picture was sent")
	}
}

func TestSubsRoundTripAndLies(t *testing.T) {
	c := &wittydvd.CellSubs{BasePTM: 90000, Units: []wittydvd.SPUUnit{{Stream: 2, Sector: 5, PTS: -1, Bytes: []byte{1, 2}}, {Stream: 31, Sector: 6, PTS: 1 << 40, Bytes: []byte{3}}}}
	b := EncodeSubs(c)
	got, err := DecodeSubs(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.BasePTM != 90000 || len(got.Units) != 2 || got.Units[0].PTS != -1 || got.Units[1].PTS != 1<<40 || got.Units[1].Bytes[0] != 3 || got.Units[0].Stream != 2 {
		t.Fatalf("%+v", got)
	}
	if e, err := DecodeSubs(EncodeSubs(&wittydvd.CellSubs{})); err != nil || len(e.Units) != 0 {
		t.Fatalf("%+v %v", e, err)
	}
	many := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 0), 0xffffffff)
	bigUnit := append([]byte{}, b...)
	binary.BigEndian.PutUint32(bigUnit[subsHead+16:], 0xffffffff)
	for name, x := range map[string][]byte{"empty": {}, "short": b[:4], "cut": b[:len(b)-1], "left over": append(append([]byte{}, b...), 1), "too many units": many, "huge unit": bigUnit} {
		if _, err := DecodeSubs(x); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecodeLimits(t *testing.T) {
	if _, err := DecodeDisc(make([]byte, maxDiscAnswer+1)); err == nil {
		t.Error("an oversized disc was read")
	}
	if _, err := DecodeNav(make([]byte, maxStepAnswer+1)); err == nil {
		t.Error("an oversized step was read")
	}
	if _, err := DecodePGC(make([]byte, maxPGCAnswer+1)); err == nil {
		t.Error("an oversized program chain was read")
	}
	if _, err := DecodeExtents(make([]byte, maxExtentsAnswer+1)); err == nil {
		t.Error("an oversized list was read")
	}
	if _, err := DecodeNav([]byte("this is not gob at all")); err == nil {
		t.Error("garbage was read")
	}
}

func TestHelperNeedsADisc(t *testing.T) {
	h := new(Helper)
	for _, m := range []string{MethodNav, MethodMenu, MethodStart, MethodSubs} {
		if _, _, err := h.Handle(m, nil); err == nil {
			t.Errorf("%s answered with no disc open", m)
		}
	}
	if _, _, err := h.Handle(MethodOpen, json.RawMessage(`{}`)); err == nil {
		t.Error("opened nothing")
	}
}

// tinyDisc writes the index files of a disc with one title of one program
// chain, by hand: two films' worth of bytes would tell this test no more.
func tinyDisc(t *testing.T) string {
	t.Helper()
	be16, be32 := binary.BigEndian.PutUint16, binary.BigEndian.PutUint32
	vmg := make([]byte, 2*2048)
	copy(vmg, "DVDVIDEO-VMG")
	be32(vmg[0x0c:], 3)
	be32(vmg[0x1c:], 1)
	be16(vmg[0x3e:], 1)
	be32(vmg[0xc4:], 1) // TT_SRPT
	tt := vmg[2048:]
	be16(tt, 1)
	be32(tt[4:], 19)
	tt[8+1], tt[8+3], tt[8+6], tt[8+7] = 1, 1, 1, 1 // one angle, one chapter, title set 1, title 1

	vts := make([]byte, 3*2048)
	copy(vts, "DVDVIDEO-VTS")
	be32(vts[0x0c:], 3+50000+2)
	be32(vts[0x1c:], 2)
	be32(vts[0xc4:], 3) // title VOBs
	be32(vts[0xc8:], 1) // VTS_PTT_SRPT
	be32(vts[0xcc:], 2) // VTS_PGCIT
	ptt := vts[2048:]
	be16(ptt, 1)
	be32(ptt[4:], 15)
	be32(ptt[8:], 12)
	be16(ptt[12:], 1)
	be16(ptt[14:], 1)
	it := vts[2*2048:]
	be16(it, 1)
	be32(it[8:], 0x81<<24)
	be32(it[12:], 16)
	pgc := it[16:]
	pgc[2], pgc[3] = 1, 1
	copy(pgc[4:], []byte{0x01, 0x30, 0x00, 0x40}) // 1:30:00
	be16(pgc[0xe6:], 0xec)
	be16(pgc[0xe8:], 0xee)
	be16(pgc[0xea:], 0xee+24)
	pgc[0xec] = 1
	cell := pgc[0xee:]
	copy(cell[4:], []byte{0x01, 0x30, 0x00, 0x40})
	be32(cell[8:], 0)
	be32(cell[20:], 49999)
	be16(cell[24:], 1)
	cell[27] = 1

	dir := filepath.Join(t.TempDir(), "VIDEO_TS")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"VIDEO_TS.IFO": vmg, "VIDEO_TS.BUP": vmg, "VTS_01_0.IFO": vts, "VTS_01_0.BUP": vts} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The ranking and the read statistics travel as JSON, and say what the
// engine itself says.
func TestAnalyzeCall(t *testing.T) {
	h := new(Helper)
	defer h.Close()
	params, _ := json.Marshal(OpenParams{Path: tinyDisc(t)})
	if _, _, err := h.Handle(MethodOpen, params); err != nil {
		t.Fatal(err)
	}
	_, blob, err := h.Handle(MethodAnalyze, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeRanking(blob)
	if err != nil {
		t.Fatal(err)
	}
	if r.Main != 1 || r.Why == nil || r.Why.Summary == "" || r.Feature == nil || r.Feature.Duration != 5400 || len(r.Candidates) != 1 || len(r.Candidates[0].Evidence) == 0 {
		t.Fatalf("ranking over the wire: %s", blob)
	}
	_, blob, err = h.Handle(MethodReadStats, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := DecodeReadStats(blob); err != nil || st.BadSectors != 0 {
		t.Fatalf("read stats %+v, %v", st, err)
	}
	if _, err := DecodeRanking(make([]byte, maxRankingAnswer+1)); err == nil {
		t.Fatal("a ranking over the limit was read")
	}
}

// Whatever a fork sends, the server gets an answer or an error.
func FuzzDecode(f *testing.F) {
	st, _ := Encode(&NavAnswer{Step: &wittydvd.Step{Kind: wittydvd.StepMenu, Title: &wittydvd.TitleInfo{}}})
	f.Add(st)
	d, _ := EncodeDisc(&wittydvd.Disc{VMG: &wittydvd.VMG{FirstPlay: testPGC()}})
	f.Add(d)
	m, _ := EncodeMenu(&wittydvd.MenuPicture{Normal: &wittydvd.RGBA{Width: 1, Height: 1, Pix: make([]byte, 4)}, Buttons: []wittydvd.MenuButton{{N: 1}}})
	f.Add(m)
	f.Add(EncodeSubs(&wittydvd.CellSubs{Units: []wittydvd.SPUUnit{{Bytes: []byte{1}}}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeNav(b)
		_, _ = DecodeDisc(b)
		_, _ = DecodePGC(b)
		_, _ = DecodeExtents(b)
		_, _ = DecodeMenu(b)
		_, _ = DecodeSubs(b)
		_, _ = DecodeRanking(b)
		_, _ = DecodeReadStats(b)
	})
}
