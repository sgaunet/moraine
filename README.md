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
- **Theme classification** in three stages: heuristic (altitude → `mountain`)
  → **Ollama** vision model constrained to the theme set (optional) → guaranteed
  **fallback** (`other`). A theme is **always** assigned, even without Ollama.
- **Ollama diagnostics**: a *preflight* logs whether the model is ready, whether Ollama
  is **unreachable** (`ollama serve`), or whether the **model is missing**
  (`ollama pull <model>`). An out-of-list answer from the model is logged (no more silent
  fallback).
- **Sampling**: a group of **3 photos or fewer** is analyzed in full; a large group is
  sampled (evenly spaced photos, configurable count).
- **Safe, idempotent copy**: `O_EXCL` + `fsync`, never overwrites. An identical file
  already present is **skipped** (safe re-runs); a same-named file with different content
  is **suffixed** ` (1)`.
- **Single-photo mode**: pass a file instead of a directory.

## Requirements

- **Go 1.26+** (`go version`).
- **exiftool** (required) — used to read RAW files. Install with
  `brew install exiftool` (macOS) or `sudo apt install libimage-exiftool-perl`
  (Debian/Ubuntu). moraine verifies it at startup and exits if it is missing; point at a
  custom binary with `-exiftool <path>`.
- *(Optional)* [Ollama](https://ollama.com) running locally with a vision model:
  `ollama pull qwen3-vl:8b`. Without Ollama, classification falls back to the heuristic
  and then to the fallback theme.

## Build

```bash
# Static binary, no CGo
CGO_ENABLED=0 go build -o moraine .

# With a version number (otherwise "dev")
CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always)" -o moraine .
```

## Usage

```bash
# Organize a photo directory
./moraine -dest ~/Photos/sorted ~/Photos/2025

# A single photo
./moraine -dest ~/Photos/sorted ~/Photos/2025/IMG_1234.jpg

# Disable Ollama entirely (heuristic + fallback only)
./moraine -sample 0 -dest ~/Photos/sorted ~/Photos/2025

# Custom theme vocabulary + verbose logs
./moraine -themes "friends,hiking,party,nature" -fallback-theme "misc" \
  -log-level debug -dest ~/Photos/sorted ~/Photos/2025

# Detailed help and version
./moraine -help
./moraine -version
```

Each photo is **copied** to `destination/<theme>/<year>/<year-month-day>/`
(e.g. `~/Photos/sorted/nature/2025/2025-08-12/IMG_1234.jpg`). Originals stay in place.

### Flags

| Flag             | Type     | Default                   | Role                                                       |
|------------------|----------|---------------------------|------------------------------------------------------------|
| `<source>`       | argument | *(required)*              | **directory** (batch) or **file** (single photo)           |
| `-dest`          | string   | `<source>/_sorted`        | destination root (excluded from the scan)                  |
| `-gap`           | duration | `6h`                      | max time gap within an event                               |
| `-sample`        | int      | `3`                       | photos sampled per **large** group (`0` = no AI)           |
| `-model`         | string   | `qwen3-vl:8b`             | Ollama vision model                                        |
| `-ollama-url`    | string   | `http://127.0.0.1:11434`  | base URL of the Ollama API                                 |
| `-themes`        | string   | `family,mountain,special-events,nature` | themes (comma-separated slugs)               |
| `-fallback-theme`| string   | `other`                   | fallback theme when none is determined                     |
| `-log-level`     | string   | `info`                    | `debug` \| `info` \| `warn` \| `error`                     |
| `-exiftool`      | string   | `exiftool`                | exiftool executable (name on `PATH` or absolute path); **required** for RAW |
| `-help` / `-h`   | bool     | —                         | print the detailed help and exit                           |
| `-version`       | bool     | —                         | print the version and exit                                 |

**Exit codes**: `0` success, `1` runtime error, `2` usage error.

> **HEIC note**: HEIC photos are dated and organized, but **not** sent to the vision
> model (no pure-Go HEIC decoding, due to the "no CGo" constraint). A HEIC-only group
> falls back to the heuristic or to the fallback theme.
>
> **RAW note**: RAW photos are dated, organized, and **classified** via their embedded
> preview, extracted with **exiftool** (required). Small events (≤3 photos) send every
> eligible photo including RAW; large events prefer JPEG/PNG and extract RAW previews only
> to fill the sample. A RAW with no usable preview is still copied and dated, and falls
> back to the heuristic or the fallback theme.

## Architecture

Business logic in pure Go packages, decoupled from transport (Constitution, Principle III):

```
main.go                 parse flags → typed Config → app.Organize → exit codes
internal/
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
