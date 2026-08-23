# moraine

**Automatic photo organizer** — a single, CGo-free Go binary that organizes a photo
directory **with no UI and no interaction**. It analyzes the photos, groups them into
**events** (by capture time), assigns a **theme** to each group, then **copies** each
photo to `destination/<theme>/<year>/<year-month-day>/`. Originals are **never** modified
or deleted. Every step is explained in the logs.

## Features

- **Pure Go, no CGo, single binary** — runtime deps: **exiftool** (required, for RAW)
  and Ollama (optional).
- **Temporal grouping** of JPEG / PNG / HEIC / RAW photos (configurable gap).
- **RAW support** (`.dng/.nef/.cr2/.cr3/.arw/.raf/.rw2/.orf/.pef/.srw`): RAW pixels can't
  be decoded in pure Go, so the camera-embedded JPEG preview is extracted with **exiftool**
  (in memory, never written to disk) and sent to the model.
- **Theme classification** in three stages: **Ollama** vision model constrained to
  the theme set (optional) → altitude heuristic (`≥ --mountain-altitude`, default
  1500 m → `mountain`) → guaranteed **fallback** (`other`). A theme is **always**
  assigned, even without Ollama.
- **Ollama diagnostics**: a *preflight* logs whether the model is ready, whether Ollama
  is **unreachable** (`ollama serve`), or whether the **model is missing**
  (`ollama pull <model>`). An out-of-list answer from the model is logged (no more silent
  fallback).
- **Sampling**: a group of **3 photos or fewer** is analyzed in full; a large group is
  sampled (evenly spaced photos, configurable count).
- **Safe, idempotent copy**: every copy is staged in a temporary file, `fsync`ed, and
  published atomically — a photo appears at its destination whole or not at all, even if
  the machine dies mid-run, and never overwrites. Copies keep the original's
  **modification time**. An identical file already present is **skipped** (safe re-runs);
  a same-named file with different content is **suffixed** ` (1)`.
- **Companion (sidecar) files** (on by default): files other software leaves next to a
  photo are copied into the same folder — both appended sidecars (`IMG.jpg.xmp`,
  `IMG.jpg.json`) and same-base-name sidecars (`IMG.xmp`). They follow the photo's final
  name on a collision rename, obey the same no-overwrite rules, and are removed by `clean`
  too. Disable with `--sidecars=false`.
- **Single-photo mode**: pass a file instead of a directory.

## Requirements

- **Go 1.26+** (`go version`).
- **exiftool** (required) — used to read RAW files. Install with
  `brew install exiftool` (macOS) or `sudo apt install libimage-exiftool-perl`
  (Debian/Ubuntu). moraine verifies it at startup and exits if it is missing; point at a
  custom binary with `--exiftool <path>`.
- *(Optional)* [Ollama](https://ollama.com) running locally with a vision model:
  `ollama pull qwen3-vl:8b`. Without Ollama, classification falls back to the heuristic
  and then to the fallback theme.

## Install

### Homebrew (macOS and Linux)

```bash
brew install --cask sgaunet/tools/moraine
```

Note the `--cask`: moraine ships as a Homebrew **cask**, not a formula. The cask
pulls in `exiftool` and installs **shell completions for bash, zsh, fish and
powershell** automatically — see [Shell completion](#shell-completion).

> **Upgrading from the old formula?** The formula is superseded by the cask. Run
> `brew uninstall moraine` first, then the command above; a formula and a cask of
> the same name cannot coexist, and Homebrew silently prefers the formula.

### Direct download

Grab a `.tar.gz` for your platform from the
[releases page](https://github.com/sgaunet/moraine/releases) and put `moraine` on
your `PATH`. Remember to install `exiftool` yourself (see Requirements).

## Build

```bash
# Static binary, no CGo
CGO_ENABLED=0 go build -o moraine .

# With a version number (otherwise "dev")
CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always)" -o moraine .
```

## Usage

`moraine` is organized into subcommands: **`sort`** (organize photos), **`clean`**
(delete originals already copied), **`completion`** (shell completion scripts) and
**`version`**. Run `moraine --help` to list them
and `moraine <command> --help` for command-specific options and examples.

```bash
# Organize a photo directory
./moraine sort --dest ~/Photos/sorted ~/Photos/2025

# A single photo (short flags: -d dest)
./moraine sort -d ~/Photos/sorted ~/Photos/2025/IMG_1234.jpg

# Disable Ollama entirely (heuristic + fallback only; -s sample)
./moraine sort -s 0 -d ~/Photos/sorted ~/Photos/2025

# photos only — do not copy companion/sidecar files
./moraine sort --sidecars=false -d ~/Photos/sorted ~/Photos/2025

# Custom theme vocabulary + verbose logs (-l log-level)
./moraine sort --themes "friends,hiking,party,nature" --fallback-theme "misc" \
  -l debug -d ~/Photos/sorted ~/Photos/2025

# Delete originals already safely copied — dry-run by default, then commit
./moraine clean -d ~/Photos/sorted ~/Photos/2025            # preview (deletes nothing)
./moraine clean --delete -d ~/Photos/sorted ~/Photos/2025   # actually delete

# Help and version
./moraine --help
./moraine sort --help
./moraine version          # or: ./moraine --version
```

Each photo is **copied** to `destination/<theme>/<year>/<year-month-day>/`
(e.g. `~/Photos/sorted/nature/2025/2025-08-12/IMG_1234.jpg`). Originals stay in place.

> **Migrating from the pre-1.0 flag CLI**: the interface moved to subcommands with
> GNU-style flags. `moraine <dir>` → `moraine sort <dir>`; `-dest` → `--dest` (or `-d`);
> `-version` → `moraine version` (or `--version`). The old rootless form and single-dash
> long flags are no longer accepted.

### `sort` flags

| Flag               | Short | Type     | Default                   | Role                                                       |
|--------------------|-------|----------|---------------------------|------------------------------------------------------------|
| `<source>`         |       | argument | *(required)*              | **directory** (batch) or **file** (single photo)           |
| `--dest`           | `-d`  | string   | `<source>/_sorted`        | destination root (excluded from the scan)                  |
| `--gap`            | `-g`  | duration | `6h`                      | max time gap within an event                               |
| `--sample`         | `-s`  | int      | `3`                       | photos sampled per **large** group (`0` = no AI)           |
| `--model`          |       | string   | `qwen3-vl:8b`             | Ollama vision model                                        |
| `--ollama-url`     |       | string   | `http://127.0.0.1:11434`  | base URL of the Ollama API                                 |
| `--themes`         |       | string   | `mountain,special-events,cook,family` | themes (comma-separated slugs)                 |
| `--fallback-theme` |       | string   | `other`                   | fallback theme when none is determined                     |
| `--log-level`      | `-l`  | string   | `info`                    | `debug` \| `info` \| `warn` \| `error`                     |
| `--exiftool`       |       | string   | `exiftool`                | exiftool executable (name on `PATH` or absolute path); **required** for RAW |
| `--sidecars`       |       | bool     | `true`                    | also copy each photo's companion/sidecar files (`--sidecars=false` to disable) |
| `--mountain-altitude` |    | float    | `1500`                    | metres at/above which the altitude heuristic labels a group `mountain` (must be `> 0`) |
| `--help`           | `-h`  | bool     | —                         | print the detailed help and exit                           |

### `clean` flags

| Flag          | Short | Type     | Default            | Role                                                          |
|---------------|-------|----------|--------------------|--------------------------------------------------------------|
| `<source>`    |       | argument | *(required)*       | source **directory** to clean                                |
| `--dest`      | `-d`  | string   | `<source>/_sorted` | destination library holding the copies (**never** deleted from) |
| `--delete`    |       | bool     | `false`            | actually delete matched originals (default: dry-run)         |
| `--log-level` | `-l`  | string   | `info`             | `debug` \| `info` \| `warn` \| `error`                       |

`moraine version` (or `--version`) prints the version. **Exit codes**: `0` success,
`1` runtime error, `2` usage error.

### Shell completion

Installed automatically by the Homebrew cask. For any other install, print the script
and load it yourself:

```bash
moraine completion zsh  > "${fpath[1]}/_moraine"          # zsh (then restart the shell)
moraine completion bash > /etc/bash_completion.d/moraine   # bash
moraine completion fish > ~/.config/fish/completions/moraine.fish
moraine completion powershell | Out-String | Invoke-Expression
```

Beyond command and flag names, completion knows the values:

| Where | Completes to |
|-------|--------------|
| `<source>` argument | directories, and files with a recognised photo extension |
| `--dest` | directories only |
| `--log-level` | `debug` \| `info` \| `warn` \| `error` |
| `--themes` | the built-in themes, comma-appending and skipping ones already listed |
| `--fallback-theme` | the built-in themes plus `other` |
| `--gap` | common durations (`30m`, `1h`, `6h`, `12h`, `24h`) |
| `--mountain-altitude` | common altitudes (`800`, `1000`, `1500`, `2000`, `2500`) |

The candidate lists are derived from the same constants the parser uses
(`config.DefaultThemes`, `photo.Extensions`), so they cannot drift from what
`moraine` actually accepts.

> **HEIC note**: HEIC photos are dated and organized, but **not** sent to the vision
> model (no pure-Go HEIC decoding, due to the "no CGo" constraint). A HEIC-only group
> falls back to the heuristic or to the fallback theme.
>
> **RAW note**: RAW photos are dated, organized, and **classified** via their embedded
> preview, extracted with **exiftool** (required). Small events (≤3 photos) send every
> eligible photo including RAW; large events prefer JPEG/PNG and extract RAW previews only
> to fill the sample. A RAW with no usable preview is still copied and dated, and falls
> back to the heuristic or the fallback theme.
>
> **Companion (sidecar) note**: by default `sort` also copies, into a photo's destination
> folder, any file in the photo's source directory whose name is either the photo's full
> name plus a suffix (`IMG.jpg.xmp`, `IMG.jpg.json`) or its base name with a different
> extension (`IMG.xmp`). Companions follow the photo's final name when it is collision-
> renamed (`IMG (1).jpg.xmp`), are never overwritten, and are removed by `clean` once
> archived (matched by content). A companion-named file that is itself a photo is sorted on
> its own, not duplicated. **Behavior change (v0):** companion copying is on by default;
> earlier versions copied photos only — pass `--sidecars=false` for the previous behavior.

## Architecture

Business logic in pure Go packages, decoupled from transport (Constitution, Principle III):

```
main.go                 inject build version → cli.Execute → exit codes
internal/
  cli/      Cobra command tree (sort/clean/version), flag binding, exit-code mapping
  config/   centralized typed configuration + validation (slugs, file/directory source)
  app/      testable orchestration: scan → exif → cluster → classify → organize + logs
  photo/    domain types (Photo, Cluster, Format)
  scan/     recursive walk, format filter, EXCLUDES destRoot
  exifmeta/ EXIF extraction (date, GPS, altitude) + mtime fallback
  cluster/  temporal grouping (configurable gap)
  classify/ heuristic → Ollama (constrained themes) → fallback; Ollama HTTP client
  organize/ builds the <theme>/<year>/<date> path, hash-based identity, durable copy
```

Detailed contracts: [`specs/002-auto-photo-organizer/contracts/`](specs/002-auto-photo-organizer/contracts/).

## Development

```bash
go test ./... -race         # tests (data-race free) — Constitution, Principle IV
gofmt -l . && go vet ./...   # formatting + static analysis
golangci-lint run ./...      # lint (v2 config in .golangci.yml)
```

## License

See [LICENSE](LICENSE).
