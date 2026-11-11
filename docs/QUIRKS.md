# Damaged, protected and odd discs

What odd DVD-Video discs do with the format, and how WittyDVD's reading and title mapping handle
each. It's for people working on the engine. How titles are scored is in
[TITLE-HEURISTICS.md](TITLE-HEURISTICS.md). What the format can do in the first place (commands,
stills, highlights, sub-pictures) is in the server's
[DVD quirks](https://github.com/wittywilla/WittyMediaServer/blob/master/docs/DVD-QUIRKS.md).

- [How this was checked](#how-this-was-checked)
- [Decryption](#decryption)
- [Structure protection](#structure-protection)
- [Features that only the commands know](#features-that-only-the-commands-know)
- [TV discs](#tv-discs)
- [Angles, branching and cuts](#angles-branching-and-cuts)
- [Damaged index files](#damaged-index-files)
- [Unreadable sectors](#unreadable-sectors)
- [Copies: folders and images](#copies-folders-and-images)
- [Hostile index files](#hostile-index-files)
- [Not handled](#not-handled)

## How this was checked

**Without a disc.** Nothing below has been run against physical media or a commercial disc image.
Every case is a synthetic disc written by a generator in the test code, read back through the same
code a real disc goes through:

- `fixture_test.go` writes the index files byte by byte: `TT_SRPT`, `PTL_MAIT`, the VMGM and VTSM
  `PGCI_UT`, the first-play PGC, `VTS_PTT_SRPT`, the `PGCIT` with program maps, cell playback and
  cell position tables and pre, post and cell commands, `VTS_TMAPTI`, `C_ADT` and `VOBU_ADMAP`.
- `synth_test.go` writes the VOBs as sparse files that hold only NAV packs (the VOB and cell id, how
  far the VOBU runs, a menu's buttons), and lays the disc out as a `VIDEO_TS` folder or as a UDF,
  ISO 9660 or bridge image. A two-hour "film" is a few kilobytes on disk.
- `scenarios_test.go` has one disc per pattern and says what the engine must make of it.
  `TestRankingInvariants` renumbers the titles and throws in random decoys, and the pick, the
  episodes and the feature must not change.
- `robust_test.go` damages the index files and the VOBs.
- `fuzz_entry_test.go` has a fuzz target for each way bytes get in, seeded with the synthetic discs.

The layouts come from the public DVD-Video structure references and from public descriptions of
what protected discs look like. A real disc may do something none of them describes. The test named
in each row is what there is to go on. Three scenarios combine patterns: a linked film among forty
decoys with a table gone from each copy of the index, a chained film among fake title sets, and a
TV disc among sixty decoys.

## Decryption

| Quirk | WittyDVD |
|---|---|
| **CSS scrambling** | **Refused.** Scrambled packs are detected (`PackScrambled`) and the disc stops with a message. Nothing here decrypts anything, and no decryption library is linked, bundled or loaded, including optional ones the user may have installed (libdvdcss). That is the project's stated policy: see the README and NOTICE. The server makes its own check, whatever a helper says |
| **Region codes, RCE** | Not a protection WittyDVD has to get round: the region is a register the disc reads, set to a region the disc allows. See the server's quirks page |

Everything below works from what is never scrambled: the index files, the navigation commands, and
NAV packs.

## Structure protection

Discs that guard their film by making the index misleading (ARccOS, RipGuard and similar layouts).
A player follows the disc's menus and never meets the junk. A program that reads the index does.

| Pattern | What it is | WittyDVD | Test |
|---|---|---|---|
| **Dozens of titles of the film's length** | Up to 99 titles, one real | Every title is scored; copies of the film in the same order are duplicates, the rest are decoys by one of the signs below | `99 titles, 98 decoys` |
| **Shuffled cells** | A title plays the film's cells in another order, sometimes with a few minutes added so it has more video than the film | `shuffled`: it holds 80% of another title's video and doesn't keep that title's order. Its credit for video the other title plays in order is taken back | `shuffled-cell decoy` |
| **Scrambled titles made of several titles' cells** | A title plays every cell on the disc in no order: more distinct video than anything | `scrambled`: a quarter or more of its steps jump back on the disc. Loses the credit for all video other titles play in order | `TestRankingInvariants` (found it) |
| **Repeated cells** | A few cells played again and again for hours | Each sector counts once; the repeats cost points; half the play time in repeats is a decoy | `99 titles, 98 decoys` |
| **Cells outside the VOB** | Cells past the end of the title VOBs, or that end before they start | Not counted; cost points; half the cells is a decoy. The navigator passes over them | `99 titles, 98 decoys`, `TestJunkCells` |
| **Cells in the unreadable zone** | Cells inside the VOB that point at sectors pressed unreadable. They look like video | The title set's cell address table doesn't hold them (`unlisted`): not counted. The table is only believed when it agrees with at least one cell of one chain | `titles in the unreadable zone` |
| **Zero-length and sub-second cells** | Hundreds of cells of no length, sometimes with real ones hidden among them | `cellSpam` | `zero-length cell spam` |
| **Impossible cell times** | A cell that claims nine hours | `absurd`: more than four hours isn't counted | `zero-length cell spam` |
| **Hollow cells** | Cells that claim hours and are a few sectors long | `hollow`: under half a sector a second for a minute or more isn't counted | `hollow cells` |
| **Overlapping cells** | Cells that lie over each other, so the same video adds up to hours | Each sector counts once; `overlap` | `overlapping cells` |
| **Hundreds of fake program chains** | Titles that chain through program chain after program chain of junk | Chains are followed with a limit (512 chains, 4096 cells to a title), and judged like any title | `five hundred fake program chains`, `TestStressDiscIsBounded` |
| **Chains that link in circles** | `next` links or commands that lead back | `loop`; the navigator and the VM stop after a bounded number of hops and steps, and all dry runs of an analysis share one budget | `TestLoopingChainAndBadChapters`, `TestStressDiscIsBounded` |
| **A long title with no soundtrack** | A clean-looking title, a little longer than the film, whose program chain turns on no audio stream | `noAudioStreams`: it loses 20 points where other titles of its title set have sound | `a longer title with no soundtrack` |
| **Fake title sets** | Title sets that are an index with no video files, each claiming a longer film | `noVideo`: on an opened disc none of their time is counted. From the index alone they can't be told | `fake title sets` |
| **A longer bait title and a guarded film** | The film's commands bounce a player that didn't come through the menu; a longer title is unguarded | The first menu's default button is pressed in a dry run and what it plays gains most (`menuPlay`); titles nothing leads to lose (`unreachable`) | `guarded feature behind the Play button`, `TestMenuPlayButton` |
| **Mismatched IFO and BUP** | The two copies of an index differ, one of them wrong on purpose or by damage | See [Damaged index files](#damaged-index-files) | `TestCorruptIFOGoodBUP`, `TestTableByTableFallback` |

## Features that only the commands know

| Pattern | What it is | WittyDVD | Test |
|---|---|---|---|
| **A film linked together by commands** | The title's chapter table names a short chain; `LinkPGCN` commands lead on, out of order, through the chains that hold the film | The title is dry-run in the disc's VM and every cell it shows is noted. That reading is kept when it shows more real video (`assembled`), and `Ranking.Feature` lists the cells in the order played | `feature linked together by commands` |
| **A film cut into several titles** | Each title ends with a jump to the next; no title is the film | The chain is followed; the first title is credited with the rest (`continues`), the others are `part`, and `Feature` runs through all of them. Decoys that scramble the whole chain's cells are compared with all the parts together | `feature cut into chained titles` |
| **A guard in the pre-commands** | The title jumps to a menu unless a register was set | Noted (`guarded`); the title is read from its chapter table. `Nav.PlayTitle` enters it without the guard when the viewer picks it | `guarded feature behind the Play button` |
| **Dispatcher chains** | Buttons set a register and link to a chain whose commands jump by its value | The static reading ignores compares, so every title a dispatcher can jump to counts as reachable; the dry run of each button of the first menu finds which button plays what | `TestMenuPlayButton` |
| **Titles nothing leads to** | Left on the disc, with no command or button naming them | `unreachable`, only when every menu's buttons were read | `TestMenuPlayButton`, `TestUnreadableMenuSaysNothing` |

## TV discs

| Pattern | WittyDVD | Test |
|---|---|---|
| **Episodes as titles, and a play-all title** | Titles of similar length are episodes; the title that holds all their video is play-all and gives their order, whatever their numbers | `TV disc with play all` |
| **Episodes as titles, no play-all** | Three or more are needed; order is where they lie on the disc, not their numbers | `TV disc without play all` |
| **Play-all only, a chain per episode** | The title is split at its program chains; each episode has a start time and a first chapter | `play all only, a chain per episode` |
| **Play-all only, one chain** | Split where the VOB id changes, with less confidence | `play all only, one chain` |
| **Play-all that plays each episode as one cell** | Video is compared by sectors, not by cells, so it still matches | `play all of one cell per episode` |

## Angles, branching and cuts

| Pattern | WittyDVD | Test |
|---|---|---|
| **Angle blocks** | Each angle is read as its own version, with its own length; `TT_SRPT`'s angle count is checked against the blocks | `three camera angles` |
| **Interleaved cells** | A cell's sectors are the VOBUs whose NAV pack names it (`CellRanges`), so another angle or branch never plays in between | `TestBadSectorsArePadded`, `TestSameFromFolderAndImages` |
| **Two cuts as two titles that share cells** | The cut with the most video is main; the other is `alternate` and listed in `Versions`. A longer cut keeps the shorter one's order, which is what tells it from a shuffled copy | `two cuts sharing cells` |
| **Cuts chosen by parental level** | The title is dry-run at each level; levels that play the same cells are one version | `parental block` |
| **Seamless flags, layer breaks** | Carried on each cell (`Seamless`, `SeamlessAngle`, `STCDiscontinuity`); they change how a player reads, not which cells a title has. Nothing here depends on them | none |

## Damaged index files

| Damage | WittyDVD | Test |
|---|---|---|
| **An IFO that is missing, zeroed or cut short** | The BUP is read. A read that fails part-way keeps the tables before the fault | `TestOpenFallsBackToBUP`, `TestTruncatedIndexFiles` |
| **One table damaged in the IFO, another in the BUP** | Tables are read one by one, and each is taken from the copy where it is whole. `Disc.IFOReports` says which | `TestTableByTableFallback` |
| **A table gone from both copies** | The chapter table is rebuilt from the program chains | `TestChapterTableRebuilt` |
| **Cells that point at sectors that don't exist** | Mended from the cell address table by VOB and cell id, all or nothing for a chain | `TestCellsMendedFromAddressTable` |
| **Cells with no play time** | Worked out from the time map, or by sectors from the chain's play time; the chapters decision says so | `TestCellTimesEstimated` |
| **A header with no VOB size** | Taken from the cell address table | `TestVOBSizeFromAddressTable` |
| **A chapter table that names nothing** | Each program becomes a chapter | `chapter table points nowhere` |
| **A program chain that can't be read** | Its row keeps its number with an empty chain, so the other chains keep theirs | `FuzzParseVTS` |

Everything mended or noticed is in `Disc.Repairs`, and the diagnose report lists it.

## Unreadable sectors

| Case | WittyDVD | Test |
|---|---|---|
| **A sector that can't be read** | The read is tried twice more, then done sector by sector; what can't be read comes back as zeros and is counted (`Opened.ReadStats`). A read never fails for a bad sector | `TestBadSectorsArePadded` |
| **A zone of thousands** | After 32 unreadable sectors in a row the rest of the request is padded without asking the drive | `TestDeadZoneIsBounded` |
| **A NAV pack lost inside an interleaved cell** | The scan carries on from the next VOBU, from the IFO's VOBU map or by reading on; the VOBU in between is left out | `TestBadSectorsArePadded` |
| **A menu that can't be read** | No button is known, so nothing is called unreachable and no Play button is credited | `TestUnreadableMenuSaysNothing` |
| **Zero-filled sectors in an image** | The same thing, already done by whatever made the image: a zeroed sector is no NAV pack | as above |

These cover what WittyDVD reads itself: NAV packs, menus, sub-pictures. The video goes to the
converter as byte ranges of the files, read by the server.

## Copies: folders and images

A disc reads the same whatever it was copied to (`TestSameFromFolderAndImages`,
`TestImageOverFourGigabytes`): the ranking, the sectors of each cell and the bytes at them are
compared between

- a folder holding `VIDEO_TS`, the `VIDEO_TS` folder itself, and one whose names are in lower case;
- title VOBs split into `VTS_nn_1.VOB` to `VTS_nn_9.VOB` at any size, or kept as one file;
- a UDF image, an ISO 9660 image, and a UDF/ISO 9660 bridge image as a pressed disc has;
- files stored in several extents, as files over 4 GiB are in ISO 9660 and files over 1 GiB in UDF;
- an image over 4 GiB, with its files past what 32 bits of byte offset reach.

A folder that holds `VIDEO_TS.IFO` directly is opened whatever it is called.

## Hostile index files

An index file is input from a stranger.

- Every table read is bounds-checked, with arithmetic that can't overflow on 32-bit builds.
- Nothing is allocated from a length the file states without the file's own size bounding it. One
  budget covers all the index files of a disc: 32768 program chains and 400000 cells. Rows of a
  PGCIT that point at the same chain share it.
- A title is never followed past 4096 cells or 512 chains, the VM stops after 10000 commands, the
  navigator after 1000 hops, and the dry runs of an analysis share a million of both.
- Comparing titles is linear in their cells after grouping titles that play the same thing, with a
  budget; a disc of 99 titles and 2941 program chains opens in about 17 ms and ranks in about 65 ms
  on a laptop (`BenchmarkOpenStressDisc`, `BenchmarkAnalyzeStressDisc`), using about 85 MiB of
  short-lived memory in all.
- The fuzz targets hold all of this to no panic, no endless loop and answers that can be written as
  JSON.

A disc at the budget's limit makes a structure too large for the helper's answer to `open` (16 MiB),
and the server then refuses it. The stress disc's is 3.7 MiB.

## Not handled

- **Decryption of any kind.** See [Decryption](#decryption).
- **Telling a decoy made of real video in a believable order from a film** by structure alone. The
  disc's own program is the only evidence, and `MainTitle`, which reads only the index, has the
  least of it. See the limits in [TITLE-HEURISTICS.md](TITLE-HEURISTICS.md#limits).
- **Menus in every language unit and of any length.** Buttons are read from every menu cell up to
  256 VOBUs each and 8192 in all; past that nothing is called unreachable.
- **Register values a menu sets before the film.** Titles are dry-run from a cold start. A film
  whose commands choose between chains by a register only its menu sets is read by its default
  path.
- **Repairing video.** Unreadable sectors inside a title are the converter's to deal with; WittyDVD
  only says how many it met in what it read.
