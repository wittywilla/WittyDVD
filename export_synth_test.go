// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

// Synthetic discs for other projects' tests. A server or a player that uses
// this engine through its helper can't import the generator: it is test code
// of this package, and the engine is no dependency of theirs. So the
// scenarios are written out, on request, as discs on disk with what the
// right answer for each is:
//
//	WITTYDVD_SYNTH_OUT=<folder> go test -run '^TestExportSyntheticDiscs$' -count=1 .
//
// <folder>/expected.json lists them (SynthDisc). Every disc is opened again
// from where it was written and has to pass its scenario's own checks before
// it is listed, so a list that exists is a list of discs this engine gets
// right. Without the variable the test is skipped.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnvSynthOut names the folder the synthetic discs are written to.
const EnvSynthOut = "WITTYDVD_SYNTH_OUT"

// SynthDisc is one exported disc and its right answer.
type SynthDisc struct {
	Name string `json:"name"`
	// Path is the disc, relative to the list: a folder holding VIDEO_TS, or
	// an image file.
	Path  string `json:"path"`
	Image bool   `json:"image,omitempty"`
	Main  int    `json:"main"`
	// Feature are the titles the film runs through, in order, when the disc
	// cuts it into several or only commands put it together.
	Feature    []int   `json:"feature,omitempty"`
	FeatureSec float64 `json:"featureSec,omitempty"`
	Chapters   int     `json:"chapters,omitempty"`
	// Episodes are the titles of the episodes in viewing order, and
	// EpisodeStarts where each starts when one title holds them all.
	Episodes      []int     `json:"episodes,omitempty"`
	EpisodeStarts []float64 `json:"episodeStartsSec,omitempty"`
	PlayAll       int       `json:"playAll,omitempty"`
	MinDecoys     int       `json:"minDecoys,omitempty"`
	Versions      []string  `json:"versions,omitempty"`
	// Titles is how many titles the disc claims to have.
	Titles int `json:"titles"`
}

// exported are the scenarios written out: a plain disc, and the obfuscated
// ones a caller most needs to get right. A synthetic disc has no video, but
// its files are as long as a real disc's index says, and not every file
// system keeps the emptiness for free, so the list is short. The chained
// film is written as an image as well.
var exported = map[string]string{
	"simple movie":                                           "simple",
	"play all only, a chain per episode":                     "playall",
	"two cuts sharing cells":                                 "cuts",
	"linked film, forty decoys, a table gone from each copy": "linked-decoys-damaged",
	"chained film among fake title sets":                     "chained-fakes",
}

func TestExportSyntheticDiscs(t *testing.T) {
	out := os.Getenv(EnvSynthOut)
	if out == "" {
		t.Skipf("set %s to a folder to write the synthetic discs there", EnvSynthOut)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var list []SynthDisc
	verify := func(sc scenario, path string) int {
		t.Helper()
		o, err := Open(path)
		if err != nil {
			t.Fatalf("%s: %v", sc.name, err)
		}
		defer o.Close()
		sc.check(t, o.Analyze())
		return len(o.Disc.VMG.Titles)
	}
	for _, sc := range scenarios {
		slug, ok := exported[sc.name]
		if !ok {
			continue
		}
		d := sc.disc()
		dir := filepath.Join(out, slug)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		d.writeFolderAt(t, dir)
		if sc.damage != nil {
			files := d.files()
			sc.damage(files)
			for name, f := range files {
				if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), f.Data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		entry := SynthDisc{Name: sc.name, Path: slug, Main: sc.main, Feature: sc.feature, FeatureSec: sc.length * 60, Chapters: sc.chapters,
			Episodes: sc.episodes, PlayAll: sc.playAll, MinDecoys: sc.decoys, Versions: sc.versions}
		for _, s := range sc.starts {
			entry.EpisodeStarts = append(entry.EpisodeStarts, s*60)
		}
		entry.Titles = verify(sc, dir)
		list = append(list, entry)
		if strings.HasPrefix(slug, "chained") && sc.damage == nil {
			// The same disc as a pressed disc's image: UDF and ISO 9660 together.
			img := d.writeImageAt(t, imageOptions{udf: true, iso: true}, filepath.Join(out, slug+".iso"))
			image := entry
			image.Name, image.Path, image.Image = sc.name+", as an image", slug+".iso", true
			verify(sc, img)
			list = append(list, image)
		}
	}
	if len(list) < len(exported) {
		t.Fatalf("%d of %d scenarios were found by name", len(list), len(exported))
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "expected.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
