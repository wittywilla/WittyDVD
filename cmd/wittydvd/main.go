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

// Command wittydvd inspects DVD-Video images and VIDEO_TS folders with the
// WittyDVD engine: titles, menus and languages, the disc's program, a
// simulated run through its menus, and the menu artwork. It reads nothing
// but the disc and writes nothing but what you ask for, so its output is
// safe to attach to a bug report.
package main

import (
	"fmt"
	"os"

	"github.com/wittywilla/wittydvd/cli"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "disc":
			os.Exit(cli.Disc(os.Args[2:]))
		case "info", "walk", "crawl", "pgc", "render":
			os.Exit(cli.DVD(os.Args[1:]))
		case "version", "--version", "-v":
			fmt.Println("wittydvd", version)
			return
		}
	}
	fmt.Fprint(os.Stderr, `wittydvd `+version+`: inspect DVD-Video images and VIDEO_TS folders

  wittydvd info    <image>              titles, menus, languages, first-play program
  wittydvd walk    <image> [button...]  simulate inserting the disc and pressing buttons
  wittydvd crawl   <image>              press every button of every menu; report dead ends
  wittydvd pgc     <image> <domain> <vts> <pgcn>   the commands of one program chain
  wittydvd render  <image> <out-dir>    save the first menu's highlight images
  wittydvd disc ls <image>              list the files inside a disc image

<image> is an .iso/.img file or a VIDEO_TS folder. Scrambled (CSS) discs are refused.
`)
	os.Exit(2)
}
