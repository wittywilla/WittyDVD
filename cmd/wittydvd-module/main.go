// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyDVD - DVD images with their real menus, chapters, angles and subtitles.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Command wittydvd-module is WittyDVD as a helper process, one for each open disc, so
// a new version of the engine can reach a server without a new server. See WittyModule
// for the protocol and package wire for what is said over it.
package main

import (
	"github.com/wittywilla/wittydvd"
	"github.com/wittywilla/wittydvd/diagnose"
	"github.com/wittywilla/wittydvd/wire"
	"github.com/wittywilla/wittymodule"
)

func main() {
	h := &diagnose.Handler{Next: new(wire.Helper).Handle}
	wittymodule.Main(wittydvd.ManifestJSON, h.Handle)
}
