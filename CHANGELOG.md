# Changelog

What changed in WittyDVD, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## Unreleased

- **Title heuristics:** a scoring engine ranks every title and explains its choice (`Disc.RankTitles`, `Disc.Analyze`, `Disc.Explain`, `Opened.Analyze`). It works from distinct video, repeated, shuffled and out-of-range cells, zero-length cell spam, impossible cell lengths, looping PGC chains, broken chapter tables, a dry run of first play, menu jump commands, the first menu's Play button, stream counts, chapter regularity and the largest title set. `MainTitle` uses it and keeps its signature. Other titles are classed as decoy, duplicate, alternate cut, trailer, featurette or extra.
- **TV discs:** `Disc.Episodes` finds the episodes, in viewing order, and the play-all title.
- **IFO against BUP:** when both copies parse but differ, the one whose tables are consistent is used. `Disc.IFOReports` says which copy was read and why.
- The diagnose report ranks the titles with their reasons, and warns about structure-protection decoys.
- None of this bypasses CSS. Scrambled discs are still refused, and nothing is decrypted. See `docs/TITLE-HEURISTICS.md`.
- Tests build synthetic IFOs in memory: a movie, a 99-title decoy disc, a shuffled decoy, a TV disc, a corrupt IFO with a good BUP, first play and menu targets, and cell spam. A fuzz test covers the scorer.

- **WittyDVD 0.3.1:** The checksums it records are those of what was published. 0.3.0 was never released.

- The copy for WittyMediaPlayer (`wittydvdplayer`) goes to 0.2.0, on module API 3.
- The helper is now the only way a server reaches WittyDVD, with no copy compiled in. It says a disc's label and format when opening it, and draws subtitles itself (`spu`).
- WittyDVD is now a project of its own, developed and released from this repository.
