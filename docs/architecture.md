# Architecture

## System Overview

`moraine` is a layered, single-binary CLI with five subcommands (`sort`, `clean`,
`undo`, `config`, `version`). The `sort` pipeline (`scan → exifmeta → cluster → classify → organize`)
is wired exclusively behind the exported `internal/app.Organize()` function
(Constitution Principle III). The `clean` subcommand (delete originals already copied)
is wired behind `internal/app.Clean()`, backed by the pure-logic `internal/clean`
package, and the `undo` subcommand (remove the copies the last run made) behind
`internal/app.Undo()`, backed by `internal/undo` and the run manifest
(`internal/manifest`). The `config` subcommand tree is the one part of moraine that
*writes* the configuration file, backed by `internal/configfile`'s node-tree editor and
`internal/configform`'s interactive form, and `internal/ui` is how a run narrates
itself on a terminal. The CLI transport lives in `internal/cli` (a **Cobra** command
tree): it binds flags, builds the typed config, runs the matching `app` function, and
maps the outcome to the exit code. `main.go` is a shim that injects the build version
and calls `cli.Execute`, holding no domain logic itself. Each stage is a distinct
package with a single, narrow responsibility, so business logic stays decoupled from
the CLI transport and from disk I/O — no domain package imports Cobra.

## Components

- **`internal/cli`** — the CLI transport: a Cobra command tree (root + `sort`/
  `clean`/`undo`/`config`/`version`, plus Cobra's built-in `completion`) that binds flags into
  `config.Options`/`CleanOptions`/`UndoOptions`, calls the config constructors and `app`
  orchestrators, and maps execution to exit codes 0/1/2 via a `runtimeError`
  marker. The only package that imports Cobra. `completion.go` holds the shell
  completion candidates; it derives them from `config.DefaultThemes` and
  `photo.Extensions()` so the suggestions cannot drift from what the parsers
  accept. `output.go` owns the **stdout contract** (see decision 8): the JSON
  document types, the one-line text summary, and the `reporter` that collects
  per-file records through the `app` orchestrators' `onResult` callback. Per-event
  data does not come through that callback — it arrives whole on `app.Summary.Events`,
  because there are only as many events as there are events. `render.go` is the
  companion choice for **stderr**: the bullet renderer or the plain text handler,
  decided once per run (see decision 23).
- **`internal/configfile`** — decodes the optional YAML configuration file and
  nothing else: no Cobra, no knowledge of flag defaults. Every setting is a pointer,
  so "absent from the file" is distinguishable from "present and equal to the
  default" — which is what lets a flag win without this package knowing any default.
  Decoding is strict (`KnownFields(true)`): a typo'd key is an error, because a
  setting that silently does nothing is the worst failure a config file can have. The
  three-layer precedence itself lives in `internal/cli/configfile.go`, the only layer
  that can ask cobra whether a flag was actually typed (`Flags().Changed`) — comparing
  a value against its default would be wrong, since `--sample 3` means it even when 3
  is the default.
- **`internal/config`** — single immutable `Config`/`CleanConfig`/`UndoConfig` struct
  holding every runtime parameter; `New`/`NewClean`/`NewUndo` (syntax/cross-field
  checks, no I/O) is split from `Validate` (filesystem checks, default-destination
  resolution).
- **`internal/scan`** — walks the source tree, produces `[]Found`. The destination
  root is excluded by directory identity (`os.SameFile`), not by string equality, so
  a destination reached through a symlink or a case-variant is still skipped.
  Symlinks are never traversed as directories; a symlinked file with a recognised
  extension is listed like any other photo.
- **`internal/exifmeta`** — reads EXIF, turns `Found` into `[]photo.Photo`. The
  capture date resolves in three tiers: EXIF, then a date encoded in the file name
  (`filename.go`), then the file mtime. `decodeEXIF` is a panic boundary: the
  third-party parser runs on a worker goroutine over bytes off a camera card, so a
  crash there is converted into `ErrEXIFPanic` and the photo falls through the same
  tiers as one with no EXIF at all, rather than taking the process with it.
- **`internal/photo`** — core domain types (`Photo`, `Cluster`).
- **`internal/cluster`** — groups photos into events by capture-time `-gap`.
- **`internal/classify`** — assigns a theme to each cluster via the
  `Classifier` interface (optional Ollama → altitude heuristic → fallback; the
  model decides first so it sees the actual scene). A `Classifier` returns a
  `Verdict` (theme plus a confidence from 0 to 1); `Options.MinConfidence` rejects a
  verdict below a threshold, which routes the cluster on to the heuristic exactly as
  an abstention does. `vote.go` classifies each sampled photo of a large group
  separately and reduces the answers with `tallyVotes`, whose winning share becomes
  the confidence — the one signal that catches a mixed event.
  For model input it reads JPEG/PNG directly and obtains RAW previews through
  the `RawExtractor` interface.
- **`internal/rawpreview`** — the only package that talks to **exiftool**:
  `EnsureAvailable` (mandatory startup probe) and `Extract` (largest embedded
  preview, captured in memory — never written to disk).
- **`internal/organize`** — copies files to `dest/` under the layout its `Template`
  describes (default `<theme>/<year>/<year-month-day>/`, set by `--path-template`;
  `template.go` parses and validates it, and collapses the date part of an undated
  event to `unknown-date`), enforcing no-overwrite and — outside `Move` — copy-only. An
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
- **`internal/diskspace`** — how many bytes are free on the filesystem holding a
  path, over `statfs(2)`. Build-tagged per platform (`unix` plus a `!unix` stub, so the
  tree still builds where the syscall does not exist), with the portable file holding
  the one piece of real logic: a walk up to the nearest ancestor that exists, because
  the destination root is created on first use and so is usually absent when the
  question is asked.
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
- **`internal/configfile`** — the configuration file, in both directions.
  `configfile.go` reads it: every setting is a pointer, so "absent" is distinguishable
  from "present and equal to the default", which is what lets a flag beat a file
  without this package knowing a single flag default. `document.go` writes it, as a
  `yaml.Node` tree rather than by decoding into `File` and marshalling back — see
  decision 16. `candidate` is the single definition of the search order, used by
  `Load` (which tolerates having no file) and `Target` (which must name one to write).
- **`internal/configform`** — the interactive form behind `config edit`, built on
  `github.com/charmbracelet/huh`. It knows nothing about moraine's settings, flags or
  defaults: the transport hands it fully-resolved fields (a title, a help line, a kind,
  and the value to start from) and gets them back holding the answers. That is what
  keeps the terminal out of `internal/cli` and lets the package be tested without one
  (Principle III), through huh's accessible mode over a `strings.Reader`.
- **`internal/ui`** — the bullet rendering of stderr, built on
  `github.com/sgaunet/bullets`. It is one of the two things a run's stderr can be, and
  it is both halves of that: a `slog.Handler` (so the pipeline keeps logging through
  `slog` and no domain package knows) and the `app.Progress` the phases report into.
  `Enabled` is the single decision of which rendering a run gets — see decision 23.
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
3. **Copy by default, atomic publish, content comparison** — a copy is staged in a
   hidden temp file in the destination directory, fsynced, given the source's mtime,
   and published with `link(2)`, which fails `EEXIST` rather than overwriting. So
   overwriting is structurally impossible *and* a half-written copy can never occupy
   a canonical name: an interrupted run would otherwise leave a truncated stub there,
   and every later run — unable to tell a stub from different content — would
   suffix-rename the real photo and let the stub keep the good name. The parent
   directory is fsynced too, since durable bytes behind a lost directory entry are
   still a lost photo. Comparing content makes re-runs idempotent (skip identical,
   suffix-rename same-name/different-content). Originals are never touched — unless
   `--move` is asked for explicitly, in which case a source is removed only after the
   published copy has been read back and matched against the digest accumulated while
   writing it (two full reads, where re-reading the source to compare would cost
   three). A mismatch removes the destination and keeps the source. Skips, failures,
   cancellations and `--dry-run` never remove anything, and a moved file is recorded as
   such so `undo` refuses to delete what is now the only copy.
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
   it does not. That converter is **optional** and `Detect` reports "none installed"
   with a second, boolean result rather than returning an error: a HEIC photo is
   still scanned, dated, organized and copied, and only its group's classification
   falls back. The bool is what keeps a nil `*Converter` out of the
   `PreviewExtractor` interface, where it would read as "configured". `classify` keeps the two
   seams separate (`RawPreview`, `HEICPreview`) precisely because having one says
   nothing about having the other. Small events send every eligible photo (previews
   included); large events prefer JPEG/PNG and fill the sample with extracted
   previews (FR-012), which cost a process spawn each.
6. **Cheap, stable model calls** — `internal/classify` keeps what reaches Ollama
   small and what comes back meaningful. Images are downscaled to 1024 px on the
   long side before base64 (a vision model tiles its input, so the dimensions —
   not the file size — set the inference cost). `shrink` reads the header with
   `image.DecodeConfig` first and refuses anything declaring more than 128 MP: the
   decoders size their buffers from what the file says about itself, so a few
   hundred KB claiming gigapixel dimensions is an out-of-memory kill dressed as a
   photo. A decoder that panics is refused the same way. Either costs that photo its
   place in the sample and nothing else — it is still copied. A RAW or HEIC shot alongside its
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
   `key=value` line (`--output=text`) or one JSON object with every per-file record, an
   `events` array and the summary (`--output=json`); logs, progress and errors go to
   stderr. Summary keys are additive and read by name, so a new counter
   (`bytes_copied`, `bytes_skipped`) may be inserted anywhere in the text line; only a
   key's name or meaning is fixed. Per-event detail is JSON-only, since the text
   rendering is one line per run — `Method` used to be logged once and discarded, so a
   run could report totals but never which event produced them. Anything else
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
   before returning `interrupted: copied N, …` with exit 1. Every stage honours the same
   context, the intake ones included: `scan.Scan` stops at the next directory entry and
   returns the context error bare rather than a partial list, and `readMeta` stops taking
   on new files (the workers already running finish theirs — one EXIF read each). On a
   large library those two are frequently the longest phase of the run, and so the phase a
   Ctrl-C actually lands in. Cancelling there returns the counts the run had reached —
   `scanned` from a completed walk, `unreadable` only for files genuinely read and
   rejected. That last one is why `readMeta` reports its own failures instead of leaving
   them to be derived from `len(found) - len(photos)`: once the stage can stop early, the
   shortfall also counts everything the interrupt never reached, and an interrupted run
   would announce itself as a library full of unreadable photos.

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

13. **The free-space preflight warns, never aborts** — after the scan, `app` compares
    the bytes it found against `diskspace.Available(destRoot)` and logs one warning
    when they do not fit. Without it a full disk announces itself as one
    `placement failed` per photo, with nothing in the run naming the disk as the cause.
    Aborting on the comparison would be wrong, because the estimate is an upper bound
    in two directions: it cannot see companion files, which `organize` only discovers
    per source directory, and it counts every photo even though an incremental re-run
    skips the ones already placed byte-identically. On a nearly-full destination
    holding an already-complete archive, a size-based abort would refuse a run that
    writes nothing at all. So the run proceeds and decides photo by photo — and a
    platform that cannot answer at all degrades to one debug line, not a warning per
    run.

14. **Placement resolves every name before it copies anything** — `organize.Place`
    runs in two phases: one goroutine resolves each destination name in the cluster's
    own order and reserves it, then a pool of `--jobs` workers copies the bytes. The
    obvious alternative — copy concurrently and retry `uniqueName` when the publish
    returns `EEXIST` — is a smaller change and the wrong one. Three properties depend
    on names being handed out by a single goroutine in a fixed order: which photo
    keeps the un-suffixed name (which is *why* `cluster.Cluster` imposes a total order
    at all), the contiguity of the ` (N)` indices that `existingIdentical` relies on
    to stop its scan at the first gap, and the order of `results[]` and `events[]`,
    which is the public stdout contract. A retry loop trades all three for a race.

    Resolving first also means the shared state stays where it was: the reservation
    map, the directory-listing cache and the memoised destination directory are
    touched only while planning, and the caller keeps its tallies, the run manifest
    and the `onResult` stream on its own goroutine. The concurrency added no locks
    anywhere.

    The cost is that a copy is no longer on disk when the next file is resolved, so
    the reservation records `dst -> src` rather than a bare name: a collision then
    compares against the *source* that reserved the destination, which is a real file
    holding exactly the bytes that are going there. That is what keeps two
    byte-identical same-named photos reported as one copy and one skip — and it is
    what finally makes `--dry-run` agree with the run it previews on that case, which
    it did not before, because with nothing written there was no file to compare.

15. **Classification runs one event ahead of placement** — `labelAhead` classifies on
    a goroutine of its own and delivers events in cluster order, one buffered and one
    in flight, so the model round-trip for the next event overlaps the copy I/O of
    this one. Exactly one producer, which is what makes the ordering a property of the
    construction rather than something to restore afterwards. It is deliberately not a
    fan-out: Ollama serialises requests per model unless `OLLAMA_NUM_PARALLEL` is
    raised, so a second concurrent classification would queue server-side and win
    nothing, while blocking inside `warmOnce.Do` for the whole cold load of a vision
    model. What is bought here is pipelining, not parallel inference.

16. **The configuration file is edited as a node tree, not re-serialised** — a
    `moraine.yaml` is hand-maintained, and the comments in it are the part moraine did
    not write. `configfile.Document` therefore edits the parsed `yaml.Node` tree in
    place: comments, key order and any key this version does not know about all
    survive a `config set`. An existing value is rewritten *in place* rather than
    replaced, because a trailing comment (`gap: 6h  # a long day`) attaches to the
    value node — swapping that node is precisely how such a comment would be lost.
    What is not preserved: yaml.v3 does not record blank lines between plain keys and
    re-emits with its own layout, so the first write normalises spacing and indent
    (to two spaces) and every write after that is stable. `Save` publishes atomically
    (temp file, fsync, rename, fsync the parent) — the discipline `organize` uses for a
    copy, with `os.Rename` rather than `os.Link` because replacing the file is the
    point here.

17. **`moraine config` restates no defaults and no help text** — `configkeys.go` holds
    one table of settable values (flag name, YAML key, kind), and everything else is
    read back from the real commands. Defaults come from
    `registerSortFlags`/`registerCleanFlags`/`registerUndoFlags`, which pflag writes
    into the bound `Options` merely by registering them, so `config show` reports
    exactly what `--help` prints — including the defaults that are literals at the flag
    rather than `config.Default*` constants. Help text is the flag's own `Usage`.
    Origins come from the existing `applySortFile` overlayer (`pflag.Changed` answers
    `false` for a name a flag set does not have, so passing a command that registers
    none of them yields exactly "which keys did the file supply"), so the precedence
    rule has one implementation, not two. `configkeys_test.go` fails the suite if a
    flag of `sort`, `clean` or `undo` is neither in that table nor in the
    `unconfigurable` list.

18. **A candidate file is validated through the run's own path before it is saved** —
    `writeSettings` renders the edited document, decodes it with the same strict
    decoder a run uses, overlays it onto default options with the same `applyXxxFile`
    functions the real commands call, and runs the result through `config.New` /
    `NewClean` / `NewUndo`. A value no run would accept — a confidence above 1, a theme
    colliding with the fallback theme — is refused with the file untouched, rather than
    written and discovered at the start of the next run. The interactive form validates
    each typed answer the same way (`checkValues`), so it refuses at the question
    instead of at the end of a session.

19. **The form draws on stderr and needs a real terminal** — stdout carries the
    resulting settings (Principle V), so `config edit --output=json > settings.json`
    works while the questions are on screen. Whether a terminal exists is asked of the
    terminal driver (`charmbracelet/x/term`, already in the graph via huh) rather than
    inferred from `os.ModeCharDevice`, which `/dev/null` also has —
    `moraine config edit < /dev/null` would otherwise open a form nobody can answer.
    Without a terminal the command fails with exit 1 and names the two ways forward:
    `--accessible` (plain prompts, also the screen-reader mode) or `config set`.

20. **`config edit` writes only what changed** — a form prefilled with defaults that
    wrote every answer would stamp today's defaults into the file, freezing that user
    at them so a later change to a default never reached them. Each answer is compared
    with the value the question started from: changed ⇒ set, changed back to the
    default ⇒ unset, untouched ⇒ left exactly as it was, which is also what preserves
    the comment on a line nobody edited.

21. **`config edit` asks which settings before asking their values** — huh binds
    "submit" to enter and enables it only on a form's *last* field, so the first
    version, which asked about every setting in one form, could only be saved by
    pressing enter through two dozen questions. Asking first *which* settings to change
    makes the picker a single field — submittable at once — and the second form as long
    as the change rather than as long as the settings list. The picker doubles as a
    finder: it shows each setting's effective value and marks the ones the file sets.
    Answers still map back through `applyAnswers`, which compares each one with what
    its question started from.

22. **Nothing between huh and the reader may buffer** — huh's accessible prompts build
    a fresh `bufio.Scanner` per field over one reader, and a scanner reads ahead by up
    to 64 KiB, so the first question would swallow every later answer.
    `configform.lineReader` limits each read to a single byte, which makes each scanner
    stop at the newline it was after. It also holds no buffer of its own, which is the
    other half: `config edit` runs two forms in sequence over one standard input, and
    anything read ahead by the first would be lost when it returned. Both halves have a
    test that fails without them.

23. **Two renderings of stderr, chosen once** — on a terminal, stderr is bullet lines
    with a progress bar per stage; otherwise it is the plain `slog` text records
    moraine has always written. `internal/cli/render.go` is the only place that
    chooses, and `--progress=auto|always|never` is how a user overrides it.

    The pipeline does not know which it got. Progress reaches the transport as *data*
    through `app.Progress`/`app.Tracker` — one `Begin(phase, total)` per stage and one
    `Inc()` per unit — exactly as placement results reach it through `onResult`
    (decision 8), so `internal/app` stays presentation-free. `internal/ui` maps a
    phase to the words it wears; naming a stage for a reader is not the pipeline's job.
    A phase opened with a total of 0 is indeterminate rather than empty, which is how
    `clean`'s hashing pass — whose size nothing knows in advance — gets a spinner
    instead of a bar.

    `Progress` is the one seam in the pipeline that must be **safe for concurrent
    use**: the EXIF pool, the look-ahead classifier and the copy pool each report from
    their own goroutine. Everything else — `Summary`, `manifest.Writer`, `onResult` —
    stays on `Organize`'s goroutine, and keeping progress separate is why that is
    still true.

    The bullet rendering also **drops the per-event `group` line**
    (`ui.phaseNarration`). That is the one place the two renderings deliberately
    disagree about content, and the criterion is narrow: a message qualifies only if it
    arrives once per unit of work and adds nothing to the bar tracking those units. The
    line exists at info because the text rendering has nowhere else to put per-event
    facts — the stdout summary is one line per run by contract — but beside a classify
    bar and a copy bar it is noise that also pushes them up the screen once per event.
    Its per-run neighbours stay: `exif` carries `raw=N` and `cluster` the gap, which no
    bar shows. The filter matches on the message, so the transport's suite drives a real
    run through the *plain* rendering and fails if the pipeline stops emitting it — a
    rename cannot leave the filter matching nothing.

    `auto` is deliberately conservative, and each clause answers a different question.
    Both stdout **and** stderr must be terminals, which is Principle V's rule read
    literally — redirecting the run result turns the bars off, and `--progress=always`
    is the way to insist. `NO_COLOR` must be unset, because the library colours
    unconditionally and has no monochrome mode to fall back to, so honouring the
    variable means not drawing. `TERM` must not be `dumb`, since every repaint moves
    the cursor. And the verbosity must be `info` or `warn`: `--verbose` asks for a line
    per file, which competes with a bar for the same rows, and `--quiet` asks for
    silence.

24. **A progress report is not entitled to count what the tally does not** — the copy
    phase advances once per photo *reached*, so a unit the cancellation beat advances
    nothing. `organize.execute` ticks idle units only while the context is live, and
    `executeUnit` ticks after its own cancellation check, so an interrupted run closes
    on `photos placed · 3 of 400` rather than a full bar. This is the same mistake
    `notAttempted` exists to prevent in `Summary` (decision 10), in a second place: a
    bar that reads 100% next to `copied=3` on stdout is a bar that lies.

25. **The bullet renderer owns stderr, and truncates to keep it** — every repaint is
    relative to the cursor, so a second writer interleaving lines, or a single line
    long enough to wrap, desynchronises the display. Hence: all stderr goes through the
    one `Renderer`, and each line is cut to the terminal's width (asked per line, not
    cached, so a mid-run resize cannot leave the renderer truncating to a width the
    terminal no longer has). Repaints are throttled to one per 75 ms with the final
    unit always drawn — a ten-thousand-photo library would otherwise drive ten thousand
    cursor round-trips for an animation nobody can read that fast. Level gating happens
    in the `slog.Handler` rather than in the library, whose line counter advances even
    for a record its own level suppresses; a suppressed line would offset every later
    repaint, so none is allowed to reach it.

## Integration Points

- **External APIs**: optional local **Ollama** vision model
  (`-ollama-url`, `-model`); a startup `Preflight()` returns a typed status
  and the model stage is skipped (set to `nil`) on any non-ready status.
- **External programs**: **exiftool** (required, `-exiftool`) for RAW preview
  extraction, invoked via `os/exec` (argument vector, timeout-bounded, no shell).
- **Database / queues**: none — the only persistent state is the copied
  output tree on the filesystem.

## Data Flow

Source files → `scan.Found` → `photo.Photo` (dated from EXIF, the file name, or
mtime) → `[]photo.Cluster` (temporal, ordered by capture time then path so the result
never depends on which EXIF worker finished first) → theme label per cluster
(classified one event ahead of placement) → resolved to a destination name serially,
then copied by a pool of `--jobs` workers to `dest/` under the `--path-template`
layout (by default
`<theme>/<year>/<year-month-day>/`, and `<theme>/unknown-date/` when no date could be
determined). Per-photo errors are collected into the run `Summary`
rather than aborting the pipeline, as are the images the scan found but could not
read (`Scanned`/`Unreadable`).

For `undo`: read the most recent manifest under the destination → walk its records
newest first → remove each file the run created that still matches its record → prune
the folders that emptied → mark the manifest `.undone` so the next `undo` steps back a
run. Nothing outside the destination root is touched, and sources are never read.

For `clean`: index destination files by size → walk the source (skipping the
destination subtree) → for each regular file, hash only on a size collision and
compare against the destination's same-size content sums → delete (or, in dry-run,
report) matches. Per-file errors are collected into the clean `Summary`; nothing
under the destination is ever removed.
