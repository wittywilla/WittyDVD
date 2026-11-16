// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package commands is the disc module's own commands, as its sdui.json declares them. It is a
// package of its own, with no networking in it, so the engine's root package (and through it
// WittyMediaPlayer's helper, which must not contain network code) can describe them without
// linking the service in package api, which answers them.
package commands

import (
	"fmt"
	"strings"

	core "github.com/wittywilla/wittymodule/sdui/core"
)

// Commands, after "<module>.": its own routes, which check again who is asking.
const (
	Choose      = "choose"      // PUT /discs/{item}/choice {"ttn"}
	ClearChoice = "clearChoice" // DELETE /discs/{item}/choice
	Rank        = "rank"        // POST /discs/{item}/analysis
	Compare     = "compare"     // POST /discs/{item}/analysis {"secondOpinion":true}, Blu-rays
)

// Unit is what the disc calls its titles.
func Unit(bluray bool) string {
	if bluray {
		return "Playlist"
	}
	return "Title"
}

// List is the commands of module (wittydvd or wittybluray); bluray adds the second opinion.
func List(module string, bluray bool) []core.CommandSpec {
	routes := "/api/modules/" + module + "/discs/{item}"
	unit := strings.ToLower(Unit(bluray))
	out := []core.CommandSpec{
		{Op: module + "." + Choose, Label: "Make it the film", Method: "PUT", Path: routes + "/choice", Body: `{"ttn":"{ttn}"}`,
			Params:      []core.CommandParam{{Name: "ttn", Type: "int", Label: Unit(bluray), Required: true}},
			Description: fmt.Sprintf("An administrator makes params.ttn (a %s) the disc's film (item), for everyone, instead of the engine's pick.", unit)},
		{Op: module + "." + ClearChoice, Label: "Use the engine's pick", Method: "DELETE", Path: routes + "/choice",
			Description: "Forgets the film an administrator chose for the disc (item): the engine's pick plays again."},
		{Op: module + "." + Rank, Label: "Rank again", Method: "POST", Path: routes + "/analysis", Body: `{"secondOpinion":false}`,
			Description: fmt.Sprintf("An administrator has the engine work out which %s of the disc (item) is the film again.", unit)},
	}
	if bluray {
		out = append(out, core.CommandSpec{Op: module + "." + Compare, Label: "Compare with libbluray", Method: "POST", Path: routes + "/analysis", Body: `{"secondOpinion":true}`,
			Description: "An administrator has a libbluray installed on the server compare its reading of the disc (item); the server's setting has to allow it."})
	}
	return out
}
