# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Operating Guidelines

**Read `docs/operating-guidelines.md` at the start of every session.** It
defines how to plan, verify, and iterate in this repository: plan mode,
subagent strategy, verification gates, self-improvement loop, and the
communication contract. Treat it as load-bearing context.

## Repository Overview

`moraine` is a single-binary, **pure-Go (no CGo)** command-line photo
organizer. It scans a source folder, groups photos into events by capture
time, assigns each group a theme, then **copies** them to
`dest/<theme>/<year>/<year-month-day>/`. Originals are never modified or
deleted. Repo: `github.com/sgaunet/moraine` (MIT).

## Architecture

- **Layered pipeline** orchestrated by `internal/app.Organize()`:
  `scan → exifmeta → cluster → classify → organize`. `main.go` only parses
  config and calls `Organize`; it holds no domain logic.
- **Centralized typed config** (`internal/config`) splits `Parse` (syntax /
  flags, no I/O) from `Validate` (filesystem); exposes the `ErrHelp` sentinel.
- **Copy-only, no-overwrite**: destination opened with `O_EXCL`; SHA-256
  content hashing dedupes (skip-identical vs ` (N)` suffix-rename).
- **Interface-based classifier**: altitude heuristic → optional Ollama vision
  model (constrained to the theme set) → guaranteed fallback theme. Degrades
  gracefully when Ollama is unreachable (model stage is skipped).
- See `docs/architecture.md` for detailed design decisions.

## Development Commands

```bash
# Build (production: CGO disabled → single static binary)
CGO_ENABLED=0 go build ./...

# Test (race detector requires CGO)
CGO_ENABLED=1 go test ./... -race -count=1

# Lint
golangci-lint run

# Run — source is a positional arg; -dest defaults to <source>/_sorted
go run . [-dest <out>] [-gap 6h] [-themes a,b,c] <source-dir>
```

## Code Quality Standards

**Linters configured** (do not duplicate rules):
- golangci-lint: see `.golangci.yml` — v2, `default: all` (76 linters) with
  opinionated/stylistic ones disabled (err113, wrapcheck, mnd, gosec, cyclop,
  funlen, paralleltest, testpackage, …); `revive` `exported` off; errcheck/dupl
  relaxed in `_test.go`; gofmt + goimports. Tree is lint-clean.
- CI: `.github/workflows/ci.yml` runs the gofmt check, `go vet`,
  `go test -race`, and golangci-lint.

**Key conventions:**
- Black-box tests (`package foo_test`), table-driven with `t.Run` subtests;
  HTTP deps faked via `net/http/httptest` (no mock framework).
- Wrap errors with `fmt.Errorf("context: %w", err)`; use typed sentinels for
  machine-testable conditions (`config.ErrHelp`,
  `organize.ErrInvalidDestSubdir`).
- Per-photo failures are non-fatal — recorded in the run summary, never abort.

## File Locations

- **Source**: `internal/` (`app`, `config`, `scan`, `photo`, `exifmeta`,
  `cluster`, `classify`, `organize`)
- **Entrypoint**: `main.go`
- **Tests**: co-located `internal/**/*_test.go`
- **Specs / plans**: `specs/002-auto-photo-organizer/` (`plan.md`,
  `research.md`, `data-model.md`, `contracts/`, `quickstart.md`)
- **Constitution**: `.specify/memory/constitution.md`
- **Config**: `.golangci.yml`

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
