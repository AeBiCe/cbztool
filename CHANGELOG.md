# Changelog

All notable changes to cbztool are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and cbztool adheres to
[Semantic Versioning](https://semver.org/) from 1.0.0 on.

## [Unreleased]

Work toward 1.0.0.

### Added

- `cbz --version` (and `cbz version`) prints the version, taken from the git
  tag at build time (a pseudo-version between tags); the API User-Agent
  carries it too (`cbztool/<version> (+https://github.com/AeBiCe/cbztool)`).
- Licensed under GPL-3.0-or-later.

- **Layered configuration** (`internal/config`). Settings are read from
  built-in defaults, then `$XDG_CONFIG_HOME/cbz/config.json`, then a file given
  with the new global `-c`/`--config` flag (or `CBZ_CONFIG`), then environment
  variables; each layer overrides only the keys it sets. Unknown keys are an
  error.
  - `providers.order` — provider precedence (replaces the single `provider` key).
  - `providers.prefer` — per-field provider overrides, e.g.
    `{"Summary": "comicvine"}`; `Year` stands for the whole date, and fields
    merged from every provider can't be preferred.
  - `providers.cache_ttl` — how long API responses are cached (default `24h`).
  - `rename.template` — the `cbz rename` name template.
  - `credentials.<provider>` — `username`, plus `password_file` /
    `api_key_file` pointing at secret files (absolute or `~/`; agenix and
    sops-nix secrets work directly).
- Secret files are read only by commands that contact a provider, so a
  missing API key file never breaks an offline command.
- `cbz config path | show | init` — list the files read, print the effective
  settings (secrets shown only as "(set)"), or write a starter config.
- **Page integrity checks in `cbz validate`**: every page's header is decoded,
  and a page that isn't a readable image fails validation. `--full` also reads
  every page to the end to verify its checksum. OS junk and extra files are
  reported as warnings.
- `cbz clean [--extra] [--dry-run]` — remove OS junk (`__MACOSX/`, `._*`,
  `.DS_Store`, `Thumbs.db`, `desktop.ini`) from archives; `--extra` also
  removes entries that are neither pages nor `ComicInfo.xml`. Kept entries are
  copied byte-for-byte.
- **`<Pages>` is filled on every ComicInfo write**: each page's image index,
  size, width and height, `DoublePage` for pages wider than they are tall, and
  `FrontCover` on the first page. Hand-set page types, bookmarks and keys are
  kept across rewrites while the page count is unchanged. `cbz info` prints a
  one-line summary of `<Pages>`.
- `cbz rename [-t template] [-n] <path>` — rename archives from their
  `ComicInfo.xml`. Templates use `{Field}`, zero-padding (`{Number:3}`), and
  optional groups (`[ ({Year})]`) that drop out when a field is empty; the
  default is `{Series}[ ({Year})] #{Number:3}`. Characters not allowed in file
  names are replaced, and a rename never overwrites an existing file.
- `cbz edit <file.cbz>` — edit one archive's metadata as JSON in
  `$VISUAL` / `$EDITOR` (default `vi`, run without a shell). Every field is
  listed, and emptying a value removes the field. Invalid input offers to
  reopen the editor, and an aborted edit keeps your changes in the temp file.
  Nothing is written when nothing changed; the changed fields are listed
  after saving.
- `cbz stats [-r] [-v] <path>` — library summary: archives, size, pages,
  series, tagged/untagged, and counts of archives with page-numbering problems
  or OS junk (`-v` lists the files behind each count). Reads only each ZIP
  directory and `ComicInfo.xml`.
- `cbz dupes [-r] <dir>` — find archives whose pages are byte-identical, from
  the CRC-32s and sizes in the ZIP directory (nothing is decompressed), so
  renamed, repacked, re-tagged and cleaned copies are matched. Never deletes.
- `cbz missing [-r] [-p provider] [--series-id id] [--offline] <dir>` — for
  each series, list the issues you don't have. Archives are identified by
  their `ComicInfo.xml` (falling back to the filename), series are matched as
  `cbz tag` matches them (or pinned with `--series-id`), and the provider's full
  issue list is compared by number; consecutive gaps are shown as ranges, and
  local issues the provider doesn't list are reported too. `--offline` needs
  no credentials and reports gaps in the local numbering only.
- `-r` on `stats`, `dupes` and `missing` includes subdirectories (symlinked directories
  are not followed, so link loops are safe).
- `internal/imagemeta` — image format and dimensions from the header alone:
  the standard library for JPEG/PNG/GIF, bounds-checked parsers for WebP
  (VP8/VP8L/VP8X), BMP and AVIF, tested against real encoder output and fuzzed.

### Changed

- **Breaking: renamed.** The project is now **cbztool** and the command is
  **`cbz`** (was `cb`). Along with it:
  - Go module `github.com/AeBiCe/cbztool`; install with
    `go install github.com/AeBiCe/cbztool/cmd/cbz@latest`.
  - Config and cache move to `$XDG_CONFIG_HOME/cbz/` and `$XDG_CACHE_HOME/cbz/`
    (move an existing `~/.config/cb` to `~/.config/cbz`; the old cache can
    simply be deleted).
  - Environment variables are `CBZ_CONFIG`, `CBZ_PROVIDER`,
    `CBZ_METRON_USERNAME`, `CBZ_METRON_PASSWORD`, `CBZ_COMICVINE_API_KEY`.
  - Online tagging notes its sources as `Tagged by cbztool: …` in `Notes`.
  - The Nix flake package is `cbztool` (`mainProgram = "cbz"`).

- **Breaking: config file layout.** The old top-level `provider`, `metron` and
  `comicvine` keys are rejected with a migration hint; see
  `config.example.json` or `cbz config init`.
- **Breaking: secrets are no longer allowed inline** in the config file
  (`password`, `api_key`). Use `password_file` / `api_key_file` or the
  `CBZ_METRON_PASSWORD` / `CBZ_COMICVINE_API_KEY` environment variables. The
  config file can now be shared (dotfiles, home-manager) safely; the
  permission warning moved from the config file to the secret files.
- `CBZ_PROVIDER` now moves the named provider to the front of
  `providers.order`.
- OS junk with an image extension (e.g. `__MACOSX/._001.jpg`) is no longer
  counted as a page, which fixes inflated `PageCount` values and spurious
  "duplicate page" reports.
- `cbz validate` now fails archives containing a page that doesn't decode as
  an image.
- `PageCount` is now set in one place (the shared write path) for every
  command that writes `ComicInfo.xml`.
- **Clearer output.** Warnings are marked `[!]` and errors `[X]` across all
  commands (yellow / red on a terminal; `NO_COLOR` turns color off), replacing
  the `cbz:` / `cbz: warning:` prefixes.
  - `cbz tag` on a directory prints a header per series saying what it matched
    (name, year, publisher, provider and id) with its files indented under it,
    each with its issue number and one outcome: `tagged`, `skipped: …` or
    `failed: …`. Provider warnings appear under the file they concern.
  - Series ids are shown the way `--series-id` takes them
    (`comicvine 4050-98801`, `metron 5`).
  - Owning only part of a series is no longer reported as "incomplete".
  - `-v` drops the `[verbose]` prefix and the separate file listing; it adds the
    parsed fields and series candidates under the series or file they explain.
  - Single-file `tag` prints the file name, the matched series and
    `tagged as Angor #1 "Runaway" (…)` instead of the parsed-fields and
    auto-match lines. A file that's already tagged is now skipped (exit 0)
    rather than an error, as in batch mode.
  - `cbz validate` marks problems `[X]` and warnings `[!]`; `cbz missing` and
    `cbz rename` use the same markers.

### Fixed

- Archives named by volume only (`Angor v01 - Runaway (2017).cbz`,
  `Saga Vol. 2.cbz`) were skipped with "no issue number parsed"; the volume is
  now used as the issue number, and a subtitle after it no longer ends up in
  the series name.
