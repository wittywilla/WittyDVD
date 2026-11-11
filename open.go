// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wittywilla/wittydisc/isofs"
)

// MaxIFOBytes bounds how much of an IFO we read.
const MaxIFOBytes = 16 << 20

// Space names a VOB address space.
type Space struct {
	Domain Domain
	VTS    int
}

// Source abstracts an ISO image or a VIDEO_TS folder.
type Source interface {
	ReadIFO(name string) ([]byte, error)
	// VOBExtents returns the byte extents (in the backing file(s)) of the VOB
	// address space, in order. Each extent names its file.
	VOBExtents(sp Space) ([]FileExtent, error)
	Close() error
	Describe() string
}

// FileExtent is a byte range in a named file.
type FileExtent struct {
	File   string `json:"file"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

// Opened is a DVD ready for navigation and playback.
type Opened struct {
	Disc   *Disc
	Source Source
	Label  string
	Format string

	mu      sync.Mutex
	spaces  map[Space][]FileExtent
	readers map[Space]*spaceReader
	scans   map[scanKey]*MenuPicture
	subs    map[subsKey]*CellSubs
	ranges  map[rangesKey][]SectorRange
	stats   readCounters

	rankOnce sync.Once
	rank     *Ranking

	// vobReader, when set, stands in for the VOB files (tests).
	vobReader func(Space) (SectorReader, error)
}

// scanKey identifies a menu scan whose result does not depend on viewer
// state (the default button is applied afterwards with WithDefault).
type scanKey struct {
	sp          Space
	first, last uint32
	palette     [16]uint32
	video       VideoAttr
	subp        SubpControl
	hasSubp     bool
	wide        bool
	maxHops     int
}

// Open opens an ISO/IMG image or a VIDEO_TS directory.
func Open(p string) (*Opened, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	var src Source
	label := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	format := "folder"
	if st.IsDir() {
		src, err = openFolder(p)
	} else {
		var img *isofs.Disc
		img, err = isofs.Open(p)
		if err == nil {
			src = &imageSource{img: img}
			if img.Label != "" {
				label = img.Label
			}
			format = img.Format
		}
	}
	if err != nil {
		return nil, err
	}
	return openSource(src, label, format)
}

// openSource reads the IFOs of an opened source. It closes the source when
// the disc can't be read.
func openSource(src Source, label, format string) (*Opened, error) {
	bud := newBudget()
	vmg, rep, err := readIFO(src, "VIDEO_TS", vmgTables, func(b []byte) (*VMG, error) { return parseVMG(b, bud) })
	if err != nil {
		src.Close()
		return nil, err
	}
	d := &Disc{VMG: vmg, VTS: map[int]*VTS{}, IFOReports: []IFOReport{rep}}
	count := vmg.NumTitleSets
	for _, t := range vmg.Titles {
		if t.VTS > count {
			count = t.VTS
		}
	}
	if count > maxTitleSets {
		count = maxTitleSets
	}
	for n := 1; n <= count; n++ {
		v, rep, err := readIFO(src, fmt.Sprintf("VTS_%02d_0", n), vtsTables, func(b []byte) (*VTS, error) { return parseVTS(b, n, bud) })
		if err != nil {
			continue
		}
		rep.VTS = n
		d.IFOReports = append(d.IFOReports, rep)
		d.VTS[n] = v
	}
	d.Repairs = d.CrossCheck()
	return &Opened{Disc: d, Source: src, Label: label, Format: format, spaces: map[Space][]FileExtent{}, readers: map[Space]*spaceReader{}, scans: map[scanKey]*MenuPicture{}}, nil
}

// ifoFile is what readIFO needs of a parsed index file.
type ifoFile interface {
	comparable
	tables() ifoDiag
	problems() int
	adopt(table string, from any)
}

// readIFO reads and parses an index file and its backup copy (.BUP), and
// answers with the best of the two, table by table.
//
// Damaged discs and images made from them often have a broken IFO and an
// intact backup, and a player reads the backup too. When only one copy can be
// read, it is used. When both parse but differ, the copy whose tables hang
// together better (fewer problems) is the base, the IFO on a tie. Then every
// table that is missing or damaged in the base and whole in the other copy is
// taken from the other copy: a scratch through one table of the IFO and
// another table of the BUP still leaves a whole index.
func readIFO[T ifoFile](src Source, base string, names []string, parse func([]byte) (T, error)) (T, IFOReport, error) {
	var zero T
	rep := IFOReport{Name: base, Used: "IFO"}
	// A read that fails part-way still gives the tables before the fault.
	b, err := src.ReadIFO(base + ".IFO")
	var v T
	ifoOK := false
	if err == nil || len(b) > 0 {
		var perr error
		if v, perr = parse(b); perr == nil {
			ifoOK = true
		} else if err == nil {
			err = perr
		}
	}
	bb, berr := src.ReadIFO(base + ".BUP")
	var bv T
	bupOK := false
	// Two copies with the same bytes say the same thing: parse one.
	same := ifoOK && err == nil && berr == nil && bytes.Equal(b, bb)
	if !same && (berr == nil || len(bb) > 0) {
		if x, perr := parse(bb); perr == nil {
			bv, bupOK = x, true
		}
	}
	report := func(v T, used, other string, adopted map[string]bool) {
		d := v.tables()
		for _, name := range names {
			t, ok := d[name]
			if !ok {
				continue
			}
			from := used
			if adopted[name] {
				from = other
			}
			rep.Tables = append(rep.Tables, TableReport{Name: name, State: t.state.String(), From: from, Note: t.note})
		}
	}
	switch {
	case same:
		report(v, "IFO", "BUP", nil)
		return v, rep, nil
	case !ifoOK && !bupOK:
		if err == nil {
			err = berr
		}
		return zero, rep, fmt.Errorf("dvd: %s: %w", base, err)
	case ifoOK && !bupOK:
		if berr == nil {
			rep.Mismatch, rep.Note = true, "the backup doesn't parse"
		}
		report(v, "IFO", "BUP", nil)
		return v, rep, nil
	case !ifoOK:
		rep.Used, rep.Mismatch, rep.Note = "BUP", true, "the IFO is missing or damaged ("+plainIFOErr(err)+")"
		report(bv, "BUP", "IFO", nil)
		return bv, rep, nil
	}
	rep.Mismatch = true
	pi, pb := v.problems(), bv.problems()
	use, other, usedName, otherName := v, bv, "IFO", "BUP"
	if pb < pi {
		use, other, usedName, otherName = bv, v, "BUP", "IFO"
		rep.Used, rep.Note = "BUP", fmt.Sprintf("the IFO and its backup differ, and the backup's tables are consistent where the IFO's aren't (%d problems against %d)", pb, pi)
	} else {
		rep.Note = fmt.Sprintf("the IFO and its backup differ; the IFO is used (%d problems against the backup's %d)", pi, pb)
	}
	adopted := map[string]bool{}
	var took []string
	for _, name := range names {
		if !use.tables().usable(name) && other.tables().usable(name) {
			use.adopt(name, other)
			adopted[name] = true
			took = append(took, name)
		}
	}
	if len(took) > 0 {
		rep.Note += fmt.Sprintf("; %s taken from the %s, where the %s's copy is missing or damaged", strings.Join(took, ", "), otherName, usedName)
	}
	report(use, usedName, otherName, adopted)
	return use, rep, nil
}

func plainIFOErr(err error) string {
	if err == nil {
		return "unknown"
	}
	return strings.TrimPrefix(strings.TrimPrefix(err.Error(), "dvd: "), "malformed IFO: ")
}

func (v *VMG) tables() ifoDiag { return v.diag }

// problems counts what doesn't hang together in a VMG.
func (v *VMG) problems() int {
	n := 0
	if len(v.Titles) == 0 {
		n += 5
	}
	for _, t := range v.diag {
		if t.state == tableDamaged {
			n += 5
		}
	}
	for _, t := range v.Titles {
		if t.VTS < 1 || t.VTS > maxTitleSets || (v.NumTitleSets > 0 && t.VTS > v.NumTitleSets) || t.VTSTitle < 1 {
			n++
		}
	}
	return n
}

func (v *VMG) adopt(table string, from any) {
	o := from.(*VMG)
	switch table {
	case tblTTSRPT:
		v.Titles = o.Titles
	case tblFirstPlay:
		v.FirstPlay = o.FirstPlay
	case tblPTLMAIT:
		v.Parental = o.Parental
	case tblVMGMPGCIUT:
		v.Menus = o.Menus
	case tblVMGMCADT:
		v.MenuCellAddrs = o.MenuCellAddrs
	case tblVMGMADMAP:
		v.menuVOBUs = o.menuVOBUs
	}
	v.diag[table] = o.diag[table]
	if v.NumTitleSets == 0 {
		v.NumTitleSets = o.NumTitleSets
	}
	if v.MenuVOBSectors == 0 {
		v.MenuVOBSec, v.MenuVOBSectors = o.MenuVOBSec, o.MenuVOBSectors
	}
}

func (v *VTS) tables() ifoDiag { return v.diag }

// problems counts what doesn't hang together in a VTS: missing or damaged
// tables, chapters naming PGCs that don't exist, cells outside the title VOB.
func (v *VTS) problems() int {
	n := 0
	if len(v.PGCs) == 0 {
		n += 5
	}
	if len(v.PTT) == 0 {
		n += 5
	}
	for _, t := range v.diag {
		if t.state == tableDamaged {
			n += 5
		}
	}
	for _, list := range v.PTT {
		for _, p := range list {
			if p.PGCN < 1 || p.PGCN > len(v.PGCs) {
				n++
			}
		}
	}
	done := map[*PGC]bool{}
	for _, e := range v.PGCs {
		if e.PGC == nil || e.Damaged {
			n++
			continue
		}
		if done[e.PGC] {
			continue
		}
		done[e.PGC] = true
		for _, c := range e.PGC.Cells {
			if badRange(c, v.TitleVOBSectors) {
				n++
			}
		}
	}
	return n
}

func (v *VTS) adopt(table string, from any) {
	o := from.(*VTS)
	switch table {
	case tblPTT:
		v.PTT = o.PTT
	case tblPGCIT:
		v.PGCs = o.PGCs
	case tblVTSMPGCIUT:
		v.Menus = o.Menus
	case tblTMAPTI:
		v.timeMaps = o.timeMaps
	case tblVTSMCADT:
		v.MenuCellAddrs = o.MenuCellAddrs
	case tblVTSMADMAP:
		v.menuVOBUs = o.menuVOBUs
	case tblCADT:
		v.CellAddrs = o.CellAddrs
	case tblADMAP:
		v.vobus, v.VOBUs = o.vobus, o.VOBUs
	}
	v.diag[table] = o.diag[table]
	// A header whose sizes were wiped along with the table's pointer.
	if v.TitleVOBSectors == 0 {
		v.TitleVOBSec, v.TitleVOBSectors = o.TitleVOBSec, o.TitleVOBSectors
	}
	if v.MenuVOBSectors == 0 {
		v.MenuVOBSec, v.MenuVOBSectors = o.MenuVOBSec, o.MenuVOBSectors
	}
}

// Close releases the reader file handles and the source.
func (o *Opened) Close() error {
	o.mu.Lock()
	for _, r := range o.readers {
		r.Close()
	}
	o.readers = map[Space]*spaceReader{}
	o.mu.Unlock()
	return o.Source.Close()
}

// Extents returns the VOB address space extents (cached).
func (o *Opened) Extents(sp Space) ([]FileExtent, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.extentsLocked(sp)
}

func (o *Opened) extentsLocked(sp Space) ([]FileExtent, error) {
	if e, ok := o.spaces[sp]; ok {
		return e, nil
	}
	e, err := o.Source.VOBExtents(sp)
	if err != nil {
		return nil, err
	}
	o.spaces[sp] = e
	return e, nil
}

// SpaceFor maps a step to its VOB address space.
func SpaceFor(domain Domain, vts int) Space {
	switch domain {
	case DomainFP, DomainVMGM:
		return Space{Domain: DomainVMGM}
	case DomainVTSM:
		return Space{Domain: DomainVTSM, VTS: vts}
	}
	return Space{Domain: DomainVTS, VTS: vts}
}

// RangeExtents maps sectors [first,last] of a space onto file byte extents.
func (o *Opened) RangeExtents(sp Space, first, last uint32) ([]FileExtent, error) {
	all, err := o.Extents(sp)
	if err != nil {
		return nil, err
	}
	if last < first {
		return nil, nil // a cell that ends before it starts holds nothing
	}
	return sliceExtents(all, int64(first)*Sector, (int64(last)-int64(first)+1)*Sector), nil
}

func sliceExtents(all []FileExtent, off, length int64) []FileExtent {
	var out []FileExtent
	for _, e := range all {
		if length <= 0 {
			break
		}
		if off >= e.Length {
			off -= e.Length
			continue
		}
		take := e.Length - off
		if take > length {
			take = length
		}
		out = append(out, FileExtent{File: e.File, Offset: e.Offset + off, Length: take})
		length -= take
		off = 0
	}
	return out
}

// Reader returns a SectorReader over a space. Readers are shared and keep
// their file handles open until the disc is closed.
func (o *Opened) Reader(sp Space) (SectorReader, error) {
	if o.vobReader != nil {
		return o.vobReader(sp)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if r, ok := o.readers[sp]; ok {
		return r, nil
	}
	ext, err := o.extentsLocked(sp)
	if err != nil {
		return nil, err
	}
	if len(ext) == 0 {
		return nil, errors.New("dvd: VOB missing")
	}
	r := &spaceReader{ext: ext, files: map[string]blockFile{}, stats: &o.stats}
	o.readers[sp] = r
	return r, nil
}

// Menu scans a cell for its highlight information, caching the result per
// disc so revisiting a menu (or moving the highlight) never touches the disk.
func (o *Opened) Menu(sp Space, first, last uint32, opt MenuScanOptions) (*MenuPicture, error) {
	k := scanKey{sp: sp, first: first, last: last, palette: opt.Palette, video: opt.Video, wide: opt.Wide, maxHops: opt.MaxHops}
	if opt.SubpControl != nil {
		k.subp, k.hasSubp = *opt.SubpControl, true
	}
	o.mu.Lock()
	pic, ok := o.scans[k]
	o.mu.Unlock()
	if !ok {
		rd, err := o.Reader(sp)
		if err != nil {
			return nil, err
		}
		pic, err = ScanMenu(rd, first, last, opt)
		if err != nil {
			return nil, err
		}
		o.mu.Lock()
		if len(o.scans) > 512 {
			o.scans = map[scanKey]*MenuPicture{}
		}
		o.scans[k] = pic
		o.mu.Unlock()
	}
	return pic.WithDefault(opt.NavButton), nil
}

// ReadStats counts what reading the VOBs of an opened disc has cost so far.
type ReadStats struct {
	// BadSectors is how many sectors could not be read and were handed on
	// as zeros, the way a copying tool pads what a scratched disc won't give.
	BadSectors int64 `json:"badSectors"`
	// Retries is how many reads were tried again after a fault.
	Retries int64 `json:"retries"`
}

// ReadStats says how many sectors have been unreadable so far.
func (o *Opened) ReadStats() ReadStats {
	return ReadStats{BadSectors: o.stats.bad.Load(), Retries: o.stats.retries.Load()}
}

type readCounters struct{ bad, retries atomic.Int64 }

// blockFile is a VOB (or an image holding one) as the reader needs it.
type blockFile interface {
	io.ReaderAt
	io.Closer
}

// openBlockFile opens a file of a VOB address space; tests put faults here.
var openBlockFile = func(name string) (blockFile, error) { return os.Open(name) }

const (
	// readRetries is how often a failed read is tried again before its
	// sectors are given up one by one.
	readRetries = 2
	// badRunLimit is how many unreadable sectors in a row are tried one by
	// one. Past it the rest of the request is padded without trying: a drive
	// can take seconds over each sector of a deliberately unreadable zone.
	badRunLimit = 32
)

type spaceReader struct {
	ext   []FileExtent
	mu    sync.Mutex
	files map[string]blockFile
	stats *readCounters
}

func (r *spaceReader) file(name string) (blockFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.files[name]; ok {
		return f, nil
	}
	f, err := openBlockFile(name)
	if err != nil {
		return nil, err
	}
	r.files[name] = f
	return f, nil
}

// ReadSectors reads count sectors from first. Reading past the end of the
// VOB gives what there is. Sectors that can't be read (a scratch, or the
// unreadable zones some discs carry on purpose) are tried again, then given
// as zeros and counted: one bad sector never fails a read, and a zeroed
// sector is no NAV pack, so whoever scans for one simply moves on.
func (r *spaceReader) ReadSectors(first uint32, count int) ([]byte, error) {
	if count <= 0 {
		return nil, nil
	}
	want := int64(count) * Sector
	parts := sliceExtents(r.ext, int64(first)*Sector, want)
	var have int64
	for _, p := range parts {
		have += p.Length
	}
	out := make([]byte, have)
	got := 0
	for _, p := range parts {
		f, err := r.file(p.File)
		if err != nil {
			if got == 0 {
				return nil, err
			}
			return out[:got], err
		}
		// Read straight into the result: no intermediate copy per extent.
		n := r.readTolerant(f, out[got:got+int(p.Length)], p.Offset)
		got += n
		if int64(n) < p.Length {
			break // the file is shorter than its directory entry says
		}
	}
	return out[:got], nil
}

// readTolerant fills buf from off and returns how much of it holds data or
// padding; less than len(buf) only at the end of the file.
func (r *spaceReader) readTolerant(f blockFile, buf []byte, off int64) int {
	n, err := f.ReadAt(buf, off)
	if err == nil || errors.Is(err, io.EOF) {
		return n
	}
	for try := 0; try < readRetries; try++ {
		r.count(0, 1)
		if n, err = f.ReadAt(buf, off); err == nil || errors.Is(err, io.EOF) {
			return n
		}
	}
	// Sector by sector: keep what reads, pad what doesn't.
	run := 0
	for at := 0; at < len(buf); at += Sector {
		end := min(at+Sector, len(buf))
		if run >= badRunLimit {
			clear(buf[at:])
			r.count(int64((len(buf)-at+Sector-1)/Sector), 0)
			break
		}
		k, err := f.ReadAt(buf[at:end], off+int64(at))
		if errors.Is(err, io.EOF) {
			return at + k
		}
		if err != nil {
			clear(buf[at:end])
			r.count(1, 0)
			run++
			continue
		}
		run = 0
	}
	return len(buf)
}

func (r *spaceReader) count(bad, retries int64) {
	if r.stats != nil {
		r.stats.bad.Add(bad)
		r.stats.retries.Add(retries)
	}
}

// Close releases file handles held by the reader.
func (r *spaceReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.files {
		f.Close()
	}
	r.files = map[string]blockFile{}
}

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

type imageSource struct{ img *isofs.Disc }

func (s *imageSource) Describe() string { return s.img.Path }
func (s *imageSource) Close() error     { return s.img.Close() }

func (s *imageSource) ReadIFO(name string) ([]byte, error) {
	b, err := s.img.ReadFile("VIDEO_TS/"+name, MaxIFOBytes)
	if err != nil && strings.HasSuffix(name, ".IFO") {
		// Fall back to the backup copy.
		b, err = s.img.ReadFile("VIDEO_TS/"+strings.TrimSuffix(name, ".IFO")+".BUP", MaxIFOBytes)
	}
	return b, err
}

func vobNames(sp Space) []string {
	switch sp.Domain {
	case DomainVMGM:
		return []string{"VIDEO_TS.VOB"}
	case DomainVTSM:
		return []string{fmt.Sprintf("VTS_%02d_0.VOB", sp.VTS)}
	}
	var out []string
	for k := 1; k <= 9; k++ {
		out = append(out, fmt.Sprintf("VTS_%02d_%d.VOB", sp.VTS, k))
	}
	return out
}

func (s *imageSource) VOBExtents(sp Space) ([]FileExtent, error) {
	var out []FileExtent
	for _, name := range vobNames(sp) {
		f, ok := s.img.Find("VIDEO_TS/" + name)
		if !ok {
			break
		}
		for _, e := range f.Extents {
			out = append(out, FileExtent{File: s.img.Path, Offset: e.Offset, Length: e.Length})
		}
	}
	return out, nil
}

type folderSource struct {
	dir string

	once  sync.Once
	names map[string]string // upper-cased name -> name on disk
}

// openFolder opens a VIDEO_TS folder: the folder itself, whatever it is
// called, when it holds VIDEO_TS.IFO or its backup, or else the VIDEO_TS
// folder inside it. Names are matched without regard to case, as copies made
// on other systems come out in lower case.
func openFolder(p string) (Source, error) {
	holds := func(dir string) bool {
		s := &folderSource{dir: dir}
		_, ifo := s.find("VIDEO_TS.IFO")
		_, bup := s.find("VIDEO_TS.BUP")
		return ifo || bup
	}
	if holds(p) {
		return &folderSource{dir: p}, nil
	}
	entries, _ := os.ReadDir(p)
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), "VIDEO_TS") {
			return &folderSource{dir: filepath.Join(p, e.Name())}, nil
		}
	}
	if strings.EqualFold(filepath.Base(p), "VIDEO_TS") {
		return &folderSource{dir: p}, nil
	}
	return nil, fmt.Errorf("dvd: no VIDEO_TS folder in %s", p)
}

func (s *folderSource) Describe() string { return s.dir }
func (s *folderSource) Close() error     { return nil }

// find looks a file up without regard to case. The folder is listed once: a
// disc's files don't change while it is open, and a disc of 99 title sets
// asks for a thousand names.
func (s *folderSource) find(name string) (string, bool) {
	s.once.Do(func() {
		s.names = map[string]string{}
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if k := strings.ToUpper(e.Name()); s.names[k] == "" {
				s.names[k] = e.Name()
			}
		}
	})
	if n, ok := s.names[strings.ToUpper(name)]; ok {
		return filepath.Join(s.dir, n), true
	}
	return "", false
}

func (s *folderSource) ReadIFO(name string) ([]byte, error) {
	p, ok := s.find(name)
	if !ok {
		if p, ok = s.find(strings.TrimSuffix(name, ".IFO") + ".BUP"); !ok {
			return nil, fmt.Errorf("%s not found", name)
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxIFOBytes))
}

func (s *folderSource) VOBExtents(sp Space) ([]FileExtent, error) {
	var out []FileExtent
	for _, name := range vobNames(sp) {
		p, ok := s.find(name)
		if !ok {
			break
		}
		st, err := os.Stat(p)
		if err != nil {
			break
		}
		out = append(out, FileExtent{File: p, Offset: 0, Length: st.Size()})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Title summaries (for the library and the "Play main feature" shortcut)
// ---------------------------------------------------------------------------

// TitleSummary is a flattened view of one title.
type TitleSummary struct {
	Number   int         `json:"number"`
	VTS      int         `json:"vts"`
	VTSTitle int         `json:"vtsTitle"`
	Duration float64     `json:"durationSec"`
	Chapters []float64   `json:"chapters"` // chapter start offsets in seconds
	Angles   int         `json:"angles"`
	Audio    []AudioAttr `json:"audio"`
	Subp     []SubpAttr  `json:"subtitles"`
	Video    VideoAttr   `json:"video"`
	Cells    []TitleCell `json:"-"`
	// ChapterSource says where the chapter times come from: "chapterTable"
	// (VTS_PTT_SRPT), "programs" (the table names nothing this title plays,
	// so each program is a chapter) or "" (no chapters).
	ChapterSource string `json:"chapterSource,omitempty"`
	// Estimated says some cells give no play time of their own, and theirs
	// was worked out from the time map or their share of the sectors.
	Estimated bool `json:"timesEstimated,omitempty"`
	// Truncated says the title lists more cells than any real title has, and
	// only the first of them were read.
	Truncated bool `json:"truncated,omitempty"`
	// Path says how the cells were found: "chapters" (the chapter table and
	// next-PGC links) or "commands" (a dry run of the title's own commands).
	Path string `json:"path,omitempty"`
}

// TitleCell is one cell of a flattened title with its PGC.
type TitleCell struct {
	PGCN  int
	CellN int
	Cell  Cell
	Start float64
}

// Titles flattens every title (following PGC chains in program order).
func (d *Disc) Titles() []TitleSummary {
	var out []TitleSummary
	for _, t := range d.VMG.Titles {
		if ts := d.Title(t.Number); ts != nil {
			out = append(out, *ts)
		}
	}
	return out
}

// Title flattens one title by number.
func (d *Disc) Title(number int) *TitleSummary { return d.TitleAngle(number, 1) }

// nextTitlePGC is the PGC a title continues with after pgcn: its next-PGC
// link, or else the next PGC the title's chapter table names (discs whose
// chapters span PGCs without linking them). 0 ends the title.
func nextTitlePGC(v *VTS, vtsTTN, pgcn int) int {
	if v == nil || pgcn < 1 || pgcn > len(v.PGCs) {
		return 0
	}
	if next := v.PGCs[pgcn-1].PGC.NextPGCN; next != 0 {
		return next
	}
	if vtsTTN >= 1 && vtsTTN <= len(v.PTT) {
		for _, x := range v.PTT[vtsTTN-1] {
			if x.PGCN > pgcn && x.PGCN <= len(v.PGCs) {
				return x.PGCN
			}
		}
	}
	return 0
}

// MainTitle guesses the feature with the title scoring engine (see
// Analyze): the title with the most distinct, readable video, marked down for
// the signs of decoys (cells replayed many times over or in scrambled order,
// cells outside the title VOB, zero-length cell spam, looping PGC chains) and
// up for what the disc's own first play reaches. It reads the IFOs only.
func (d *Disc) MainTitle() *TitleSummary {
	r := d.Analyze(RankOptions{})
	if len(r.Candidates) == 0 {
		return nil
	}
	return r.Candidates[0].summary
}
