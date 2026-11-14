// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package api is a disc module's own HTTP API and SDUI sources (module API 5): a disc's page, why
// a title is its film and the choice of another, the names given to its titles and chapters, and
// its sections on a title's page in every app. WittyMediaServer passes the routes on at
// /api/modules/<module>/... once it has checked who may call them (integration.json).
//
// What a disc holds and which title is its film rests on what only the server keeps and checks:
// the scan's ranking and an administrator's choice are columns of the server's item, which its own
// disc sessions play from, and every answer of the disc engine is read in a helper of its own and
// checked by the server (its scramble and AACS guards stay the server's). So this asks the host
// API, on behalf of the person asking (/host/v1/discs/...), and shows the answer. The names given
// to titles and chapters are this module's own, in its own database.
//
// The same files are in WittyDVD and WittyBluray (Config says which): they move into WittyDisc
// with its next release.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
)

// Config is which disc module this is.
type Config struct {
	Module string // wittydvd or wittybluray
	Bluray bool
}

// Unit is what the disc calls its titles.
func (c Config) Unit() string {
	if c.Bluray {
		return "Playlist"
	}
	return "Title"
}

// Host is the host API, as much of it as this needs.
type Host interface {
	Do(ctx context.Context, on host.On, method, path string, body, out any) error
}

// App is what the routes and sources need.
type App struct {
	Config
	Host   Host
	Labels *Store
	Log    *slog.Logger
}

// Disc is what the host API says of a disc, for a person (WittyMediaServer host_discs.go).
type Disc struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Label      string          `json:"label"`
	RuntimeSec float64         `json:"runtimeSec"`
	Scrambled  bool            `json:"scrambled"`
	Main       int             `json:"main"`
	EngineMain int             `json:"engineMain"`
	Chosen     json.RawMessage `json:"chosen"`
	ChosenBy   string          `json:"chosenBy,omitempty"`
	Rank       json.RawMessage `json:"rank"`
	FeatureSec float64         `json:"featureSec,omitempty"`
	RankError  string          `json:"rankError,omitempty"`
	Pending    bool            `json:"pending"`
	NoPlay     bool            `json:"noPlay"`
	Page       *Page           `json:"page,omitempty"`
	PageError  string          `json:"pageError,omitempty"`
	Second     json.RawMessage `json:"secondOpinion,omitempty"`

	rank *Rank
}

// IsChosen says an administrator chose the film.
func (d *Disc) IsChosen() bool { return len(d.Chosen) > 0 && string(d.Chosen) != "null" }

// Ranking is the kept ranking, read, or nil.
func (d *Disc) Ranking() *Rank {
	if d.rank == nil && len(d.Rank) > 0 && string(d.Rank) != "null" {
		var r Rank
		if json.Unmarshal(d.Rank, &r) == nil {
			d.rank = &r
		}
	}
	return d.rank
}

// Page is a disc's page: the film, the other titles worth watching, the menus to jump into.
type Page struct {
	Main   *Title  `json:"main"`
	Extras []Title `json:"extras"`
	Menus  []Menu  `json:"menus"`
	Hidden int     `json:"hidden"`
}

// Title is one title (a DVD's) or playlist (a Blu-ray's).
type Title struct {
	TTN         int       `json:"ttn"`
	Label       string    `json:"label"` // the name someone gave it, "" for none
	DurationSec float64   `json:"durationSec"`
	Chapters    []Chapter `json:"chapters"`
	Audio       []string  `json:"audio"`
	Subtitles   []string  `json:"subtitles"`
	Angles      int       `json:"angles"`
	Wide        bool      `json:"wide"`
}

// Chapter is one of a title's chapters.
type Chapter struct {
	N     int     `json:"n"`
	Start float64 `json:"start"`
	Label string  `json:"label"`
}

// Menu is one of the disc's menus a viewer can jump into.
type Menu struct {
	Kind  string `json:"kind"`
	VTS   int    `json:"vts"`
	Label string `json:"label"`
}

// Rank is the parts of a kept ranking the pages show.
type Rank struct {
	Engine     string      `json:"engine"`
	Main       int         `json:"main"`
	Confidence float64     `json:"confidence"`
	Level      string      `json:"level"`
	Why        string      `json:"why"`
	Versions   []Version   `json:"versions"`
	Episodes   []Episode   `json:"episodes"`
	Candidates []Candidate `json:"candidates"`
	Decoys     int         `json:"decoys"`
	Hidden     int         `json:"hidden"`
	Repairs    []string    `json:"repairs"`
	RepairCnt  int         `json:"repairCount"`
	Issues     []Issue     `json:"issues"`
	Padded     int64       `json:"paddedSectors"`
	Second     *Second     `json:"secondOpinion"`
}

type Version struct {
	Kind        string  `json:"kind"`
	TTN         int     `json:"ttn"`
	Angle       int     `json:"angle"`
	DurationSec float64 `json:"durationSec"`
	Label       string  `json:"label"`
	Main        bool    `json:"main"`
}

type Episode struct {
	Index       int     `json:"index"`
	TTN         int     `json:"ttn"`
	DurationSec float64 `json:"durationSec"`
	StartSec    float64 `json:"startSec"`
	Part        bool    `json:"part"`
}

type Candidate struct {
	TTN         int     `json:"ttn"`
	DurationSec float64 `json:"durationSec"`
	Class       string  `json:"class"`
	Chapters    int     `json:"chapters"`
}

type Issue struct {
	What     string `json:"what"`
	Severity string `json:"severity"`
	TTN      int    `json:"ttn"`
}

type Second struct {
	Source      string   `json:"source"`
	Agrees      bool     `json:"agrees"`
	Note        string   `json:"note"`
	Differences []string `json:"differences"`
}

// isVersion says a title is a cut of the film: the one that plays is the page's main title, the
// others its versions, and none of them an extra.
func (r *Rank) isVersion(ttn int) bool {
	for _, v := range r.Versions {
		if v.TTN == ttn && v.Kind != "angle" {
			return true
		}
	}
	return false
}

// disc asks the host about one of this module's discs for a person, with its labels.
func (a *App) disc(ctx context.Context, on host.On, id int64, query string) (*Disc, error) {
	var d Disc
	path := "/discs/" + strconv.FormatInt(id, 10)
	if query != "" {
		path += "?" + query
	}
	if err := a.Host.Do(ctx, on, "GET", path, nil, &d); err != nil {
		return nil, err
	}
	if d.Page != nil {
		a.Labels.Get(ctx, id).apply(d.Page)
	}
	return &d, nil
}

// Routes adds the module's routes, as integration.json lists them.
func (a *App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /discs/{id}", a.handlePage)
	mux.HandleFunc("GET /discs/{id}/analysis", a.handleAnalysis)
	mux.Handle("POST /discs/{id}/analysis", service.Admins(http.HandlerFunc(a.handleRank)))
	mux.Handle("PUT /discs/{id}/choice", service.Admins(http.HandlerFunc(a.handleChoose)))
	mux.Handle("DELETE /discs/{id}/choice", service.Admins(http.HandlerFunc(a.handleChoose)))
	mux.Handle("PUT /discs/{id}/labels", service.Admins(http.HandlerFunc(a.handleLabels)))
	if a.Bluray {
		mux.Handle("GET /libbluray", service.Admins(http.HandlerFunc(a.handleLibbluray)))
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeHostErr passes on what the host API said, with its status.
func writeHostErr(w http.ResponseWriter, err error) {
	var he *host.Error
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 600 {
		writeErr(w, he.Status, he.Message)
		return
	}
	writeErr(w, http.StatusBadGateway, "The server didn't answer about this disc.")
}

func itemID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return 0, false
	}
	return id, true
}

// GET /discs/{id}: the disc's page: the film first, with its chapters, then bonus features,
// then shortcuts into the disc's menus, with why the film is the film.
func (a *App) handlePage(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	d, err := a.disc(r.Context(), host.For(r), id, "page=1")
	if err != nil {
		writeHostErr(w, err)
		return
	}
	switch {
	case d.PageError == "encrypted":
		writeErr(w, 403, "This disc is encrypted, and encrypted discs aren't supported.")
		return
	case d.PageError != "":
		writeErr(w, 500, d.PageError)
		return
	}
	out := map[string]any{"main": nil, "extras": d.Page.Extras, "menus": d.Page.Menus, "rank": nil}
	if d.Page.Main != nil {
		out["main"] = *d.Page.Main
	}
	if d.Page.Hidden >= 0 {
		out["hidden"] = d.Page.Hidden
	}
	if rk := d.Ranking(); rk != nil {
		why := map[string]any{"why": rk.Why, "confidence": rk.Confidence, "level": rk.Level, "engineMain": rk.Main, "decoys": rk.Decoys}
		if d.IsChosen() {
			why["chosen"] = d.Main
		}
		out["rank"] = why
		if len(rk.Episodes) > 0 {
			out["episodes"] = rk.Episodes
		}
		if len(rk.Versions) > 0 {
			out["versions"] = rk.Versions
		}
	}
	writeJSON(w, 200, out)
}

// Analysis is which title is the film and why.
type Analysis struct {
	Kind string `json:"kind"` // dvd or bluray
	// Ranked: the engine gave a ranking; without one, the main title is the older rule's pick.
	Ranked bool `json:"ranked"`
	// Pending: the ranking is being worked out on a server set to go easy on its machine.
	Pending bool `json:"pending"`
	// Main is what Play plays; EngineMain the engine's own pick; -1 when there is none.
	Main          int             `json:"main"`
	EngineMain    int             `json:"engineMain"`
	Chosen        json.RawMessage `json:"chosen"`
	ChosenBy      string          `json:"chosenBy,omitempty"`
	Rank          json.RawMessage `json:"rank"`
	CanChoose     bool            `json:"canChoose"`
	RankError     string          `json:"rankError,omitempty"`
	Protected     bool            `json:"protected"`
	SecondOpinion json.RawMessage `json:"secondOpinion,omitempty"`
}

func (a *App) analysis(r *http.Request, d *Disc) Analysis {
	null := json.RawMessage("null")
	v := Analysis{Kind: d.Kind, Ranked: d.Ranking() != nil, Pending: d.Pending, Main: d.Main, EngineMain: d.EngineMain,
		Chosen: d.Chosen, ChosenBy: d.ChosenBy, Rank: d.Rank, CanChoose: service.Who(r).Admin, RankError: d.RankError,
		Protected: d.Scrambled, SecondOpinion: d.Second}
	if len(v.Chosen) == 0 {
		v.Chosen = null
	}
	if len(v.Rank) == 0 {
		v.Rank = null
	}
	return v
}

// GET /discs/{id}/analysis: why this title is the film, worked out again first when the kept
// ranking is another engine's. Anyone who may see the disc may read it; who chose is told to
// administrators only.
func (a *App) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	d, err := a.disc(r.Context(), host.For(r), id, "rank=1")
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, 200, a.analysis(r, d))
}

// POST /discs/{id}/analysis {"secondOpinion": bool}: work the ranking out again, for an
// administrator; a Blu-ray's can ask a libbluray on the server to compare.
func (a *App) handleRank(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var b struct {
		SecondOpinion bool `json:"secondOpinion"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil {
			writeErr(w, 400, "expected a JSON body")
			return
		}
	}
	var d Disc
	if err := a.Host.Do(r.Context(), host.For(r), "POST", "/discs/"+strconv.FormatInt(id, 10)+"/rank", b, &d); err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, 200, a.analysis(r, &d))
}

// PUT /discs/{id}/choice {"ttn": n}: n is the disc's film, for everyone. DELETE forgets the
// choice: the engine's pick is the film again. Administrators only.
func (a *App) handleChoose(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	b := struct {
		TTN *int `json:"ttn"`
	}{}
	if r.Method == http.MethodDelete {
		none := -1
		b.TTN = &none
	} else if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil || b.TTN == nil || *b.TTN < 0 {
		writeErr(w, 400, "Say which title: ttn.")
		return
	}
	var d Disc
	if err := a.Host.Do(r.Context(), host.For(r), "PUT", "/discs/"+strconv.FormatInt(id, 10)+"/choice", b, &d); err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, 200, a.analysis(r, &d))
}

// PUT /discs/{id}/labels {"titles":{"3":"Making of"},"chapters":{"1":{"4":"The dragon"}}}:
// the names of a disc's titles and chapters, for everyone. Administrators only.
func (a *App) handleLabels(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	// The disc has to be one this person may see, and this module's.
	if _, err := a.disc(r.Context(), host.For(r), id, ""); err != nil {
		writeHostErr(w, err)
		return
	}
	var b Labels
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&b); err != nil {
		writeErr(w, 400, "expected {titles, chapters}")
		return
	}
	b = b.Clean()
	if err := a.Labels.Set(r.Context(), id, b); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

// GET /libbluray: whether a libbluray on the server can give a second opinion, and where it is.
func (a *App) handleLibbluray(w http.ResponseWriter, r *http.Request) {
	var out json.RawMessage
	if err := a.Host.Do(r.Context(), host.For(r), "GET", "/discs/libbluray", nil, &out); err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}
