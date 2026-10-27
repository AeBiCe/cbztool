# cbztool

A small, fast, security-conscious CLI for comic book archives. Its command,
`cbz`, builds and
edits `.cbz` files — listing pages, checking for gaps, and managing
`ComicInfo.xml` metadata — without ever manually unzipping anything.

> **Developed with AI.** This project is written collaboratively with an AI
> coding assistant (Claude). That's not hidden — design decisions and
> conventions are documented in [AGENTS.md](AGENTS.md).

## What it is (and isn't)

- **It is** a focused tool for the `.cbz` format: a `.cbz` is just a ZIP of page
  images, optionally with a `ComicInfo.xml` metadata file. `cbz` packs, inspects,
  validates, and tags those archives.
- **It isn't** a general comic reader, converter, or library manager. And it
  supports **only `.cbz` (ZIP)** — `.cbr` (RAR) and `.cb7` (7-Zip) are
  deliberately out of scope.

It's built around three priorities: **speed**, **security** (it parses untrusted
ZIPs), and **simplicity**.

## Installation / getting started

Requires **Go 1.26+** (provided via `devenv` in this repo).

```sh
go install github.com/AeBiCe/cbztool/cmd/cbz@latest   # installs cbz into $(go env GOPATH)/bin
# or, from a checkout:
go build -o cbz ./cmd/cbz   # produces ./cbz
go test ./...               # optional: run the test suite
```

With Nix: `nix run github:AeBiCe/cbztool`, or
`nix profile install github:AeBiCe/cbztool`.

Put the resulting `cbz` binary on your `PATH`, then:

```sh
cbz pack ./my-comic/     # build my-comic.cbz from a folder of images
cbz tag my-comic.cbz     # fetch metadata online (or: cbz tag -m to type it in)
cbz ls my-comic.cbz      # see what's inside
```

Online tagging needs provider credentials — see [Configuration](#configuration).

## Features

- **Zero third-party dependencies** — pure Go standard library, a single static
  binary that's trivial to distribute and cross-compile.
- **Safe by design** — zip-slip path-traversal rejection, decompression-bomb
  caps (per-entry, total, and entry-count), and atomic writes (temp file +
  rename) so an interrupted run never corrupts an archive. Editing through a
  symlink changes the file it points to and leaves the link intact. Source
  folders are never modified.
- **Authoring** — `pack` turns a directory of images into a valid `.cbz`.
- **Validation** — detect missing, duplicate, or unnumbered pages, pages that
  aren't readable images, and (with `--full`) corrupted data; `clean` strips
  OS junk such as `__MACOSX/` and `Thumbs.db`.
- **Metadata** — read, write, and remove the full ComicInfo v2.1 schema.
- **Online tagging** — pull rich metadata from **Metron** and **ComicVine**
  combined (or either one alone) with accurate, ID-based matching and edition
  awareness (TPB/HC/GN/…), single files or whole directories.

## Usage guide

```
cbz [-c <config>] <command> [flags] <path>
```

For most commands `<path>` is a single `.cbz` **or** a directory of `.cbz` files
(processed as a batch). The library-wide commands (`stats`, `dupes`, `missing`) take `-r`
to include every subdirectory as well (symlinked directories are not followed). `pack` is the exception — it takes a directory of
**image files**.

Batch runs of `tag` print a header per series saying what it matched, with one
numbered line per file under it, and end with a summary that lists every
skipped and failed file with its reason:

```
Saga (2012) — Image · metron 5 · 2 files, 66 issues on record
      [1/2] #12 Saga 012.cbz — skipped: already tagged (--force to overwrite)
      [2/2] #13 Saga 013.cbz — tagged (metron + comicvine)
done: 1 tagged, 1 skipped, 0 failed
```

Warnings are marked `[!]` and errors `[X]` (in yellow and red on a terminal;
set `NO_COLOR` to turn color off). `-v` adds the series candidates and how each
provider matched, indented under the line they belong to. Every command
**exits non-zero if any file failed**, so scripts and cron jobs can detect it;
a skipped file (for example, one already tagged) is not a failure.

| Command | Aliases | Description |
|---------|---------|-------------|
| `cbz list <path>`     | `ls` | Print each archive's contents (entry sizes + names). |
| `cbz validate <path>` | `vl` | Check for a contiguous `1..N` page sequence (missing/duplicate/unnumbered pages) and that every page is a readable image (JPEG, PNG, GIF, WebP, BMP, AVIF). `--full` also reads every page to the end to verify its checksum (slower). OS junk and extra files are reported as warnings. Exits non-zero on failure. |
| `cbz info <path>`     |      | Print `ComicInfo.xml` metadata without extracting. |
| `cbz tag <path>`      |      | Write `ComicInfo.xml` from an online provider, or `-m`/`--manual` to enter it by hand. |
| `cbz strip <path>`    |      | Remove `ComicInfo.xml`. |
| `cbz edit <file>`     |      | Edit one archive's metadata as JSON in `$VISUAL`/`$EDITOR` (default `vi`). Every field is listed; empty a value to remove it. Nothing is written if nothing changed; the changed fields are listed after saving. |
| `cbz rename <path>`   |      | Rename archives from their `ComicInfo.xml` using a name template (see below). `-n` previews. |
| `cbz stats <path>`    |      | Library summary: archives, size, pages, series, tagged/untagged, and how many have page-numbering problems or OS junk. Reads only the ZIP directory and `ComicInfo.xml`, so it's fast; `-v` lists the files behind each count. |
| `cbz missing <dir>`   |      | For each series, list the issues you don't have (see below). `--offline` reports only gaps in your local numbering. |
| `cbz dupes <dir>`     |      | Find archives with byte-identical pages (renamed, repacked, re-tagged or cleaned copies included), without decompressing anything. Reports only; never deletes. |
| `cbz clean <path>`    |      | Remove OS junk (`__MACOSX/`, `._*`, `.DS_Store`, `Thumbs.db`, `desktop.ini`); `--extra` also removes files that are neither pages nor `ComicInfo.xml` (`.nfo`, `.txt`, …). `--dry-run` lists what would go. Kept entries are copied byte-for-byte. |
| `cbz config <sub>`    |      | `path`, `show` or `init` the configuration (see [Configuration](#configuration)). |
| `cbz pack <dir>`      |      | Build a `.cbz` from a directory of images. |

Run any command with `-h` to see its flags.

### `cbz rename`

Renames each archive from its metadata, in place (same directory), using
`rename.template` from the [config](#configuration) or `-t`:

```sh
cbz rename -n ./library/            # preview
cbz rename ./library/
cbz rename -t '{Series} v{Volume} #{Number:3}[ - {Title}]' comic.cbz
```

| Template | Meaning |
|---|---|
| `{Series}` | a ComicInfo field's value (any field: `{Title}`, `{Year}`, `{Publisher}`, …; case-insensitive) |
| `{Number:3}` | zero-pad the integer part to 3 digits: `1` → `001`, `1.5` → `001.5` |
| `[ ({Year})]` | optional group: dropped when any field inside it is empty |
| `\{` `\[` | a literal brace or bracket |

The default, `{Series}[ ({Year})] #{Number:3}`, gives `Saga (2012) #001.cbz`.
A field outside an optional group must have a value, or the file is skipped.
Characters that aren't allowed in file names are replaced (`:` → ` -`,
`/` → `-`, `?`/`*` dropped). A rename never overwrites an existing file (that
file is skipped and reported), and files without `ComicInfo.xml` are skipped.

### `cbz missing`

Lists, per series, the issues you don't have:

```sh
cbz missing ./library/             # every series in the folder
cbz missing -r ./library/          # …and in its subfolders
cbz missing --series-id 14195 .    # pin the series (Metron id, or ComicVine 4050-…)
cbz missing --offline ./library/   # no network: gaps in your own numbering only
```

```
Saga (2012) — Image [metron 5]: have 48 of 66 issue(s) — missing 18: 12, 31–33, 54–66
  not listed by metron: 0.5
```

Each archive is identified by its `ComicInfo.xml` Series/Number when it has
one, otherwise by its filename. Series are matched the same way `cbz tag`
matches them (exact name, year tiebreak, edition-aware) and a series that
can't be matched confidently is skipped with a hint to use `--series-id`. The
provider's complete issue list is then compared by issue number (`001` = `1`;
variants count once). Missing issues are a report, not an error: the exit
status is non-zero only when a series couldn't be checked (network, auth).
`--offline` needs no credentials; it can only see gaps between `#1` and your
highest issue.

### `cbz pack`

Builds a new `.cbz` from the images in a directory, named after the directory and
written as a **sibling** (the source folder is left untouched).

```sh
cbz pack ./batman/         # -> ./batman.cbz
cbz pack --force ./batman/ # overwrite an existing output
cbz pack -t ./batman/      # pack, then tag online
cbz pack -t -m ./batman/   # pack, then tag by prompting
```

Images are stored in lexical order; if names aren't zero-padded (so `10` sorts
before `2`), `pack` warns you. An existing `ComicInfo.xml` in the folder is kept.
Page validation runs but is advisory — it never blocks the write.

### `cbz tag`

Writes `ComicInfo.xml`. By default it fetches online; `-m`/`--manual` switches to
manual mode (no network or credentials needed) — see
[Manual mode](#manual-mode--m). Run `cbz tag -h` / `cbz tag -m -h` for each mode's flags.

```sh
cbz tag comic.cbz                  # auto-match from the filename, then write
cbz tag --dry-run comic.cbz        # resolve + print, don't write
cbz tag -p comicvine comic.cbz     # use only one provider (alias: -p cv); default: all, combined
cbz tag --id 4000-675976 comic.cbz # exact issue lookup, no matching (bare number = Metron)
cbz tag --series-id 14195 ./run/   # pin a directory to a known series; match by number
cbz tag --pick comic.cbz           # force the series picker
cbz tag --refresh comic.cbz        # ignore cached responses, refetch fresh
cbz tag -v ./library/              # verbose: show the series grouping tree + candidates
cbz tag ./library/                 # batch: auto-match every .cbz, skip the unsure
```

Online matching parses the filename (series, number, year, edition), resolves
the series by exact name with a year tiebreak, then fetches the issue by number —
hands-off when unambiguous, a picker when not. `PageCount` is always taken from
the archive, never the provider.

Every write of `ComicInfo.xml` (online, manual, or any later command) also fills
`<Pages>` from the archive: each page's size and pixel dimensions, `DoublePage`
for a page wider than it is tall, and `FrontCover` on the first page. Readers
such as Komga and Kavita use this for layout. Page types you set by hand
(`InnerCover`, `Advertisement`, a moved `FrontCover`, …) and bookmarks are kept
on later writes as long as the page count hasn't changed. `cbz info` shows a
one-line summary.

For a directory, files are **grouped by series** and each series is resolved
**once**; a group that can't be matched is skipped with a single message instead
of probing every issue, and a mixed directory (several series) resolves each
group independently. Before tagging a group, cbz verifies the local issue count
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
isn't in the series are reported and skipped, never mis-tagged.

Ids work without `-p`: the id's form says which provider it belongs to. A bare
number is a **Metron** id; a **ComicVine** id keeps the resource prefix from its
URL — `4050-…` for a volume (`--series-id`), `4000-…` for an issue (`--id`),
e.g. `comicvine.gamespot.com/<slug>/4050-160423/` → `--series-id 4050-160423`.
Passing an issue id to `--series-id` (or vice versa) is caught with a clear
error. With `-p cv`, a bare number is read as a ComicVine id.

#### Manual mode (`-m`)

Manual mode never contacts a provider. It has three ways in:

```sh
cbz tag -m comic.cbz                          # prompts (Series/Number pre-filled)
cbz tag -m ./squirrel-man/                    # one series: prompt shared fields once
cbz tag -m -s AgeRating=Teen -s Publisher=Image ./squirrel-man/   # set specific fields
cbz tag -m -t meta.json                       # write a blank JSON template
cbz tag -m -t meta.json ./squirrel-man/       # write a template prefilled from the archives
cbz tag -m -f meta.json ./squirrel-man/       # apply a JSON file (add --dry-run to preview)
```

A JSON file holds fields shared by the whole series once under `series`, and
per-issue fields under `issues`, keyed by issue number (`"1"`, `"001"`, `"1.5"`)
or exact filename:

```json
{
  "series": { "Series": "Squirrel Man", "Publisher": "Indie Press", "Year": "2021", "AgeRating": "Teen" },
  "issues": {
    "1": { "Title": "Nuts", "Summary": "Where it all begins…" },
    "Squirrel Man 002.cbz": { "Summary": "Keyed by filename works too." }
  }
}
```

- **Templates** (`-t`): without a path you get every series field plus one issue,
  all `""`. With a path, each archive gets an issue entry holding its current
  values; a series field that's identical across all archives goes under
  `series`, one that differs is listed per issue. An existing template is never
  overwritten without `--force`.
- **Fields** are the ComicInfo element names (any v2.1 field, case-insensitive;
  the full list is in `cbz tag -m -h`). Unknown names and invalid values (e.g. an
  AgeRating outside the enum, a non-numeric Year) are rejected before any
  archive is touched. Numbers may be written as `2021` or `"2021"`.
- **Empty values are skipped**, so an untouched template applies as a no-op and
  you only fill in what you want. (A field can't be cleared this way; use
  `cbz edit`, or `cbz strip` to start over.)
- **Layering:** the archive's existing `ComicInfo.xml` → `series` → that file's
  issue entry → `-s` fields. You can tag online first and then patch from a file
  or with `-s`, no `--force` needed. `Series`/`Number` default to the filename;
  `PageCount` always comes from the archive.

#### Combining Metron and ComicVine

By default `cbz tag` uses **every configured provider and combines them**:

1. The issue is resolved on Metron (the filename match below). Only when Metron
   has no match at all is the same search tried on ComicVine.
2. The same issue is then fetched from the other provider **by exact id**, never
   by a second search: Metron records each issue's ComicVine id, and in the other
   direction Metron is asked for the issue carrying that ComicVine id. Only when
   neither id link exists does cbz fall back to a unique exact series-name match
   plus the issue number (ambiguous → skipped). A lookup that returns a different
   issue number is discarded with a warning.
3. The two are merged: for single values (Title, Summary, Publisher, …)
   **Metron's value wins** and ComicVine only fills what Metron left empty; the
   date is taken whole from one source; Characters, Teams, Locations, StoryArc,
   Genre and Tags are **combined without duplicates**
   (case, punctuation, and a trailing `(…)` are ignored when comparing, so
   `Medusa (Marvel)` and `Medusa` are one entry); creator roles (Writer,
   Penciller, …) keep Metron's credits and take ComicVine's only for a role
   Metron left empty (ComicVine lists cover artists under interior roles too);
   `Web` keeps both URLs.
   `Notes` records the source ids, e.g. `Tagged by cbz: metron issue 12345,
   comicvine issue 675976`.

If a provider has no credentials configured it's simply left out, so a
Metron-only (or ComicVine-only) setup works as before. If the other provider's
lookup fails (network, rate limit), the file is still tagged from the source that
answered, with a warning. `-v` shows how each source was matched, and
`--dry-run` names the sources (`resolved via metron + comicvine`).

Use `-p`/`--provider` to use **only** one provider for a run (aliases `mt` and
`cv`). To make ComicVine the provider whose values win, put it first in
`providers.order` in the config (or set `CBZ_PROVIDER=comicvine`). To prefer a
provider for individual fields only, use `providers.prefer`, e.g.
`{"Summary": "comicvine"}` (see [Configuration](#configuration)).

```sh
cbz tag ./library/                 # metron + comicvine, combined
cbz tag -p cv comic.cbz            # comicvine only
```

What each source contributes:

| Field | Metron | ComicVine |
|---|---|---|
| Series, Number, Title, Summary, Web, date (store date, else cover date) | ✅ | ✅ |
| Writer / Penciller / Inker / Colorist / Letterer / CoverArtist / Editor | ✅ | ✅ |
| Characters, Teams, StoryArc, Publisher | ✅ | ✅ |
| Volume, Genre, Format, AgeRating, GTIN, Imprint, AlternateNumber | ✅ | — |
| Locations | — | ✅ |

`Count` is never written from a provider (for an ongoing series it goes stale);
set it by hand with `cbz tag -m -s Count=12` if you want it.

Matching is identical across providers and deliberately **not** fuzzy: cbz uses a
provider's search only to *gather* candidates, then accepts one only on an exact,
punctuation-normalized name match (with the filename's year as a tiebreaker among
equals). A near-miss is skipped, never guessed — the safeguard that keeps cbz from
the mistagging that fuzzy taggers are prone to. `PageCount` always comes from
the archive regardless.

Responses are cached on disk under `$XDG_CACHE_HOME/cbztool/` (or `~/.cache/cbztool/`) for
24h (`providers.cache_ttl`), so repeat and batch runs are fast and easy on the API. The cache is safe to
delete at any time; pass `--refresh` to bypass it for one run (e.g. after fixing
metadata upstream).

### Configuration

cbz reads settings from `$XDG_CONFIG_HOME/cbztool/config.json` (or
`~/.config/cbztool/config.json`). Create a starter file with:

```sh
cbz config init        # writes the default location; or: cbz config init <file>
cbz config show        # the effective settings (secrets are never printed)
cbz config path        # which files are read
```

```json
{
  "providers": {
    "order": ["metron", "comicvine"],
    "prefer": { "Summary": "comicvine" },
    "cache_ttl": "24h"
  },
  "rename": { "template": "{Series}[ ({Year})] #{Number:3}" },
  "credentials": {
    "metron":    { "api_key_file": "~/.config/cbztool/metron-token" },
    "comicvine": { "api_key_file": "/run/agenix/comicvine" }
  }
}
```

- `providers.order` — precedence: the first is searched first and its values win
  when sources disagree. Aliases `mt`/`cv` are accepted.
- `providers.prefer` — per-field overrides of that order. Fields merged from
  every source (Genre, Tags, Characters, Teams, Locations, StoryArc) can't be
  preferred; `Year` stands for the whole date.
- `providers.cache_ttl` — how long API responses are cached.
- `credentials` — one block per provider. Configure both to get combined
  metadata; a provider without credentials is skipped (unless you select it with
  `-p`).
  - **Metron** — a free account at [metron.cloud](https://metron.cloud/), then
    an API token from your profile page (**API Tokens**). Username/password
    sign-in isn't supported: Metron is retiring it for the API.
  - **ComicVine** — a free API key from
    [comicvine.gamespot.com/api](https://comicvine.gamespot.com/api/).

**The config file holds no secrets**, so it's safe to keep in a dotfiles repo or
generate with home-manager. API keys and tokens are read from the file named by
`api_key_file` (absolute, or starting with `~/`; `chmod
600` — cbz warns otherwise), which fits agenix / sops-nix secrets, or from
environment variables. A secret written inline in the config is rejected.

Settings are layered; each layer overrides only the keys it sets:

1. built-in defaults
2. `~/.config/cbztool/config.json`
3. `cbz -c <file> …` / `--config <file>` (or `CBZ_CONFIG`) — must exist
4. environment variables:

```sh
export CBZ_PROVIDER=comicvine         # move a provider to the front of the order
export CBZ_METRON_TOKEN=...
export CBZ_COMICVINE_API_KEY=...
```

Huge thanks to Metron (https://metron.cloud/) and
ComicVine (https://comicvine.gamespot.com/) for their APIs.

## Project layout

A thin `cmd/cbz/main.go` routes each command to a handler in `internal/command`, which
builds on focused packages. Dependencies flow one way: `command` → {`archive`,
`comicinfo`, `config`, `filename`, `provider`}; `archive`, `config` and
`provider` → `comicinfo`; `archive` → `imagemeta`; `provider` → `version`.

| Path | What lives there |
|---|---|
| `cmd/cbz` | `package main`: command dispatch, the global `-c/--config` flag, and `version`. |
| `internal/command` | One file per command (`list`, `validate`, `info`, `edit`, `strip`, `clean`, `rename`, `stats`, `dupes`, `missing`, `pack`, `tag`, `config`). `tag.go` is online tagging and matching; `tag_merge.go` combines providers; `tag_manual.go` / `tag_template.go` are manual mode (`-m`: prompts, `-f`, `-s`, `-t`). |
| `internal/archive` | Safe `.cbz` reading and atomic writing: zip-slip and decompression-bomb guards, temp-file + rename; entry classification (page / ComicInfo / junk / extra) and per-page header reading. |
| `internal/imagemeta` | Image format and dimensions from the header alone: stdlib for JPEG/PNG/GIF, small bounds-checked parsers for WebP/BMP/AVIF (tested against real encoder output and fuzzed). |
| `internal/comicinfo` | The ComicInfo v2.1 model: XML read/write (`comicinfo.go`), field access by name (`fields.go`), and merging two sources (`merge.go`). |
| `internal/config` | Loading the layered configuration (defaults, files, env) and reading secret files. |
| `internal/filename` | Parsing messy comic filenames into series / number / year / volume. |
| `internal/provider` | The `Provider` interface, the Metron and ComicVine backends, and the on-disk response cache. |
| `internal/version` | The version shown by `cbz --version` and sent in the API User-Agent, taken from the git tag at build time. |
| `internal/term` | The `[!]` warning and `[X]` error markers, colored only on a terminal (honors `NO_COLOR`). |

## Versioning

cbztool follows [Semantic Versioning](https://semver.org/). Releases are git
tags (`v1.0.0`); the version is never written in the source — `go build` /
`go install` stamp it from the tag (between tags you get a pseudo-version such
as `0.0.0-20260905163357-20ddd4c6b591`, with `+dirty` for uncommitted changes),
and `cbz --version` prints it. Changes are listed in [CHANGELOG.md](CHANGELOG.md).

What the version number protects: command names and flags, exit status, the
config file format and environment variables, and the `ComicInfo.xml` cbz
writes. Human-readable output (messages, summaries) may change in any release.

- **From 1.0.0:** a breaking change to any of the above bumps the major
  version, a new feature the minor, a fix the patch.
- **Before 1.0 (0.x):** a minor bump (0.9 → 0.10) may break things; a patch
  bump only fixes them. Breaking changes are marked **Breaking:** in the
  changelog.
- **Nix flake builds** can't see git tags, so they report
  `0-unstable-<commit>`; packaged releases (e.g. nixpkgs) carry the tag's
  version.

## License

cbztool is licensed under the GNU General Public License v3.0 or later
(SPDX: `GPL-3.0-or-later`).
