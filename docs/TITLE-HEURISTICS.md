# Title heuristics

How WittyDVD decides which title is the film, which titles are a TV disc's episodes, and which are
decoys or extras. The code is in `heuristics.go`, `episodes.go` and `analyze_open.go`.

**This does not bypass CSS.** The engine reads only what's unencrypted on every DVD: the IFO and BUP
index files, PGC chains, cell timings and sector addresses, VOB sizes, the disc's navigation
commands and the NAV packs that carry menu buttons. It doesn't decrypt anything, read or derive title
keys, or try to. A CSS-scrambled disc is still refused, whatever its titles score. A disc whose menus
are scrambled simply has no menu signals.

## API

| Call | What it gives |
|---|---|
| `Disc.MainTitle() *TitleSummary` | The best-scoring title from the IFOs alone. Same signature as before |
| `Disc.RankTitles() []Candidate` | Every title, best first |
| `Disc.Analyze(RankOptions) *Ranking` | Candidates plus the main title, episodes, play-all title, decoys and warnings |
| `Disc.Episodes() []Episode` | A TV disc's episodes in viewing order, or nil |
| `Disc.Explain() string` | The ranking in words, for logs and the command line |
| `Opened.Analyze() *Ranking` | Like `Disc.Analyze`, plus the real VOB file sizes and the first menu's buttons |

A `Candidate` holds `TitleNum`, `VTS`, `VTSTitle`, `PGC` (the title's first PGC), `Duration`,
`Distinct` (each readable cell counted once), `Chapters`, `Cells`, `Score`, `Class`, `Flags` and
`Reasons`. `Flags` serialises as a list of names: `main`, `decoy`, `menuTarget`, `menuPlay`,
`firstPlay`, `intro`, `episode`, `playAll`, `duplicate`, `shuffled`, `outOfRange`, `repeats`,
`cellSpam`, `loop`, `absurd`, `badChapters`, `fromBackup` and `alternate`. Every number added to a
score comes with a reason that says why.

## Signals and weights

Scores are counted in minutes of distinct video, so a weight of 15 is worth a quarter of an hour of
unique cells. The weights are in `DefaultWeights`, and `RankOptions.Weights` can override them.

| Signal | Weight | What it looks at |
|---|---|---|
| Distinct video | +1 per minute | Play time of readable cells, each cell counted once |
| Cells outside the VOB | −10, then −20 × their share | Cells that start past the title VOB the IFO describes, or end before they start |
| Repeats | −30 × the share of time spent on repeats | Cells played again within the title |
| Out of disc order | −5, then −35 × the share of steps that jump back | Steps from one cell to the next that go backwards on the disc |
| Shuffled copy | −25 | Holds at least 80% of another title's cells, in a scrambled order |
| Duplicate | −0.5 | Same cells in the same order as a lower-numbered title. Only breaks the tie |
| Cell spam | −30 | At least 10 cells under a second making up half the title, or 20 zero-length cells |
| Absurd cell | −20 | A cell that claims more than four hours. Its time isn't counted |
| Looping chain | −15 | The title's next-PGC links lead back to a PGC already visited |
| Broken chapters | −10 | The chapter table names a PGC or program that doesn't exist |
| Even chapters | +3 | Three or more chapters, averaging 1 to 20 minutes, without wild swings in length |
| Audio streams | +2 | Its title set has the most audio streams, where they differ |
| Subtitle streams | +1 | Its title set has the most subtitle streams, where they differ |
| Largest title set | +5 | Its title set holds the most video, on discs with more than one |
| First play | +15 | A dry run of the disc's own first-play program in the VM, with fixed random numbers, reaches it without a button press |
| Menu reference | +4 | A `JumpTT`, `JumpVTS_TT` or `JumpVTS_PTT` in a first-play or menu PGC names it. This is read statically, without running anything |
| Menu Play button | +25 | The first menu's default button plays it (`Opened.Analyze` only) |

Some things add a reason but no points:

- Cells that run past the end of the VOB files: the copy may be incomplete.
- A title set read from its `.BUP` copy.
- Short titles that first play passes through on the way.

The sizes of the real VOB files never change a score. That way `MainTitle` gives the same answer for
an image and for a copy of its index files.

**IFO against BUP.** `Open` reads both copies. If only one parses, it uses that one. If both parse
and differ, it counts problems in each: chapters that point nowhere, cells outside the VOB, missing
tables. The copy with fewer problems wins, and the IFO wins a tie. `Disc.IFOReports` records which
copy was used and why.

## Classes

Each title gets one class: `main`, `playAll`, `episode`, `decoy`, `duplicate` or `alternate`.
`alternate` means at least 60% of its cells are the main title's, like another cut of the film.
Titles that are none of those are sorted by length:

| Class | Length |
|---|---|
| `intro` | A short title that first play passes through |
| `stub` | Under 15 seconds |
| `trailer` | Under 4 minutes |
| `featurette` | Under 45 minutes |
| `extra` | Anything longer |

A title is a **decoy** if any of these is true:

- It's a shuffled copy of another title.
- It has cell spam, an absurd cell or a looping chain.
- At least half its cells are outside the VOB.
- At least half its play time repeats cells it has already played.

## Episodes

A disc counts as a TV disc when it has at least three clean titles, each 8 to 75 minutes long and all
within ±15% of their median length, and none of them shares more than half its cells with another.
Two such titles are enough when a play-all title is also found.

A **play-all** title is in the same title set, holds at least 90% of each episode's cells, and lasts
within 15% of the episodes' total.

Episodes are ordered by where they start inside the play-all title. Without a play-all title, they're
ordered by title set and first sector, and then by title number.

## Limits

- No physical disc has been through this yet. The fixtures are synthetic IFOs built byte by byte in
  `fixture_test.go`. They cover a simple movie, a 99-title decoy disc, a shuffled-cell decoy, a TV
  disc with four episodes and a play-all title, a corrupt IFO with a good BUP, a first play and a
  menu that pick a less obvious title, and zero-length cell spam. Real protection schemes vary, and
  the weights may need tuning once real discs are seen.
- The menu Play button needs the menu VOB's NAV packs, which only `Opened.Analyze` reads.
  `MainTitle` can't see it. A disc whose menu is the only thing that tells the film apart will rank
  correctly in the diagnose report, but **Play movie** may still pick by structure.
- A decoy that is a clean, in-order copy of the film is the same video, so picking it does no harm.
- The VM dry run only covers first play. Discs that choose the film by random register values, or by
  player settings such as region or parental level, are run with defaults.
- Episode detection expects the episodes to be separate titles. Discs that keep every episode as
  chapters of one title only show that title.
