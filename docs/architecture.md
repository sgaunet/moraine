# Architecture

## System Overview

`moraine` is a layered, single-binary CLI with three subcommands (`sort`, `clean`,
`version`). The `sort` pipeline (`scan → exifmeta → cluster → classify → organize`)
is wired exclusively behind the exported `internal/app.Organize()` function
(Constitution Principle III). The `clean` subcommand (delete originals already copied)
is wired behind `internal/app.Clean()`, backed by the pure-logic `internal/clean`
package. The CLI transport lives in `internal/cli` (a **Cobra** command tree): it binds
flags, builds the typed config, runs the matching `app` function, and maps the outcome to
the exit code. `main.go` is a shim that injects the build version and calls
`cli.Execute`, holding no domain logic itself. Each stage is a distinct package with a
single, narrow responsibility, so business logic stays decoupled from the CLI transport
and from disk I/O — no domain package imports Cobra.

## Components

- **`internal/cli`** — the CLI transport: a Cobra command tree (root + `sort`/
  `clean`/`version`) that binds flags into `config.Options`/`CleanOptions`, calls the
  config constructors and `app` orchestrators, and maps execution to exit codes
  0/1/2 via a `runtimeError` marker. The only package that imports Cobra.
- **`internal/config`** — single immutable `Config`/`CleanConfig` struct holding every
  runtime parameter; `New`/`NewClean` (syntax/cross-field checks, no I/O) is split from
  `Validate` (filesystem checks, default-destination resolution).
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
  `dest/<theme>/<year>/<year-month-day>/`, enforcing copy-only/no-overwrite. Also
  copies each photo's **companion (sidecar)** files (`sidecar.go`) into the same
  folder, naming them to track the photo's final name; a caller-injected
  `IsPrimary` predicate keeps a scanned photo from being copied as a companion, so
  the package stays decoupled from `scan`.
- **`internal/contenthash`** — the single definition of content identity
  (`Hash(path) → Sum`, a streaming SHA-256). Shared by `organize` (dedup on copy)
  and `clean` (matching originals to copies) so both agree on "same content".
- **`internal/clean`** — the `clean` subcommand's filesystem logic: deletes source
  originals whose byte-identical copy exists under the destination, matching by
  content (never filename) and never touching the destination tree. Depends only on
  the filesystem and `contenthash` (no classifier/Ollama/exiftool).
- **`internal/app`** — orchestrates the sort pipeline (`Organize`) and the clean run
  (`Clean`), tallying each run's summary.

## Design Decisions

1. **Thin entrypoint + `Organize()` facade** — `main.go` only injects the version and
   calls `cli.Execute`; the Cobra tree in `internal/cli` is the sole transport, keeping
   domain logic testable and independent of the CLI, satisfying the decoupling principle.
2. **New/Validate split + Cobra-owned parsing** — flag parsing lives in `internal/cli`
   (Cobra/pflag); `config.New`/`NewClean` do the no-I/O cross-field checks and `Validate`
   does the filesystem checks. `cli.Execute` silences Cobra's own output and classifies the
   returned error into exit codes: cross-field/parse/arity errors → usage (2), validation/
   dependency/run failures (wrapped with `asRuntime`) → runtime (1), help/version → 0.
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
6. **`clean` subcommand — dry-run by default, content-matched deletion** — feature
   004. A source original is deleted only when a byte-identical copy (same
   `contenthash.Sum`) exists anywhere under the destination; filenames are never
   used, so suffix-renamed and skipped-identical copies still match. The default is
   a **dry run** (reports a plan, deletes nothing); `-delete` is required to remove
   files — satisfying the constitution's "destructive actions need an explicit flag".
   Matching is gated by a **file-size pre-filter**: a content hash is computed only
   when a source file's size matches some destination file's size (and destination
   files are hashed lazily, only for colliding sizes), which is correctness-preserving
   (equal content ⇒ equal size) and bounds I/O on large libraries. Safety invariants:
   files under the destination tree are never deleted (even when `-dest` is nested in
   the source, the default `<source>/_sorted` case); on any read/hash/permission error
   the original is retained (fail-safe); only regular files are considered (symlinks
   and special files are skipped); per-file failures are non-fatal and cancellation
   stops the run promptly.

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

For `clean`: index destination files by size → walk the source (skipping the
destination subtree) → for each regular file, hash only on a size collision and
compare against the destination's same-size content sums → delete (or, in dry-run,
report) matches. Per-file errors are collected into the clean `Summary`; nothing
under the destination is ever removed.
