// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// The rest of the synthetic disc generator: the VOBs, and the whole disc as
// a VIDEO_TS folder or as a disc image.
//
// A synthetic VOB holds no video. It is a sparse file of the right size with
// a NAV pack wherever a VOBU starts, carrying what the engine reads from
// real NAV packs: the VOB and cell id, how far the VOBU runs, and a menu's
// buttons. A two-hour "film" is a few kilobytes on disk.
//
// The image writer makes UDF 1.02 / ISO 9660 bridge images, the layout of a
// pressed DVD, or either file system alone, and can cut a file into several
// extents the way files over 4 GiB are stored.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// vfile is a file of a synthetic disc: its size and the bytes that aren't
// zero.
type vfile struct {
	name   string
	size   int64
	chunks map[int64][]byte
}

func wholeFile(name string, data []byte) vfile {
	return vfile{name: name, size: int64(len(data)), chunks: map[int64][]byte{0: data}}
}

// write puts the file's bytes at base in f, leaving the rest a hole.
func (v vfile) write(f *os.File, base int64) error {
	for off, data := range v.chunks {
		if _, err := f.WriteAt(data, base+off); err != nil {
			return err
		}
	}
	return nil
}

// navAt is a VOBU start: where, how many sectors follow, and whose it is.
type navAt struct {
	sector  uint32
	end     uint32 // sectors after the NAV pack that belong to the VOBU
	vob, id int
	buttons []Command
}

func buttonPCI(cmds []Command) []byte {
	pci := make([]byte, pciMin)
	if len(cmds) == 0 {
		return pci
	}
	pci[pciHLISS+1] = 1
	pci[pciBTNNS] = byte(len(cmds))
	pci[pciBTNMD] = 0x10
	for i, cmd := range cmds {
		b := pciBTNIT + i*btnSize
		x0, x1, y0, y1 := 100, 200, 50+40*i, 80+40*i
		pci[b] = 1<<6 | byte(x0>>4)
		pci[b+1] = byte(x0&0x0f)<<4 | byte(x1>>8)
		pci[b+2] = byte(x1 & 0xff)
		pci[b+3] = byte(y0 >> 4)
		pci[b+4] = byte(y0&0x0f)<<4 | byte(y1>>8)
		pci[b+5] = byte(y1 & 0xff)
		copy(pci[b+10:], cmd[:])
	}
	return pci
}

func (n navAt) pack() []byte {
	dsi := make([]byte, 0x3fa)
	binary.BigEndian.PutUint32(dsi[4:], n.sector)
	binary.BigEndian.PutUint32(dsi[8:], n.end)
	binary.BigEndian.PutUint16(dsi[0x18:], uint16(n.vob))
	dsi[0x1b] = byte(n.id)
	return navSector(buttonPCI(n.buttons), dsi)
}

// navsOf lists the VOBU starts of a VOB: one per piece of every listed cell.
func navsOf(pgcs []fxPGC, ids cellIDs, size uint32) []navAt {
	var out []navAt
	seen := map[[4]int]bool{}
	for _, p := range pgcs {
		for i, c := range p.cells {
			k := cellRange{c.first, c.last}
			vob, id := ids.of(c, i)
			// Cells of an angle block share their sectors and differ in id.
			key := [4]int{int(c.first), int(c.last), vob, id}
			if seen[key] || !c.listed(size) {
				continue
			}
			seen[key] = true
			pieces := c.pieces
			if pieces == nil {
				pieces = []cellRange{k}
			}
			for _, pc := range pieces {
				out = append(out, navAt{sector: pc.first, end: pc.last - pc.first, vob: vob, id: id, buttons: p.buttons})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sector < out[j].sector })
	return out
}

// vobFiles cuts a VOB address space of the given size into files of at most
// split sectors, each holding its NAV packs.
func vobFiles(names func(k int) string, sectors, split uint32, navs []navAt) []vfile {
	if sectors == 0 {
		return nil
	}
	if split == 0 {
		split = sectors
	}
	var out []vfile
	for k, at := 0, uint32(0); at < sectors; k, at = k+1, at+split {
		n := min(split, sectors-at)
		f := vfile{name: names(k), size: int64(n) * Sector, chunks: map[int64][]byte{}}
		for _, nv := range navs {
			if nv.sector >= at && nv.sector < at+n {
				f.chunks[int64(nv.sector-at)*Sector] = nv.pack()
			}
		}
		out = append(out, f)
	}
	return out
}

// defaultSplit is the usual size of one title VOB file: a gigabyte less a
// sector.
const defaultSplit = 524287

// vfiles is the whole disc as files: index files, backups and VOBs.
func (d fxDisc) vfiles() []vfile {
	var out []vfile
	vmg := encodeVMG(d)
	out = append(out, wholeFile("VIDEO_TS.IFO", vmg), wholeFile("VIDEO_TS.BUP", vmg))
	menus, sectors := layoutMenus(d.menus)
	out = append(out, vobFiles(func(int) string { return "VIDEO_TS.VOB" }, sectors, 0, navsOf(menus, idsFor(menus, 0), 0))...)
	split := d.split
	if split == 0 {
		split = defaultSplit
	}
	for i, v := range d.vts {
		b := encodeVTS(v)
		base := fmt.Sprintf("VTS_%02d_", i+1)
		out = append(out, wholeFile(base+"0.IFO", b), wholeFile(base+"0.BUP", b))
		menus, sectors := layoutMenus(v.menus)
		out = append(out, vobFiles(func(int) string { return base + "0.VOB" }, sectors, 0, navsOf(menus, idsFor(menus, 0), 0))...)
		if !v.noVOB {
			out = append(out, vobFiles(func(k int) string { return fmt.Sprintf("%s%d.VOB", base, k+1) }, v.titleSectors, split, navsOf(v.pgcs, idsFor(v.pgcs, v.titleSectors), v.titleSectors))...)
		}
	}
	return out
}

// writeFolder writes the disc as a VIDEO_TS folder and returns its parent.
func (d fxDisc) writeFolder(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "VIDEO_TS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range d.vfiles() {
		f, err := os.Create(filepath.Join(dir, v.name))
		if err == nil {
			if err = f.Truncate(v.size); err == nil {
				err = v.write(f, 0)
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ---------------------------------------------------------------------------
// Disc images
// ---------------------------------------------------------------------------

// imageOptions choose the file systems of a synthetic image.
type imageOptions struct {
	udf, iso bool
	// maxExtent cuts files into extents of at most this many bytes (0: the
	// most each file system allows), as files over 4 GiB are stored.
	maxExtent int64
	// pad puts this many empty sectors before the files, to push them past
	// an offset (4 GiB, say) without writing anything.
	pad int64
}

type placed struct {
	vfile
	lba int64 // absolute sector of its first byte
}

func le16(b []byte, v int) { binary.LittleEndian.PutUint16(b, uint16(v)) }
func le32(b []byte, v int64) {
	binary.LittleEndian.PutUint32(b, uint32(v))
}
func both32(b []byte, v int64) {
	binary.LittleEndian.PutUint32(b, uint32(v))
	binary.BigEndian.PutUint32(b[4:], uint32(v))
}

// isoDirRecord is one ISO 9660 directory record.
func isoDirRecord(name string, lba, size int64, flags byte) []byte {
	l := 33 + len(name)
	if l%2 == 1 {
		l++
	}
	r := make([]byte, l)
	r[0] = byte(l)
	both32(r[2:], lba)
	both32(r[10:], size)
	r[25] = flags
	r[28] = 1
	r[32] = byte(len(name))
	copy(r[33:], name)
	return r
}

// extentsOf cuts a file of size bytes at lba into extents of at most limit.
func extentsOf(lba, size, limit int64) [][2]int64 {
	if size == 0 {
		return [][2]int64{{lba, 0}}
	}
	var out [][2]int64
	for off := int64(0); off < size; off += limit {
		out = append(out, [2]int64{lba + off/Sector, min(limit, size-off)})
	}
	return out
}

// writeImage writes the disc as an image file: system area, ISO 9660 and
// UDF structures as asked, then the files of VIDEO_TS one after another.
// The file is sparse.
func (d fxDisc) writeImage(t testing.TB, opt imageOptions) string {
	t.Helper()
	files := d.vfiles()
	const (
		isoRoot    = 20 // ISO 9660 root directory
		isoVideoTS = 21 // ISO 9660 VIDEO_TS directory (up to 8 sectors)
		udfVDS     = 32 // UDF volume descriptor sequence
		udfAnchor  = 256
		udfPart    = 272 // UDF partition start
		// Inside the partition: block 0 file set descriptor, 1 root entry,
		// 2 root directory, 3 VIDEO_TS entry, 4..11 VIDEO_TS directory, then
		// one file entry per file.
		udfFirstFE = 12
	)
	dataStart := int64(udfPart+udfFirstFE+len(files)) + opt.pad
	var put []placed
	at := dataStart
	for _, f := range files {
		put = append(put, placed{f, at})
		at += (f.size + Sector - 1) / Sector
	}
	total := at + 300 // room for the closing anchors a real disc has
	path := filepath.Join(t.TempDir(), "DISC.ISO")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(out.Truncate(total * Sector))
	sector := func(n int64, b []byte) { _, err := out.WriteAt(b, n*Sector); must(err) }

	if opt.iso {
		limit := int64(0xfffff800) // the most one ISO 9660 extent holds
		if opt.maxExtent > 0 {
			limit = opt.maxExtent
		}
		pvd := make([]byte, Sector)
		pvd[0] = 1
		copy(pvd[1:], "CD001")
		pvd[6] = 1
		copy(pvd[40:72], fmt.Sprintf("%-32s", "SYNTH_ISO"))
		both32(pvd[80:], total)
		copy(pvd[156:], isoDirRecord("\x00", isoRoot, Sector, 2))
		sector(16, pvd)
		term := make([]byte, Sector)
		term[0] = 255
		copy(term[1:], "CD001")
		sector(17, term)
		root := append(isoDirRecord("\x00", isoRoot, Sector, 2), isoDirRecord("\x01", isoRoot, Sector, 2)...)
		const dirSectors = 8
		root = append(root, isoDirRecord("VIDEO_TS", isoVideoTS, dirSectors*Sector, 2)...)
		sector(isoRoot, root)
		dir := make([]byte, 0, dirSectors*Sector)
		add := func(rec []byte) {
			// Records never span sectors.
			if len(dir)%Sector+len(rec) > Sector {
				dir = append(dir, make([]byte, Sector-len(dir)%Sector)...)
			}
			dir = append(dir, rec...)
		}
		add(isoDirRecord("\x00", isoVideoTS, dirSectors*Sector, 2))
		add(isoDirRecord("\x01", isoRoot, Sector, 2))
		for _, f := range put {
			ext := extentsOf(f.lba, f.size, limit)
			for i, e := range ext {
				flags := byte(0)
				if i+1 < len(ext) {
					flags = 0x80 // more extents of this file follow
				}
				add(isoDirRecord(f.name+";1", e[0], e[1], flags))
			}
		}
		if len(dir) > dirSectors*Sector {
			t.Fatal("too many files for the synthetic ISO 9660 directory")
		}
		sector(isoVideoTS, dir)
	}

	if opt.udf {
		limit := int64(0x3ffff800) // the most one UDF short_ad holds
		if opt.maxExtent > 0 {
			limit = opt.maxExtent
		}
		tag := func(b []byte, id int) { le16(b, id) }
		avdp := make([]byte, Sector)
		tag(avdp, 2)
		le32(avdp[16:], 4*Sector)
		le32(avdp[20:], udfVDS)
		sector(udfAnchor, avdp)
		pv := make([]byte, Sector)
		tag(pv, 1)
		label := "SYNTH_UDF"
		pv[24] = 8
		copy(pv[25:], label)
		pv[55] = byte(len(label) + 1)
		sector(udfVDS, pv)
		pd := make([]byte, Sector)
		tag(pd, 5)
		le32(pd[188:], udfPart)
		le32(pd[192:], total-udfPart)
		sector(udfVDS+1, pd)
		lvd := make([]byte, Sector)
		tag(lvd, 6)
		le32(lvd[248:], Sector) // file set descriptor: one block, block 0, partition 0
		le32(lvd[264:], 6)
		le32(lvd[268:], 1)
		lvd[440], lvd[441] = 1, 6
		sector(udfVDS+2, lvd)
		td := make([]byte, Sector)
		tag(td, 8)
		sector(udfVDS+3, td)

		block := func(n int64, b []byte) { sector(udfPart+n, b) }
		longAD := func(b []byte, length, blk int64) {
			le32(b, length)
			le32(b[4:], blk)
		}
		fsd := make([]byte, Sector)
		tag(fsd, 256)
		longAD(fsd[400:], Sector, 1)
		block(0, fsd)
		// fileEntry is a File Entry with short allocation descriptors.
		fileEntry := func(dir bool, size int64, ext [][2]int64) []byte {
			fe := make([]byte, Sector)
			tag(fe, 261)
			fe[27] = 5
			if dir {
				fe[27] = 4
			}
			binary.LittleEndian.PutUint64(fe[56:], uint64(size))
			if 176+8*len(ext) > Sector {
				t.Fatal("too many extents for one synthetic UDF file entry")
			}
			le32(fe[172:], int64(8*len(ext)))
			for i, e := range ext {
				le32(fe[176+8*i:], e[1])
				le32(fe[180+8*i:], e[0]-udfPart)
			}
			return fe
		}
		fid := func(name string, chars byte, icb int64) []byte {
			l := 38
			if name != "" {
				l += 1 + len(name)
			}
			b := make([]byte, (l+3)&^3)
			tag(b, 257)
			b[18] = chars
			longAD(b[20:], Sector, icb)
			if name != "" {
				b[19] = byte(1 + len(name))
				b[38] = 8
				copy(b[39:], name)
			}
			return b
		}
		rootDir := append(fid("", 0x0a, 1), fid("VIDEO_TS", 0x02, 3)...)
		block(1, fileEntry(true, int64(len(rootDir)), [][2]int64{{udfPart + 2, int64(len(rootDir))}}))
		block(2, rootDir)
		dir := fid("", 0x0a, 1)
		for i, f := range put {
			dir = append(dir, fid(f.name, 0, int64(udfFirstFE+i))...)
			block(int64(udfFirstFE+i), fileEntry(false, f.size, extentsOf(f.lba, f.size, limit)))
		}
		if len(dir) > 8*Sector {
			t.Fatal("too many files for the synthetic UDF directory")
		}
		block(3, fileEntry(true, int64(len(dir)), [][2]int64{{udfPart + 4, int64(len(dir))}}))
		_, err := out.WriteAt(dir, (udfPart+4)*Sector)
		must(err)
	}
	for _, f := range put {
		must(f.write(out, f.lba*Sector))
	}
	return path
}

// ---------------------------------------------------------------------------
// Faults
// ---------------------------------------------------------------------------

var errBadSector = errors.New("input/output error")

// faultyFile fails every read that touches one of its bad sectors, the way
// a drive does on a scratch or on a zone made unreadable on purpose.
type faultyFile struct {
	*os.File
	bad   func(sector int64) bool
	mu    sync.Mutex
	reads int
}

func (f *faultyFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	for s := off / Sector; s*Sector < off+int64(len(p)); s++ {
		if f.bad(s) {
			return 0, errBadSector
		}
	}
	return f.File.ReadAt(p, off)
}

// withBadSectors makes every file opened for VOB reads during the test fail
// on the sectors bad says are unreadable (counted from the start of the
// file).
func withBadSectors(t testing.TB, bad func(name string, sector int64) bool) {
	t.Helper()
	old := openBlockFile
	openBlockFile = func(name string) (blockFile, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(name)
		return &faultyFile{File: f, bad: func(s int64) bool { return bad(base, s) }}, nil
	}
	t.Cleanup(func() { openBlockFile = old })
}

var _ io.ReaderAt = (*faultyFile)(nil)
