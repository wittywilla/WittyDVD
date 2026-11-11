# Title heuristics

How WittyDVD decides which title is the film, which titles are a TV disc's episodes, and which are
decoys or extras, and how sure it is. The code is in `heuristics.go` (scoring), `paths.go` (what a
title plays), `reach.go` (what the disc's commands can get to), `episodes.go` (episodes, classes,
versions, decisions) and `analyze_open.go` (what an opened disc adds). What each odd disc layout is
and how it's handled is in [QUIRKS.md](QUIRKS.md).

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
| `Disc.Analyze(RankOptions) *Ranking` | The whole answer: see below |
| `Disc.Episodes() []Episode` | A TV disc's episodes in viewing order, or nil |
| `Disc.Explain() string` | The ranking in words, for logs and the command line |
| `Disc.CrossCheck() []Repair` | Compares the tables with each other and mends what can be mended. `Open` has already called it: see `Disc.Repairs` |
| `Opened.Analyze() *Ranking` | Like `Disc.Analyze`, plus the real VOB file sizes and the menus' buttons. Worked out once and kept: don't change what it returns |
| `Opened.RankOptions() RankOptions` | What an opened disc adds, for callers that want to change the weights |
| `Opened.ReadStats() ReadStats` | How many unreadable sectors were padded so far |
| `wittydvd rank <disc> [json]` | The same on the command line |
| Helper call `analyze` | The ranking as JSON, for a server (`wire.MethodAnalyze`, `wire.DecodeRanking`) |

A `Ranking` holds:

| Field | What it is |
|---|---|
| `Candidates` | Every title, best first. Each has `Score`, `Class`, `Flags`, `Reasons` (words) and `Evidence` (the same as data: `Signal`, `Points`, `Detail`) |
| `Main`, `Confidence`, `Level` | The pick, how sure it is from 0 to 1, and the same as `high` (0.8 and up), `medium` (0.5 and up) or `low` |
| `Why` | The pick explained: `Summary`, `Because` and `Against` (evidence, strongest first), `RunnerUp` and `Margin` |
| `Decisions` | Everything decided, each with its confidence and evidence: `main`, `feature`, `episodes`, `decoys`, `chapters` |
| `Feature` | Exactly what to play: runs of consecutive cells (`Segments`), across titles when the disc chains several into one film. `Source` is `chapters`, `commands` or `chain` |
| `Versions` | The ways to see the feature: each camera angle, each cut the parental levels choose, and other titles that are mostly the same video |
| `Episodes`, `PlayAll` | A TV disc's episodes in viewing order. `StartSec` and `FirstChapter` say where an episode begins when a play-all title is the only way to it |
| `Decoys`, `Warnings` | Decoy titles, and notes for people |
| `FirstPlay`, `MenuPlay`, `Reachable`, `ButtonsRead` | What the disc's own program does |

`Flags` serialises as a list of names: `main`, `decoy`, `menuTarget`, `menuPlay`, `firstPlay`,
`intro`, `episode`, `playAll`, `duplicate`, `shuffled`, `outOfRange`, `repeats`, `cellSpam`, `loop`,
`absurd`, `badChapters`, `fromBackup`, `alternate`, `unlisted`, `hollow`, `overlap`, `unreachable`,
`assembled`, `part`, `noVideo` and `truncated`. Names are only ever added.

A `TitleSummary` (from `Candidate.Summary()` or `MainTitle`) says where its chapter times come from
(`ChapterSource`), whether cell times had to be worked out (`Estimated`), and how its cells were
found (`Path`).

## What a title plays

A title is read two ways, and the reading that scores better is kept (`Candidate.Path`):

- **`chapters`:** start at the program chain the first chapter names and follow next-PGC links, or
  without one the next chain the chapter table names. This is `Disc.Title`.
- **`commands`:** start the title in the disc's own virtual machine, as a viewer who picked it
  would, and note every cell it shows until the disc leaves the title. Random numbers are fixed. A
  film put together by `LinkPGCN` commands only shows up this way. A title whose commands bounce a
  cold start to a menu is noted (`guarded`) and read from its chapters.

Both are bounded: 4096 cells and 512 chains to a title, and all the dry runs of one analysis share a
budget of three million commands and navigator hops.

**Chapters** come from the chapter table: a chapter starts where the program its entry names starts.
An entry that names nothing the title plays keeps its number, at the time of the chapter before it,
unless it comes last. When no entry can be placed, each program becomes a chapter
(`ChapterSource: "programs"`). Times are the sum of the cells' play times. A cell that holds video
and says zero gets its time from the chain's time map (`VTS_TMAPTI`), or else its share, by sectors,
of what the chain's play time leaves over; `Estimated` says so, and the `chapters` decision is then
less sure.

## Signals and weights

Scores are counted in minutes of distinct video, so a weight of 15 is worth a quarter of an hour of
unique cells. The weights are in `DefaultWeights`, and `RankOptions.Weights` can override them. The
name in the first column is the `Signal` of the evidence.

| Signal | Weight | What it looks at |
|---|---|---|
| `distinctVideo` | +1 per minute | Play time of readable cells. Each sector counts once: a cell replayed, or lying over another cell, adds nothing new |
| `outOfRange` | −10, then −20 × their share | Cells that start past the title VOB the IFO describes, or end before they start. On an opened disc, also cells past the end of the VOB files |
| `unlistedCells` | −8, then −25 × their share | Cells the title set's cell address table (`VTS_C_ADT`) doesn't hold. Their time isn't counted |
| `offVOBU` | −10 × their share | Cells that don't start where the VOBU map (`VTS_VOBU_ADMAP`) says a VOBU starts |
| `hollowCells` | −20 × their share of the time | Cells that claim a minute or more and hold under half a sector for each second. Their time isn't counted |
| `overlappingCells` | −15 | Distinct cells that lie over each other, at least two and a tenth of the title |
| `repeats` | −30 × the share of time spent on repeats | Cells played again within the title |
| `outOfOrder` | −5, then −35 × the share of steps that jump back | Steps from one cell to the next that go backwards on the disc |
| `scrambled` | −25 | A quarter or more of its steps jump back |
| `shuffled` | −25 | Holds at least 80% of another title's video, or of a chained feature's, and plays it in another order than that title does |
| `borrowedVideo` | minus the video it shares | A scrambled or shuffled title loses the credit for video that other titles play in order |
| `duplicate` | −0.5 | Same cells in the same order as a lower-numbered title. Only breaks the tie |
| `cellSpam` | −30 | At least 10 cells under a second making up half the title, or 20 zero-length cells |
| `absurdCell` | −20 | A cell that claims more than four hours. Its time isn't counted |
| `loop` | −15 | The title's next-PGC links lead back to a chain already visited |
| `badChapters` | −10 | The chapter table names a chain or program that doesn't exist |
| `timeMismatch` | −3 | A chain's play time isn't the sum of its cells' |
| `noVideoFiles` | −60 | Its title set has an index and no video files (opened discs). None of its time is counted |
| `noAudioStreams` | −20 | Its first program chain turns on no audio stream (the PGC's audio stream control), where other titles of its title set do, and it is four minutes or longer |
| `evenChapters` | +3 | Three or more chapters, averaging 1 to 20 minutes, without wild swings in length |
| `audioStreams` | +2 | Its title set has the most audio streams, where they differ |
| `subtitleStreams` | +1 | Its title set has the most subtitle streams, where they differ |
| `largestTitleSet` | +5 | Its title set holds the most video, on discs with more than one |
| `firstPlay` | +15, and +0.25 per minute of its video | A dry run of the disc's own first-play program in the VM reaches it without a button press |
| `menuReference` | +4 | A `JumpTT`, `JumpVTS_TT` or `JumpVTS_PTT` in first play, a menu or a menu button names it. Read statically |
| `firstMenuJump` | +6 | It is the first title the first menu on the way jumps to |
| `menuButton` | +4 | A button of the first menu plays it, in a dry run |
| `menuPlay` | +25, and +0.35 per minute of its video | The first menu's default button plays it, in a dry run (opened discs) |
| `unreachable` | −25 | No command and no menu button leads to it. Only said when every menu's buttons were read |
| `continues` | the video of the titles it runs on into | When it ends the disc plays on into other titles by itself: one feature in parts |

Some signals add a remark and no points: `assembled`, `guarded`, `commandLoop`, `partOfFeature`,
`pastVOBEnd`, `pastFileEnd`, `fromBackup`, `timesEstimated`, `chaptersFromPrograms`, `truncated`,
`intro`, `episode`, `playAll`, `alternateCut` and `picked`.

By itself, `RankOptions.FileSectors` never changes a score, so `MainTitle` gives the same answer for
an image and for a copy of its index files. `Opened.Analyze` sets `FilesKnown` when the disc has VOB
files, and then a title set without them, and cells past their end, count.

### What the disc's own program says

Three readings, from sure to thorough:

1. **A dry run of first play** in the VM, with fixed random numbers, until a title of 20 minutes or
   more plays by itself. Shorter titles on the way are intros.
2. **A dry run of the first menu's buttons** (opened discs): each button is pressed in a copy of the
   navigator, and the title it leads to is noted. The default button's is `MenuPlay`.
3. **A static reading of every command** (`reach.go`). Nothing is run: every command of every chain
   is decoded, its compare is ignored and its link or jump becomes an edge, so the answer takes in
   every path any register values could choose. A title this can't reach from first play is one no
   player would show. The reverse needs the menus' button commands, which are in the menu VOB's NAV
   packs; `Opened.Analyze` reads them (one sector per VOBU, at most 8192 in all), and only when
   every menu cell was read to its end is anything called `unreachable`.

### Confidence

The main title's confidence starts from its lead over the runner-up: `1 − e^(−lead/15)`, so a
quarter of an hour of video ahead is about 0.63 and an hour is 0.98. The runner-up is the best title
that is a different thing: not a copy or another cut of the pick, not a part of it, not a decoy, and
on a TV disc not another episode. Then:

- the disc starting or the Play button playing the pick raises it to at least 0.9;
- any sign of damage in the pick itself (cells out of range, repeated, unlisted, hollow or
  overlapping) multiplies it by 0.7;
- first play or the Play button going to a different thing multiplies it by 0.6;
- under five minutes of video halves it.

The other decisions have fixed confidences: episodes that are titles with a play-all title 0.92,
without one 0.6 to 0.8, a play-all title split by program chain 0.7 and by VOB 0.5; decoys 0.7 to
0.95 by how many independent signs the least obvious one shows; chapters 0.95 from the chapter
table, 0.6 from programs, and 0.7 times that when cell times were worked out.

## Classes

Each title gets one class: `main`, `playAll`, `episode`, `decoy`, `part`, `duplicate` or
`alternate`. `part` is a title the disc plays straight after another as one feature. `alternate`
means at least 60% of its video is the main title's, like another cut of the film. Titles that are
none of those are sorted by length:

| Class | Length |
|---|---|
| `intro` | A short title that first play passes through |
| `stub` | Under 15 seconds |
| `trailer` | Under 4 minutes |
| `featurette` | Under 45 minutes |
| `extra` | Anything longer |

A title is a **decoy** if any of these is true:

- It's a shuffled copy of another title, or scrambled.
- It has cell spam, an absurd cell or a looping chain.
- At least half its cells are outside the VOB, or missing from the cell address table, or lie over
  other cells.
- At least half its play time is in hollow cells, or repeats cells it has already played.
- Its title set has no video files.

## A feature in parts

When a title ends with a jump to another title and nothing in between waits for the viewer, the disc
plays them as one. The first title of such a chain is credited with the video of the rest
(`continues`), the others are classed `part`, and `Ranking.Feature` lists the cells of all of them in
order. Three or more titles of an episode's length and about as long as each other are taken for
episodes that play on, not for a film in parts.

## Versions

- **Angles:** the title is read once for each camera angle (`TT_SRPT`'s count, checked against the
  angle blocks). Each has its own length when the angles differ.
- **Parental cuts:** when the title's chains hold a parental block and the disc has a parental
  table, the title is dry-run at each parental level, and levels that play the same cells are one
  version.
- **Cuts:** other titles classed `alternate`, with how much of their video the main title shares. A
  longer cut plays the shorter one's cells in the same order with more in between, wherever on the
  disc the extra scenes lie; that order is what tells it from a shuffled copy.

The main title is the cut with the most distinct video, so a longer cut wins over the theatrical
one. A server can offer the others from `Versions`.

## Episodes

A disc counts as a TV disc when it has at least three clean titles, each 8 to 75 minutes long and all
within ±15% of their median length, that play forwards with nothing repeated, and none of them shares
more than half its video with another. Two such titles are enough when a play-all title is also
found.

A **play-all** title is in the same title set, holds at least 90% of each episode's video, and lasts
within 15% of the episodes' total. Video is compared by sectors, so a play-all title that plays each
episode as one long cell still matches.

Episodes are ordered by where they start inside the play-all title. Without a play-all title, they're
ordered by title set and first sector, and then by title number.

**A play-all title with no episode titles** is split: if one of the three best titles is made of
three or more stretches of an episode's length, each a program chain of its own (`Source: "pgcs"`) or
a VOB of its own (`"vobs"`), those are the episodes, with `StartSec` and `FirstChapter` saying where
each begins. Stretches under 90 seconds go with the episode after them. A film is one chain and,
but for the odd logo, one VOB, so it doesn't split.

## IFO against BUP, and table against table

`Open` reads both copies of each index file, and each table by itself. If only one copy parses, it
is used. If both parse and differ, the copy with fewer problems is the base, the IFO on a tie; then
every table that is missing or damaged in the base and whole in the other copy is taken from the
other copy. `Disc.IFOReports` records which copy was used, and per table its state and where it came
from.

Then the tables are checked against each other (`Disc.CrossCheck`, results in `Disc.Repairs`):

| Wrong | Mended from | When |
|---|---|---|
| A title's chapter list is missing, or starts at a chain that doesn't exist | The program chains | The title has an entry chain |
| `TT_SRPT`'s chapter count | The chapter table | They differ |
| `TT_SRPT`'s angle count | The angle blocks | It is 0, over 9, or the blocks hold more than one angle |
| The size of the title VOBs in the header | The cell address table | The header says 0, or the address table and the VOBU map both reach past it |
| A chain's cells point at sectors that can't exist | The cell address table, by VOB and cell id | Every such cell of the chain is in the table, and the chain then plays forwards without a cell over another. All or nothing, so a decoy that borrows real ids in a scrambled order stays a decoy |
| Cells with no VOB and cell id | The cell address table | A row starts and ends where the cell does |
| A chain has cells and no usable program map | Itself | It is read as one program |

Disagreements that can't be settled are reported and left: chapters that name nothing, programs that
start at cells the chain doesn't have, address table rows that don't start at a VOBU, a time map
count that isn't the chain count.

## Limits

- **No physical disc has been through this.** Every disc it has seen is synthetic: written by the
  generator in `fixture_test.go` and `synth_test.go`, one per pattern in `scenarios_test.go`. The
  generator writes what the public references say these layouts look like. Real protection schemes
  vary, and the weights may need tuning once real discs are seen.
- **Decoys made of real, listed video in a believable order can't be told from a film by structure.**
  A title that plays the film's cells in order with a trailer's cells spliced in looks like a longer
  cut and has more distinct video. Only the disc's own program tells them apart: first play, the
  Play button, reachability. `MainTitle` has only the first of those; `Opened.Analyze` has all
  three.
- **Title sets without video files** are only seen by `Opened.Analyze`. From the IFOs alone an index
  that claims a long film looks like one.
- **Dry runs stand for one set of register values.** Discs that choose the film by random register
  values, or by player settings such as region, are run with defaults. Parental levels are tried
  one by one for `Versions`, with the country set to US.
- **The static reading is only as complete as the buttons read.** A menu in another language unit,
  a menu cell longer than 256 VOBUs, a scrambled or unreadable menu VOB: any of these and nothing is
  called unreachable.
- **A film in parts of even length looks like episodes**, and episodes that play on look like a film
  in parts. The lengths decide.
- **Splitting a play-all title by VOB** rests on the VOB ids alone when it is one chain: confidence
  0.5.
