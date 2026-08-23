# Architecture

## System Overview

`moraine` is a layered, single-binary CLI with four subcommands (`sort`, `clean`,
`undo`, `version`). The `sort` pipeline (`scan → exifmeta → cluster → classify → organize`)
is wired exclusively behind the exported `internal/app.Organize()` function
(Constitution Principle III). The `clean` subcommand (delete originals already copied)
is wired behind `internal/app.Clean()`, backed by the pure-logic `internal/clean`
package, and the `undo` subcommand (remove the copies the last run made) behind
`internal/app.Undo()`, backed by `internal/undo` and the run manifest
(`internal/manifest`). The CLI transport lives in `internal/cli` (a **Cobra** command
tree): it binds flags, builds the typed config, runs the matching `app` function, and
maps the outcome to the exit code. `main.go` is a shim that injects the build version
and calls `cli.Execute`, holding no domain logic itself. Each stage is a distinct
package with a single, narrow responsibility, so business logic stays decoupled from
the CLI transport and from disk I/O — no domain package imports Cobra.

## Components

- **`internal/cli`** — the CLI transport: a Cobra command tree (root + `sort`/
  `clean`/`undo`/`version`, plus Cobra's built-in `completion`) that binds flags into
  `config.Options`/`CleanOptions`/`UndoOptions`, calls the config constructors and `app`
  orchestrators, and maps execution to exit codes 0/1/2 via a `runtimeError`
  marker. The only package that imports Cobra. `completion.go` holds the shell
  completion candidates; it derives them from `config.DefaultThemes` and
  `photo.Extensions()` so the suggestions cannot drift from what the parsers
  accept. `output.go` owns the **stdout contract** (see decision 8): the JSON
  document types, the one-line text summary, and the `reporter` that collects
  per-file records through the `app` orchestrators' `onResult` callback.
- **`internal/config`** — single immutable `Config`/`CleanConfig`/`UndoConfig` struct
  holding every runtime parameter; `New`/`NewClean`/`NewUndo` (syntax/cross-field
  checks, no I/O) is split from `Validate` (filesystem checks, default-destination
  resolution).
- **`internal/scan`** — walks the source tree, produces `[]Found`.
- **`internal/exifmeta`** — reads EXIF, turns `Found` into `[]photo.Photo`.
- **`internal/photo`** — core domain types (`Photo`, `Cluster`).
- **`internal/cluster`** — groups photos into events by capture-time `-gap`.
- **`internal/classify`** — assigns a theme to each cluster via the
  `Classifier` interface (optional Ollama → altitude heuristic → fallback; the
  model decides first so it sees the actual scene).
  For model input it reads JPEG/PNG directly and obtains RAW previews through
  the `RawExtractor` interface.
- **`internal/rawpreview`** — the only package that talks to **exiftool**:
  `EnsureAvailable` (mandatory startup probe) and `Extract` (largest embedded
  preview, captured in memory — never written to disk).
- **`internal/organize`** — copies files to
  `dest/<theme>/<year>/<year-month-day>/`, enforcing copy-only/no-overwrite. An
  injected `Placed` hook lets an incremental run skip a source whose recorded copy is
  still in place, expressed in sizes and times so the package needs no manifest
  dependency. Also
  copies each photo's **companion (sidecar)** files (`sidecar.go`) into the same
  folder, naming them to track the photo's final name; a caller-injected
  `IsPrimary` predicate keeps a scanned photo from being copied as a companion, so
  the package stays decoupled from `scan`.
- **`internal/contenthash`** — the single definition of content identity: same bytes,
  same file. It offers the two shapes its callers need — `Hash(path) → Sum` (streaming
  SHA-256) for indexing many files, used by `clean` to match originals to copies, and
  `Equal(a, b) → bool` (streaming byte compare, short-circuiting on the first
  difference) for the one-pair question `organize` asks when deduping a copy.
- **`internal/manifest`** — the record of what a run placed: one JSON Lines file per
  run under `<dest>/.moraine/runs/`, a header line plus one record per placed file
  (photo or companion) carrying its destination and the size/mtime it was left with.
  The file is created by the first record, so a dry run or an empty run writes nothing.
  It also reads manifests back — `Latest`/`ReadRun` for `undo`, `Load` into a
  source → record `Index` for an incremental `sort`.
- **`internal/undo`** — reverses one recorded run: it removes only files a record says
  that run *created* (`copied`/`renamed`, never `skipped-identical`) and only while
  they still match the recorded size and mtime, prunes the folders it empties, and
  never touches anything outside the destination root. Dry-run until `Delete`.
- **`internal/clean`** — the `clean` subcommand's filesystem logic: deletes source
  originals whose byte-identical copy exists under the destination, matching by
  content (never filename) and never touching the destination tree. Depends only on
  the filesystem and `contenthash` (no classifier/Ollama/exiftool).
- **`internal/app`** — orchestrates the sort pipeline (`Organize`), the clean run
  (`Clean`) and the undo run (`Undo`), tallying each run's summary. `manifest.go` is
  the seam between the pipeline and the manifest in both directions: it records every
  placement, and on an incremental run turns the index into the organizer's `Placed`
  hook and reuses a known event's theme.

## Design Decisions

1. **Thin entrypoint + `Organize()` facade** — `main.go` only injects the version and
   calls `cli.Execute`; the Cobra tree in `internal/cli` is the sole transport, keeping
   domain logic testable and independent of the CLI, satisfying the decoupling principle.
2. **New/Validate split + Cobra-owned parsing** — flag parsing lives in `internal/cli`
   (Cobra/pflag); `config.New`/`NewClean` do the no-I/O cross-field checks and `Validate`
   does the filesystem checks. `cli.Execute` silences Cobra's own output and classifies the
   returned error into exit codes: cross-field/parse/arity errors → usage (2), validation/
   dependency/run failures (wrapped with `asRuntime`) → runtime (1), help/version → 0.
3. **Copy-only + atomic publish + content comparison** — a copy is staged in a
   hidden temp file in the destination directory, fsynced, given the source's mtime,
   and published with `link(2)`, which fails `EEXIST` rather than overwriting. So
   overwriting is structurally impossible *and* a half-written copy can never occupy
   a canonical name: an interrupted run would otherwise leave a truncated stub there,
   and every later run — unable to tell a stub from different content — would
   suffix-rename the real photo and let the stub keep the good name. The parent
   directory is fsynced too, since durable bytes behind a lost directory entry are
   still a lost photo. Comparing content makes re-runs idempotent (skip identical,
   suffix-rename same-name/different-content). Originals are never touched.
4. **Interface-based classifier with guaranteed fallback** — a theme is
   always returned; the network/model stage is optional and degrades to the
   fallback when Ollama is unreachable.
5. **RAW via mandatory exiftool, HEIC via an optional converter** — neither
   format's pixels can be decoded in pure Go, but they fail differently and so are
   served by different programs. A RAW carries a full JPEG preview, which
   `internal/rawpreview` copies straight out with exiftool (`JpgFromRaw →
   PreviewImage → ThumbnailImage`), in memory, never writing a temp file. exiftool
   is **required**: `main.run()` calls `rawpreview.EnsureAvailable` after
   `config.Validate` and before `app.Organize`, so a missing dependency fails fast
   (exit 1) before any file is touched.

   A HEIC carries no such preview — measured on iPhone files, all three exiftool
   tags return zero bytes, because its derived images are HEVC-coded inside the
   HEIF container — so `internal/heicpreview` decodes it with the first of `sips`,
   `heif-convert`, `ffmpeg` or `magick` found on PATH, reading the JPEG from stdout
   where the tool allows and from a scratch file (removed before returning) where
   it does not. That converter is **optional** and `Detect` returns nil rather than
   an error when none exists: a HEIC photo is still scanned, dated, organized and
   copied, and only its group's classification falls back. `classify` keeps the two
   seams separate (`RawPreview`, `HEICPreview`) precisely because having one says
   nothing about having the other. Small events send every eligible photo (previews
   included); large events prefer JPEG/PNG and fill the sample with extracted
   previews (FR-012), which cost a process spawn each.
6. **Cheap, stable model calls** — `internal/classify` keeps what reaches Ollama
   small and what comes back meaningful. Images are downscaled to 1024 px on the
   long side before base64 (a vision model tiles its input, so the dimensions —
   not the file size — set the inference cost); a RAW or HEIC shot alongside its
   own JPEG is recognised by directory + base name and sent once; the cluster's
   capture span, highest altitude and location ride along as text. The model is
   loaded once per run by an empty-message `/api/chat` warm-up, issued outside any
   single classification's timeout and only when there is actually something to
   classify, and `keep_alive` holds it resident between clusters. Retries are
   reserved for transient failures (transport, 5xx, 408, 429) with exponential
   backoff: decoding is pinned to temperature 0 and a fixed seed, so re-asking
   after a rejected answer would only re-read the same answer.
7. **`clean` subcommand — dry-run by default, content-matched deletion** — feature
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
8. **Data on stdout, logs on stderr** — stdout carries the run result only, as one
   `key=value` line (`--output=text`) or one JSON object with every per-file record plus
   the summary (`--output=json`); logs, progress and errors go to stderr. Anything else
   written to stdout corrupts a downstream pipe, which is why Principle V makes this
   non-negotiable. The document types live in `internal/cli/output.go` rather than
   reusing `app.Summary` directly, so renaming an internal tally field cannot silently
   change what scripts parse — the wire format is a public API and is spelled out as
   one. The `app` orchestrators stay presentation-free: they hand every `Result` to an
   `onResult` callback (the shape `clean.Cleaner.Run` already used) and the transport
   decides how to render it. Per-file *narration* splits by command: `sort` logs it at
   debug, since a real library produces thousands of lines, while `clean` keeps it at
   info because previewing that plan is the point of running it.
9. **Dry run that writes nothing** — `--dry-run` reaches `organize` as
   `Organizer.DryRun`, which skips both the copy *and* the `MkdirAll`, so a preview
   leaves no empty folders either. Every `Result` still carries the Action the real run
   would take. To keep that promise across intra-run collisions, the organizer remembers
   the destination names it has already promised (`planned`) and feeds them to
   `uniqueName` alongside `exists` — otherwise two same-named photos would both be
   previewed as landing on the same path, under-reporting a rename the real run performs.
10. **Interrupt is a report, not a crash** — `organize.Place` records the context error
   against every photo it never reached; `app.tally` excludes those from the error count
   (nothing failed — nothing was attempted), and the transport prints the partial summary
   before returning `interrupted: copied N, …` with exit 1.

11. **The manifest is a shortcut, never an authority** — an incremental run trusts a
    record only while *both* ends still match it: the source must have the recorded
    size and mtime, and so must the copy. Any mismatch — an edited photo, a copy
    removed by hand, a partly undone run — falls through to the full byte comparison,
    so a stale manifest can only ever cost the skip, never correctness. `undo` applies
    the same rule before deleting anything, which is what makes "only removes what this
    run created" true rather than merely intended. Mtimes survive the round trip
    because a copy is stamped with its source's (see decision 3), so one recorded pair
    fingerprints both files.
12. **Theme reuse over re-classification** — on an incremental run a cluster whose
    already-placed photos agree on one still-configured theme keeps it
    (`method=manifest`), instead of asking the model again. That is not only cheaper:
    it keeps a photo added to an old event filed *with* that event, which classifying
    the newcomer on its own would not guarantee.

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

For `undo`: read the most recent manifest under the destination → walk its records
newest first → remove each file the run created that still matches its record → prune
the folders that emptied → mark the manifest `.undone` so the next `undo` steps back a
run. Nothing outside the destination root is touched, and sources are never read.

For `clean`: index destination files by size → walk the source (skipping the
destination subtree) → for each regular file, hash only on a size collision and
compare against the destination's same-size content sums → delete (or, in dry-run,
report) matches. Per-file errors are collected into the clean `Summary`; nothing
under the destination is ever removed.
