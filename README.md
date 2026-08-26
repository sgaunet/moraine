# moraine

**Automatic photo organizer** — a single, CGo-free Go binary that organizes a photo
directory **with no UI and no interaction**. It analyzes the photos, groups them into
**events** (by capture time), assigns a **theme** to each group, then **copies** each
photo to `destination/<theme>/<year>/<year-month-day>/` — a layout you can change with
`--path-template`. Originals are **never** modified or deleted unless you explicitly ask
for `--move`, and then only after the copy has been verified. Every step is explained in
the logs.

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
- **Customizable layout**: `--path-template "{year}/{month}"` and friends, from
  `{theme}` `{year}` `{month}` `{day}` `{date}`; the default reproduces
  `<theme>/<year>/<year-month-day>`.
- **Per-event and volume reporting**: the summary reports `bytes_copied` /
  `bytes_skipped` (how much a re-run *saved*), and `--output=json` adds an `events`
  array with each event's theme, how that theme was decided, span, and cost.
- **Optional `--move`**: removes each source, but only after re-reading the published
  copy and matching it byte for byte. Never on a skip, an error, an interrupt or a
  dry run — and a moved run is deliberately **not** undoable.
- **Configuration file**: `~/.config/moraine.yaml` (or `--config`) supplies flag
  defaults; a flag always wins, and mode flags are never configurable. Manage it with
  `moraine config` — `show` reports every effective setting and where it came from,
  `set`/`unset` write it with the same flags the real commands take, and `edit` fills
  in a form prefilled with the values already in effect. Your comments survive.
- **Companion (sidecar) files** (on by default): files other software leaves next to a
  photo are copied into the same folder — both appended sidecars (`IMG.jpg.xmp`,
  `IMG.jpg.json`) and same-base-name sidecars (`IMG.xmp`). They follow the photo's final
  name on a collision rename, obey the same no-overwrite rules, and are removed by `clean`
  too. Disable with `--sidecars=false`.
- **Run manifest**: every run records what it placed (photos *and* companions, with
  where each file went and the fingerprint it was left with) as one JSON Lines file
  under `<destination>/.moraine/runs/`. It is the audit trail of a run, and what
  `undo` and `--incremental` read.
- **Undo**: `moraine undo <destination>` gives back what the last run copied — and
  only that. A file the run merely recognised, one edited since, and anything outside
  the destination are all kept. Dry-run by default, `--delete` commits, emptied
  folders are pruned.
- **Incremental runs**: `sort --incremental` skips photos the manifest already records
  as copied (matching size and modification time instead of re-reading both files) and
  reuses each known event's theme, so a re-import over a large library asks the model
  nothing and re-reads nothing.
- **Free-space preflight**: after the scan, the destination filesystem is asked how
  much room it has, and a run that will not fit says so once up front instead of
  reporting the same full disk once per photo. It **warns, never refuses** — the
  estimate cannot see sidecars, and it counts photos an `--incremental` re-run will
  skip, so it is deliberately not allowed to block a run that would have succeeded.
- **Dry run**: `--dry-run` reports exactly what would be copied, skipped or renamed —
  including the ` (1)` renames — and writes nothing at all, not even a folder.
- **Pipe-safe output**: the run result goes to **stdout** (`--output=text|json`), logs
  and progress to **stderr**. Ctrl-C is graceful: it reports how far it got.
- **Progress on a terminal**: stderr is drawn as bullet lines with a progress bar per
  stage — metadata read, classification, copy. `--progress=never` gives the plain log
  records back, which is the form to read when debugging.
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
(delete originals already copied), **`undo`** (remove the copies of the last run),
**`completion`** (shell completion scripts) and **`version`**. Run `moraine --help` to list them
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

# Preview the plan — writes nothing at all (-n dry-run)
./moraine sort --dry-run -d ~/Photos/sorted ~/Photos/2025

# Machine-readable result on stdout, logs discarded
./moraine sort --output=json -d ~/Photos/sorted ~/Photos/2025 2>/dev/null | jq .summary

# Throttle a network drive to one worker, reading and copying alike (-j jobs)
./moraine sort --jobs 1 -d ~/Photos/sorted ~/Photos/2025

# Custom theme vocabulary + per-file logs (-v verbose; -q for errors only)
./moraine sort --themes "friends,hiking,party,nature" --fallback-theme "misc" \
  --verbose -d ~/Photos/sorted ~/Photos/2025

# Re-import a card into an already-organized library: skip what is already there
./moraine sort --incremental -d ~/Photos/sorted ~/Photos/2025

# Delete originals already safely copied — dry-run by default, then commit
./moraine clean -d ~/Photos/sorted ~/Photos/2025            # preview (deletes nothing)
./moraine clean --delete -d ~/Photos/sorted ~/Photos/2025   # actually delete

# Take back what the last sort copied — dry-run by default, then commit
./moraine undo ~/Photos/sorted                              # preview (removes nothing)
./moraine undo --delete ~/Photos/sorted                     # actually remove

# See, and change, the configuration file
moraine config show                       # every effective setting + where it came from
moraine config set sort --gap 8h --jobs 4 # write settings
moraine config edit sort                  # or fill in a form

# Help and version
./moraine --help
./moraine sort --help
./moraine version          # or: ./moraine --version
```

Each photo is **copied** to `destination/<theme>/<year>/<year-month-day>/`
(e.g. `~/Photos/sorted/nature/2025/2025-08-12/IMG_1234.jpg`). Originals stay in place.

### Choosing the folder layout: `--path-template`

The layout below the destination root is a template built from the placeholders
`{theme}` `{year}` `{month}` `{day}` `{date}`, separated by `/`. The default,
`{theme}/{year}/{date}`, is the layout above.

```console
./moraine sort --path-template "{theme}/{year}/{month}" -d ~/Photos/sorted ~/Photos/2025
#   ~/Photos/sorted/nature/2025/08/IMG_1234.jpg

./moraine sort --path-template "{year}/{month}-{day}/{theme}" -d ~/Photos/sorted ~/Photos/2025
#   ~/Photos/sorted/2025/08-12/nature/IMG_1234.jpg

./moraine sort --path-template "{year}" -d ~/Photos/sorted ~/Photos/2025
#   ~/Photos/sorted/2025/IMG_1234.jpg
```

`{year}` is `2025`, `{month}` `08`, `{day}` `12`, `{date}` `2025-08-12`. Literal text
between placeholders is kept (`photos/{year}` → `photos/2025/`). Omitting `{theme}`
is allowed: events of different themes then share a folder, and the usual
skip-identical / ` (N)` rules resolve any name clash.

A template is rejected (exit `2`, before anything is written) when it names an unknown
placeholder, is absolute, has an empty / `.` / `..` segment, or starts with
`.moraine` — the destination's own bookkeeping directory.

**Undated photos**: the date-derived stretch of the path collapses to a single
`unknown-date` segment, so `{theme}/{year}/{date}` gives `<theme>/unknown-date/` and
`{year}/{month}/{theme}` gives `unknown-date/<theme>/` — never a folder that looks
like a real date.

**Changing the template later** is safe but does not move anything: with
`--incremental`, files an earlier run recorded are skipped at the paths they already
have, and the run warns on stderr naming both templates. A full (non-incremental)
re-run copies them into the new layout, leaving the old copies where they are —
`moraine undo` or `moraine clean` cleans up.

### Moving instead of copying: `--move`

By default moraine only ever copies. `--move` removes each source file — but **only
after reading its copy back and confirming byte for byte that it landed intact**:

```console
./moraine sort --move --dry-run -d ~/Photos/sorted ~/Photos/2025   # preview first
./moraine sort --move           -d ~/Photos/sorted ~/Photos/2025
```

The verification is not decoration. moraine hashes the bytes as it writes them (free —
they are already streaming past), then re-reads the published file and compares. If the
two disagree the **destination** is removed and the source is kept, because a provably
wrong file sitting on a canonical name is the one outcome the atomic copy exists to
prevent.

**Budget for the read-back.** That verification costs a second pass over the bytes: a
`--move` run reads every photo twice, once on the way out and once back from the
published file, where a plain copy reads it once. Moving 200 GB over a slow
disk or a network share therefore reads about 400 GB, so plan for roughly twice the read
time of the same run without `--move`, not the same run minus a delete. On a machine with
RAM to spare the read-back is often served from the page cache and costs much less than
that arithmetic suggests; on a full disk or a remote share it is not.

**Nothing else removes a source.** Not a skipped photo — one already in your library is
left alone, since a skip verifies nothing during that run (and with `--incremental` it
never even reads the bytes). Not a failed copy, not an interrupted run, and not
`--dry-run`, which still writes and deletes nothing. The summary reports `moved=N`, and
each JSON record carries `"moved": true`.

> **A move run cannot be undone.** `moraine undo` will not remove those copies: with the
> original gone, the copy is the only remaining file, so deleting it would destroy your
> photo. `undo` reports them as `kept` and says why. If you want a reversible tidy-up,
> use the default copy and then `moraine clean --delete` — which hashes the destination
> before deleting anything and leaves the copies undoable.

### Dating: EXIF, then the file name, then mtime

A date is always assigned, from the first tier that has one:

1. the **EXIF** capture date;
2. a date encoded in the **file name** — `IMG_20230815_120000.jpg`,
   `IMG-20230815-WA0001.jpg`, `PXL_20230815_120000123.jpg`,
   `Screenshot 2023-08-15 at 12.00.00.png`. A folder of downloads shares one
   modification time, so without this tier a year of WhatsApp photos and screenshots
   collapses into one giant "event" dated by the day they were downloaded. The
   pattern is deliberately narrow — a 19xx/20xx year plus a month and a day — so a
   frame counter (`IMG_1234.jpg`) is never mistaken for a date;
3. the file's **modification time**.

A photo left with no usable date at all goes to `<theme>/unknown-date/` rather than
into a folder named after year 1.

**Symlinks** are never followed as directories: a symlinked folder under the source
is not descended into (reported at `--verbose`), while a symlinked file whose name
has a recognised extension is read and copied like any other photo. The destination
is excluded by directory *identity*, so naming it through a symlink or with different
letter case still keeps already-sorted photos out of the scan.

### Confidence and voting

The model reports how sure it is of each answer, and `--min-confidence` rejects a
verdict below a threshold — such a group falls through to the altitude heuristic and
then the fallback theme, exactly as an outright abstention does. It defaults to `0`,
which accepts every verdict: pick a threshold from the eval harness below rather than
from a guess, and note that a model reporting no confidence at all is never rejected
(silence is not evidence of doubt).

`--vote` classifies each sampled photo of a large group **separately** and lets the
answers vote; the share of votes the winning theme takes becomes its confidence, and
a tie abstains. It costs one model call per sampled photo instead of one per group,
which is why it is opt-in. What it buys is mixed events — the lunch stop inside a
hiking day, the party that starts at a dinner table — which a single call cannot see:

```console
# one folder holding 3 mountain photos and 3 meal photos
$ moraine sort -n -l debug -d /tmp/dest /tmp/mixed 2>&1 | grep 'msg=group'
... msg=group size=6 method=model-sample theme=mountain      # all 3 images in one call

$ moraine sort -n --vote -l debug -d /tmp/dest /tmp/mixed 2>&1 | grep -E 'vote result|msg=group'
... msg="vote result" theme="" confidence=0 votes=3 of=3     # 1 mountain, 2 abstentions
... msg=group size=6 method=fallback theme=other
```

In that run the single call answered `mountain` at a self-reported confidence of
**0.9** — the model was confidently wrong, and only the disagreement between photos
caught it. That is the difference between the two signals worth remembering.

### Measuring accuracy

Prompt, threshold and sampling changes are otherwise judged by eye. `task eval`
measures them against a corpus of **your own** labeled photos (none are committed
here — size, licensing, privacy), laid out one directory per theme and one
sub-directory per event:

```
~/eval/mountain/col-du-galibier/*.jpg
~/eval/cook/refuge-lunch/*.jpg
~/eval/family/sunday-lunch/*.jpg
```

```console
$ MORAINE_EVAL_CORPUS=~/eval task eval
  corpus /home/me/eval: 20 events, themes [cook family mountain special-events]
  accuracy 17/20 (85.0%)
    mountain          7/7 100.0%
    cook              5/6  83.3%  (1 -> family)
    special-events    5/7  71.4%  (2 -> family)
    wrong: /home/me/eval/cook/refuge-lunch -> family (method model-all, confidence 0.55)
  confidence: right 0.91 (n=17), wrong 0.62 (n=3) (20 of 20 answers reported one)
    method model-all      12
    method model-sample    8
```

The theme set is read from the corpus, so adding a theme means adding a directory.
The last confidence line is the one to choose `--min-confidence` from: if wrong
answers are as confident as right ones, no threshold will help. `MORAINE_EVAL_MODEL`,
`MORAINE_EVAL_SAMPLE`, `MORAINE_EVAL_VOTE`, `MORAINE_EVAL_MIN_CONFIDENCE` and
`MORAINE_EVAL_MIN_ACCURACY` (a floor that fails the run) tune it; see
`internal/classify/eval_test.go`. Without `MORAINE_EVAL_CORPUS` it skips, so it costs
`task test` and CI nothing.

### Output: data on stdout, logs on stderr

**stdout carries the run result and nothing else**, so `moraine` is safe on either
side of a pipe. Logs, progress and errors always go to **stderr**.

`--output=text` (the default) prints one `key=value` line:

```console
$ ./moraine sort -d ~/Photos/sorted ~/Photos/2025 2>/dev/null
scanned=423 unreadable=2 groups=3 copied=412 skipped=8 renamed=1 errors=0 \
moved=0 bytes_copied=1983472104 bytes_skipped=41203998 \
companions_copied=37 companions_skipped=0 companions_renamed=0 companions_errors=0 \
companions_bytes_copied=284419 companions_bytes_skipped=0 \
dry_run=false interrupted=false
```

`scanned` is how many images the scan found and `unreadable` how many of those the
run could not read metadata from — a file counted there was never placed, so it
appears in no other counter.

`bytes_copied` is what was actually written (copies and renames alike; on a
`--dry-run`, what *would* be written), and `bytes_skipped` is what an
already-identical destination saved writing again — the "41 MB I did not re-copy"
figure that a count of skipped files cannot give you, since 8 skipped files could be
8 KB or 8 GB.

`--output=json` prints one object with every per-file record plus the summary:

```console
$ ./moraine clean -d ~/Photos/sorted ~/Photos/2025 --output=json 2>/dev/null | jq .
{
  "command": "clean",
  "source": "/home/me/Photos/2025",
  "dest": "/home/me/Photos/sorted",
  "delete": false,
  "interrupted": false,
  "results": [
    { "path": "/home/me/Photos/2025/IMG_1234.jpg",
      "decision": "would-delete",
      "reason": "identical copy found" }
  ],
  "summary": { "deleted": 0, "would_delete": 37, "kept": 4, "errors": 0,
               "source_hashed": 41, "dest_hashed": 37 }
}
```

`--output=json` additionally carries an **`events`** array, one entry per event the
run placed — its theme, how that theme was decided (`method`: `model-all`,
`model-sample`, `heuristic`, `manifest` or `fallback`), its capture-time span, and
what placing it cost. There is no text equivalent: the text rendering is one line per
run by contract, so it can carry totals but not a breakdown.

```console
$ ./moraine sort -d ~/Photos/sorted ~/Photos/2025 --output=json 2>/dev/null \
    | jq -c '.events[]'
{"theme":"mountain","method":"model-sample","photos":184,"start":"2025-08-12",
 "end":"2025-08-14","copied":184,"skipped":0,"renamed":0,"errors":0,
 "bytes_copied":892341008,"bytes_skipped":0}
```

An event's counters cover every file it placed — photos **and** their companions — so
`copied` can exceed `photos`. The run's own totals keep the two apart.

Per-file *narration* is a log, not data: `sort` keeps it at `--verbose`, since a real
library produces thousands of lines, while `clean` and `undo` report each decision at
the default level — previewing that plan is the reason you run them.

### Progress on a terminal (`--progress`)

On a terminal, stderr is drawn as bullet lines with a progress bar per stage rather
than as plain log records:

```console
$ moraine sort -d ~/Photos/sorted ~/Photos/2025
  • scan images=423 excluded_dest=/home/me/Photos/sorted
  • metadata read · 421 of 423
  • model ready url=http://127.0.0.1:11434 model=qwen3-vl:8b
  • events classified · 6
  • copying photos [============>       ] 61%
```

There is a bar for each stage whose size is known in advance — the metadata read (one
unit per file), classification (one per event) and the copy (one per photo, companions
travelling with the photo they belong to). The **per-event `group` line is dropped**
here, since the two bars already say what it says and it would arrive once per event,
pushing them up the screen as a library grows; `--output=json`'s `events` array carries
those facts in full, and `--progress=never` restores the line. `undo` gets one over the records it is
unwinding, and `clean` a spinner while it hashes the destination, since neither its
index nor its source walk knows how much there is to do until it is done. A preview
never claims to have written: `sort --dry-run` ends on `photos checked`, not
`photos placed`.

`--progress` decides when that happens:

| Value    | Behaviour                                                          |
|----------|--------------------------------------------------------------------|
| `auto`   | the default — draw only when every condition below holds            |
| `always` | draw regardless, e.g. to record a demo                             |
| `never`  | keep the plain log records                                         |

`auto` draws only when **all** of these are true, and falls back to the plain records
otherwise:

- **both stdout and stderr are terminals.** Constitution Principle V forbids progress
  bars when stdout is not a TTY, and that is taken literally — so
  `moraine sort > result.txt` shows no bars even from a terminal. Use
  `--progress=always` if you want them anyway.
- **`NO_COLOR` is unset.** The bullet renderer has no monochrome mode, so honouring
  `NO_COLOR` means not drawing at all.
- **`TERM` is not `dumb`.** Every repaint moves the cursor.
- **the verbosity is `info` or `warn`.** `--verbose` asks for a line per file, which
  competes with a bar for the same rows and is what a debugging session wants anyway;
  `--quiet` asks for silence, and a bar is not silence.

**`--progress=never` is the debugging form**, and it is byte-for-byte what moraine
wrote before bars existed: one self-contained, timestamped `key=value` record per
event, greppable and diffable. Nothing about the run changes — **stdout is identical
in every mode**, so no script or pipeline is affected by this setting.

`undo` renders the same two ways, over the records of the run it is unwinding:

```console
$ ./moraine undo ~/Photos/sorted 2>/dev/null
removed=0 would_remove=412 kept=8 errors=0 dirs_pruned=0 delete=false interrupted=false
```

**Interrupting** a run (Ctrl-C) is graceful: everything already copied is complete and
durable, the summary still prints on stdout with `interrupted=true`, and stderr says
how far it got (`interrupted: copied 412, skipped 8, …`). Exit code is `1`, since the
run did not finish what was asked.

> **Migrating to the stdout contract** (v0, breaking): logs used to go to **stdout**,
> mixed in with everything else. They now go to **stderr**, and stdout carries only the
> run result described above. If you were capturing logs with `moraine sort … > log.txt`,
> use `2> log.txt`. Per-photo lines also moved to `--verbose`. At v0 the stdout contract
> may still change; it will be signalled here when it does.

> **Migrating to filename dating** (v0): photos with no EXIF date whose name carries
> one used to be dated by their modification time, and are now dated from the name.
> A non-incremental re-run over an existing library therefore places them under their
> correct date folder, leaving the old mtime-dated copy where it was. Nothing is lost
> (moraine copies unless you pass `--move`); `moraine undo` unwinds the run that made the
> new copies, and `moraine clean` removes sources already archived.

> **Migrating from the pre-1.0 flag CLI**: the interface moved to subcommands with
> GNU-style flags. `moraine <dir>` → `moraine sort <dir>`; `-dest` → `--dest` (or `-d`);
> `-version` → `moraine version` (or `--version`). The old rootless form and single-dash
> long flags are no longer accepted.

### Configuration file

Settings you always pass can live in a YAML file instead. A **command-line flag always
beats the file, and the file always beats the built-in default.** There is no
environment tier: `$MORAINE_CONFIG` and `$XDG_CONFIG_HOME` pick *which file* is read,
and no `MORAINE_GAP`-style variable sets an individual setting.

moraine reads the first of these that exists:

1. the file named by `--config`;
2. `$MORAINE_CONFIG`;
3. `$XDG_CONFIG_HOME/moraine.yaml`;
4. `~/.config/moraine.yaml`.

Having no configuration file is the normal case and not an error. A file named
*explicitly* (`--config` or `$MORAINE_CONFIG`) must exist, though — silently ignoring a
typo'd path would leave you wondering why nothing applied. Set `MORAINE_CONFIG=`
(empty) to ignore the file entirely for one run.

```yaml
# ~/.config/moraine.yaml
# Keys at the top level are shared; a command's section overrides them.
log_level: warn
output: json
progress: auto
dest: /Volumes/photos/sorted

sort:
  gap: 6h
  themes: [mountain, special-events, cook, family]
  fallback_theme: other
  path_template: "{theme}/{year}/{date}"
  model: qwen3-vl:8b
  ollama_url: http://127.0.0.1:11434
  sample: 3
  jobs: 4
  sidecars: true
  mountain_altitude: 1500
  min_confidence: 0.6
  vote: true
  exiftool: exiftool

clean:
  dest: /Volumes/photos/sorted   # overrides the shared value above
```

Keys are named after the flags, in `snake_case` (`--path-template` → `path_template`).
`gap` is a duration string (`"6h"`, `"30m"`); `themes` is a list. `undo` accepts only
`log_level`, `output` and `progress` — it takes its destination as an argument.

**Decoding is strict**: an unrecognised key is an error (exit `2`) rather than a
setting that silently does nothing, and the message names the file and the line.

**Mode flags are deliberately not configurable** — `--dry-run`, `--delete`,
`--incremental`, `--move`, `--quiet` and `--verbose`. The first four choose what a
single invocation *does*: a file that made `clean` delete by default would defeat the
whole point of `clean` being dry-run until you ask, and one that made every `sort` a
no-op would be worse. `--quiet`/`--verbose` are shorthands over `log_level`, so set
`log_level` directly.

### Managing the file: `moraine config`

You never have to open the file yourself.

```bash
moraine config path                        # which file is in effect, and why
moraine config show                        # every effective setting + its origin
moraine config show sort --output=json     # just sort's, machine-readable

moraine config set sort --gap 8h --jobs 4  # write settings
moraine config set shared --log-level warn # write them at the top level
moraine config unset sort gap              # take one back
moraine config edit sort                   # or answer a form instead
```

**`config show` answers "what will this run actually use?"** — the file's value where
the file sets one, the built-in default everywhere else, each tagged with which it was:

```console
$ moraine config show sort
sort.log_level=info origin=default
sort.gap=8h origin=file
sort.themes=mountain,special-events,cook,family origin=default
...
```

`--origins=false` drops the second pair for a bare `key=value` listing; `--output=json`
gives each setting a `value`, `origin` and `default`.

**`config set <section>` takes the flags of the command that section configures** —
`--gap` here is sort's `--gap`, with the same parsing, the same error messages and the
same shorthands. Only the flags you actually type are written; a flag you leave out is
left alone. Writing a value that happens to equal the default still writes it, because
typing it means you want it pinned.

**`config unset <section> <setting>...`** removes settings so they fall back to the
default. A setting can be named either way round (`path-template` or `path_template`),
and removing the last setting of a section removes the section with it.

**`config edit [section]`** ([huh][huh]) asks in two steps: **pick the settings you
want to change** from a list, then answer a question for each.

```
Which settings do you want to change?
space picks, enter moves on — picking nothing changes nothing

 > • sort.gap                6h
   • sort.themes             mountain,special-events,cook,family
   • sort.jobs               4  ←

x toggle • ↑ up • ↓ down • / filter • enter submit
```

Picking first is what keeps the form short: it is as long as your change and never
longer, and **enter saves from the very first screen**. The list shows each setting's
current value and marks with `←` the ones your file already sets, so it doubles as a
way to find the setting you were after; `/` filters it.

Each question is then **prefilled with the value in effect** — the file's, or the
default where the file is silent — ready to edit (`ctrl+u` clears it if you would
rather type a fresh one). Only answers you actually change are written, so a setting
you look at and accept is not written, and filling in a form does not pin today's
defaults into your file and freeze you at them. Answering a question with the default
*removes* the setting, which is what `config unset` does.

The form draws on **stderr**, so `moraine config edit --output=json > settings.json`
works while the questions are still on screen. `--accessible` swaps the full-screen
form for plain numbered prompts — for a screen reader, and the only mode that works
when stdin is not a terminal.

[huh]: https://github.com/charmbracelet/huh

**Your comments are kept.** Writing goes through the file's YAML structure rather than
re-serialising it, so comments, key order and any key moraine did not touch all
survive:

```console
$ moraine config set sort --gap 12h && cat ~/.config/moraine.yaml
# my library
log_level: warn # keep it quiet
sort:
  # a day out is one event
  gap: 12h
  themes: [mountain, cook]
```

Two caveats, both one-off: blank-line spacing and indentation width are **normalised
the first time moraine writes the file** (to two spaces), and stable from then on — a
second write of the same settings does not touch the file at all. And the result is
checked before it is saved: a value no run could use is refused with the file left
exactly as it was.

`--dry-run` on `set`, `unset` and `edit` reports what would result and writes nothing.

> On `config set`, `--output` names the **setting** to write, since it is one of the
> settings the commands accept — it does not change how `config set` reports its own
> result. Using it says so on stderr. For a machine-readable view, use
> `moraine config show --output=json`.

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
| `--path-template`  |       | string   | `{theme}/{year}/{date}`   | destination layout from `{theme}` `{year}` `{month}` `{day}` `{date}` |
| `--config`         |       | string   | *(see below)*             | read settings from this YAML file (flags always win)       |
| `--fallback-theme` |       | string   | `other`                   | fallback theme when none is determined                     |
| `--log-level`      | `-l`  | string   | `info`                    | `debug` \| `info` \| `warn` \| `error`                     |
| `--quiet`          | `-q`  | bool     | `false`                   | log errors only (excludes `--verbose` / `--log-level`)     |
| `--verbose`        | `-v`  | bool     | `false`                   | log every file (excludes `--quiet` / `--log-level`)        |
| `--output`         |       | string   | `text`                    | stdout format: `text` \| `json`                            |
| `--progress`       |       | string   | `auto`                    | stderr rendering: `auto` \| `always` \| `never` (see below) |
| `--dry-run`        | `-n`  | bool     | `false`                   | report the plan; writes **nothing**, not even a folder     |
| `--move`           |       | bool     | `false`                   | remove each source after **verifying** its copy; never on a skip or error; **not undoable** |
| `--jobs`           | `-j`  | int      | `0`                       | EXIF reader and copy workers (`0` = one per CPU)           |
| `--exiftool`       |       | string   | `exiftool`                | exiftool executable (name on `PATH` or absolute path); **required** for RAW |
| `--sidecars`       |       | bool     | `true`                    | also copy each photo's companion/sidecar files (`--sidecars=false` to disable) |
| `--incremental`    |       | bool     | `false`                   | skip sources the run manifest already records as copied, and reuse each known event's theme |
| `--mountain-altitude` |    | float    | `1500`                    | metres at/above which the altitude heuristic labels a group `mountain` (must be `> 0`) |
| `--min-confidence` |       | float    | `0`                       | reject a model verdict below this confidence, `0`..`1` (`0` = accept every verdict) |
| `--vote`           |       | bool     | `false`                   | classify each sampled photo of a large group separately and take the majority (one model call per sampled photo) |
| `--help`           | `-h`  | bool     | —                         | print the detailed help and exit                           |

### `clean` flags

| Flag          | Short | Type     | Default            | Role                                                          |
|---------------|-------|----------|--------------------|--------------------------------------------------------------|
| `<source>`    |       | argument | *(required)*       | source **directory** to clean                                |
| `--dest`      | `-d`  | string   | `<source>/_sorted` | destination library holding the copies (**never** deleted from) |
| `--delete`    |       | bool     | `false`            | actually delete matched originals (default: dry-run)         |
| `--log-level` | `-l`  | string   | `info`             | `debug` \| `info` \| `warn` \| `error`                       |
| `--quiet`     | `-q`  | bool     | `false`            | log errors only (excludes `--verbose` / `--log-level`)        |
| `--verbose`   | `-v`  | bool     | `false`            | log every file (excludes `--quiet` / `--log-level`)          |
| `--output`    |       | string   | `text`             | stdout format: `text` \| `json`                              |
| `--progress`  |       | string   | `auto`             | stderr rendering: `auto` \| `always` \| `never` (see below)   |
| `--config`    |       | string   | *(see above)*      | read settings from this YAML file (flags always win)          |

### `undo` flags

| Flag          | Short | Type     | Default | Role                                                       |
|---------------|-------|----------|---------|------------------------------------------------------------|
| `<dest>`      |       | argument | *(required)* | destination **root** to unwind (holds `.moraine/runs/`) |
| `--delete`    |       | bool     | `false` | actually remove the recorded copies (default: dry-run)     |
| `--log-level` | `-l`  | string   | `info`  | `debug` \| `info` \| `warn` \| `error`                     |
| `--quiet`     | `-q`  | bool     | `false` | log errors only (excludes `--verbose` / `--log-level`)      |
| `--verbose`   | `-v`  | bool     | `false` | log every file (excludes `--quiet` / `--log-level`)         |
| `--output`    |       | string   | `text`  | stdout format: `text` \| `json`                             |
| `--progress`  |       | string   | `auto`  | stderr rendering: `auto` \| `always` \| `never` (see below)  |
| `--config`    |       | string   | *(see above)* | read settings from this YAML file (flags always win)  |

`undo` acts on the **most recent** run recorded under the destination. After a
successful `--delete` pass that run's manifest is kept as an audit trail and marked
`.undone`, so running `undo` again steps back to the run before it. A run that copied
nothing (an unchanged re-run) has nothing to give back and says so.

### `config` flags

| Command | Argument | Flags | Role |
|---------|----------|-------|------|
| `config show` | `[section]` | `--origins` (bool, `true`), `--output`, `--config` | print the effective settings and where each came from |
| `config path` | — | `--output`, `--config` | print which configuration file is in effect, and why |
| `config set <section>` | — | every flag of the command that section configures, plus `--dry-run` (`-n`) | write settings |
| `config unset <section>` | `<setting>...` | `--dry-run` (`-n`), `--output`, `--config` | remove settings |
| `config edit` | `[section]` | `--accessible`, `--dry-run` (`-n`), `--output`, `--config` | answer a prefilled form |

`<section>` is `shared`, `sort`, `clean` or `undo`; `show` and `edit` cover every
section when it is omitted. **Exit codes**: `0` success, `1` runtime failure (nowhere
to write, an unwritable file, no terminal for the form, an aborted form), `2` usage
error (unknown section or setting, an invalid value, or a value no run could use — in
which case nothing is written).

`moraine version` reports the build's identity — version, commit, build time, Go
version and platform — and honours `--output=json`; `moraine --version` prints just
its first line. **Exit codes**: `0` success, `1` runtime error (including an
interrupted run), `2` usage error.

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
| `config` section arguments | `shared` \| `sort` \| `clean` \| `undo` |
| `config unset` setting names | the settings that section has, minus the ones already typed |

The candidate lists are derived from the same constants the parser uses
(`config.DefaultThemes`, `photo.Extensions`), so they cannot drift from what
`moraine` actually accepts.

> **RAW note**: RAW pixels can't be decoded in pure Go (the "no CGo" constraint), so RAW
> photos are **classified** via their embedded JPEG preview, extracted with **exiftool**
> (required). Small events (≤3 photos) send every eligible photo, previews included;
> large events prefer JPEG/PNG and extract previews only to fill the sample.
>
> **HEIC note**: a HEIC embeds no JPEG preview — its derived images are HEVC, so exiftool
> has nothing to copy out — and it is instead decoded by the first of **`sips`** (built
> into macOS), **`heif-convert`**, **`ffmpeg`** or **`magick`** found on `PATH`. That
> converter is **optional**: without one, HEIC photos are still scanned, dated, organized
> and copied, and only their classification falls back. The run logs which converter it
> picked, or names them all if it found none. **Behavior change (v0):** HEIC photos used
> to be skipped by the model unconditionally, so HEIC events that previously landed on
> the fallback theme are now themed by the model when a converter is present.
>
> **Model input note**: images are downscaled to 1024 px on their long side before being
> sent (a vision model tiles its input, so full-resolution pixels only cost time and
> bandwidth), a RAW or HEIC shot alongside its own JPEG is sent once rather than twice,
> and each group's capture time, highest altitude and location are passed as text
> alongside the pixels.
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
  cli/      Cobra command tree (sort/clean/undo/config/version), flag binding, exit-code mapping
  config/   centralized typed configuration + validation (slugs, file/directory source)
  app/      testable orchestration: scan → exif → cluster → classify → organize + logs
  configfile/ optional YAML config file (flag > file > default); reads it, and edits it
            as a YAML node tree so `moraine config` keeps the comments in it
  configform/ the interactive form behind `moraine config edit` (huh); knows no flags,
            so it stays testable without a terminal
  photo/    domain types (Photo, Cluster, Format)
  scan/     recursive walk, format filter, EXCLUDES destRoot
  exifmeta/ EXIF extraction (date, GPS, altitude); date falls back to the file name, then mtime
  cluster/  temporal grouping (configurable gap)
  classify/ heuristic → Ollama (constrained themes) → fallback; Ollama HTTP client
  organize/ builds the destination path from --path-template, hash-based identity,
            durable copy, and the verified source removal behind --move
  manifest/ per-run JSON Lines record of every placement (undo + incremental read it)
  undo/     removes the copies one recorded run made, and nothing else
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
