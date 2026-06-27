# cb

A small, fast, security-conscious CLI for comic book archives. `cb` builds and
edits `.cbz` files — listing pages, checking for gaps, and managing
`ComicInfo.xml` metadata — without ever manually unzipping anything.

> **Developed with AI.** This project is written collaboratively with an AI
> coding assistant (Claude). That's not hidden — design decisions and
> conventions are documented in [AGENTS.md](AGENTS.md).

## What it is (and isn't)

- **It is** a focused tool for the `.cbz` format: a `.cbz` is just a ZIP of page
  images, optionally with a `ComicInfo.xml` metadata file. `cb` packs, inspects,
  validates, and tags those archives.
- **It isn't** a general comic reader, converter, or library manager. And it
  supports **only `.cbz` (ZIP)** — `.cbr` (RAR) and `.cb7` (7-Zip) are
  deliberately out of scope.

It's built around three priorities: **speed**, **security** (it parses untrusted
ZIPs), and **simplicity**.

## Installation / getting started

Requires **Go 1.26+** (provided via `devenv` in this repo).

```sh
go build -o cb .        # produces ./cb
go test ./...           # optional: run the test suite
```

Put the resulting `cb` binary on your `PATH`, then:

```sh
cb pack ./my-comic/     # build my-comic.cbz from a folder of images
cb tag my-comic.cbz     # fetch metadata online (or: cb tag -m to type it in)
cb ls my-comic.cbz      # see what's inside
```

Online tagging needs provider credentials — see [Configuration](#configuration).

## Features

- **Zero third-party dependencies** — pure Go standard library, a single static
  binary that's trivial to distribute and cross-compile.
- **Safe by design** — zip-slip path-traversal rejection, decompression-bomb
  caps (per-entry, total, and entry-count), and atomic writes (temp file +
  rename) so an interrupted run never corrupts an archive. Source folders are
  never modified.
- **Authoring** — `pack` turns a directory of images into a valid `.cbz`.
- **Validation** — detect missing, duplicate, or unnumbered pages.
- **Metadata** — read, write, and remove the full ComicInfo v2.1 schema.
- **Online tagging** — pull rich metadata from a provider (**Metron** or
  **ComicVine**) with accurate, ID-based matching and edition awareness
  (TPB/HC/GN/…), single files or whole directories. Pick a provider per run, or
  let one fall back to the other.

## Usage guide

```
cb <command> [flags] <path>
```

For most commands `<path>` is a single `.cbz` **or** a directory of `.cbz` files
(processed as a batch). `pack` is the exception — it takes a directory of
**image files**.

| Command | Aliases | Description |
|---------|---------|-------------|
| `cb list <path>`     | `ls` | Print each archive's contents (entry sizes + names). |
| `cb validate <path>` | `vl` | Check for a contiguous `1..N` page sequence; report missing/duplicate/unnumbered pages. Exits non-zero on failure. |
| `cb info <path>`     |      | Print `ComicInfo.xml` metadata without extracting. |
| `cb tag <path>`      |      | Write `ComicInfo.xml` from an online provider, or `-m`/`--manual` to enter it by hand. |
| `cb strip <path>`    |      | Remove `ComicInfo.xml`. |
| `cb pack <dir>`      |      | Build a `.cbz` from a directory of images. |

Run any command with `-h` to see its flags.

### `cb pack`

Builds a new `.cbz` from the images in a directory, named after the directory and
written as a **sibling** (the source folder is left untouched).

```sh
cb pack ./batman/         # -> ./batman.cbz
cb pack --force ./batman/ # overwrite an existing output
cb pack -t ./batman/      # pack, then tag online
cb pack -t -m ./batman/   # pack, then tag by prompting
```

Images are stored in lexical order; if names aren't zero-padded (so `10` sorts
before `2`), `pack` warns you. An existing `ComicInfo.xml` in the folder is kept.
Page validation runs but is advisory — it never blocks the write.

### `cb tag`

Writes `ComicInfo.xml`. By default it fetches online; `-m`/`--manual` prompts you
instead (no network or credentials needed).

```sh
cb tag comic.cbz                  # auto-match from the filename, then write
cb tag --dry-run comic.cbz        # resolve + print, don't write
cb tag -p comicvine comic.cbz     # choose the provider (alias: -p cv); default is metron
cb tag --fallback cv ./library/   # try metron, fall back to comicvine on a miss
cb tag --id <issueID> comic.cbz   # exact provider lookup, no matching
cb tag --series-id <id> ./run/    # pin a directory to a known series; match by number
cb tag --pick comic.cbz           # force the series picker
cb tag --refresh comic.cbz        # ignore cached responses, refetch fresh
cb tag -v ./library/              # verbose: show the series grouping tree + candidates
cb tag ./library/                 # batch: auto-match every .cbz, skip the unsure
cb tag -m comic.cbz               # type the fields (Series/Number pre-filled)
cb tag -m ./squirrel-man/         # one series: prompt shared fields once
```

Online matching parses the filename (series, number, year, edition), resolves
the series by exact name with a year tiebreak, then fetches the issue by number —
hands-off when unambiguous, a picker when not. `PageCount` is always taken from
the archive, never the provider.

For a directory, files are **grouped by series** and each series is resolved
**once**; a group that can't be matched is skipped with a single message instead
of probing every issue, and a mixed directory (several series) resolves each
group independently. Before tagging a group, cb verifies the local issue count
against the provider's: fewer locally just notes the run is incomplete, but **more
issues locally than the series has on record** is treated as a likely wrong-series
/ mis-split signal and the group is refused rather than mis-tagged (only distinct
issue numbers count, so variants don't trip it). `--series-id` applies the same
verification.

Add `-v`/`--verbose` to preview the plan before anything is written: it prints a
tree of how the local files grouped into series (with each file's parsed issue
number), then, as each series resolves, the candidates it matched and the
complete/incomplete/over verdict. `--series-id` shows the same per-file tree,
marking each as *in series*, *not in series*, or *no number parsed*.

When you already know the series (its ID is in the provider URL, e.g.
`metron.cloud/series/aliens-vampirella-2015/` → `14195`), `--series-id` skips
name-matching entirely: it lists the series' issues once, then matches every file
in the directory by its issue number. That removes all fuzzy guesswork and is the
fewest API calls (one list call plus one per matched file). Files whose number
isn't in the series are reported and skipped, never mis-tagged. The id is the
provider's series identifier — a Metron *series* id or a ComicVine *volume* id
(e.g. `comicvine.gamespot.com/<slug>/4050-160423/` → `160423`).

#### Choosing a provider

cb supports two metadata sources: **Metron** (the default) and **ComicVine**.
Select one for a run with `-p`/`--provider`; the aliases `mt` and `cv` are
accepted everywhere a provider name is (flag, `config.json`, or `CB_PROVIDER`):

```sh
cb tag -p comicvine comic.cbz     # or: -p cv
cb tag -p metron    ./library/    # or: -p mt   (also the default)
```

To widen coverage without switching by hand, add `--fallback <provider>`: cb
resolves on the primary and, **only when the primary cleanly finds no match**,
retries the search on the fallback. It never falls back on a network or auth
error (that would mask a real problem), and it's reported plainly — a run summary
reads `done: 5 resolved (2 via comicvine), …`, and `-v` tags the source of each
group.

```sh
cb tag --fallback comicvine ./library/   # metron first, comicvine on a miss
```

Fallback is name-search recovery, so it doesn't apply to the ID-based lookups
`--id` and `--series-id` (an issue/series id belongs to one specific provider);
combining them is rejected. The fallback provider must be configured, just like
the primary.

Matching is identical across providers and deliberately **not** fuzzy: cb uses a
provider's search only to *gather* candidates, then accepts one only on an exact,
punctuation-normalized name match (with the filename's year as a tiebreaker among
equals). A near-miss is skipped, never guessed — the safeguard that keeps cb from
the mistagging that fuzzy taggers are prone to. A few fields differ by source
(ComicVine has no age rating or barcode, and fills Publisher from the series, not
the issue); `PageCount` always comes from the archive regardless.

Responses are cached on disk under `$XDG_CACHE_HOME/cb/` (or `~/.cache/cb/`) for
24h, so repeat and batch runs are fast and easy on the API. The cache is safe to
delete at any time; pass `--refresh` to bypass it for one run (e.g. after fixing
metadata upstream).

### Configuration

Online tagging reads credentials from `$XDG_CONFIG_HOME/cb/config.json` (or
`~/.config/cb/config.json`):

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/cb"
cp config.example.json "${XDG_CONFIG_HOME:-$HOME/.config}/cb/config.json"
chmod 600 "${XDG_CONFIG_HOME:-$HOME/.config}/cb/config.json"   # it holds secrets
$EDITOR "${XDG_CONFIG_HOME:-$HOME/.config}/cb/config.json"
```

The file holds one block per provider; you only need credentials for the
provider(s) you actually use (cb validates them when it builds that provider, so
an unconfigured backend only matters if you select it or fall back to it):

- **Metron** — a free account at [metron.cloud](https://metron.cloud/); set
  `metron.username` / `metron.password`.
- **ComicVine** — a free API key from
  [comicvine.gamespot.com/api](https://comicvine.gamespot.com/api/); set
  `comicvine.api_key`.

Or use environment variables, which override the file (so nothing sensitive needs
to sit on disk):

```sh
export CB_PROVIDER=metron            # or comicvine (aliases mt / cv accepted)
export CB_METRON_USERNAME=you
export CB_METRON_PASSWORD=secret
export CB_COMICVINE_API_KEY=...
```

Metron is the default backend. Huge thanks to Metron (https://metron.cloud/) and
ComicVine (https://comicvine.gamespot.com/) for their APIs.
