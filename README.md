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
- **Online tagging** — pull rich metadata from a provider (Metron today) with
  accurate, ID-based matching and edition awareness (TPB/HC/GN/…), single files
  or whole directories.

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
cb tag --id <issueID> comic.cbz   # exact provider lookup, no matching
cb tag --pick comic.cbz           # force the series picker
cb tag --refresh comic.cbz        # ignore cached responses, refetch fresh
cb tag ./library/                 # batch: auto-match every .cbz, skip the unsure
cb tag -m comic.cbz               # type the fields (Series/Number pre-filled)
cb tag -m ./squirrel-man/         # one series: prompt shared fields once
```

Online matching parses the filename (series, number, year, edition), resolves
the series by exact name with a year tiebreak, then fetches the issue by number —
hands-off when unambiguous, a picker when not. `PageCount` is always taken from
the archive, never the provider.

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

Or use environment variables, which override the file (so nothing sensitive needs
to sit on disk):

```sh
export CB_PROVIDER=metron
export CB_METRON_USERNAME=you
export CB_METRON_PASSWORD=secret
```

Metron is the default backend. Huge thanks to Metron for their awesome API (https://metron.cloud/).
