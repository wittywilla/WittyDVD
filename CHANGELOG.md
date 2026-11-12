# Changelog

What changed in WittyDVD, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- **WittyDVD 0.4.0:** a part of a file an image doesn't record (WittyDisc 0.2.1 gives it no place on the image) is refused with a message rather than streamed from the wrong bytes.
- **WittyDVD 0.4.0:** The title ranking engine, table-by-table index reading with cross-table repair, padded unreadable sectors and the `analyze` and `readStats` calls listed above, for WittyMediaServer up to 0.8.x. The synthetic discs can be written out for other projects' tests (`WITTYDVD_SYNTH_OUT`). Checked against images of pressed film discs, which showed two mistakes, both fixed: short titles of sub-second cells that the menus play (frame-by-frame animations) were called cell-spam decoys and the disc copy-protected, and a film's featurettes of about one length were called a TV disc's episodes.
- `wittydvdplayer` 0.3.0: the player module answers two new calls, `guide` and `choose`: the ranked main title with why and how sure, the feature's whole length, its versions (angles, parental cuts, other cuts), a TV disc's episodes, the other titles, warnings, repairs and padded sectors. A film the disc chains through several titles, or that only its commands put together, plays as one stream in the engine's order.

- **Title mapping:** a scoring engine ranks every title and explains its choice (`Disc.RankTitles`, `Disc.Analyze`, `Disc.Explain`, `Opened.Analyze`, and `wittydvd rank`). `MainTitle` uses it and keeps its signature. See `docs/TITLE-HEURISTICS.md`.
  - A title is read from its chapter table and by a bounded dry run of its own commands, and the reading that shows more real video is kept: a film that only `LinkPGCN` commands put together is found.
  - Decoys are told by cells that are replayed, shuffled, out of range, zero-length, hollow, overlapping or missing from the title set's own cell address table, by chains that loop, by title sets with no video files, and by titles no command and no menu button leads to. A scrambled title loses the credit for video that other titles play in order.
  - What the disc itself does counts for most: a dry run of first play, of each button of the first menu, and a static reading of every command and button for what can be reached at all.
  - A film cut into titles that play on into each other is one feature (`Ranking.Feature` lists exactly what to play). Camera angles, cuts chosen by parental level and alternate cuts are listed (`Ranking.Versions`).
  - **TV discs:** `Disc.Episodes` finds the episodes in viewing order and the play-all title. A play-all title that is the only way to its episodes is split, by program chain or by VOB.
  - Every decision comes with a confidence and its evidence as data (`Ranking.Why`, `Ranking.Decisions`, `Candidate.Evidence`).
- **Index files, table by table:** each table of an IFO is read by itself, and one that is damaged in one copy is taken from the other (`Disc.IFOReports` says which). The cell address tables, VOBU maps and time maps are now read too. The tables are checked against each other and mended where one can mend another (`Disc.CrossCheck`, `Disc.Repairs`): chapter lists rebuilt from program chains, cells from the cell address table, counts and sizes from the tables that agree. Chapter times fall back on programs, and cell times on the time map.
- **Unreadable sectors** are tried again, then passed on as zeros and counted (`Opened.ReadStats`). An interleaved cell carries on from its next VOBU.
- **Folders:** a `VIDEO_TS` folder is found whatever case its name is in, and a folder that holds the index files directly is opened.
- **Limits:** every table read is bounds-checked without overflow on 32-bit builds, and one budget bounds the program chains and cells of a whole disc. A PGCIT may hold 8192 rows (it was 1000), and rows that point at the same chain share it. A disc of 99 titles and 2941 program chains opens in about 17 ms and ranks in about 65 ms.
- **Fixed:** a time field whose bytes aren't decimal digits, as junk cells' often are, read as up to 165 hours, and a server refuses a whole disc for one such cell. It now reads as at most 99:59:59. One `ReadSectors` call returns at most 64 MiB.
- **For servers:** the helper answers `analyze` with the ranking as JSON and `readStats` with the sectors padded. Both are new calls; nothing else on the wire changed, and the new fields of the disc's structure are ignored by an older server.
- The diagnose report ranks the titles with their confidence and reasons, lists what was mended in the index, and warns about structure-protection decoys.
- None of this bypasses CSS. Scrambled discs are still refused, nothing is decrypted, and no decryption library is linked or loaded. See `docs/QUIRKS.md`.
- Tests write their own discs: a generator makes index files, sparse VOBs that hold only NAV packs, and whole discs as folders or UDF, ISO 9660 and bridge images. There is one disc for each obfuscation pattern, property tests that renumber titles and add decoys, tests that damage tables and sectors, and a fuzz target for each parser. **No physical disc has been through any of it.**
- **Changed for callers of the library:** `ParseVTS` no longer fails on a damaged PGCIT: the table comes back empty or with empty chains in the damaged rows (`PGCEntry.Damaged`). `Disc.Title` follows a title into the next chain its chapter table names when the chain has no next link, as the navigator already did, and keeps a chapter's number when its entry names nothing. `ReadSectors` on an opened disc pads unreadable sectors instead of failing.

- **WittyDVD 0.3.1:** The checksums it records are those of what was published. 0.3.0 was never released.

- The copy for WittyMediaPlayer (`wittydvdplayer`) goes to 0.2.0, on module API 3.
- The helper is now the only way a server reaches WittyDVD, with no copy compiled in. It says a disc's label and format when opening it, and draws subtitles itself (`spu`).
- WittyDVD is now a project of its own, developed and released from this repository.
