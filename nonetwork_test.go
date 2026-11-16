// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The engine itself has no network code: WittyMediaPlayer's helper links it, and that helper
// contains none. The module's service (package api) is what talks to the server.
func TestEngineHasNoNetworkCode(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		cmd := exec.Command("go", "list", "-deps", ".", "./commands")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Skip("go list:", err)
		}
		for _, pkg := range strings.Fields(string(out)) {
			switch {
			case pkg == "net/url" || pkg == "net/netip": // text only: WittyModule reads addresses in a manifest
			case pkg == "net" || strings.HasPrefix(pkg, "net/"), pkg == "crypto/tls", pkg == "plugin",
				pkg == "github.com/wittywilla/wittydvd/api":
				t.Errorf("%s: the engine imports %s", goos, pkg)
			}
		}
	}
}
