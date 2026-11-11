// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvdplayer

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/wittywilla/wittydisc/session"
)

// The engine's synthetic discs, written out by the engine's own test (see
// export_synth_test.go beside this folder), played through a session here: the film
// the engine picks is the film the window gets, whole and in order.

// synthDisc is one exported disc and its right answer, as the engine lists it.
type synthDisc struct {
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	Main          int       `json:"main"`
	Feature       []int     `json:"feature"`
	FeatureSec    float64   `json:"featureSec"`
	Chapters      int       `json:"chapters"`
	Episodes      []int     `json:"episodes"`
	EpisodeStarts []float64 `json:"episodeStartsSec"`
	PlayAll       int       `json:"playAll"`
	MinDecoys     int       `json:"minDecoys"`
	Versions      []string  `json:"versions"`
	Titles        int       `json:"titles"`
}

var synth struct {
	once  sync.Once
	dir   string
	list  []synthDisc
	skip  string
	fatal string
}

// TestMain removes the exported discs, which are a few hundred megabytes of mostly
// empty files, when the tests are done.
func TestMain(m *testing.M) {
	code := m.Run()
	if synth.dir != "" {
		_ = os.RemoveAll(synth.dir)
	}
	os.Exit(code)
}

// synthDiscs has the engine write its scenario discs out, once for the whole test
// binary. It skips when this folder isn't inside a checkout of the engine.
func synthDiscs(t *testing.T) (string, []synthDisc) {
	t.Helper()
	synth.once.Do(func() {
		engine, err := filepath.Abs("..")
		if err != nil {
			synth.fatal = err.Error()
			return
		}
		if _, err := os.Stat(filepath.Join(engine, "export_synth_test.go")); err != nil {
			synth.skip = "the engine's checkout isn't around this folder (no ../export_synth_test.go), so there are no synthetic discs to play"
			return
		}
		gobin, err := exec.LookPath("go")
		if err != nil {
			synth.skip = "the go command isn't on the PATH, so the engine can't write its synthetic discs out"
			return
		}
		dir, err := os.MkdirTemp("", "wittydvd-synth-")
		if err != nil {
			synth.fatal = err.Error()
			return
		}
		synth.dir = dir
		cmd := exec.Command(gobin, "test", "-run", "^TestExportSyntheticDiscs$", "-count=1", ".")
		cmd.Dir = engine
		cmd.Env = append(os.Environ(), "WITTYDVD_SYNTH_OUT="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			synth.fatal = "the engine couldn't write its synthetic discs: " + err.Error() + "\n" + string(out)
			return
		}
		b, err := os.ReadFile(filepath.Join(dir, "expected.json"))
		if err == nil {
			err = json.Unmarshal(b, &synth.list)
		}
		if err != nil {
			synth.fatal = "expected.json: " + err.Error()
		}
	})
	if synth.skip != "" {
		t.Skip(synth.skip)
	}
	if synth.fatal != "" {
		t.Fatal(synth.fatal)
	}
	return synth.dir, synth.list
}

// openSynth opens one of the discs by its path in the list.
func openSynth(t *testing.T, name string) (*dvdSession, synthDisc) {
	t.Helper()
	dir, list := synthDiscs(t)
	for _, d := range list {
		if d.Path != name {
			continue
		}
		s, err := Open(filepath.Join(dir, filepath.FromSlash(d.Path)), false, Prefs{})
		if err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s.(*dvdSession), d
	}
	t.Fatalf("the engine exported no disc called %s", name)
	return nil, synthDisc{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1 }

// streamSec and streamBytes add up what a presentation hands the player.
func streamSec(p *Presentation) (sec float64, bytes int64) {
	for _, sg := range p.Media.Segments {
		sec += sg.Duration
	}
	for _, e := range p.Media.Extents {
		bytes += e[2]
	}
	return sec, bytes
}

// Every exported disc: the guide names the engine's film, with its whole length, its
// episodes and its other cuts, and choosing the feature plays exactly that.
func TestGuideAndFeatureOfEverySyntheticDisc(t *testing.T) {
	_, list := synthDiscs(t)
	for _, d := range list {
		t.Run(d.Path, func(t *testing.T) {
			s, _ := openSynth(t, d.Path)
			if in := s.Info(); in.Main != d.Main || len(in.Titles) != d.Titles {
				t.Errorf("info: main %d of %d titles, want %d of %d", in.Main, len(in.Titles), d.Main, d.Titles)
			}
			g := s.Guide()
			if g.Feature == nil || g.Feature.Title != d.Main {
				t.Fatalf("feature %+v, want title %d", g.Feature, d.Main)
			}
			if g.Why == "" || g.Level == "" || g.Confidence <= 0 || g.Confidence > 1 {
				t.Errorf("why %q, level %q, confidence %v", g.Why, g.Level, g.Confidence)
			}
			if len(d.Feature) > 1 && !reflect.DeepEqual(g.Feature.Titles, d.Feature) {
				t.Errorf("feature titles %v, want %v", g.Feature.Titles, d.Feature)
			}
			if d.FeatureSec > 0 && !near(g.Feature.Duration, d.FeatureSec) {
				t.Errorf("feature is %v s, want %v", g.Feature.Duration, d.FeatureSec)
			}
			if g.Decoys < d.MinDecoys {
				t.Errorf("%d decoys, want at least %d", g.Decoys, d.MinDecoys)
			}
			for _, o := range g.Others {
				if o.Title == d.Main || o.Kind != "title" {
					t.Errorf("another choice is %+v", o)
				}
			}
			if len(g.Others) > maxOthers || len(g.Others)+g.Hidden+max(len(d.Feature), 1) != d.Titles {
				t.Errorf("%d others and %d hidden don't account for %d titles", len(g.Others), g.Hidden, d.Titles)
			}
			if len(g.Episodes) != len(d.Episodes) {
				t.Fatalf("%d episodes, want %d", len(g.Episodes), len(d.Episodes))
			}
			for i, e := range g.Episodes {
				if e.Title != d.Episodes[i] || (i < len(d.EpisodeStarts) && !near(e.StartSec, d.EpisodeStarts[i])) {
					t.Errorf("episode %d is %+v, want title %d", i+1, e, d.Episodes[i])
				}
			}
			if (g.PlayAll == nil) != (d.PlayAll == 0) || (g.PlayAll != nil && g.PlayAll.Title != d.PlayAll) {
				t.Errorf("play all %+v, want title %d", g.PlayAll, d.PlayAll)
			}
			if len(g.Versions) != len(d.Versions) {
				t.Errorf("versions %+v, want %v", g.Versions, d.Versions)
			}

			p, err := s.Choose("feature")
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != "title" || p.Title == nil || p.Title.TTN != d.Main || p.Media == nil {
				t.Fatalf("the feature presents as %+v", p)
			}
			if d.FeatureSec > 0 && (!near(p.Title.Duration, d.FeatureSec) || !near(p.Media.Duration, d.FeatureSec)) {
				t.Errorf("the feature plays %v s (stream %v s), want %v", p.Title.Duration, p.Media.Duration, d.FeatureSec)
			}
			sec, bytes := streamSec(p)
			if !near(sec, p.Title.Duration) || bytes <= 0 {
				t.Errorf("the stream's segments add up to %v s and %d bytes for a title of %v s", sec, bytes, p.Title.Duration)
			}
			if d.Chapters > 0 && len(d.Feature) <= 1 && len(p.Title.Chapters) != d.Chapters {
				t.Errorf("%d chapters, want %d", len(p.Title.Chapters), d.Chapters)
			}
			if g.Feature.Chapters != len(p.Title.Chapters) {
				t.Errorf("the guide says %d chapters, the title has %d", g.Feature.Chapters, len(p.Title.Chapters))
			}
			for i, c := range p.Title.Chapters {
				if c < 0 || c >= p.Title.Duration || (i > 0 && c <= p.Title.Chapters[i-1]) {
					t.Errorf("chapter %d starts at %v in a title of %v s", i+1, c, p.Title.Duration)
				}
			}
			if _, err := s.Choose("title:999"); err == nil {
				t.Error("a choice the disc doesn't have was played")
			}
		})
	}
}

// A film the disc cuts into four titles and plays in a row is one stream with one
// timeline, wherever the viewer comes into it, and the disc's navigator follows along.
func TestChainedFeaturePlaysAsOneFilm(t *testing.T) {
	for _, name := range []string{"chained-fakes", "chained-fakes.iso"} {
		t.Run(name, func(t *testing.T) {
			s, d := openSynth(t, name)
			g := s.Guide()
			if !g.Feature.OneStream || g.Feature.Source != "chain" {
				t.Fatalf("feature %+v", g.Feature)
			}
			p, _ := s.Choose("feature")
			if p.Kind != "title" || !near(p.Title.Duration, d.FeatureSec) || p.Title.StartSec != 0 {
				t.Fatalf("%+v", p.Title)
			}
			token := p.Media.Token
			// The parts lie one after another on the disc, so the stream does too.
			if len(p.Media.Segments) < len(d.Feature) {
				t.Errorf("%d segments for %d titles", len(p.Media.Segments), len(d.Feature))
			}
			// Asking for a later part by its own title number lands inside the film.
			second := s.PlayTitle(d.Feature[1], 1)
			if second.Kind != "title" || second.Title.TTN != d.Main || second.Title.StartSec <= 0 || second.Media.Token != token {
				t.Fatalf("part two presents as title %d from %v s, token %s", second.Title.TTN, second.Title.StartSec, second.Media.Token)
			}
			if !near(second.Title.Duration, d.FeatureSec) || second.Title.Chapter < 2 {
				t.Errorf("part two: %v s, chapter %d", second.Title.Duration, second.Title.Chapter)
			}
			// A chapter of the whole film, past the first title's own.
			last := len(p.Title.Chapters)
			q := s.PlayTitle(d.Main, last)
			if q.Title.TTN != d.Main || !near(q.Title.StartSec, p.Title.Chapters[last-1]) || q.Title.Chapter != last {
				t.Errorf("chapter %d starts at %v (chapter %d), want %v", last, q.Title.StartSec, q.Title.Chapter, p.Title.Chapters[last-1])
			}
			// A change of sound three quarters through keeps the place, and tells the
			// navigator which title that is.
			at := d.FeatureSec * 0.75
			r := s.Do(Action{Type: "audio", Stream: 0, PositionSec: at})
			if r.Kind != "title" || r.Title.TTN != d.Main || !near(r.Title.StartSec, at) {
				t.Fatalf("after an audio change: %+v", r.Title)
			}
			if cur := s.nav.Current(); cur.Title == nil || cur.Title.TTN == d.Main {
				t.Errorf("the navigator is still in title %v", cur.Title)
			}
			// At the end the navigator has walked through every part: what comes next
			// is not another piece of the film.
			s.Choose("feature")
			if e := s.Do(Action{Type: "ended"}); e.Kind == "title" && e.Title.TTN == d.Main {
				t.Errorf("the film started again after its end: %+v", e.Title)
			} else {
				for _, part := range d.Feature[1:] {
					if e.Kind == "title" && e.Title.TTN == part {
						t.Errorf("part %d played by itself after the film", part)
					}
				}
			}
		})
	}
}

// A film only its commands put together plays whole, not as the fragment its chapter
// table names.
func TestLinkedFeaturePlaysWhole(t *testing.T) {
	s, d := openSynth(t, "linked-decoys-damaged")
	if g := s.Guide(); g.Feature.Source != "commands" || len(g.Warnings) == 0 {
		t.Errorf("feature %+v, warnings %v", g.Feature, g.Warnings)
	}
	byTable := s.opened.Disc.Title(d.Main)
	p := s.PlayTitle(d.Main, 1)
	if p.Kind != "title" || !near(p.Title.Duration, d.FeatureSec) || byTable.Duration >= p.Title.Duration {
		t.Fatalf("plays %v s; the chapter table alone gives %v s, the film is %v s", p.Title.Duration, byTable.Duration, d.FeatureSec)
	}
	for _, ti := range s.Info().Titles {
		if ti.Main && !near(ti.Duration, d.FeatureSec) {
			t.Errorf("the title list says the film is %v s", ti.Duration)
		}
	}
}

// Episodes that only a play-all title holds start where the engine says they do.
func TestEpisodesOfAPlayAllTitle(t *testing.T) {
	s, d := openSynth(t, "playall")
	g := s.Guide()
	for i, e := range g.Episodes {
		p, err := s.Choose(e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != "title" || p.Title.TTN != d.PlayAll || !near(p.Title.StartSec, d.EpisodeStarts[i]) {
			t.Fatalf("episode %d presents as title %d from %v s, want title %d from %v s", i+1, p.Title.TTN, p.Title.StartSec, d.PlayAll, d.EpisodeStarts[i])
		}
		if i > 0 && p.Media.StartSec != p.Title.StartSec {
			t.Errorf("episode %d: the stream starts at %v s", i+1, p.Media.StartSec)
		}
	}
	if p, err := s.Choose("playall"); err != nil || p.Title.TTN != d.PlayAll || p.Title.StartSec != 0 {
		t.Errorf("play all: %+v %v", p, err)
	}
}

// Another cut of the film is a version, and plays as its own title.
func TestCutIsAVersion(t *testing.T) {
	s, d := openSynth(t, "cuts")
	g := s.Guide()
	if len(g.Versions) != 1 || g.Versions[0].Title == d.Main {
		t.Fatalf("versions %+v", g.Versions)
	}
	v := g.Versions[0]
	p, err := s.Choose(v.ID)
	if err != nil || p.Kind != "title" || p.Title.TTN != v.Title || !near(p.Title.Duration, v.Duration) {
		t.Fatalf("%+v %v", p, err)
	}
	if back, _ := s.Choose("feature"); back.Title.TTN != d.Main {
		t.Errorf("back to the feature gave title %d", back.Title.TTN)
	}
}

// The calls a window makes: the guide and a choice through the host, as the helper
// program and a player's built-in copy both answer them.
func TestHostAnswersGuideAndChoose(t *testing.T) {
	dir, list := synthDiscs(t)
	h := NewHost()
	defer h.Close()
	if _, _, err := h.Handle(MethodGuide, nil); err == nil {
		t.Error("a guide with no disc open")
	}
	params, _ := json.Marshal(map[string]any{"path": filepath.Join(dir, list[0].Path)})
	if _, _, err := h.Handle("open", params); err != nil {
		t.Fatal(err)
	}
	res, _, err := h.Handle(MethodGuide, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Kind    string `json:"kind"`
		Feature struct {
			ID    string `json:"id"`
			Title int    `json:"title"`
		} `json:"feature"`
		Why string `json:"why"`
	}
	if err := json.Unmarshal(b, &g); err != nil || g.Kind != "dvd" || g.Feature.ID != "feature" || g.Feature.Title != list[0].Main || g.Why == "" {
		t.Fatalf("%s %v", b, err)
	}
	res, _, err = h.Handle(MethodChoose, json.RawMessage(`{"id":"feature"}`))
	if p, ok := res.(*Presentation); err != nil || !ok || p.Kind != "title" {
		t.Fatalf("%+v %v", res, err)
	}
	_, _, err = h.Handle(MethodChoose, json.RawMessage(`{"id":"episode:40"}`))
	var r *session.RefusalError
	if !errors.As(err, &r) || r.Code != session.CodeMissing {
		t.Errorf("a choice that isn't there: %v", err)
	}
	if _, _, err := h.Handle("close", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Handle(MethodChoose, json.RawMessage(`{"id":"feature"}`)); err == nil {
		t.Error("a choice after the disc was closed")
	}
}
