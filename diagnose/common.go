// What every disc check shares.

package diagnose

import (
	"errors"
	"fmt"
	"github.com/wittywilla/wittydisc/isofs"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// discFile is one file of a disc: inside an image, or on disk for a disc folder.
type discFile struct {
	Name string // its path on the disc, with forward slashes
	Size int64
	Exts []isofs.Extent // an image's: byte ranges in the image
	Real string         // a folder's: where it is on disk. Never reported.
}

// discSource is what can be said about a disc before an engine sees it.
type discSource struct {
	Path    string
	IsDir   bool
	Format  string // udf, iso9660 or folder
	Label   string
	Size    int64
	Files   []discFile
	HasDVD  bool
	HasBD   bool
	img     *isofs.Disc
	byUpper map[string]*discFile
}

func (s *discSource) close() {
	if s.img != nil {
		_ = s.img.Close() // only read from
	}
}

// file finds a file by its path on the disc, whatever the case.
func (s *discSource) file(name string) *discFile { return s.byUpper[strings.ToUpper(name)] }

// read reads a file of the disc, at most max bytes.
func (s *discSource) read(name string, max int64) ([]byte, error) {
	f := s.file(name)
	if f == nil {
		return nil, fs.ErrNotExist
	}
	if s.img != nil {
		return s.img.ReadFile(f.Name, max)
	}
	h, err := os.Open(f.Real)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return io.ReadAll(io.LimitReader(h, max))
}

// maxFolderFiles bounds a disc folder's walk: real discs hold hundreds of files.
const maxFolderFiles = 50000

// discFolders are the folders a video disc keeps at its top level.
var discFolders = map[string]bool{"VIDEO_TS": true, "AUDIO_TS": true, "BDMV": true, "BDAV": true, "AACS": true, "BDSVM": true, "CERTIFICATE": true, "PRIVATE": true, "AVCHD": true}

func openSource(path string) (*discSource, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s := &discSource{Path: path, IsDir: st.IsDir(), byUpper: map[string]*discFile{}}
	if !st.IsDir() {
		s.Size = st.Size()
		img, err := isofs.Open(path)
		if err != nil {
			return s, err
		}
		s.img, s.Format, s.Label = img, img.Format, img.Label
		for _, f := range img.Files() {
			if f.IsDir {
				continue
			}
			s.Files = append(s.Files, discFile{Name: f.Path, Size: f.Size, Exts: f.Extents})
		}
	} else {
		s.Format = "folder"
		root, starts := path, []string{}
		if up := strings.ToUpper(filepath.Base(path)); up == "VIDEO_TS" || up == "BDMV" || up == "BDAV" {
			root = filepath.Dir(path)
			starts = []string{path}
		} else {
			// Only a disc's own folders count: this may be a folder that also
			// holds much else, and a check must not wander through all of it.
			ents, _ := os.ReadDir(path)
			for _, e := range ents {
				if e.IsDir() && discFolders[strings.ToUpper(e.Name())] {
					starts = append(starts, filepath.Join(path, e.Name()))
				}
			}
		}
		for _, start := range starts {
			_ = filepath.WalkDir(start, func(p string, e fs.DirEntry, err error) error {
				if err != nil || e.IsDir() {
					return nil
				}
				if !e.Type().IsRegular() || len(s.Files) >= maxFolderFiles {
					return nil
				}
				info, err := e.Info()
				if err != nil {
					return nil
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return nil
				}
				s.Files = append(s.Files, discFile{Name: filepath.ToSlash(rel), Size: info.Size(), Real: p})
				s.Size += info.Size()
				return nil
			})
		}
		slices.SortFunc(s.Files, func(a, b discFile) int { return strings.Compare(a.Name, b.Name) })
	}
	for i := range s.Files {
		f := &s.Files[i]
		up := strings.ToUpper(f.Name)
		s.byUpper[up] = f
		switch {
		case strings.HasPrefix(up, "VIDEO_TS/"):
			s.HasDVD = true
		case strings.HasPrefix(up, "BDMV/"), strings.HasPrefix(up, "BDAV/"), strings.Contains(up, "AVCHD/BDMV/"):
			s.HasBD = true
		}
	}
	return s, nil
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func hms(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

func formatName(f string) string {
	switch f {
	case "udf":
		return "UDF"
	case "iso9660":
		return "ISO 9660"
	}
	return f
}

// plainErr takes the path out of a file error, leaving only what went wrong.
func plainErr(err error) string {
	if err == nil {
		return ""
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}
