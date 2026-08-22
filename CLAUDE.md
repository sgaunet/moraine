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
  `internal/cli` (Cobra transport: `sort`/`clean`/`version`, flags, exit codes) →
  `internal/app` (single testable orchestrator) → domain packages. No domain
  package imports Cobra.
- **Procedural pipeline** in `app.Organize`: `scan → exifmeta` (worker pool sized by
  `GOMAXPROCS`) `→ cluster → classify → organize.Place`, tallying a `Summary`.
- **Typed config split** (`internal/config`): `New`/`NewClean` do pure syntax and
  cross-field checks, no I/O (usage errors → exit 2); the `Validate()` methods do
  filesystem checks and resolve the `<source>/_sorted` default (→ exit 1).
- **Copy-only, no-clobber**: destinations opened `O_EXCL` then fsynced;
  `internal/contenthash` (SHA-256) is the single content-identity source, shared by
  `organize` (skip-identical) and `clean` (match originals); collisions get a
  deterministic ` (N)` suffix; `safeJoin`/`ErrInvalidDestSubdir` block traversal.
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
./moraine clean -d ~/Photos/sorted ~/Photos/2025   # dry-run; --delete to commit
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
- **Domain**: `internal/{config,scan,exifmeta,cluster,classify,organize,clean,photo,
  contenthash,rawpreview}`; fake-exec test helper in `internal/exiftooltest`
- **Tests**: co-located `internal/**/*_test.go`
- **Specs**: `specs/00N-*/` · **Constitution**: `.specify/memory/constitution.md`
- **Config**: `.golangci.yml`, `Taskfile.yml`, `mise.toml`, `.goreleaser.yml`

## Documentation

- `docs/architecture.md`: system design and component overview
- `docs/workflows.md`: development process, testing, and release
- `docs/patterns.md`: code patterns and conventions
- `docs/operating-guidelines.md`: how Claude Code should work here

<!-- SPECKIT START -->
Active feature: **006-sidecar-files** (companion/sidecar file copying & cleaning). Read the
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

Sort pipeline: scan → EXIF → temporal cluster (`--gap`) → classify into a configurable theme
set (default `mountain`/`special-events`/`cook`/`family`, fallback `other`) → **copy** to
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

Project constitution: `.specify/memory/constitution.md` (v1.0.0). Key constraints:
pure Go / no CGo / single binary; business logic decoupled from transport & storage;
test-first (`go test ./... -race`, happy + failure paths); typed centralized config;
never overwrite/lose a file (content-hash identity); destructive actions require an
explicit documented flag (`clean` dry-run default + `--delete`); CLI errors machine-readable
& actionable with exit codes 0/1/2.
<!-- SPECKIT END -->
