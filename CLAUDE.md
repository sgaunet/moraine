# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Operating Guidelines

**Read `docs/operating-guidelines.md` at the start of every session.** It
defines how to plan, verify, and iterate in this repository: plan mode,
subagent strategy, verification gates, self-improvement loop, and the
communication contract. Treat it as load-bearing context.

## Behavioral Guidelines

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

## Repository Overview

`moraine` is a single-binary, **pure-Go (no CGo)** command-line photo organizer.
It scans a source folder, groups photos into events by capture time, assigns each
group a theme, then **copies** them — plus each photo's companion (sidecar) files —
to `dest/<theme>/<year>/<year-month-day>/`. Originals are never modified or
deleted. Repo: `github.com/sgaunet/moraine` (MIT).

## Architecture

- **Three layers**: `main.go` (injects the build version, nothing else) →
  `internal/cli` (Cobra transport: `sort`/`clean`/`undo`/`version` + built-in
  `completion`, flags, exit codes, and `output.go` — the stdout contract) →
  `internal/app` (single testable orchestrator; `Organize`/`Clean`/`Undo` take an
  `onResult func(Result)` so the transport, not the domain, renders output) →
  domain packages. No domain package imports Cobra.
- **Stdout is data, stderr is logs** (Principle V): stdout carries the run result
  only — one `key=value` line (`--output=text`) or one JSON object with every
  per-file record plus the summary (`--output=json`); `internal/cli/output.go` owns
  those types and treats them as a public API. `sort` logs per-file lines at debug,
  `clean` at info (its dry-run plan is the product). An interrupt prints the partial
  summary, then `interrupted: copied N, …` with exit 1; photos never reached are not
  counted as errors. `--dry-run` writes nothing at all, not even a directory.
- **Procedural pipeline** in `app.Organize`: `scan → exifmeta` (worker pool sized by
  `GOMAXPROCS`) `→ cluster → classify → organize.Place`, tallying a `Summary`.
- **Typed config split** (`internal/config`): `New`/`NewClean` do pure syntax and
  cross-field checks, no I/O (usage errors → exit 2); the `Validate()` methods do
  filesystem checks and resolve the `<source>/_sorted` default (→ exit 1).
- **Copy-only, no-clobber, atomic**: `copyFile` stages a `.moraine-*.tmp` in the
  destination dir, fsyncs it, copies the source mtime (`exifmeta` falls back to mtime),
  publishes it with `os.Link` (`EEXIST` ⇒ never overwrites, never a truncated file on a
  canonical name; `os.Rename` fallback for link-less filesystems), then fsyncs the
  parent dir; `internal/contenthash` is the single content-identity source — `Hash`
  (SHA-256 index) for `clean`, `Equal` (byte compare) for `organize`'s skip-identical;
  collisions get a deterministic ` (N)` suffix; `safeJoin`/`ErrInvalidDestSubdir`
  block traversal.
- **Run manifest, undo, incremental** (`internal/manifest`, `internal/undo`): every
  non-dry `sort` appends one JSON Lines record per placed file (photo *or* companion,
  with its dest and the size/mtime it was left with) to
  `<dest>/.moraine/runs/<UTC-stamp>.jsonl`, created lazily by the first record and
  ordered by run id (a same-second run takes a `-N` suffix, so `Files` sorts by stem,
  never by file name). `moraine undo <dest>` unwinds the newest run: it removes only
  records whose action is `copied`/`renamed` and whose file still matches the recorded
  fingerprint, prunes emptied dirs, never leaves the dest root, and — after a clean
  `--delete` pass — renames the manifest `.undone` so the next `undo` steps back a run.
  `sort --incremental` (opt-in) loads every manifest into a source → record index,
  feeding `organize.Organizer.Placed`, which short-circuits `placeOne` when *both*
  source and copy still match; a cluster whose placed photos agree on one configured
  theme reuses it (`method=manifest`, no model call). A missing or unreadable manifest
  always degrades to a warning + full run. `clean` is untouched.
- **Injected extension points**: `classify.Classifier` (nil = skip the model stage),
  `organize.Organizer.IsPrimary func(string) bool` (keeps `organize` decoupled from
  `scan`), `rawpreview.Extractor`. A failed Ollama preflight degrades to the
  altitude heuristic, then the fallback theme.
- See `docs/architecture.md` for detailed design decisions.

## Development Commands

Tool versions are pinned in `mise.toml` (go 1.26.2, task, golangci-lint, goreleaser).

```bash
task build   # CGO_ENABLED=0 go build -o moraine .
task test    # go test -count=2 -race ./...
task lint    # golangci-lint run
task check-before-commit   # lint + test + snapshot

./moraine sort -d ~/Photos/sorted ~/Photos/2025
./moraine sort --incremental -d ~/Photos/sorted ~/Photos/2025   # skip what the manifest knows
./moraine clean -d ~/Photos/sorted ~/Photos/2025   # dry-run; --delete to commit
./moraine undo ~/Photos/sorted                     # dry-run; --delete to commit
```

## Code Quality Standards

**Linters configured** (do not duplicate rules):
- golangci-lint: `.golangci.yml` — v2, `default: all` minus 28 opinionated or
  style-only linters (incl. `errcheck`, `wrapcheck`, `err113`, `mnd`, `gosec`,
  `cyclop`, `funlen`, `paralleltest`); formatters `gofmt` + `goimports`. There are
  **no `exclude-rules`** — the same rules apply inside `_test.go`. Tree is lint-clean.
- CI (GitHub Actions, mirrored in `.forgejo/`): `linter.yml` → `task lint`,
  `test.yml` → `task test`, `snapshot.yml`/`release.yml` → GoReleaser. `pre-commit`
  hooks shell out to `task test`/`lint`/`build`.

**Key conventions:**
- Black-box tests (`package foo_test`); `export_test.go` is the only escape hatch to
  internals. Table-driven with `t.Run`. Fakes, not mock frameworks: `httptest` for
  Ollama, `internal/exiftooltest` (writes a fake `exiftool`) for the exec path.
- Wrap errors with `fmt.Errorf("context: %w", err)`. Only two sentinels exist:
  `rawpreview.ErrNoPreview`, `organize.ErrInvalidDestSubdir`.
- Per-photo failures are non-fatal — recorded in the run `Summary`, never abort.
- Destructive actions require an explicit flag (`clean` is dry-run until `--delete`).

## File Locations

- **Entrypoint**: `main.go` → **Transport**: `internal/cli` → **Orchestration**:
  `internal/app`
- **Domain**: `internal/{config,scan,exifmeta,cluster,classify,organize,clean,undo,
  manifest,photo,contenthash,rawpreview}`; fake-exec test helper in `internal/exiftooltest`
- **Tests**: co-located `internal/**/*_test.go`
- **Specs**: `specs/00N-*/` · **Constitution**: `.specify/memory/constitution.md`
- **Config**: `.golangci.yml`, `Taskfile.yml`, `mise.toml`, `.goreleaser.yml`

## Documentation

- `docs/architecture.md`: system design and component overview
- `docs/workflows.md`: development process, testing, and release
- `docs/patterns.md`: code patterns and conventions
- `docs/operating-guidelines.md`: how Claude Code should work here

<!-- SPECKIT START -->
Latest change: **issue #11** — run manifest + `undo` + `sort --incremental` (no
`specs/` dir; issue-driven like #5/#10/#13). The destination now gains a `.moraine/`
bookkeeping directory by default (additive, hidden, never read as photos since the
dest is excluded from the scan); `--dry-run` still writes nothing at all.

Previous feature: **006-sidecar-files** (companion/sidecar file copying & cleaning). Read the
current plan: `specs/006-sidecar-files/plan.md` (see also its `research.md`, `data-model.md`,
`contracts/cli.md`, `contracts/companion-matching.md`, `quickstart.md`). `sort` now, **by
default**, copies each photo's **companion (sidecar)** files from the photo's source directory
into the same destination folder, renaming them to track the photo's final name so the link
survives a collision rename. A companion of `IMG.jpg` is a same-dir regular file named either
(a) `IMG.jpg.<suffix>` (full-name prefix) or (b) `IMG.<other-ext>` (same base name, different
extension). Opt out with `--sidecars=false` (reproduces photos-only output byte-for-byte).
This is an intentional **v0 default-on behavior change** (additive, copy-only, reversible;
migration note shipped). Prior features implemented: **002-auto-photo-organizer** (core
pipeline; `spec.md` authoritative, `plan.md` lost), **003-raw-file-support** (RAW via exiftool
previews), **004-clean-originals** (`clean`; content-hash matching, dry-run default),
**005-cobra-cli-refactor** (Cobra `sort`/`clean`/`version` tree; `internal/cli` transport;
`config.New`/`NewClean` constructors; exit codes 0/1/2).

Sort pipeline: scan → EXIF (`--jobs` workers, default one per CPU) → temporal cluster
(`--gap`) → classify into a configurable theme set (default
`mountain`/`special-events`/`cook`/`family`, fallback `other`) → **copy** to
`dest/<theme>/<year>/<year-month-day>/` (+ companions, by default).

006 changes (domain placement only; transport surface gains one flag): companion placement
lives in `internal/organize` (new `sidecar.go` — `matchCompanion`/`companionTargetName`/
`placeCompanions`), reusing the existing `copyFile`/`sameContent`/`uniqueName`/`placeOne`
primitives (copy-only, `O_EXCL`, skip-identical, ` (N)` suffix). `Organizer` gains
`Sidecars bool`, an injected `IsPrimary func(string) bool` (excludes scanned images from
companion copying — keeps `organize` decoupled from `scan`), and a lazy per-source-dir
listing cache (linear discovery, SC-006). `organize.Result` gains `IsCompanion`/`Of`;
`app.Summary` gains companion counters; `app.Organize` builds the primary-path set and logs
companions distinctly. `config.Config`/`Options` add `Sidecars bool` (default true via the
`--sidecars` flag in `internal/cli/sort.go`). **`clean` is unchanged**: it deletes purely by
SHA-256 content identity, so copied companions are already removed (proven by new tests;
never deletes an un-archived companion).

Project constitution: `.specify/memory/constitution.md` (**v2.0.0**, 9 principles).
Key constraints: single purpose, pipe-composable; reproducible static binary
(`CGO_ENABLED=0` + `-trimpath`, pinned toolchain, goreleaser v2 with checksums +
SBOM); thin cobra commands over domain packages that import no CLI package (no
`utils`/`helpers`/`common`/`base`); concrete types over generics (3-implementation
rule); errors wrapped with `%w`, `log/slog` only; **pipe-safe UX — data-only stdout
with `--output=text|json`, logs/errors/progress on stderr, `NO_COLOR` + TTY
respected, `--quiet`/`--verbose`, exit codes 0/1/2 documented in `--help` and
tested**; destructive actions need explicit opt-in (`clean` dry-run + `--delete`);
`context.Context` + `SIGINT`/`SIGTERM` cancellation, explicit I/O timeouts; TDD with
black-box `package foo_test` + `export_test.go`, `go test -count=2 -race ./...`, plus
one end-to-end test of the built binary; generators pinned as `tool` deps with output
committed; stdlib first, new deps need author approval (MIT/BSD/Apache-2.0 only),
`govulncheck ./...` in CI. Conflicts resolve in favour of composability and a stable
stdout contract. **Known carried-over non-compliance is listed in that file's Sync
Impact Report — read it before planning UX or release work.**
<!-- SPECKIT END -->
