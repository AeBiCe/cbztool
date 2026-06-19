# AGENTS.md

Working notes for AI agents and contributors on `cb`. These reflect decisions
made deliberately — follow them unless the user says otherwise.

## What cb is

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
     bytes exactly.
4. **Never mutate the user's real `.cbz` files when testing.** Copy to `/tmp`
   first. The user's `comic.cbz` and other dummies are not scratch space.

## Build, format, test (run all before claiming done)

```sh
gofmt -l .          # must print nothing
go vet ./...        # must be clean
go test ./...       # must pass
go build -o cb .
```

Honor editor/linter diagnostics, including modernization hints (e.g. prefer
`strings.SplitSeq`/`FieldsSeq` over `Split`/`Fields` in range loops). Go is
provided via `devenv` (1.26+); if `go`/`gofmt` aren't on `PATH`, they live at
`.devenv/profile/bin` — prepend it: `export PATH="$PWD/.devenv/profile/bin:$PATH"`.

## Code conventions

- Thin `package main` dispatcher over focused `internal/` packages — see the
  Project layout table in README.md. Keep the dependency flow one-way
  (`command → {archive, comicinfo, filename, provider}`; `archive`/`provider →
  comicinfo`); never introduce an import cycle.
- Each command lives in `internal/command` as an exported handler taking
  `args []string`, parsing its own `flag.FlagSet`, and working on a single
  `.cbz` **or** a directory (via `cbzFiles`). `main.go` only routes.
- Comment density and naming should match the surrounding code.

## ComicInfo conventions

- `ComicInfo` (in `comicinfo.go`) models the **full v2.1 schema in XSD document
  order** — order matters for schema validity, so keep new fields in sequence.
- Every optional field is `,omitempty`. **Empty value ⇒ element omitted**, never
  an empty tag. `<Pages>` uses a `*ComicPages` pointer so it's dropped when nil.
- `PageCount` is **always computed from the archive's image entries**, never
  trusted from a provider.
- Comma-separated fields (Writer/Genre/Tags/Characters) are single elements;
  trim tokens, drop empties (`cleanCSV`).
- `tag -m` (manual) writes a small hand-entered subset; `tag` (online) fills as
  much of v2.1 as the provider exposes.

## Tag / pack / provider conventions

- Backends implement the `Provider` interface (`provider.go`) and self-register
  via `registerProvider`. Selection is config-driven. **Do not hard-wire a single
  API.** Order of preference: Metron (primary) → ComicVine (coverage) → GCD
  (deferred; experimental API).
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

- Never commit real credentials. `config.example.json` holds placeholders; the
  real file lives at `$XDG_CONFIG_HOME/cb/config.json` (`chmod 600`).
- Env vars override the file: `CB_PROVIDER`, `CB_METRON_USERNAME`,
  `CB_METRON_PASSWORD`, `CB_COMICVINE_API_KEY`.

## Testing conventions

- Table-driven tests, **offline** — no network in tests. Provider mappings are
  tested by unmarshalling sample JSON into the response structs and asserting the
  resulting `ComicInfo` (see `metron_test.go`).
- Add fixtures/cases when fixing a parsing or mapping bug.

## Working style (what the user values)

- Discuss approach and trade-offs **before** large changes; give a clear
  recommendation with reasoning, not an exhaustive menu. Be concise.
- Favor simplicity and correctness over cleverness.
- The tool is named for what it does, not the file format (it's `cb`, not `cbz`).
