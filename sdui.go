// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittydvd

import "github.com/wittywilla/wittymodule/sdui"

// ModuleID is this module's ID, the same as module.json's.
const ModuleID = "wittydvd"

// UI is WittyDVD's part of every app (module API 4, WittyMediaServer docs/SDUI-MODULES.md),
// written to sdui.json beside module.json. It is only the theme scope, on purpose:
//
//   - A disc's page, its "Why this title", episodes and versions, and the player with the
//     disc's own menus are the server's, drawn from what this module's helper answers, so
//     a page or menu entry here would only show them twice.
//   - The helper reads no settings of its own: region, parental level and languages come
//     with each call, from the server's Playback & discs settings.
//   - It answers no data source: a disc is an item of the server's libraries already.
//
// The scope still names the module for the theme editor.
func UI() *sdui.Module {
	return sdui.New(ModuleID, "DVDs")
}
