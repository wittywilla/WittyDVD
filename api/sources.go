// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
)

// A disc's sections on its page in every app (Design 2's "discpage", "whytitle" and "discrefuse"
// boards): the feature with its scenes, a TV disc's episodes, the film's other versions, the bonus
// material, shortcuts into the disc's own menus, why this title is the film, what was wrong with the
// copy, and the plain refusal of an encrypted disc. The TV apps draw them natively. Each module's
// sources answer for its own kind of disc only: the host API says no about the other's.

// Source names, after "<module>.".
var sourceNames = []string{"feature", "scenes", "episodes", "versions", "bonus", "menus", "why", "copy", "refusal"}

// Commands, after "<module>.": its own routes, which check again who is asking.
const (
	CommandChoose      = "choose"      // PUT /discs/{item}/choice {"ttn"}
	CommandClearChoice = "clearChoice" // DELETE /discs/{item}/choice
	CommandRank        = "rank"        // POST /discs/{item}/analysis
	CommandCompare     = "compare"     // POST /discs/{item}/analysis {"secondOpinion":true}, Blu-rays
)

// Commands are the module's own commands, as its sdui.json declares them.
func (c Config) Commands() []core.CommandSpec {
	routes := "/api/modules/" + c.Module + "/discs/{item}"
	unit := strings.ToLower(c.Unit())
	out := []core.CommandSpec{
		{Op: c.Module + "." + CommandChoose, Label: "Make it the film", Method: "PUT", Path: routes + "/choice", Body: `{"ttn":"{ttn}"}`,
			Params:      []core.CommandParam{{Name: "ttn", Type: "int", Label: c.Unit(), Required: true}},
			Description: fmt.Sprintf("An administrator makes params.ttn (a %s) the disc's film (item), for everyone, instead of the engine's pick.", unit)},
		{Op: c.Module + "." + CommandClearChoice, Label: "Use the engine's pick", Method: "DELETE", Path: routes + "/choice",
			Description: "Forgets the film an administrator chose for the disc (item): the engine's pick plays again."},
		{Op: c.Module + "." + CommandRank, Label: "Rank again", Method: "POST", Path: routes + "/analysis", Body: `{"secondOpinion":false}`,
			Description: fmt.Sprintf("An administrator has the engine work out which %s of the disc (item) is the film again.", unit)},
	}
	if c.Bluray {
		out = append(out, core.CommandSpec{Op: c.Module + "." + CommandCompare, Label: "Compare with libbluray", Method: "POST", Path: routes + "/analysis", Body: `{"secondOpinion":true}`,
			Description: "An administrator has a libbluray installed on the server compare its reading of the disc (item); the server's setting has to allow it."})
	}
	return out
}

// Items answers the server's sdui.items call for one of the module's sources.
func (a *App) Items(req sdui.ItemsRequest) (sdui.ItemsAnswer, error) {
	name, ok := strings.CutPrefix(req.Source.Type(), a.Module+".")
	if !ok || req.Viewer.User <= 0 {
		return sdui.ItemsAnswer{}, nil
	}
	known := false
	for _, n := range sourceNames {
		known = known || n == name
	}
	id, err := strconv.ParseInt(srcStr(req.Source, "item"), 10, 64)
	if !known || err != nil {
		return sdui.ItemsAnswer{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	query := ""
	switch name {
	case "feature", "scenes", "bonus", "menus":
		query = "page=1"
	}
	d, err := a.disc(ctx, host.On{User: req.Viewer.User}, id, query)
	if err != nil {
		if host.IsNotFound(err) || host.IsForbidden(err) {
			return sdui.ItemsAnswer{}, nil // another module's disc, or not this person's: no section
		}
		return sdui.ItemsAnswer{}, err
	}
	labels := a.Labels.Get(ctx, id)
	v := view{App: a, d: d, labels: labels, admin: req.Viewer.Admin}
	var all []sdui.Item
	switch name {
	case "refusal":
		all = v.refusal()
	case "why":
		all = v.why()
	case "copy":
		all = v.copy()
	case "episodes":
		all = v.episodes()
	case "versions":
		all = v.versions()
	default:
		if d.Scrambled || d.Page == nil {
			break // the refusal says why there is nothing to play
		}
		switch name {
		case "feature":
			all = v.feature()
		case "scenes":
			all = v.scenes()
		case "bonus":
			all = v.bonus()
		case "menus":
			all = v.menus()
		}
	}
	limit := req.Limit
	if lim := srcInt(req.Source, "limit"); lim > 0 && (limit <= 0 || lim < limit) {
		limit = lim
	}
	total := len(all)
	if req.Offset > 0 {
		if req.Offset >= len(all) {
			all = nil
		} else {
			all = all[req.Offset:]
		}
	}
	more := false
	if limit > 0 && len(all) > limit {
		all, more = all[:limit], true
	}
	return sdui.ItemsAnswer{Items: all, Total: total, More: more}, nil
}

// view is one disc's sections for one person.
type view struct {
	*App
	d      *Disc
	labels Labels
	admin  bool
}

func (v view) id() string { return strconv.FormatInt(v.d.ID, 10) }

func (v view) play(q string) string { return fmt.Sprintf("/play/%d?mode=%s", v.d.ID, q) }

// titleName is a title as a person reads it: its own name when it was given one, else "Title 3"
// or "Playlist 00800".
func (v view) titleName(ttn int, labels Labels) string {
	if l := strings.TrimSpace(labels.title(ttn)); l != "" {
		return l
	}
	if v.Bluray {
		return fmt.Sprintf("Playlist %05d", ttn)
	}
	return fmt.Sprintf("Title %d", ttn)
}

// featureSec is how long a title plays: the whole film when it runs through several titles.
func (v view) featureSec(ttn int, own float64) float64 {
	if rk := v.d.Ranking(); rk != nil && rk.Main == ttn && v.d.FeatureSec > 0 {
		return v.d.FeatureSec
	}
	return own
}

// lengthWords is a running time as the boards write it: "1 hr 52 min", "9 min", "40 sec".
func lengthWords(sec float64) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 60:
		return fmt.Sprintf("%d sec", int(math.Round(sec)))
	}
	m := int(math.Round(sec / 60))
	if h := m / 60; h > 0 {
		if m%60 == 0 {
			return fmt.Sprintf("%d hr", h)
		}
		return fmt.Sprintf("%d hr %d min", h, m%60)
	}
	return fmt.Sprintf("%d min", m)
}

func joinWords(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// fmtClock is a place in a film: "1:04:00", "4:00".
func fmtClock(sec float64) string {
	t := int(sec)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// feature: Feature Presentation. Play the film, and how many scenes it has.
func (v view) feature() []sdui.Item {
	m := v.d.Page.Main
	if m == nil || v.d.NoPlay {
		return nil
	}
	out := []sdui.Item{{ID: "play", Kind: "view", Title: "Play the film", Subtitle: lengthWords(v.featureSec(m.TTN, m.DurationSec)), Icon: "play", Route: v.play("title")}}
	if n := len(m.Chapters); n > 1 {
		out = append(out, sdui.Item{ID: "scenes", Kind: "view", Title: "Scenes", Subtitle: fmt.Sprintf("%d chapters", n), Icon: "list",
			Route: v.play(fmt.Sprintf("title&ttn=%d&chapter=1", m.TTN))})
	}
	return out
}

// scenes: the film's chapters as pictures, each playing from its start.
func (v view) scenes() []sdui.Item {
	m := v.d.Page.Main
	if m == nil || len(m.Chapters) < 2 || v.d.NoPlay {
		return nil
	}
	out := make([]sdui.Item, 0, len(m.Chapters))
	for _, c := range m.Chapters {
		name := strings.TrimSpace(c.Label)
		if name == "" {
			name = fmt.Sprintf("Scene %d", c.N)
		}
		out = append(out, sdui.Item{ID: fmt.Sprintf("scene-%d", c.N), Kind: "view", Title: name, Subtitle: fmtClock(c.Start),
			Route:  v.play(fmt.Sprintf("title&ttn=%d&chapter=%d", m.TTN, c.N)),
			Images: map[string]string{"backdrop": fmt.Sprintf("/api/items/%d/disc/thumb/%d/%d", v.d.ID, m.TTN, c.N)}})
	}
	return out
}

// bonus: the other titles worth watching, longest first, each played once.
func (v view) bonus() []sdui.Item {
	if v.d.NoPlay {
		return nil
	}
	out := make([]sdui.Item, 0, len(v.d.Page.Extras))
	for i, x := range v.d.Page.Extras {
		name := strings.TrimSpace(x.Label)
		if name == "" {
			name = fmt.Sprintf("Bonus feature %d", i+1)
		}
		out = append(out, sdui.Item{ID: fmt.Sprintf("bonus-%d", x.TTN), Kind: "view", Title: name, Icon: "video",
			Subtitle: joinWords(lengthWords(x.DurationSec), fmt.Sprintf("%s %d", strings.ToLower(v.Unit()), x.TTN)),
			Route:    v.play(fmt.Sprintf("title&ttn=%d", x.TTN))})
	}
	return out
}

// menus: shortcuts straight into the disc's own menus.
func (v view) menus() []sdui.Item {
	if v.d.NoPlay {
		return nil
	}
	out := make([]sdui.Item, 0, len(v.d.Page.Menus))
	for _, m := range v.d.Page.Menus {
		q := "menu&menu=" + m.Kind
		if m.VTS > 0 {
			q += fmt.Sprintf("&menuVts=%d", m.VTS)
		}
		out = append(out, sdui.Item{ID: "menu-" + m.Kind, Kind: "view", Title: m.Label, Icon: "disc", Route: v.play(q)})
	}
	return out
}

// episodes: a TV disc's episodes in viewing order.
func (v view) episodes() []sdui.Item {
	rk := v.d.Ranking()
	if rk == nil || v.d.Scrambled || v.d.NoPlay {
		return nil
	}
	out := make([]sdui.Item, 0, len(rk.Episodes))
	for _, e := range rk.Episodes {
		where := fmt.Sprintf("%s %d", strings.ToLower(v.Unit()), e.TTN)
		if e.Part {
			where = fmt.Sprintf("from %s of %s", fmtClock(e.StartSec), where)
		}
		out = append(out, sdui.Item{ID: fmt.Sprintf("episode-%d", e.Index), Kind: "view", Title: fmt.Sprintf("Episode %d", e.Index), Icon: "show",
			Subtitle: joinWords(lengthWords(e.DurationSec), where), Route: v.play(fmt.Sprintf("title&episode=%d", e.Index))})
	}
	return out
}

// versions: every other way to see the film than what Play plays now. With a chosen title, the
// engine's own pick is one of them.
func (v view) versions() []sdui.Item {
	rk := v.d.Ranking()
	if rk == nil || v.d.Scrambled || v.d.NoPlay {
		return nil
	}
	main := v.d.Main
	var out []sdui.Item
	for i, x := range rk.Versions {
		if x.Kind == "angle" && x.Main && x.TTN == main || x.Kind != "angle" && x.TTN == main {
			continue
		}
		label := strings.TrimSpace(x.Label)
		if label == "" {
			label = fmt.Sprintf("%s %d", v.Unit(), x.TTN)
		}
		label = strings.ToUpper(label[:1]) + label[1:]
		q := fmt.Sprintf("title&ttn=%d", x.TTN)
		if x.Kind == "angle" && x.Angle > 0 {
			q += fmt.Sprintf("&angle=%d", x.Angle)
		}
		out = append(out, sdui.Item{ID: fmt.Sprintf("version-%d", i), Kind: "view", Title: label, Icon: "movie", Subtitle: lengthWords(x.DurationSec), Route: v.play(q)})
	}
	return out
}

// classWords say what the engine took a title for, as the board writes it.
var classWords = map[string]string{
	"main": "The film", "part": "Part of the film", "alternate": "Another cut", "featurette": "A featurette", "trailer": "A trailer",
	"intro": "An intro", "episode": "An episode", "playAll": "Plays every episode", "duplicate": "A copy of another title", "menu": "A menu's background",
	"extra": "An extra", "short": "A short", "stub": "Too short to matter",
}

var levelWords = map[string]string{"high": "High", "medium": "Medium", "low": "Low"}

// why: why this title is the film, how sure the engine is, and the other candidates, each to play
// once. With a module from before rankings there is nothing to say.
func (v view) why() []sdui.Item {
	d := v.d
	rk := d.Ranking()
	if rk == nil {
		return nil
	}
	chosen := d.IsChosen()
	sure := fmt.Sprintf("%d%% sure", int(math.Round(rk.Confidence*100)))
	head := sdui.Item{ID: "why", Kind: "view", Icon: "info"}
	if chosen {
		head.Title = v.titleName(d.Main, v.labels) + " was chosen as the film"
		head.Subtitle = "An administrator chose it."
		if rk.Main >= 0 && rk.Main != d.Main {
			head.Subtitle = fmt.Sprintf("An administrator chose it; the engine picks %s (%s).", v.titleName(rk.Main, v.labels), sure)
		}
		head.Badges = []string{"Chosen"}
	} else {
		head.Title = v.titleName(rk.Main, v.labels) + " is the film"
		if rk.Main < 0 {
			head.Title = "No title looks like the film"
		}
		head.Subtitle = joinWords(rk.Why, sure)
		if w := levelWords[rk.Level]; w != "" {
			head.Badges = []string{w}
		}
	}
	if v.admin && chosen {
		head.Subtitle = joinWords(head.Subtitle, "Choose to use the engine's pick again.")
		head.Action = &sdui.Action{Type: core.ActCommand, Op: v.Module + "." + CommandClearChoice, Item: v.id()}
	}
	out := []sdui.Item{head}
	for _, c := range rk.Candidates {
		badge := "Extra"
		switch {
		case c.TTN == d.Main:
			badge = "Film"
			if chosen {
				badge = "Chosen"
			}
		case c.TTN == rk.Main:
			badge = "Pick"
		case c.Class == "part":
			badge = "Part"
		case c.Class == "alternate" || rk.isVersion(c.TTN):
			badge = "Cut"
		case c.Class == "duplicate":
			badge = "Copy"
		}
		chapters := ""
		if c.Chapters > 0 {
			chapters = fmt.Sprintf("%d chapters", c.Chapters)
		}
		row := sdui.Item{ID: fmt.Sprintf("candidate-%d", c.TTN), Kind: "view", Title: joinWords(v.titleName(c.TTN, v.labels), lengthWords(v.featureSec(c.TTN, c.DurationSec))),
			Subtitle: joinWords(classWords[c.Class], chapters), Badges: []string{badge}}
		if c.TTN == rk.Main && c.TTN != d.Main {
			row.Subtitle = joinWords("The engine's pick", chapters)
		}
		switch {
		case v.admin && c.TTN != d.Main && !d.Scrambled && badge != "Part":
			// A part of the film is no film to choose. An administrator makes it the film, for
			// everyone; others play it once.
			row.Subtitle = joinWords("Choose to make it the film", row.Subtitle)
			row.Action = &sdui.Action{Type: core.ActCommand, Op: v.Module + "." + CommandChoose, Item: v.id(), Params: map[string]any{"ttn": c.TTN}}
		case !d.Scrambled && !d.NoPlay:
			row.Route = v.play(fmt.Sprintf("title&ttn=%d", c.TTN))
		}
		out = append(out, row)
	}
	if v.admin {
		out = append(out, sdui.Item{ID: "rank", Kind: "view", Title: "Rank again", Subtitle: "Work out which " + strings.ToLower(v.Unit()) + " is the film again", Icon: "sparkles",
			Action: &sdui.Action{Type: core.ActCommand, Op: v.Module + "." + CommandRank, Item: v.id()}})
		if v.Bluray && !d.Scrambled {
			if st := secondState(d.Second); st.Enabled && st.Available {
				out = append(out, sdui.Item{ID: "compare", Kind: "view", Title: "Compare with libbluray", Subtitle: joinWords("libbluray "+st.Version, "on this server"), Icon: "check",
					Action: &sdui.Action{Type: core.ActCommand, Op: v.Module + "." + CommandCompare, Item: v.id()}})
			}
		}
	}
	if rk.Decoys > 0 {
		out = append(out, sdui.Item{ID: "decoys", Kind: "view", Title: fmt.Sprintf("%d decoy %ss left out", rk.Decoys, strings.ToLower(v.Unit())), Icon: "warning", Badges: []string{"Decoy"},
			Subtitle: "They repeat or scramble the film's scenes, or point at video that isn't there."})
	}
	return out
}

// copy: what was wrong with this copy of the disc and what was mended, and for an administrator
// the way to check it.
func (v view) copy() []sdui.Item {
	var out []sdui.Item
	if rk := v.d.Ranking(); rk != nil {
		for i, r := range rk.Repairs {
			out = append(out, sdui.Item{ID: fmt.Sprintf("repair-%d", i), Kind: "view", Title: r, Icon: "check", Badges: []string{"Mended"}})
		}
		if more := rk.RepairCnt - len(rk.Repairs); more > 0 {
			out = append(out, sdui.Item{ID: "repairs-more", Kind: "view", Title: fmt.Sprintf("%d more mended", more), Icon: "check"})
		}
		for i, x := range rk.Issues {
			badge := "Note"
			if x.Severity == "error" {
				badge = "Won't play"
			}
			title := x.What
			if x.TTN >= 0 {
				title = joinWords(x.What, fmt.Sprintf("%s %d", strings.ToLower(v.Unit()), x.TTN))
			}
			out = append(out, sdui.Item{ID: fmt.Sprintf("issue-%d", i), Kind: "view", Title: title, Icon: "warning", Badges: []string{badge}})
		}
		if rk.Padded > 0 {
			out = append(out, sdui.Item{ID: "padded", Kind: "view", Icon: "warning", Badges: []string{"Unreadable"},
				Title: fmt.Sprintf("%d unreadable sectors were read as blank", rk.Padded), Subtitle: "While reading the disc's index. The film may skip there."})
		}
		if so := rk.Second; so != nil {
			row := sdui.Item{ID: "second-opinion", Kind: "view", Icon: "info"}
			switch {
			case so.Note != "":
				row.Title, row.Subtitle = "No second opinion", so.Note
			case so.Agrees:
				row.Title, row.Badges = so.Source+" agrees", []string{"Agrees"}
			default:
				row.Title, row.Badges = so.Source+" reads it differently", []string{"Differs"}
				row.Subtitle = strings.Join(so.Differences, "; ")
			}
			out = append(out, row)
		}
	}
	if v.admin {
		out = append(out, sdui.Item{ID: "diagnose", Kind: "view", Title: "Check this disc", Subtitle: "Diagnose…", Icon: "settings", Route: fmt.Sprintf("/settings/diagnoser?item=%d", v.d.ID)})
	}
	return out
}

// refusal: an encrypted disc gets a plain message: what happened, why, and what is still known
// about it.
func (v view) refusal() []sdui.Item {
	if !v.d.Scrambled {
		return nil
	}
	name := strings.TrimSpace(v.d.Label)
	if name == "" {
		name = v.d.Title
	}
	out := []sdui.Item{
		{ID: "refused", Kind: "view", Icon: "warning", Title: "This disc can't be played", Badges: []string{"Encrypted"},
			Subtitle: "It is copy-protected. Nothing was changed and nothing was copied: nothing here unlocks copy protection."},
		{ID: "disc", Kind: "view", Icon: "disc", Title: joinWords("Disc", name), Subtitle: "Result: Encrypted"},
	}
	if rk := v.d.Ranking(); rk != nil && rk.Main >= 0 {
		out = append(out, sdui.Item{ID: "film", Kind: "view", Icon: "info", Title: joinWords(v.titleName(rk.Main, Labels{})+" looks like the film", lengthWords(v.d.RuntimeSec)),
			Subtitle: "Read from the disc's index, which is never encrypted."})
	}
	return out
}

func srcStr(s sdui.Source, k string) string {
	switch v := s[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

func srcInt(s sdui.Source, k string) int {
	switch v := s[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// secondOpinion is whether a libbluray can be asked, as the host API says (administrators only).
type secondOpinion struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Reason    string `json:"reason"`
}

func secondState(raw []byte) secondOpinion {
	var st secondOpinion
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}
