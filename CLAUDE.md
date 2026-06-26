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
Active feature: **003-raw-file-support** (add camera RAW inputs; classify them via
exiftool-extracted embedded previews). Read the current plan:
`specs/003-raw-file-support/plan.md` (see also its `research.md`, `data-model.md`,
`contracts/`, `quickstart.md`). Base feature **002-auto-photo-organizer** is implemented;
its `spec.md` remains the authority for the core pipeline (its `plan.md` was lost and not
reconstructed).

Pipeline: scan → EXIF → temporal cluster (`-gap`) → classify into a configurable theme set
(default `mountain`/`special-events`/`cook`/`family`, fallback `other`) → **copy** to
`dest/<theme>/<year>/<year-month-day>/`. 003 adds: RAW extensions in `photo`; new
`internal/rawpreview` (mandatory exiftool probe + in-memory preview extraction); an
image-bytes provider in `classify/ollama.go`; `ExifToolPath` in `config`; a mandatory
exiftool preflight at the top of `app.Organize`. `scan`/`exifmeta`/`cluster`/`organize`
are unchanged by 003.

Project constitution: `.specify/memory/constitution.md` (v1.0.0). Key constraints:
pure Go / no CGo / single binary; business logic decoupled from transport & storage;
test-first (`go test ./... -race`, happy + failure paths); typed centralized config;
copy-only this feature (originals preserved); never overwrite/lose a photo (content-hash
identity → skip-identical / suffix-different); CLI errors machine-readable & actionable.
<!-- SPECKIT END -->
