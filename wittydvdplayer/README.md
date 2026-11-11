# WittyDVD for WittyMediaPlayer

WittyDVD as a program of its own, so WittyMediaPlayer can take a newer version without a new player. It's the same
engine, plus the session that turns its steps into what the window shows. A player also links this package as its
built-in copy, so it always works with nothing downloaded. See WittyMediaPlayer's `DESIGN.md`.

## What it tells the player about a disc

On top of the session calls every disc module answers, this one answers two about the engine's ranking of the disc
(`docs/TITLE-HEURISTICS.md`). Nothing is decrypted to work them out, and a disc with CSS is still refused.

| Call | What it gives |
|---|---|
| `guide` | The title picked as the film, one sentence on why, how sure the pick is (`confidence` from 0 to 1, and `level`: `high`, `medium` or `low`), the film's whole length, its other versions (camera angles, parental cuts, other cuts), a TV disc's episodes and its play-all title, the other titles a viewer might want instead, the engine's warnings, where the disc's tables were mended, and how many unreadable sectors were padded so far. Decoy titles are counted, not listed |
| `choose` with `{"id": "..."}` | Plays one of the guide's choices by its `id`, and answers with a presentation, as `title` does |

A film the disc cuts into several titles and plays in a row is handed to the player as one stream with one timeline and
one set of chapters, when its parts are in one title set and share their streams. Otherwise the disc's own commands carry
it from title to title, as they would in any player. A film only its commands put together plays as the engine read it,
not as the fragment its chapter table names. A parental cut plays by setting the level and starting the title; where that
cut isn't what the chapter table names, it plays cell by cell.
