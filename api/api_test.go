// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/service"
)

// fakeHost is the server's host API for one disc, 7, which person 1 (an administrator) and person
// 2 may see, and person 3 may not.
type fakeHost struct {
	mu     sync.Mutex
	main   int
	chosen bool
	calls  []string
}

func (f *fakeHost) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(host.HeaderToken) != "tok" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("X-Witty-Host", "1")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		who := r.Header.Get(host.HeaderOnUser)
		if who == "" || who == "3" {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such disc"})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, host.Base)
		switch {
		case r.Method == "PUT" && path == "/discs/7/choice":
			if who != "1" {
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "Only an administrator can do that."})
				return
			}
			var b struct{ TTN int }
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.chosen, f.main = b.TTN >= 0, b.TTN
			if b.TTN < 0 {
				f.main = 2
			}
		case r.Method == "POST" && path == "/discs/7/rank":
		case r.Method == "GET" && path == "/discs/7":
		default:
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such disc"})
			return
		}
		d := map[string]any{"id": 7, "kind": "dvd", "title": "Film", "main": f.main, "engineMain": 2, "scrambled": false,
			"rank": map[string]any{"main": 2, "confidence": 0.9, "level": "high", "why": "Title 2 is the film.",
				"candidates": []any{map[string]any{"ttn": 2, "durationSec": 5400, "class": "main", "chapters": 12}, map[string]any{"ttn": 1, "durationSec": 300, "class": "extra"}},
				"episodes":   []any{}, "versions": []any{}, "decoys": 3}}
		if f.chosen {
			d["chosen"] = map[string]any{"ttn": f.main, "by": 1}
			if who == "1" {
				d["chosenBy"] = "admin"
			}
		}
		if r.URL.Query().Get("page") == "1" {
			d["page"] = map[string]any{"main": map[string]any{"ttn": 2, "durationSec": 5400, "chapters": []any{map[string]any{"n": 1, "start": 0}, map[string]any{"n": 2, "start": 600}}},
				"extras": []any{map[string]any{"ttn": 1, "durationSec": 300, "chapters": []any{}}}, "menus": []any{map[string]any{"kind": "root", "vts": 1, "label": "Main menu"}}, "hidden": 3}
		}
		_ = json.NewEncoder(w).Encode(d)
	})
}

func newApp(t *testing.T) (*App, *fakeHost) {
	t.Helper()
	f := &fakeHost{main: 2}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &App{Config: Config{Module: "wittydvd"}, Host: host.NewAt(srv.URL, "tok", nil), Labels: st}, f
}

// do sends a request to the routes as the server would, from a person.
func do(t *testing.T, a *App, user int64, admin bool, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	a.Routes(mux)
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("X-Witty-User", strconv.FormatInt(user, 10))
	if admin {
		r.Header.Set("X-Witty-Admin", "1")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestPageWithItsNames(t *testing.T) {
	a, _ := newApp(t)
	if w := do(t, a, 2, false, "PUT", "/discs/7/labels", map[string]any{"titles": map[string]string{"1": "Making of"}}); w.Code != 403 {
		t.Errorf("a viewer named a title: %d", w.Code)
	}
	if w := do(t, a, 3, true, "PUT", "/discs/7/labels", map[string]any{"titles": map[string]string{"1": "x"}}); w.Code != 404 {
		t.Errorf("names for a disc the person can't see: %d", w.Code)
	}
	w := do(t, a, 1, true, "PUT", "/discs/7/labels", map[string]any{"titles": map[string]string{"1": "  Making of ", "x": "no"}, "chapters": map[string]any{"2": map[string]string{"2": "The dragon", "1": ""}}})
	if w.Code != 200 {
		t.Fatalf("naming: %d %s", w.Code, w.Body)
	}
	w = do(t, a, 2, false, "GET", "/discs/7", nil)
	var page struct {
		Main   Title
		Extras []Title
		Menus  []Menu
		Hidden int
		Rank   map[string]any
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatalf("the page: %d %s", w.Code, w.Body)
	}
	if page.Main.TTN != 2 || page.Main.Chapters[1].Label != "The dragon" || page.Extras[0].Label != "Making of" || page.Hidden != 3 || page.Rank["why"] != "Title 2 is the film." {
		t.Errorf("the page: %+v", page)
	}
	if w := do(t, a, 3, false, "GET", "/discs/7", nil); w.Code != 404 {
		t.Errorf("a disc the person can't see: %d", w.Code)
	}
}

func TestAnalysisAndChoice(t *testing.T) {
	a, f := newApp(t)
	var v Analysis
	w := do(t, a, 2, false, "GET", "/discs/7/analysis", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Ranked || v.Main != 2 || v.CanChoose || v.Kind != "dvd" {
		t.Fatalf("the analysis: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(strings.Join(f.calls, " "), "/discs/7?rank=1") {
		t.Errorf("the analysis didn't ask for a ranking: %v", f.calls)
	}
	if w := do(t, a, 2, false, "PUT", "/discs/7/choice", map[string]int{"ttn": 1}); w.Code != 403 {
		t.Errorf("a viewer chose: %d", w.Code)
	}
	w = do(t, a, 1, true, "PUT", "/discs/7/choice", map[string]int{"ttn": 1})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Main != 1 || v.ChosenBy != "admin" || !v.CanChoose {
		t.Fatalf("choosing: %d %s", w.Code, w.Body)
	}
	w = do(t, a, 1, true, "DELETE", "/discs/7/choice", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Main != 2 || string(v.Chosen) != "null" {
		t.Fatalf("clearing: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, 1, true, "POST", "/discs/7/analysis", map[string]bool{"secondOpinion": false}); w.Code != 200 {
		t.Errorf("ranking again: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, 1, true, "GET", "/libbluray", nil); w.Code != 404 {
		t.Errorf("a DVD module answers for libbluray: %d", w.Code)
	}
}

func TestSources(t *testing.T) {
	a, _ := newApp(t)
	items := func(src string, admin bool) []sdui.Item {
		t.Helper()
		ans, err := a.Items(sdui.ItemsRequest{Source: sdui.Source{"type": "wittydvd." + src, "item": "7"}, Limit: 50, Viewer: sdui.Viewer{User: 2, Admin: admin}})
		if err != nil {
			t.Fatal(err)
		}
		return ans.Items
	}
	if f := items("feature", false); len(f) != 2 || f[0].Title != "Play the film" || f[0].Route != "/play/7?mode=title" {
		t.Errorf("feature: %+v", f)
	}
	if s := items("scenes", false); len(s) != 2 || s[1].Images["backdrop"] != "/api/items/7/disc/thumb/2/2" {
		t.Errorf("scenes: %+v", s)
	}
	why := items("why", true)
	if len(why) < 3 || why[0].Title != "Title 2 is the film" {
		t.Fatalf("why: %+v", why)
	}
	var choose, rank bool
	for _, r := range why {
		if r.Action != nil && r.Action.Op == "wittydvd.choose" && r.Action.Params["ttn"] == 1 {
			choose = true
		}
		if r.Action != nil && r.Action.Op == "wittydvd.rank" {
			rank = true
		}
	}
	if !choose || !rank {
		t.Errorf("an administrator's why has no choose or rank: %+v", why)
	}
	for _, r := range items("why", false) {
		if r.Action != nil {
			t.Errorf("a viewer's row runs %s", r.Action.Op)
		}
	}
	if r := items("refusal", false); len(r) != 0 {
		t.Errorf("a refusal of a disc that plays: %+v", r)
	}
	// Another module's sources, or a person who can't see the disc: nothing.
	if ans, _ := a.Items(sdui.ItemsRequest{Source: sdui.Source{"type": "wittybluray.why", "item": "7"}, Viewer: sdui.Viewer{User: 2}}); len(ans.Items) != 0 {
		t.Error("answered another module's source")
	}
	if ans, err := a.Items(sdui.ItemsRequest{Source: sdui.Source{"type": "wittydvd.why", "item": "7"}, Viewer: sdui.Viewer{User: 3}}); err != nil || len(ans.Items) != 0 {
		t.Errorf("a disc the person can't see: %+v %v", ans, err)
	}
}

// The commands are the module's own, each one of its routes.
func TestCommands(t *testing.T) {
	cs := Config{Module: "wittybluray", Bluray: true}.Commands()
	if len(cs) != 4 || cs[0].Op != "wittybluray.choose" || cs[0].Path != "/api/modules/wittybluray/discs/{item}/choice" || cs[3].Op != "wittybluray.compare" {
		t.Errorf("%+v", cs)
	}
	if len(Config{Module: "wittydvd"}.Commands()) != 3 {
		t.Error("a DVD can compare with libbluray")
	}
}

// One program: the helper's own calls go to the helper; the service starts behind its secret;
// the database goes with the module only when its data isn't kept.
func TestDual(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DataEnv, dir)
	helped := ""
	d := &Dual{Config: Config{Module: "wittydvd"}, Helper: func(method string, _ json.RawMessage) (any, []byte, error) {
		helped = method
		return "ok", nil, nil
	}}
	if res, _, err := d.Handle("open", nil); err != nil || res != "ok" || helped != "open" {
		t.Fatalf("a helper call: %v %v %q", res, err, helped)
	}
	p, _ := json.Marshal(service.StartParams{Host: "http://127.0.0.1:1", HostToken: "x"})
	res, _, err := d.Handle(service.MethodStart, p)
	st, ok := res.(*service.Started)
	if err != nil || !ok || !strings.HasPrefix(st.URL, "http://127.0.0.1:") || st.Token == "" {
		t.Fatalf("start: %v %v", res, err)
	}
	if again, _, _ := d.Handle(service.MethodStart, nil); again.(*service.Started).URL != st.URL {
		t.Error("asked again, it started another listener")
	}
	resp, err := http.Get(st.URL + "/http/discs/7")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("without the secret: %d", resp.StatusCode)
	}
	if _, err := d.App(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
		t.Fatalf("no database: %v", err)
	}
	keep, _ := json.Marshal(wittymodule.LifecycleArgs{KeepData: true})
	if _, _, err := d.Handle(wittymodule.CallUninstall, keep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
		t.Error("the data went although it was kept")
	}
	if _, _, err := d.Handle(wittymodule.CallUninstall, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, File)); !os.IsNotExist(err) {
		t.Errorf("the data stayed: %v", err)
	}
	// A source is the service's to answer, never the helper's: here it asks the host, which
	// isn't there.
	helped = ""
	if _, _, err := d.Handle(sdui.MethodItems, json.RawMessage(`{"source":{"type":"wittydvd.why","item":"7"},"viewer":{"user":2}}`)); err == nil || helped != "" {
		t.Errorf("a source: %v, helper asked %q", err, helped)
	}
}

func TestLabelsClean(t *testing.T) {
	l := Labels{Titles: map[string]string{"1": " a ", "x": "b", "2": strings.Repeat("é", 300)}, Chapters: map[string]map[string]string{"1": {"1": "", "2": "c"}, "y": {"1": "d"}}}.Clean()
	if len(l.Titles) != 2 || l.Titles["1"] != "a" || len([]rune(l.Titles["2"])) != maxLabel || len(l.Chapters) != 1 || l.Chapters["1"]["2"] != "c" {
		t.Errorf("%+v", l)
	}
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Set(ctx, 7, l); err != nil {
		t.Fatal(err)
	}
	if got := st.Get(ctx, 7); got.Titles["1"] != "a" || got.Chapters["1"]["2"] != "c" {
		t.Errorf("read back %+v", got)
	}
	if got := st.Get(ctx, 8); got.Titles == nil || len(got.Titles) != 0 {
		t.Errorf("a disc with no names: %+v", got)
	}
}
