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
	// The helper answers neither sdui.items nor settings.changed.
	if ui := UI(); len(ui.Sources) != 0 || len(ui.Settings) != 0 {
		t.Error("UI() declares sources or settings: the helper must answer sdui.items or settings.changed first")
	}
}
