// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"os"
	"testing"

	"github.com/wittywilla/wittymodule"
)

// integration.json is what a server reads strictly at install: it passes WittyModule's own check,
// and every command sdui.json declares is one of its routes.
func TestIntegrationFile(t *testing.T) {
	m, err := wittymodule.ParseManifest(ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(wittymodule.IntegrationFile)
	if err != nil {
		t.Fatal(err)
	}
	in, err := wittymodule.ParseIntegration(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Validate(m); err != nil {
		t.Error(err)
	}
	ops := map[string]bool{}
	for _, op := range in.SDUI.Ops {
		ops[op.ID] = true
	}
	for _, c := range UI().Commands {
		if !ops[c.Op] {
			t.Errorf("%s isn't among integration.json's ops", c.Op)
		}
	}
}
