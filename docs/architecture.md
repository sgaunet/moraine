# Architecture

## System Overview

`moraine` is a layered, single-binary CLI. The processing pipeline
(`scan → exifmeta → cluster → classify → organize`) is wired exclusively
behind the exported `internal/app.Organize()` function (Constitution
Principle III). `main.go` is a thin shell: it parses configuration and calls
`Organize`, holding no domain logic itself. Each stage is a distinct package
with a single, narrow responsibility, so business logic stays decoupled from
the CLI transport and from disk I/O.

## Components

- **`internal/config`** — single immutable `Config` struct holding every
  runtime parameter; `Parse` (syntax/flags, no I/O) is split from `Validate`
  (filesystem checks, default-destination resolution).
- **`internal/scan`** — walks the source tree, produces `[]Found`.
- **`internal/exifmeta`** — reads EXIF, turns `Found` into `[]photo.Photo`.
- **`internal/photo`** — core domain types (`Photo`, `Cluster`).
- **`internal/cluster`** — groups photos into events by capture-time `-gap`.
- **`internal/classify`** — assigns a theme to each cluster via the
  `Classifier` interface (altitude heuristic → optional Ollama → fallback).
  For model input it reads JPEG/PNG directly and obtains RAW previews through
  the `RawExtractor` interface.
- **`internal/rawpreview`** — the only package that talks to **exiftool**:
  `EnsureAvailable` (mandatory startup probe) and `Extract` (largest embedded
  preview, captured in memory — never written to disk).
- **`internal/organize`** — copies files to
  `dest/<theme>/<year>/<year-month-day>/`, enforcing copy-only/no-overwrite.
- **`internal/app`** — orchestrates the pipeline and tallies the run summary.

## Design Decisions

1. **Thin entrypoint + `Organize()` facade** — keeps domain logic testable
   and independent of the CLI, satisfying the decoupling principle.
2. **Parse/Validate split with `ErrHelp` sentinel** — syntactic parsing has
   no side effects; `-h` returns a machine-testable sentinel so `main.go`
   exits 0 via `errors.Is`. Flags are registered in one place to prevent drift
   between parsing and usage output.
3. **Copy-only + `O_EXCL` + SHA-256** — overwriting is structurally
   impossible; content hashing makes re-runs idempotent (skip identical,
   suffix-rename same-name/different-content). Originals are never touched.
4. **Interface-based classifier with guaranteed fallback** — a theme is
   always returned; the network/model stage is optional and degrades to the
   fallback when Ollama is unreachable.
5. **RAW via mandatory exiftool, previews in memory** — RAW pixels can't be
   decoded in pure Go, so `internal/rawpreview` shells out to exiftool to
   extract the embedded JPEG preview (`JpgFromRaw → PreviewImage →
   ThumbnailImage`) and feeds the bytes to the model without ever writing a
   temp file. exiftool is **required**: `main.run()` calls
   `rawpreview.EnsureAvailable` after `config.Validate` and before
   `app.Organize`, so a missing dependency fails fast (exit 1) before any file
   is touched. A RAW with no usable preview degrades like HEIC. Small events
   send every eligible photo (RAW included); large events prefer JPEG/PNG and
   fill the sample with RAW (FR-012).

## Integration Points

- **External APIs**: optional local **Ollama** vision model
  (`-ollama-url`, `-model`); a startup `Preflight()` returns a typed status
  and the model stage is skipped (set to `nil`) on any non-ready status.
- **External programs**: **exiftool** (required, `-exiftool`) for RAW preview
  extraction, invoked via `os/exec` (argument vector, timeout-bounded, no shell).
- **Database / queues**: none — the only persistent state is the copied
  output tree on the filesystem.

## Data Flow

Source files → `scan.Found` → `photo.Photo` (with EXIF) →
`[]photo.Cluster` (temporal) → theme label per cluster → copied to
`dest/<theme>/<year>/<year-month-day>/`. Per-photo errors are collected into
the run `Summary` rather than aborting the pipeline.
