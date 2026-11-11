// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Damage: tables wiped or cut short in one copy of an index or in both,
// tables that disagree, sectors that can't be read, and the same disc read
// from a folder and from every kind of image.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

const vtsIFO, vtsBUP = "VIDEO_TS/VTS_01_0.IFO", "VIDEO_TS/VTS_01_0.BUP"

func tableOf(rep IFOReport, name string) TableReport {
	for _, t := range rep.Tables {
		if t.Name == name {
			return t
		}
	}
	return TableReport{}
}

func vtsReport(o *Opened, n int) IFOReport {
	for _, r := range o.Disc.IFOReports {
		if r.VTS == n {
			return r
		}
	}
	return IFOReport{}
}

// A scratch through the program chains of the IFO and another through the
// chapter table of the BUP: neither copy is whole, and together they are.
func TestTableByTableFallback(t *testing.T) {
	files := decoy99().files()
	wipeTable(files[vtsIFO].Data, 0xcc) // VTS_PGCIT
	wipeTable(files[vtsBUP].Data, 0xc8) // VTS_PTT_SRPT
	wipeTable(files[vtsBUP].Data, 0xe0) // VTS_C_ADT
	o := openFS(t, files)
	rep := vtsReport(o, 1)
	if !rep.Mismatch || len(rep.Tables) == 0 {
		t.Fatalf("report %+v", rep)
	}
	other := map[string]string{"IFO": "BUP", "BUP": "IFO"}[rep.Used]
	for name, want := range map[string]string{tblPGCIT: "BUP", tblPTT: "IFO", tblCADT: "IFO"} {
		if got := tableOf(rep, name); got.State != "ok" || got.From != want {
			t.Errorf("%s: %+v, want it whole from the %s (base copy %s, other %s)", name, got, want, rep.Used, other)
		}
	}
	r := o.Disc.Analyze(RankOptions{})
	if r.Main != 37 || len(r.Decoys) < 98 {
		t.Fatalf("main %d, %d decoys\n%s", r.Main, len(r.Decoys), r.Explain())
	}
}

// The same table gone from both copies: what the other tables say is used.
func TestChapterTableRebuilt(t *testing.T) {
	d := simpleMovie()
	d.vts[0].pgcs[0].programs = []int{1, 11, 21}
	files := d.files()
	for _, name := range []string{vtsIFO, vtsBUP} {
		wipeTable(files[name].Data, 0xc8)
	}
	o := openFS(t, files)
	var fixed []Repair
	for _, r := range o.Disc.Repairs {
		if r.Fixed && r.Table == tblPTT {
			fixed = append(fixed, r)
		}
	}
	if len(fixed) != 3 || fixed[0].From != tblPGCIT {
		t.Fatalf("repairs %+v", o.Disc.Repairs)
	}
	m := o.Disc.MainTitle()
	if m == nil || m.Number != 1 || len(m.Chapters) != 3 || m.Chapters[1] != 40*min_ || m.ChapterSource != "chapterTable" {
		t.Fatalf("main %+v", m)
	}
	// The navigator plays from the mended table too.
	if st := NewNav(o.Disc, NavOptions{}).PlayTitle(1, 2); st.Kind != StepTitle || st.CellN != 11 {
		t.Fatalf("chapter 2 of the mended title: %+v", st)
	}
}

// pgcAt is the offset of a program chain inside a title set's IFO.
func pgcAt(ifo []byte, pgcn int) int {
	base := int(binary.BigEndian.Uint32(ifo[0xcc:])) * Sector
	return base + int(binary.BigEndian.Uint32(ifo[base+8+8*(pgcn-1)+4:]))
}

// eachCell hands every cell playback entry of a program chain to f.
func eachCell(ifo []byte, pgcn int, f func(i int, entry []byte)) {
	p := pgcAt(ifo, pgcn)
	pb := p + int(binary.BigEndian.Uint16(ifo[p+0xe8:]))
	for i := 0; i < int(ifo[p+3]); i++ {
		f(i, ifo[pb+24*i:pb+24*i+24])
	}
}

// The film's cells point at sectors that don't exist, in both copies; the
// cell address table still knows where each cell is.
func TestCellsMendedFromAddressTable(t *testing.T) {
	files := simpleMovie().files()
	for _, name := range []string{vtsIFO, vtsBUP} {
		eachCell(files[name].Data, 1, func(i int, e []byte) {
			if i%3 == 1 {
				binary.BigEndian.PutUint32(e[8:], 0xfff00000)
				binary.BigEndian.PutUint32(e[20:], 0x00000010)
			}
		})
	}
	o := openFS(t, files)
	r := o.Disc.Analyze(RankOptions{})
	c := r.Candidate(1)
	if r.Main != 1 || c.Flags.Has(FlagOutOfRange) || c.Distinct != 120*min_ {
		t.Fatalf("main %d, %s, %v distinct\n%s", r.Main, flagsOf(c), c.Distinct, r.Explain())
	}
	mended := false
	for _, rep := range o.Disc.Repairs {
		mended = mended || (rep.Fixed && rep.Table == tblPGCIT && rep.From == tblCADT)
	}
	if !mended || !strings.Contains(strings.Join(r.Warnings, " "), "mended") {
		t.Fatalf("repairs %+v, warnings %q", o.Disc.Repairs, r.Warnings)
	}
	// A decoy's junk cells stay junk: ids that aren't in the table, or that
	// would make the chain play backwards.
	if r2 := openFS(t, decoy99().files()).Disc.Analyze(RankOptions{}); r2.Main != 37 || !r2.Candidate(7).Flags.Has(FlagOutOfRange) {
		t.Fatalf("a decoy was mended into video\n%s", r2.Explain())
	}
}

// Cells that give no play time: the time map says how long they are, and
// without it the chain's play time is shared out by size.
func TestCellTimesEstimated(t *testing.T) {
	for _, noMap := range []bool{false, true} {
		d := simpleMovie()
		d.vts[0].noTMAP = noMap
		d.vts[0].pgcs[0].programs = []int{1, 11, 21}
		files := d.files()
		for _, name := range []string{vtsIFO, vtsBUP} {
			eachCell(files[name].Data, 1, func(i int, e []byte) { clear(e[4:8]) })
		}
		o := openFS(t, files)
		m := o.Disc.MainTitle()
		if m == nil || m.Number != 1 || !m.Estimated {
			t.Fatalf("no map %v: main %+v", noMap, m)
		}
		if math.Abs(m.Duration-120*min_) > 5 || len(m.Chapters) != 3 || math.Abs(m.Chapters[1]-40*min_) > 5 || math.Abs(m.Chapters[2]-80*min_) > 5 {
			t.Fatalf("no map %v: %v long, chapters %v", noMap, m.Duration, m.Chapters)
		}
		r := o.Disc.Analyze(RankOptions{})
		var ch *Decision
		for i := range r.Decisions {
			if r.Decisions[i].Kind == "chapters" {
				ch = &r.Decisions[i]
			}
		}
		if ch == nil || ch.Level == "high" || len(ch.Against) == 0 {
			t.Fatalf("the chapters decision doesn't say the times are estimates: %+v", ch)
		}
	}
}

// A header that gives no size for the title VOBs: the address table does.
func TestVOBSizeFromAddressTable(t *testing.T) {
	files := decoy99().files()
	for _, name := range []string{vtsIFO, vtsBUP} {
		clear(files[name].Data[0x0c:0x10])
	}
	o := openFS(t, files)
	if got := o.Disc.VTS[1].TitleVOBSectors; got != 28000 {
		t.Fatalf("title VOB size %d, want the 28000 sectors the cell address table reaches", got)
	}
	if r := o.Disc.Analyze(RankOptions{}); r.Main != 37 || !r.Candidate(7).Flags.Has(FlagOutOfRange) {
		t.Fatalf("main %d\n%s", r.Main, r.Explain())
	}
}

// An index cut off at any length, with or without a whole backup, opens or
// is refused; it never crashes, and with the backup the film is found.
func TestTruncatedIndexFiles(t *testing.T) {
	good := tvDisc().files()
	for _, name := range []string{"VIDEO_TS/VIDEO_TS.IFO", vtsIFO} {
		whole := good[name].Data
		for cut := 0; cut <= len(whole); cut += 97 {
			files := fstest.MapFS{}
			for k, v := range good {
				files[k] = v
			}
			files[name] = &fstest.MapFile{Data: whole[:cut]}
			o, err := openSource(fsSource{files}, "cut", "folder")
			if err != nil {
				t.Fatalf("%s cut at %d with a whole backup: %v", name, cut, err)
			}
			if r := o.Disc.Analyze(RankOptions{}); r.Main != 1 || len(r.Episodes) != 4 {
				t.Fatalf("%s cut at %d: main %d, %d episodes\n%s", name, cut, r.Main, len(r.Episodes), r.Explain())
			}
			// And with no backup at all: whatever is left must not crash.
			delete(files, strings.TrimSuffix(name, ".IFO")+".BUP")
			if o, err := openSource(fsSource{files}, "cut", "folder"); err == nil {
				_ = o.Disc.Analyze(RankOptions{}).Explain()
				_ = o.Disc.Titles()
				n := NewNav(o.Disc, NavOptions{})
				n.Start()
				n.PlayTitle(1, 1)
			}
		}
	}
}

// sample reads what the engine would read of a disc: every table, the
// ranking and the first sector of each of the film's cells.
func sample(t *testing.T, path string) string {
	t.Helper()
	o, err := Open(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer o.Close()
	var b strings.Builder
	r := o.Analyze()
	j, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	b.Write(j)
	for _, tc := range r.Candidate(r.Main).Summary().Cells {
		sp := Space{Domain: DomainVTS, VTS: r.Candidate(r.Main).VTS}
		rd, err := o.Reader(sp)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		buf, err := rd.ReadSectors(tc.Cell.FirstSector, 1)
		if err != nil || len(buf) != Sector {
			t.Fatalf("%s: cell at %d: %d bytes, %v", path, tc.Cell.FirstSector, len(buf), err)
		}
		nav := ScanSectors(buf, tc.Cell.FirstSector).Navs
		if len(nav) != 1 || nav[0].DSI == nil || nav[0].DSI.VOBID != tc.Cell.VOBID || nav[0].DSI.CellID != tc.Cell.CellID {
			t.Fatalf("%s: no NAV pack of cell %d/%d at sector %d", path, tc.Cell.VOBID, tc.Cell.CellID, tc.Cell.FirstSector)
		}
		rs, err := o.CellRanges(sp, tc.Cell)
		if err != nil {
			t.Fatal(err)
		}
		ext, err := o.CellExtents(sp, tc.Cell)
		if err != nil {
			t.Fatal(err)
		}
		var bytesIn int64
		for _, e := range ext {
			bytesIn += e.Length
		}
		fmt.Fprintf(&b, "\n%d %v %d %x", tc.Cell.FirstSector, rs, bytesIn, buf[:64])
	}
	fmt.Fprintf(&b, "\n%+v", o.ReadStats())
	return b.String()
}

// A disc reads the same from a folder, from a folder in lower case, with its
// VOBs split small or not at all, and from every kind of image.
func TestSameFromFolderAndImages(t *testing.T) {
	for _, disc := range []func() fxDisc{guardedFeature, multiAngle, tvDisc} {
		d := disc()
		folder := d.writeFolder(t)
		want := sample(t, folder)
		if !strings.Contains(want, `"main":`) {
			t.Fatal("nothing sampled")
		}
		check := func(what, path string) {
			t.Helper()
			if got := sample(t, path); got != want {
				t.Errorf("%s reads differently from the folder:\n got %.300s\nwant %.300s", what, got, want)
			}
		}
		check("the VIDEO_TS folder itself", filepath.Join(folder, "VIDEO_TS"))
		small := d
		small.split = 4000 // VTS_01_1.VOB to VTS_01_8.VOB
		small.vts = append([]fxVTS(nil), d.vts...)
		check("split VOBs", small.writeFolder(t))
		check("a bridge image", d.writeImage(t, imageOptions{udf: true, iso: true}))
		check("a UDF image", d.writeImage(t, imageOptions{udf: true}))
		check("an ISO 9660 image", d.writeImage(t, imageOptions{iso: true}))
		check("an image with files in many extents", d.writeImage(t, imageOptions{udf: true, iso: true, maxExtent: 1000 * Sector}))
		check("an ISO 9660 image with files in many extents", d.writeImage(t, imageOptions{iso: true, maxExtent: 1500 * Sector}))

		// Lower-case names, as copies made on other systems have. On a file
		// system that ignores case this is the same folder again.
		lower := t.TempDir()
		src := filepath.Join(folder, "VIDEO_TS")
		dst := filepath.Join(lower, "video_ts")
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(src)
		for _, e := range entries {
			if err := os.Link(filepath.Join(src, e.Name()), filepath.Join(dst, strings.ToLower(e.Name()))); err != nil {
				t.Skipf("no hard links here: %v", err)
			}
		}
		check("a lower-case folder", lower)
	}
}

// An image over 4 GiB: the files lie past what 32 bits of byte offset reach,
// and the film's VOBs are a gigabyte each. The image is sparse, so this
// writes a few kilobytes.
func TestImageOverFourGigabytes(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs a sparse file of 7 GiB")
	}
	f := cellsAt(0, 30, 4*min_, 80000) // 2.4 million sectors: 4.6 GiB of title VOBs
	d := fxDisc{titles: titlesFor(1, 2), vts: []fxVTS{{titleSectors: 2450000, audio: 2, pgcs: []fxPGC{
		{title: 1, cells: f},
		{title: 2, cells: cellsAt(2400000, 2, 2*min_, 20000)},
	}}}}
	folder := d.writeFolder(t)
	want := sample(t, folder)
	for what, opt := range map[string]imageOptions{
		"a bridge image past 4 GiB":             {udf: true, iso: true, pad: 1100000},
		"an ISO 9660 image past 4 GiB":          {iso: true, pad: 1100000},
		"a UDF image of one VOB in two extents": {udf: true, maxExtent: 0x3ffff800},
	} {
		img := d.writeImage(t, opt)
		if st, err := os.Stat(img); err != nil || st.Size() <= 4<<30 {
			t.Fatalf("%s: image of %d bytes, %v", what, st.Size(), err)
		}
		if got := sample(t, img); got != want {
			t.Errorf("%s reads differently from the folder:\n got %.300s\nwant %.300s", what, got, want)
		}
	}
	// One VOB of 4.6 GiB, as some copying tools write: ISO 9660 stores it in
	// two extents.
	one := d
	one.split = 2450000
	img := one.writeImage(t, imageOptions{iso: true})
	o, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	ext, err := o.Extents(Space{Domain: DomainVTS, VTS: 1})
	if err != nil || len(ext) != 2 || ext[0].Length+ext[1].Length != 2450000*Sector {
		t.Fatalf("extents of the one big VOB: %+v, %v", ext, err)
	}
	if got := sample(t, img); !strings.HasPrefix(got, want[:strings.Index(want, "\n")]) {
		t.Errorf("one big VOB ranks differently from the folder")
	}
}

// Unreadable sectors don't stop a read: they come back as zeros and are
// counted, and an interleaved cell carries on from its next VOBU.
func TestBadSectorsArePadded(t *testing.T) {
	for _, noMap := range []bool{false, true} {
		d := multiAngle()
		d.vts[0].noADMAP = noMap
		folder := d.writeFolder(t)
		// Angle 2's cell: ten interleaved units of 100 sectors from 8100.
		// The NAV packs of its third and fourth units can't be read, and
		// neither can a stretch inside its sixth.
		bad := map[int64]bool{8700: true, 9000: true}
		for s := int64(9650); s < 9690; s++ {
			bad[s] = true
		}
		withBadSectors(t, func(name string, s int64) bool { return name == "VTS_01_1.VOB" && bad[s] })
		o, err := Open(folder)
		if err != nil {
			t.Fatal(err)
		}
		defer o.Close()
		sp := Space{Domain: DomainVTS, VTS: 1}
		cell := o.Disc.VTS[1].PGCs[0].PGC.Cells[9]
		if cell.VOBID != 2 || cell.CellID != 2 || !cell.Interleaved {
			t.Fatalf("not the cell this test is about: %+v", cell)
		}
		rs, err := o.CellRanges(sp, cell)
		if err != nil {
			t.Fatal(err)
		}
		var want []SectorRange
		for u := uint32(0); u < 10; u++ {
			if u == 2 || u == 3 {
				continue // their NAV packs are gone: the units are left out
			}
			want = append(want, SectorRange{8100 + 300*u, 8199 + 300*u})
		}
		if fmt.Sprint(rs) != fmt.Sprint(want) {
			t.Fatalf("no map %v: ranges %v, want %v", noMap, rs, want)
		}
		st := o.ReadStats()
		if st.BadSectors < 2 || st.Retries == 0 {
			t.Fatalf("no map %v: stats %+v", noMap, st)
		}
		// A read across the dead stretch gives every sector, zeros where
		// the disc gave nothing.
		rd, _ := o.Reader(sp)
		buf, err := rd.ReadSectors(9600, 200)
		if err != nil || len(buf) != 200*Sector {
			t.Fatalf("read across bad sectors: %d bytes, %v", len(buf), err)
		}
		if !bytes.Equal(buf[50*Sector:90*Sector], make([]byte, 40*Sector)) {
			t.Fatal("unreadable sectors aren't zeros")
		}
		if nav := ScanSectors(buf[:Sector], 9600).Navs; len(nav) != 1 || nav[0].DSI.CellID != 2 {
			t.Fatal("the readable sectors around them were lost")
		}
		if o.ReadStats().BadSectors < st.BadSectors+40 {
			t.Fatalf("bad sectors not counted: %+v", o.ReadStats())
		}
		// The ranking doesn't need the title VOBs at all.
		if r := o.Analyze(); r.Main != 1 || len(r.Versions) != 3 {
			t.Fatalf("main %d\n%s", r.Main, r.Explain())
		}
	}
}

// A zone of thousands of unreadable sectors is given up on quickly: after a
// run of failures the rest of a read is padded without asking the drive.
func TestDeadZoneIsBounded(t *testing.T) {
	d := simpleMovie()
	folder := d.writeFolder(t)
	var files []*faultyFile
	old := openBlockFile
	openBlockFile = func(name string) (blockFile, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		ff := &faultyFile{File: f, bad: func(s int64) bool { return s >= 20000 && s < 30000 }}
		files = append(files, ff)
		return ff, nil
	}
	t.Cleanup(func() { openBlockFile = old })
	o, err := Open(folder)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	rd, _ := o.Reader(Space{Domain: DomainVTS, VTS: 1})
	buf, err := rd.ReadSectors(19990, 4000)
	if err != nil || len(buf) != 4000*Sector {
		t.Fatalf("%d bytes, %v", len(buf), err)
	}
	if n := files[0].reads; n > 10+readRetries+badRunLimit+1 {
		t.Fatalf("%d reads for one request across a dead zone", n)
	}
	if got := o.ReadStats().BadSectors; got != 3990 {
		t.Fatalf("%d bad sectors counted, want 3990", got)
	}
}

// Menus that can't be read are no reason to call a title unreachable.
func TestUnreadableMenuSaysNothing(t *testing.T) {
	d := guardedFeature()
	folder := d.writeFolder(t)
	withBadSectors(t, func(name string, s int64) bool { return name == "VIDEO_TS.VOB" })
	o, err := Open(folder)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	r := o.Analyze()
	for _, c := range r.Candidates {
		if c.Flags.Has(FlagUnreachable) || c.Flags.Has(FlagMenuPlay) {
			t.Fatalf("title %d: %s, from a menu that can't be read\n%s", c.TitleNum, flagsOf(&c), r.Explain())
		}
	}
	if r.Main == 0 || r.ButtonsRead {
		t.Fatalf("main %d, buttons read %v", r.Main, r.ButtonsRead)
	}
}
