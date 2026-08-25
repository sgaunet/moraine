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
to `dest/<theme>/<year>/<year-month-day>/` (the layout is a `--path-template`,
defaulting to that). Originals are never modified or deleted unless `--move` is asked
for explicitly, and then only after the copy has been verified. Repo: `github.com/sgaunet/moraine` (MIT).

## Architecture

- **Three layers**: `main.go` (injects the build version, nothing else) →
  `internal/cli` (Cobra transport: `sort`/`clean`/`undo`/`version` + built-in
  `completion`, flags, exit codes, and `output.go` — the stdout contract) →
  `internal/app` (single testable orchestrator; `Organize`/`Clean`/`Undo` take an
  `onResult func(Result)` so the transport, not the domain, renders output) →
  domain packages. No domain package imports Cobra.
- **Stdout is data, stderr is logs** (Principle V): stdout carries the run result
  only — one `key=value` line (`--output=text`) or one JSON object with every
  per-file record, an `events` array (JSON only — the text line is one line per run)
  and the summary (`--output=json`); `internal/cli/output.go` owns those types and
  treats them as a public API. Summary keys are additive and read by name, never by
  position. `bytes_copied`/`bytes_skipped` (and companion equivalents) report the
  volume written and the volume an identical destination spared. `sort` logs per-file lines at debug,
  `clean` at info (its dry-run plan is the product). An interrupt prints the partial
  summary, then `interrupted: copied N, …` with exit 1; photos never reached are not
  counted as errors. `--dry-run` writes nothing at all, not even a directory.
- **Procedural pipeline** in `app.Organize`: `scan → exifmeta` (worker pool sized by
  `--jobs`, default `GOMAXPROCS`) `→ cluster → classify → organize.Place`, tallying a
  `Summary`. Every stage honours the run's context: `scan` stops at the next directory
  entry, `readMeta` stops taking on new files, and neither counts what it never
  reached. Between scan and exifmeta, `checkFreeSpace` compares the scanned bytes
  against `diskspace.Available(destRoot)` and warns — never aborts — when they do not
  fit. **Three stages are concurrent, the tallies are not**: `readMeta`, `labelAhead`
  (one producer classifying up to `lookAhead`=2 events ahead of the one being placed)
  and `organize.Place`'s copy pool. `tally`/`tallyEvent`/`rec.add`/`onResult` all stay
  on `Organize`'s own goroutine, which is why `Summary`, `manifest.Writer` and
  `cli.reporter` need no synchronisation.
- **Typed config split** (`internal/config`): `New`/`NewClean` do pure syntax and
  cross-field checks, no I/O (usage errors → exit 2); the `Validate()` methods do
  filesystem checks and resolve the `<source>/_sorted` default (→ exit 1).
- **Config file** (`internal/configfile` + `internal/cli/configfile.go`): optional
  YAML at `--config`, `$MORAINE_CONFIG`, `$XDG_CONFIG_HOME/moraine.yaml`, then
  `~/.config/moraine.yaml` (**not** `os.UserConfigDir()` — on macOS that is
  `~/Library/Application Support`). Precedence is **flag > file > default**, decided
  by `cmd.Flags().Changed`, never by comparing against a default. Strict decoding, so
  an unknown key is exit 2. `--dry-run`/`--delete`/`--incremental`/`--quiet`/
  `--verbose` and the positional source are deliberately **not** configurable (mode
  flags stay per-invocation; Principle V). `MORAINE_CONFIG=` (empty) disables the file
  — which is how `internal/cli`'s `TestMain` keeps the suite off a developer's real
  config.
- **`--move` (opt-in, verified)**: the only thing that removes a source, and only
  after `verifyCopy` re-reads the published file and matches it against the SHA-256
  `copyFile` accumulated while writing (an `io.MultiWriter` — hashing the stream keeps
  it at two full reads, not three). A mismatch removes the *destination* and keeps the
  source. Never removes on a skip (a skip verifies nothing that run; the incremental
  skip never reads the bytes), an error, a cancellation, or `--dry-run`. Every removal
  funnels through `Organizer.copy`. `organize.Result`/`manifest.Record` gain `Moved`,
  `Summary`/stdout gain `moved`, and **`undo` refuses a moved record** — the original
  is gone, so the copy is the only file left. A move run cannot be undone.
- **Copy-only by default, no-clobber, atomic**: `copyFile` stages a `.moraine-*.tmp` in the
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
  feeding `organize.Organizer.Placed`, which short-circuits `resolveOne` when *both*
  source and copy still match; a cluster whose placed photos agree on one configured
  theme reuses it (`method=manifest`, no model call). A missing or unreadable manifest
  always degrades to a warning + full run. `clean` is untouched.
- **Two-phase placement** (`organize.Place`): `plan` resolves every destination name
  serially, in cluster order, reserving each in `Organizer.reserved` (`dst → src`);
  `execute` then copies the bytes with `--jobs` workers, writing into a pre-sized
  result slice indexed by plan position. Resolving names on one goroutine is what
  keeps ` (N)` allocation deterministic, the variant indices contiguous (which
  `existingIdentical` walks), and `results[]`/`events[]` in the order a serial run
  produced. `contentAt` is the single resolver: a reserved-but-unwritten destination
  answers with the **source** that reserved it, so an intra-run duplicate still
  compares real bytes. The unit of concurrent work is a photo **plus its companions**,
  since a photo whose write fails places none. `Place` clears `reserved` on return
  unless `DryRun` — a dry run has no files to speak for themselves.
- **Injected extension points**: `classify.Classifier` (nil = skip the model stage),
  `organize.Organizer.IsPrimary func(string) bool` (keeps `organize` decoupled from
  `scan`), and two `classify.PreviewExtractor` seams: `rawpreview.Extractor`
  (`RawPreview`, exiftool, **mandatory**) and `heicpreview.Converter` (`HEICPreview`,
  the first of sips/heif-convert/ffmpeg/magick on PATH, **optional** — `Detect`
  returns `(*Converter, bool)`, and only a `true` may be assigned into the
  interface: a nil `*Converter` in there reads as "configured"). Both seams hand
  the external program its path through an `operand()` that neutralises a leading
  `-` with an explicit `./`; exiftool additionally gets its documented `--`. A HEIC
  embeds no JPEG preview for exiftool to copy out (its derived images are HEVC),
  which is why the two are separate. A failed Ollama preflight
  degrades to the altitude heuristic, then the fallback theme.
- **Model-call economy** (`internal/classify`): images are downscaled to 1024 px on
  the long side (`downscale.go`, `golang.org/x/image/draw`) before base64; a RAW or
  HEIC twin of a JPEG in the same directory is sent once; the cluster's capture
  span, max altitude and GPS ride along as prompt text; the model is warmed up once
  per run (empty-message `/api/chat`, outside any classification's timeout, only
  when something is actually classified) and held with `keep_alive`; only transient
  failures (transport, 5xx, 408, 429) are retried, with exponential backoff. The HTTP
  client carries an explicit `Transport` cloned from `DefaultTransport` (whose
  `MaxIdleConnsPerHost` of 2 throttles keep-alive under concurrency) and drains every
  response body before closing, bounded by a byte cap **and** the request context.
  `--vote` fans its per-photo calls out `defaultVoteWorkers`=2 wide — not wider,
  because a vote's timeout starts when its goroutine does, so with Ollama serialising
  per model a 4-wide fan-out makes the last vote time out for the same wall-clock.
- See `docs/architecture.md` for detailed design decisions.

## Development Commands

Tool versions are pinned in `mise.toml` (go 1.26.6, task, golangci-lint, goreleaser,
govulncheck).

```bash
task build   # CGO_ENABLED=0 go build -o moraine .
task test    # go test -count=2 -race ./...
task lint    # golangci-lint run
task vuln    # govulncheck ./...
task check-before-commit   # lint + test + snapshot + vuln

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
- Wrap errors with `fmt.Errorf("context: %w", err)`. Only three sentinels exist:
  `rawpreview.ErrNoPreview`, `organize.ErrInvalidDestSubdir`, `exifmeta.ErrEXIFPanic`.
- Per-photo failures are non-fatal — recorded in the run `Summary`, never abort.
- Destructive actions require an explicit flag (`clean` is dry-run until `--delete`).

## File Locations

- **Entrypoint**: `main.go` → **Transport**: `internal/cli` → **Orchestration**:
  `internal/app`
- **Domain**: `internal/{config,scan,exifmeta,cluster,classify,organize,clean,undo,
  manifest,photo,contenthash,rawpreview,diskspace}`; fake-exec test helper in `internal/exiftooltest`
- **Tests**: co-located `internal/**/*_test.go`
- **Specs**: `specs/00N-*/` · **Constitution**: `.specify/memory/constitution.md`
- **Config**: `.golangci.yml`, `Taskfile.yml`, `mise.toml`, `.goreleaser.yml`

## Documentation

- `docs/architecture.md`: system design and component overview
- `docs/workflows.md`: development process, testing, and release
- `docs/patterns.md`: code patterns and conventions
- `docs/operating-guidelines.md`: how Claude Code should work here

<!-- SPECKIT START -->
Latest change: **issue #35** — three low-priority items from a codebase audit
(issue-driven, no `specs/` dir), in three commits.

1. **`fix(exec)`**: `rawpreview` and `heicpreview` handed the photo path to the
   external program as a bare positional argument, so a name starting with `-`
   would have been parsed as an option — and exiftool carries write-capable ones
   (`-@`, `-o`, `-tagsFromFile`). Unreachable today, since `config.Config.Validate`
   runs `filepath.Abs` on the source root, but the guard now sits next to the
   `exec` call instead of several packages away. **The issue's prescribed fix was
   wrong for two of the five tools**: probed against the real programs, `sips`
   answers `unknown function "--"` and `ffmpeg` reads the `-i` after a `--` as an
   output name, while exiftool documents `--` as end-of-options and heif-convert
   honours it. An explicit `./` prefix is the one form all five accept, so that is
   the mechanism; exiftool additionally gets its `--`. Regression tests assert the
   argv itself and fail against the previous construction; `exiftooltest` gains
   `Args(dir)` beside its invocation counter.
2. **`refactor`**: `sort.Slice` → `slices.SortFunc` in `cluster` and (not named in
   the issue, same one-liner) `manifest.Files`, matching `readMeta`'s sibling sort.
   Both comparators are total orders with no ties, so the documented determinism is
   unchanged.
3. **`refactor(heicpreview)`**: `Detect` returns `(*Converter, bool)`. The invariant
   used to live in a doc comment, while `extractorFor` trusts a plain `== nil` on
   the interface that a direct assignment would defeat. The nil-receiver guards on
   `Name`/`Extract` stay — they are why a slip degrades to a per-photo error rather
   than a panic.

**Deliberately not done**: the issue's section 4 (`manifest.Index.Load` re-parsing
archive history, `uniqueName`'s O(m²) collision walk) is marked "no action now" by
the issue itself, and the benchmarks its closing note suggests are not a checklist
item. **#35 stays open** for those.

**Unrelated pre-existing flake**, found while running the gate and reproduced on
`main`: under the full `-count=2 -race` suite,
`cli.TestSortCompanionsDefaultAndOptOut/default_copies_companions` fails at exactly
5.00s with `exiftool … signal: killed` — `rawpreview.EnsureAvailable`'s `verTimeout`
against a freshly written shell stub, on a machine where each stub exec already
costs ~0.5s. Wants its own issue.

Previous change: **issue #31** — the constitution's configuration-precedence rule
(documentation only, issue-driven, no `specs/` dir). Principle V (**NON-NEGOTIABLE**)
mandated **flags > environment > config file > defaults**; the tool has implemented
three tiers ever since the config file landed, and the missing one was not among the
Sync Impact Report's deferred items either — so it read as an unmet non-negotiable
rather than the design choice it is.

Of the issue's two options — implement `MORAINE_GAP`/`MORAINE_JOBS`/… or amend the
document — **the author chose to amend**: moraine is an interactive, local, single-shot
CLI, not a containerised service, and a third configuration source costs tests and docs
on a surface deliberately kept small. Principle V now reads **flags > config file >
defaults** and says outright that environment variables select *which file* is read,
never what a setting is, so introducing the tier later requires an amendment.
Constitution **v2.0.0 → v2.1.0** — MINOR: a normative MUST is removed, but nothing that
ever complied stops complying, since the tier was never built. The amendment and its
rationale are recorded in a new `AMENDMENTS` section of the Sync Impact Report.
Principle IX's "configuration comes from the environment" clause was reworded to cover
credential material only — a flag is visible in `ps`, which is why that is the one
exception to the chain.

**No code change, and `--help` was never wrong**: `internal/cli/root.go:48-51` and
README already documented the real three-layer precedence, as the issue itself grants.
README now also states that the absent environment tier is deliberate. Note that
`.specify/` and `specs/` are **gitignored** here, so the amendment lives in the working
tree only; the commit carries the tracked docs (`README.md`, `docs/workflows.md`, this
file).

Prior to that: **issue #30** — honouring SIGINT during the intake stages
(issue-driven, no `specs/` dir). `sort` wired a `signal.NotifyContext` all the way
down and then consulted it for the first time in the label loop, so on a large
library Ctrl-C did nothing until the scan and EXIF read had finished on their own —
frequently the longest phase of the run, and exactly when a user realises they
pointed at the wrong folder.

1. **`fix(scan)`**: `Scan` takes a `ctx` and checks it at the top of the `WalkDir`
   callback, the shape `clean.Cleaner.Run` already used. A cancellation comes back
   bare rather than wrapped as `walking source directory`, and with no partial list.
2. **`fix(app)`**: `buildClusters` and `readMeta` take it too. `readMeta` checks
   before the blocking `sem <- struct{}{}`, so an interrupt costs at most the files
   already in flight (one EXIF read each), and `buildClusters` returns straight after
   — ahead of `buildClassifier`, so a Ctrl-C is not answered with "Ollama
   unreachable" on the way out. `Organize` reports the partial counts and
   `internal/cli` needed no change at all: `isInterrupt` already routes
   `context.Canceled` to the summary, then `interrupted: …`, then exit 1.
3. **The tally had to change with it.** `unreadable` was derived as
   `len(found) - len(photos)`; once the stage can stop early, that arithmetic also
   counts every file the interrupt never reached, so an interrupted run would
   announce itself as a library full of unreadable photos — the exact mistake
   `notAttempted` exists to avoid on the copy side. `readMeta` now returns its own
   failure count.

**Tests are deterministic rather than timed.** A `slog.Handler` that cancels on the
first record proves the walk stops at the *next* entry (one record logged, not five);
it fails against a check-after-the-walk implementation, which returns the right error
having done all the work anyway. A second one cancels on the `scan` line and pins
`read=0 of=3`, `scanned=3`, `unreadable=0` and an untouched destination.

**Deliberately out of scope**: `copyFile`/`writeTemp` still take no context — the
issue itself calls that not worth fixing, bounded as it is by one file's size — and
`clean.indexDestination` hashes the whole destination before its own cancellable
walk, which is the same gap in a different command and wants its own issue.

Before those: **issue #32** — panic and allocation boundaries around untrusted
image data (issue-driven, no `specs/` dir). Two stages parsed camera-card bytes on
goroutines nobody could recover from, and the tree contained **no `recover()` at
all**, contradicting the repo's own "per-photo failures are non-fatal" contract.

1. **`fix(exifmeta)`**: `decodeEXIF` is a panic boundary around `imagemeta.Decode`
   *and* around every read of what it returns (those accessors walk parser state
   too). A recovered panic becomes `ErrEXIFPanic`, and `Read` hands back a photo
   dated by the same filename → mtime tiers it already used for an unparsable EXIF
   block: **the photo is kept, not dropped**. `readMeta` and `singleCluster` warn
   with the path and carry on, so `Summary.unreadable` is untouched. Losing a file
   because a third-party parser crashed on its metadata would be the worse bug.
2. **`fix(classify)`**: `shrink` reads the header with `image.DecodeConfig` before
   decoding and refuses anything over `maxImagePixels` (128 MP — above every
   consumer sensor, ~512 MB as RGBA), and recovers a decoder panic. It now returns
   `(out []byte, ok bool)`; `sampleImages` skips `!ok` with a warn, reusing the path
   it already had for unreadable photos. The photo is still scanned, dated and
   copied — only its vote on the theme is lost. Reading dimensions from the header
   also lets an already-small image skip decoding entirely.

**Measured**, on a 408 KB PNG declaring 20000×7000, with a stub Ollama: peak RSS
**812 MB on `main` against 21 MB** after, with identical output trees. The same
trick at 65535×65535 — which `image.DecodeConfig` reports without complaint, as the
tests assert — is ~17 GB, i.e. the OOM.

**The issue's premise was partly stale.** imagemeta v1.0.0 *does* recover, in
`meta/jpeg/scanner.go`, and every JPEG path funnels through it — so JPEG EXIF was
already guarded upstream. Unprotected: the TIFF/DNG/NEF/CR2/ARW, HEIC/HEIF and PNG
branches of `imagemeta.Decode`, plus the `imagetype` sniff ahead of all of them. The
exposure is real but is a **RAW/HEIC** risk, not the WhatsApp-JPEG one the issue
leads with. A 2m30s fuzz (12.7M execs) over `imagemeta.Decode` found no crashing
input, so the EXIF boundary is defence in depth rather than a fix for a reproducible
crash; the classify one is not — the bomb is trivially reproducible, above.

Before that: **issue #9** — parallelising the serial tail, in four commits
(issue-driven, no `specs/` dir). Everything after clustering used to run on one
goroutine.

1. **I/O efficiency** (`perf(io)`): `rawpreview.Extract` asked for its three preview
   tags one `exec.Command` at a time; all three now go out in a single `-json -b`
   call and the winner is base64-decoded from the reply (0.147s against 0.345s on a
   real DNG, byte-identical output). That also fixes a latent bug — `Timeout` was
   **per invocation**, so a three-tag probe could take 3× the bound and overrun the
   classifier's own 60s budget. `internal/exiftooltest` had to change with it: its
   stub kept the *last* recognised tag, so one call passing all three would always
   have resolved to `ThumbnailImage`. Plus the Ollama transport/drain fix and pooled
   copy/hash buffers (1092 MB/s against 1012 MB/s on a 40 MB file).
2. **Concurrent classification** (`perf(classify)`): `labelAhead` pipelines the model
   round-trip for event N+1 against the copy I/O of event N, and `--vote` fans out.
   15.20s → 13.03s on 6 events against a 2s-latency stub.
3. **Parallel copy** (`perf(organize)!`): the two-phase `Place` above. 1.89s → 0.79s
   (2.4×) on 302 MB / 150 photos; `--jobs 1` costs ~3.5% for the planning pass.
   Corrects a pre-existing `--dry-run` defect where an intra-run byte-identical
   duplicate previewed as `renamed` while the real run reported `skipped-identical`.
4. **`fix(diskspace)`**: `TestAvailableWalksUpToAnExistingAncestor` required two
   `statfs` readings of a live filesystem to be *equal*. Reproduced on `main` under
   write load; now compared within 1%.

**The issue's premise was partly stale.** Its "stream SHA-256 through the copy" item
had already landed with `--move` (`copy.go`, `io.MultiWriter`), and its "both use
io.Copy's 32 KiB default" claim was wrong for `contenthash.Equal`, which already read
in 64 KiB chunks — the fix there was pooling, not sizing. Its prescribed
retry-on-EEXIST for `uniqueName` was **deliberately not implemented**: resolving names
serially makes it unnecessary *and* preserves the determinism a retry would destroy.
Three of its five "concurrency hazards" dissolve the same way. **#9 stays open** only
if the author wants the untouched items (the dedup re-read, base64 double-buffering).

Preceding it: **issue #19** — free-space preflight (split out of #13). New
`internal/diskspace`: `Available(path)` over `syscall.Statfs`, build-tagged `unix` with
a `!unix` stub so the tree still builds on Windows, and — the part that matters — it
answers for the **nearest existing ancestor**, since the destination root is created on
first use and so is normally absent when asked. `scan.Found` gains `Size` (one extra
lstat per recognised file, invisible next to the EXIF read that follows); `app.buildClusters`
sums it in the loop that already builds the primaries set and calls `checkFreeSpace`,
which logs `space needed_bytes=… available_bytes=…` at debug always and **warns** when
it does not fit. **Warn, never abort** — the estimate is an upper bound both ways
(blind to companions, and it counts photos an incremental re-run skips), so an abort
could refuse a run that writes nothing. A platform that cannot answer degrades to one
debug line, not a warning per run. No new flag, no stdout-contract change. Verified on
a 1 MB DMG: the warning fired before any write, the run continued and copied 2 of 3
with the third an ENOSPC per-photo error.

And before that: **issue #12** — the feature backlog, four independently shippable
items in four commits (issue-driven, no `specs/` dir). **#12 stays open**: its title
also mentions video, which the body never turned into a checklist item and which
`scan` still deliberately ignores.

1. **`--path-template`** (`internal/organize/template.go`) replaces the hardcoded
   layout with `{theme} {year} {month} {day} {date}`. Validated in `config.New`, so a
   bad template is exit 2 before anything is written; it closes the two gaps
   `safeJoin` does not (a template rendering empty, and a `.moraine` first segment
   that would collide with the bookkeeping tree). An undated event collapses the
   *date-derived stretch* of the path to one `unknown-date` segment, so
   `{year}/{month}/{theme}` gives `unknown-date/<theme>` and not
   `unknown-date/unknown-date`. The destination directory is now created **on first
   use**, so an incremental re-run stops leaving an empty folder per skipped event.
   The manifest header records the template and a changed one warns. Verified by
   `diff -r`-ing a default run against a `main`-built binary: byte-identical.
2. **Per-event + volume report**: `bytes_copied`/`bytes_skipped` (+ companion
   equivalents) in both output modes, and an `events[]` array in JSON only — the text
   line is one line per run by contract. `classify.Method` used to be logged once and
   discarded, so a run could report totals but never which event produced them. No new
   syscalls on the copy path: `copyFile` returns `io.Copy`'s own count and the
   incremental skip reuses the size its fingerprint already verified.
3. **YAML config file** (`internal/configfile` + `internal/cli/configfile.go`), new
   dep `go.yaml.in/yaml/v3`. See the bullet above for precedence and exclusions. The
   subtle part is test isolation: `internal/cli`'s `TestMain` sets `MORAINE_CONFIG=`
   so the suite never reads a developer's real file.
4. **`--move`** — see the bullet above. **The issue's premise here was wrong**: it
   claims the constitution mandates copy-only. It does not — the constitution's only
   relevant rule is Principle V's explicit-opt-in gate, which `--move` satisfies. The
   copy-only mandate was **spec-level** (`specs/002` FR-006, `specs/006`, `specs/003`),
   so those are annotated as superseded and the constitution stays at **v2.0.0**.
   `specs/005`'s "no new configuration sources" is annotated the same way.

Before that: **issue #8** — classification accuracy work (issue-driven, no
`specs/` dir). `Classifier` now returns a **`Verdict{Theme, Confidence}`**: the
structured answer carries a `confidence` (0..1, required in the schema; anything out
of range counts as unreported), and `Options.MinConfidence` — `--min-confidence`,
default 0 = off — routes a weak verdict to the heuristic then the fallback, exactly as
an abstention goes. Opt-in **`--vote`** (`internal/classify/vote.go`) classifies each
sampled photo of a group larger than `SmallGroupMax` in its own call and reduces the
answers with `tallyVotes`: abstentions vote too, an exact tie abstains, and the
winner's vote share becomes the confidence. An **eval harness**
(`internal/classify/eval_test.go`, `task eval`, `MORAINE_EVAL_CORPUS=<dir>` with
`<corpus>/<theme>/<event>/*.jpg`) measures accuracy, per-theme confusion, and the
confidence of right vs wrong answers; it skips without the env var, so CI is
untouched. **Measured on the real `qwen3-vl:8b`**: a folder of 3 mountain + 3 meal
photos was labeled `mountain` at self-reported confidence **0.9** by a single call,
while `--vote` split it 1–2 and abstained — self-reported confidence does not catch a
mixed event, the vote margin does. Pick a threshold from `task eval`, not by guessing.

Earlier: **issue #14** — dating & scanning correctness (issue-driven, no
`specs/` dir). Capture dates now resolve in three tiers — EXIF → **a date in the file
name** (`internal/exifmeta/filename.go`) → mtime — so a folder of downloads sharing
one mtime no longer collapses into one event dated by download day (verified: three
WhatsApp-named files went from one `2026-01-01/` folder to three correct 2023 ones).
No usable date ⇒ `<theme>/unknown-date/`, not `0001/0001-01-01`. `cluster.Cluster`
orders by **capture time then path** (and `readMeta` re-sorts by path after the worker
pool), so which photo keeps the un-suffixed name is deterministic. `scan` excludes the
destination by `os.SameFile` identity, not string equality — a dest named through a
symlink used to be re-ingested — and its symlink rule is now documented and logged
(a symlinked *directory* is never descended into; a symlinked *file* with a recognised
extension is read like any other photo). `app.Summary` and the stdout contract gain
`scanned`/`unreadable` (additive keys, first in the text line); unreadable
*directories* stay a stderr warning, a deliberate scope call. Adds the repo's first
`testdata/`: a 719-byte EXIF-dated JPEG pinning that EXIF still beats the filename.

Earlier change: **issue #7** — classification coverage and cost (no `specs/` dir;
issue-driven like #5/#10/#11/#13). HEIC now reaches the vision model via a new
`internal/heicpreview` converter, so HEIC events that used to land on the fallback
theme are classified (verified on real iPhone files: `method=fallback` → `model-all`);
model calls carry downscaled images (16.8× smaller on real 16 MP photos) plus EXIF
context, the model is warmed once per run and kept alive, and only transient failures
are retried. Adds one dependency, `golang.org/x/image` (BSD-3, author-approved).
The issue's cache item is deliberately unticked — `--incremental` already covers the
re-run case. **The issue's premise for HEIC was wrong**: exiftool cannot extract a
preview from an iPhone HEIC (all three tags return 0 bytes), hence the converter.

Earlier still: **issue #11** — run manifest + `undo` + `sort --incremental`.
The destination gains a `.moraine/` bookkeeping directory by default (additive,
hidden, never read as photos since the dest is excluded from the scan);
`--dry-run` still writes nothing at all.

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
`dest/` under `--path-template` (default `{theme}/{year}/{date}`, i.e.
`dest/<theme>/<year>/<year-month-day>/`) (+ companions, by default).

006 changes (domain placement only; transport surface gains one flag): companion placement
lives in `internal/organize` (new `sidecar.go` — `matchCompanion`/`companionTargetName`/
`planCompanions`, then named `placeCompanions`), reusing `copyFile`/`sameContent`/`uniqueName`/`resolveOne`
primitives (copy-only, `O_EXCL`, skip-identical, ` (N)` suffix). `Organizer` gains
`Sidecars bool`, an injected `IsPrimary func(string) bool` (excludes scanned images from
companion copying — keeps `organize` decoupled from `scan`), and a lazy per-source-dir
listing cache (linear discovery, SC-006). `organize.Result` gains `IsCompanion`/`Of`;
`app.Summary` gains companion counters; `app.Organize` builds the primary-path set and logs
companions distinctly. `config.Config`/`Options` add `Sidecars bool` (default true via the
`--sidecars` flag in `internal/cli/sort.go`). **`clean` is unchanged**: it deletes purely by
SHA-256 content identity, so copied companions are already removed (proven by new tests;
never deletes an un-archived companion).

Project constitution: `.specify/memory/constitution.md` (**v2.1.0**, 9 principles).
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
