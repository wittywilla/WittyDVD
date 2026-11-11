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

// wittydvd-player is WittyDVD as a helper for WittyMediaPlayer: a program of its own, so it can be
// updated without a new player, and a damaged disc can only take this down with it.
package main

import (
	"encoding/json"
	"errors"

	"github.com/wittywilla/wittydisc/session"
	"github.com/wittywilla/wittydvd/wittydvdplayer"
	"github.com/wittywilla/wittymodule"
)

func main() {
	h := wittydvdplayer.NewHost()
	wittymodule.Main(wittydvdplayer.Manifest, func(method string, params json.RawMessage) (any, []byte, error) {
		res, blob, err := h.Handle(method, params)
		var r *session.RefusalError
		if errors.As(err, &r) {
			return nil, nil, &wittymodule.CallError{Code: r.Code, Message: r.Message}
		}
		return res, blob, err
	})
}
