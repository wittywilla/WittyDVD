// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/sdui/sduitest"
)

// sdui.json is UI(), and UI() passes the kit's checks. After changing UI():
// go test -run TestSDUIFileIsCurrent -update-sdui
func TestSDUIFileIsCurrent(t *testing.T) {
	sduitest.Golden(t, UI(), sdui.File)
}

// The file belongs to this module, and the manifest says it must carry one.
func TestSDUIMatchesTheManifest(t *testing.T) {
	m, err := wittymodule.ParseManifest(ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != ModuleID || UI().Module != m.ID || UI().Theme.Scope != m.ID {
		t.Errorf("manifest %q, sdui.json module %q, scope %q: all three must be the same", m.ID, UI().Module, UI().Theme.Scope)
	}
	if m.API < wittymodule.SDUIFromAPI {
		t.Errorf("api %d: the manifest should be built for module API %d or newer", m.API, wittymodule.SDUIFromAPI)
	}
	// Its sources are about the server's copy of a disc, so the server answers them; the
	// helper reads no settings of its own.
	ui := UI()
	if len(ui.Settings) != 0 {
		t.Error("UI() declares settings: the helper must answer settings.changed first")
	}
	for _, s := range ui.Sources {
		if len(s.PageKinds) != 1 || s.PageKinds[0] != "detail" || len(s.Params) == 0 || s.Params[0].Name != "item" {
			t.Errorf("%s: a disc's section takes its page's title, on a title's page only", s.Type)
		}
	}
	if len(ui.Inserts) != len(ui.Sources) {
		t.Errorf("%d sections for %d sources", len(ui.Inserts), len(ui.Sources))
	}
}
