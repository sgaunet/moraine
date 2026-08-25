# Code Patterns & Best Practices

## Error Handling

Wrap errors with context using `%w` so the full chain is preserved; expose
typed sentinels for conditions callers must test programmatically.

```go
// internal/organize/path.go — a sentinel, for a condition a caller must branch on
return "", fmt.Errorf("%w: %s", ErrInvalidDestSubdir, rel)

// internal/cli/exit.go — a wrapper type drives the exit code, not string matching
return asRuntime(cfg.Validate())          // a post-parse failure → exit 1
...
case errors.As(err, new(*runtimeError)):  // classify(err)
	return exitRuntime                    // anything left unwrapped is a usage error (2)
```

Exactly three *exported* sentinels exist in the whole tree —
`rawpreview.ErrNoPreview`, `organize.ErrInvalidDestSubdir` and
`exifmeta.ErrEXIFPanic` — and each has a caller in another package that must branch
on it. (`classify.errTransient` is the one unexported sentinel, and it never leaves
its retry loop.) Everything else is wrapped context, so add a fourth only when some
caller genuinely has to tell that condition apart.

`main.go` decides nothing: it is `os.Exit(cli.Execute(...))`. `internal/cli/exit.go`
owns the 0/1/2 mapping, and which side of it an error lands on is decided by whether
the CLI wrapped it — post-parse failures (filesystem validation, the exiftool
preflight, the run itself) go through `asRuntime`; flag-parse errors, unknown
commands and the config constructors' cross-field checks are left alone and classify
as usage errors.

A panic in a third-party parser is caught where the untrusted bytes are handed
over — `exifmeta.decodeEXIF`, `classify.shrink` — and never at a goroutine
boundary. Those two are the only `recover()`s in the tree, and the distinction is
deliberate: a recover next to the parse turns a hostile file into one skipped
step, while a catch-all around `readMeta` or `labelAhead` would turn moraine's own
bugs into quietly degraded runs.

Per-photo failures are **non-fatal**: they are recorded in `Result.Err` and
tallied into the run `Summary.Errors` (see `internal/app/app.go`) rather than
aborting the whole run. A *cancelled* run is the exception: `organize.Place` records
the context error against every photo it never reached, and `tally` deliberately does
not count those — nothing failed, nothing was attempted. The same rule shapes the
intake stages: `readMeta` counts the files it actually failed to read rather than
letting the caller derive them from the shortfall, so a cancellation cannot inflate
`unreadable` with files it never opened.

## Output Contract (stdout is data)

- **stdout carries the run result only**; logs, progress and errors go to **stderr**
  (Constitution Principle V — anything else on stdout corrupts a pipe).
- `--output=text` renders one `key=value` summary line; `--output=json` renders one
  object with every per-file record, an `events` array describing each placed event,
  plus the summary. The document types live in `internal/cli/output.go` and are
  treated as a public API, not an internal detail.
- New summary keys are **additive**: consumers read by key, not position, so a key may
  be inserted anywhere in the text line (`scanned`/`unreadable` went in first,
  `bytes_copied`/`bytes_skipped` in the middle). What must not change is a key's name
  or meaning.
- Per-event data exists only in the JSON rendering — the text line is one line per
  run. `app.Summary.Events` is bounded by the event count, not the photo count, which
  is why the run keeps it while deliberately not keeping per-file records in text mode.
- The `app` orchestrators stay presentation-free: `Organize`/`Clean` take an
  `onResult func(Result)` callback — the shape `clean.Cleaner.Run` already used — and
  the transport decides how to render each record.
- Per-file narration splits by command: `sort` logs it at **debug** (thousands of lines
  on a real library), `clean` at **info** (the dry-run plan is why you ran it).
- Key names are snake_case in both renderings, matching the slog attribute keys the
  tool has always emitted (`companions_copied`, `would_delete`). `.golangci.yml`
  configures `tagliatelle` accordingly.

## Testing Patterns

- **File naming**: co-located `*_test.go` next to the package.
- **Organization**: black-box external packages (`package foo_test`) everywhere,
  including `organize`; its unexported helpers (`safeJoin`, `copyFile`,
  `sameContent`, `uniqueName`) are reached through `export_test.go` re-exports.
- **Dry run**: `TestDryRunMatchesRealRun` asserts a preview and the run it previews
  report the same actions — the property that makes `--dry-run` worth trusting.
- **Style**: table-driven cases with `t.Run` subtests.
- **Fakes**: real `net/http/httptest` servers for Ollama; the `Classifier`
  interface allows a `fakeClassifier` in tests — no mock framework.
- **Race**: CI runs `-race -count=1` (`CGO_ENABLED=1`).

## Safety Invariants (copy by default; `--move` verifies before removing)

- Copies are **atomic**: `copyFile()` writes to a `.moraine-*.tmp` file in the
  destination's own directory, fsyncs it, then publishes it with `os.Link()`.
  `link(2)` fails `EEXIST` instead of clobbering, so overwriting is impossible *and*
  a destination name only ever appears complete. A crash can leave a hidden temp
  behind, never a truncated file on the canonical name. Filesystems without hard
  links (FAT/exFAT/SMB) fall back to `os.Rename()` behind an existence check; what
  keeps that check-then-act safe while copies run concurrently is that `Place`
  reserves every destination name on one goroutine before any copy starts, so no two
  workers can target the same name.
- **Placement is two-phase**: `Place()` resolves every destination name serially, in
  the cluster's own order, then copies the bytes with a pool of `--jobs` workers.
  That is what lets the copies overlap without the naming becoming a race — which
  photo keeps the un-suffixed name is decided by `cluster.Cluster`'s total order and
  nothing else, the ` (N)` indices stay contiguous for `existingIdentical()` to walk,
  and the returned results keep the order a serial run produced. The caller's tallies,
  the run manifest and the `onResult` stream all stay on its own goroutine.
- Durability: the file's bytes **and** its parent directory are fsynced — data alone
  is not enough, a lost directory entry loses the photo.
- Copies keep the source's **modification time** (`os.Chtimes`). This is load-bearing:
  `exifmeta` falls back to mtime when a photo has no readable EXIF date.
- `sameContent()` short-circuits on size mismatch, then compares the bytes via
  `contenthash.Equal()`: identical → `ActionSkippedIdentical`; same name, different
  content → `ActionRenamed` with a ` (N)` suffix via `uniqueName()`.
- A failed copy leaves the destination directory exactly as it found it.
- **`--move`** is the one thing that removes a source, and only ever after reading the
  published copy back: `copyFile` accumulates the SHA-256 of the bytes it writes (free,
  through an `io.MultiWriter`), and `verifyCopy` re-hashes the published file and
  compares. Hashing the stream rather than re-reading the source is what keeps this at
  two full reads instead of three. A mismatch removes the *destination* — a provably
  wrong file on a canonical name is the exact failure the atomic publish exists to
  prevent — and keeps the source.
- Every source removal goes through `Organizer.copy`, the same single funnel that makes
  "a dry run writes nothing" true in one line. Nothing removes a source on a skip (a
  skip verifies nothing that run, and the incremental variant never reads the bytes at
  all), an error, a cancellation, or a dry run.
- A moved placement is recorded as `moved` in the run manifest, and `undo` refuses it:
  with the original gone, the copy is the only remaining file. **A move run cannot be
  undone**, and `undo` says so rather than silently keeping the file.
- `safeJoin()` rejects path traversal in destination subdirectories.
- **Companion (sidecar) files** reuse the same primitives: a companion of `IMG.jpg`
  is a same-directory regular file named `IMG.jpg<suffix>` (appended) or
  `IMG.<other-ext>` (same base name); it is resolved via `resolveOne()` so it inherits
  skip-identical / ` (N)`-suffix / no-overwrite. Its name tracks the photo's final
  placed name (`IMG (1).jpg.xmp`). `clean` removes companions through the same
  content-identity match it uses for photos (never by name).

## Reversibility & Incrementality (run manifest)

- A run's record is **append-as-you-go**, one JSON Lines file per run under
  `<dest>/.moraine/runs/`. The file is created by the *first* record, so "a dry run
  writes nothing at all" stays true without the caller having to remember it, and an
  interrupted run still leaves a usable record of what it did place.
- The recorded identity of a placed file is **size + mtime**, not a hash. Copies carry
  their source's mtime, so one pair fingerprints both ends — which is what lets an
  incremental run skip a file without reading either copy.
- **A manifest is a shortcut, never an authority.** `undo` deletes only records whose
  action is `copied`/`renamed` (never `skipped-identical` — that file predates the run)
  and only while the file still matches its fingerprint; `Organizer.Placed` skips only
  when *both* source and copy still match. Any mismatch falls through to the normal
  path, so a stale manifest costs a skip, never correctness.
- The manifest stays out of the domain packages: `organize` speaks of `Placement`
  (dest, size, mtime) through an injected `Placed` hook, and `app/manifest.go` is the
  only place that translates between the two — the same decoupling `IsPrimary` uses.

## Go-Specific Patterns

- **No CGo, single static binary**: production builds use `CGO_ENABLED=0`.
- **Interface seams** for swappable behavior (`classify.Classifier`).
- **Single source of truth for flags**: `registerFlags()` is reused by both
  `Parse` and usage output to prevent drift.
- **Immutable config**: one `Config` struct, no mutable package globals.

## Common Utilities

- `internal/photo`: shared domain types (`Photo`, `Cluster`).
- `internal/organize/path.go`: safe destination path construction
  (`safeJoin`, `uniqueName`).
- `internal/manifest`: what a run placed (`New`/`Add` to write, `Latest`/`ReadRun`/
  `Load` to read it back).
