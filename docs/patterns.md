# Code Patterns & Best Practices

## Error Handling

Wrap errors with context using `%w` so the full chain is preserved; expose
typed sentinels for conditions callers must test programmatically.

```go
// internal/organize/path.go
return "", fmt.Errorf("%w: %s", ErrInvalidDestSubdir, rel)

// main.go — sentinel drives exit code, not string matching
if errors.Is(err, config.ErrHelp) {
    os.Exit(0)
}
```

Per-photo failures are **non-fatal**: they are recorded in `Result.Err` and
tallied into the run `Summary.Errors` (see `internal/app/app.go`) rather than
aborting the whole run.

## Testing Patterns

- **File naming**: co-located `*_test.go` next to the package.
- **Organization**: black-box external packages (`package foo_test`) for every
  package except `organize`, which white-box-tests its unexported helpers
  (`safeJoin`, `copyFile`, `sameContent`, `uniqueName`).
- **Style**: table-driven cases with `t.Run` subtests.
- **Fakes**: real `net/http/httptest` servers for Ollama; the `Classifier`
  interface allows a `fakeClassifier` in tests — no mock framework.
- **Race**: CI runs `-race -count=1` (`CGO_ENABLED=1`).

## Safety Invariants (copy-only)

- Copies are **atomic**: `copyFile()` writes to a `.moraine-*.tmp` file in the
  destination's own directory, fsyncs it, then publishes it with `os.Link()`.
  `link(2)` fails `EEXIST` instead of clobbering, so overwriting is impossible *and*
  a destination name only ever appears complete. A crash can leave a hidden temp
  behind, never a truncated file on the canonical name. Filesystems without hard
  links (FAT/exFAT/SMB) fall back to `os.Rename()` behind an existence check.
- Durability: the file's bytes **and** its parent directory are fsynced — data alone
  is not enough, a lost directory entry loses the photo.
- Copies keep the source's **modification time** (`os.Chtimes`). This is load-bearing:
  `exifmeta` falls back to mtime when a photo has no readable EXIF date.
- `sameContent()` short-circuits on size mismatch, then compares the bytes via
  `contenthash.Equal()`: identical → `ActionSkippedIdentical`; same name, different
  content → `ActionRenamed` with a ` (N)` suffix via `uniqueName()`.
- A failed copy leaves the destination directory exactly as it found it.
- `safeJoin()` rejects path traversal in destination subdirectories.
- **Companion (sidecar) files** reuse the same primitives: a companion of `IMG.jpg`
  is a same-directory regular file named `IMG.jpg<suffix>` (appended) or
  `IMG.<other-ext>` (same base name); it is placed via `placeOne()` so it inherits
  skip-identical / ` (N)`-suffix / no-overwrite. Its name tracks the photo's final
  placed name (`IMG (1).jpg.xmp`). `clean` removes companions through the same
  content-identity match it uses for photos (never by name).

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
