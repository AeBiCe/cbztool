# AGENTS.md

Working notes for AI agents and contributors on `cbz`. These reflect decisions
made deliberately — follow them unless the user says otherwise.

# The repository
Dependencies are defined in devenv.nix and the work is done on a nixos machine. Unless otherwise specified you are not to modify or alter the `devenv.nix` file.

## What cbz is

A small, fast, security-conscious CLI for **`.cbz` (ZIP) comic archives only**
(`.cbr`/`.cb7` are explicitly out of scope). It builds archives from loose
images (`pack`), lists contents, validates page sequences, and
reads/writes/removes `ComicInfo.xml` metadata (`info`/`tag`/`strip`). See
README.md for usage.

## Golden rules

1. **Standard library only.** Zero third-party dependencies is a hard design
   constraint (smaller attack surface for a tool parsing untrusted ZIPs). Do not
   add modules to `go.mod`. `regexp`, `encoding/xml`, `archive/zip`, `net/http`,
   etc. are all stdlib and fine.
2. **Keep it simple.** Prefer the obvious, readable solution. The CLI is plain
   `flag` + a `switch` dispatch — don't introduce a CLI framework.
3. **Security invariants** (in `archive.go`) are non-negotiable:
   - zip-slip: reject entry names that are absolute, contain `\`, or escape via `..`.
   - decompression-bomb caps: per-entry, total, and entry-count limits.
   - edits are **atomic**: write a temp file in the same dir, then `rename`;
     never modify an archive in place. Copy entries with `CreateRaw` to preserve
     bytes exactly. Resolve symlinks first (`realTarget`) so the rename replaces
     the target, not the link; keep the target's permissions (new archives 0644).
4. **Never mutate the user's real `.cbz` files when testing.** Copy to `/tmp`
   first. The user's `comic.cbz` and other dummies are not scratch space.

## Changelog

Record every user-visible change under `## [Unreleased]` in `CHANGELOG.md`
(Keep a Changelog sections: Added / Changed / Fixed / Removed), as part of the
same change. Mark breaking changes with **Breaking:**.

## Versioning

Semver from git tags only: the user tags releases (`vX.Y.Z`) by hand — never
write a version number into the source, and don't add release scripts.
`internal/version` reads it from the build info (or `-ldflags -X …/internal/version.version=…`
for Nix builds). At release time the user moves `[Unreleased]` in
CHANGELOG.md under the new version.

## Build, format, test (run all before claiming done)

```sh
gofmt -l .          # must print nothing
go vet ./...        # must be clean
go test ./...       # must pass
go build -o cbz ./cmd/cbz
```

Honor editor/linter diagnostics, including modernization hints (e.g. prefer
`strings.SplitSeq`/`FieldsSeq` over `Split`/`Fields` in range loops). Go is
provided via `devenv` (1.26+); if `go`/`gofmt` aren't on `PATH`, they live at
`.devenv/profile/bin` — prepend it: `export PATH="$PWD/.devenv/profile/bin:$PATH"`.

## Code conventions

- Thin `package main` dispatcher over focused `internal/` packages — see the
  Project layout table in README.md. Keep the dependency flow one-way
  (`command → {archive, comicinfo, config, filename, provider}`;
  `archive`/`config`/`provider → comicinfo`; `archive → imagemeta`;
  `provider → version`; `command`/`config`/`cmd/cbz → term`); never
  introduce an import cycle.
  `config` doesn't import `provider`: provider names are checked through the
  registry `command` passes to `config.Load`.
- Each command lives in `internal/command` as an exported handler taking
  `args []string`, parsing its own `flag.FlagSet`, and working on a single
  `.cbz` **or** a directory (via `cbzFiles`). `cmd/cbz/main.go` only routes.
- Comment density and naming should match the surrounding code.
- Multi-file commands report through `batch` (`batch.go`: numbered lines, end
  summary) or `failedErr`, and **return an error when any file failed** so the
  exit status is non-zero. Skips are not failures.
- Output: one line per file with exactly one outcome (`tagged`,
  `skipped: …`, `failed: …`); extra context is indented under the line it
  belongs to, never printed before it. Mark warnings `[!]` and errors `[X]`
  via `internal/term` (`term.Warn`/`Error`, `Warnf`/`Errorf`) — no other
  ad-hoc `cbz: warning:` prefixes. The skipped/failed summary lists every file.

## ComicInfo conventions

- `ComicInfo` (in `comicinfo.go`) models the **full v2.1 schema in XSD document
  order** — order matters for schema validity, so keep new fields in sequence.
- Every optional field is `,omitempty`. **Empty value ⇒ element omitted**, never
  an empty tag. `<Pages>` uses a `*Pages` pointer so it's dropped when nil.
- `PageCount` and `<Pages>` are **always computed from the archive's page
  images**, never trusted from a provider. Both are set in `saveComicInfo`, the
  single write path — route every ComicInfo write through it. `<Pages>` keeps
  hand-set Type/Bookmark/Key when the page count is unchanged
  (`archive.ComicPages`).
- Comma-separated fields (Writer/Genre/Tags/Characters) are single elements;
  trim tokens, drop empties (`comicinfo.CleanCSV`).
- `tag -m` (manual) is fully local: prompts, a JSON file (`-f`, template via
  `-t`), or `-s Field=Value`; the JSON file and `-s` share one field layer
  (`comicinfo.Set`), empty values are skipped. `tag` (online) fills as much of
  v2.1 as the provider exposes.

## Tag / pack / provider conventions

- Backends implement the `Provider` interface (`provider.go`) and self-register
  via `registerProvider`. **Do not hard-wire a single
  API.** By default `tag` combines every configured provider (`combine` in
  `tag_merge.go`): resolve on the first in `providers.order` (default Metron;
  the next is tried only on a clean no-match), then fetch the same issue
  from the others **by id** (`Issue.XRef`, `XRefLookup`), and merge with
  `comicinfo.Merge` (preferred provider wins single values and creator roles;
  Genre/Tags/Characters/Teams/Locations/StoryArc unioned without duplicates),
  then apply per-field `providers.prefer` overrides (`applyPrefer`).
  `ComicInfo.Marshal` writes element text itself (only `& < >` + `\r` escaped,
  illegal chars → U+FFFD) so the XML stays readable; never interpolate values
  anywhere but escaped text, and keep `TestMarshalInjection` passing. `-p`
  restricts to one provider. ComicVine ids carry their `4000-`/`4050-` prefix;
  bare numbers are Metron. Never write `Count` from a provider. GCD is deferred
  (experimental API).
- Matching is **ID-based and human/heuristic-disambiguated**, not fuzzy: parse
  filename → search series → auto-match when name (+year tiebreak) is unique →
  resolve issue by number. Ambiguous single files fall back to a picker; batch
  mode skips and reports them.
- Matching is **edition-aware** (`preferEdition`): TPB/HC/GN/Omnibus/etc.
  variants must agree with the filename's edition intent.
- `pack` is **pure creation** — it builds an archive and never prompts on its
  own. `-t` opts into chaining a `tag`; `-m` (which requires `-t`) makes that
  chained tag manual. `pack` output is a sibling `.cbz`, written atomically via
  `archive.BuildArchive` (Store, not Deflate; same safeName + size caps), and it
  never touches the source directory. Its validation is **advisory** (warn,
  never block).
- Be a polite client: per-provider rate limiting, sensible `User-Agent`,
  timeouts via `context`. (On-disk response caching is a planned improvement.)
- Robust **filename parsing** (`filename.go`) is a first-class concern — when it
  gains capability, extend the table tests in `filename_test.go`.

## Secrets

- Never commit real credentials. The config (`$XDG_CONFIG_HOME/cbz/config.json`,
  plus an optional `-c`/`CBZ_CONFIG` layer) holds **settings only**; secrets
  come from `password_file`/`api_key_file` or env vars, and an inline secret is
  rejected. Keep it that way — the config must stay safe to share (dotfiles,
  home-manager / Nix store).
- `config.Load` never reads secret files; commands that contact a provider
  call `cfg.ResolveSecrets()` (as `openProviders` does), so offline commands
  don't depend on credentials.
- `config.example.json` must equal `config.Example` (`cbz config init` output);
  `TestExampleMatchesRepoFile` enforces it. New settings go in both, with a
  built-in default in `config.Default`.
- Env vars override the files: `CBZ_PROVIDER`, `CBZ_METRON_USERNAME`,
  `CBZ_METRON_PASSWORD`, `CBZ_COMICVINE_API_KEY`.
- When testing in a sandbox that can't read `~/.config/cbz`, point
  `XDG_CONFIG_HOME` at a scratch dir and pass a sample config with `-c`.

## Testing conventions

- Table-driven tests, **offline** — no network in tests. Provider mappings are
  tested by unmarshalling sample JSON into the response structs and asserting the
  resulting `ComicInfo` (see `metron_test.go`).
- Add fixtures/cases when fixing a parsing or mapping bug.
- `imagemeta` parses untrusted image headers by hand (WebP/BMP/AVIF): keep every
  offset bounds-checked, add a real-encoder fixture to `testdata/` for any new
  format or variant, and run `go test ./internal/imagemeta -fuzz FuzzDecode`
  after changing a parser.

## Working style (what the user values)

- Discuss approach and trade-offs **before** large changes; give a clear
  recommendation with reasoning, not an exhaustive menu. Be concise.
- Favor simplicity and correctness over cleverness.
- Naming: the project is **cbztool**; the command is **`cbz`** (it handles only
  `.cbz`, so the format name is accurate, and `cbz pack` reads naturally). The
  module is `github.com/AeBiCe/cbztool`; `cmd/cbz` holds `package main`.
