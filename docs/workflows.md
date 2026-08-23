# Development Workflows

## Feature Development

This repo uses a Spec-Kit driven flow (`specs/<feature>/`):

1. Create a feature branch from `main` (e.g. `002-auto-photo-organizer`).
2. Spec → plan → tasks live under `specs/<feature>/` (`spec.md`, `plan.md`,
   `tasks.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`).
3. Implement test-first; keep business logic in `internal/` packages.
4. Run the verification suite locally (see below) before pushing.
5. Open a PR; CI must be green before merge.

## Code Review Process

- All changes go through a PR; automated checks (`.github/workflows/ci.yml`)
  must pass.
- Honor the project constitution (`.specify/memory/constitution.md`, v2.0.0):
  single purpose; reproducible static binary (`CGO_ENABLED=0`, `-trimpath`);
  thin commands over domain packages; concrete idiomatic Go; pipe-safe UX
  (data-only stdout, logs on stderr, documented exit codes 0/1/2); interruptible
  bounded I/O; test-first black-box tests; pinned committed codegen; minimal
  audited dependencies. Known carried-over gaps are listed in that file's Sync
  Impact Report.

## Testing Strategy

- **Unit tests**: co-located `internal/**/*_test.go`, written as black-box
  external packages (`package foo_test`).
- **Style**: table-driven with `t.Run` subtests; HTTP dependencies faked with
  `net/http/httptest` rather than a mock framework.
- **Coverage**: both happy and failure paths are required (constitution).
- **Command**: `CGO_ENABLED=1 go test ./... -race -count=1`. CGo is enabled
  *only* for the race detector; production builds keep `CGO_ENABLED=0`.

## Measuring Classification Accuracy

`task eval` is a measurement, not a gate: it runs the real Ollama model over a
labeled corpus of photos you supply (`MORAINE_EVAL_CORPUS=<dir>`, laid out
`<corpus>/<theme>/<event>/*.jpg`) and reports accuracy, a per-theme confusion
breakdown, and the confidence behind right versus wrong answers. It lives in
`internal/classify/eval_test.go` and skips when the variable is unset, so it never
runs in CI — a real corpus cannot be committed and a vision model cannot run there.
Run it before and after any change to the prompt, the sampling rules, or the
confidence threshold.

## Verification Suite (run before every push)

```bash
gofmt -l .                                   # must print nothing
go vet ./...
CGO_ENABLED=1 go test ./... -race -count=1
golangci-lint run
```

## Release Process

- CI (GitHub Actions, mirrored under `.forgejo/`): `test.yml` runs `task test`,
  `linter.yml` runs `task lint`, `snapshot.yml` runs `task snapshot` on `main`.
- `release.yml` fires on a `v*` tag and runs `task release` (GoReleaser), which
  builds linux/darwin x amd64/arm64 `.tar.gz` archives plus checksums, publishes
  the GitHub release, and pushes `Casks/moraine.rb` to the `sgaunet/homebrew-tools`
  tap. It needs `HOMEBREW_TAP_TOKEN` configured in the CI environment.
- moraine ships as a Homebrew **cask**, not a formula. The cask declares
  `exiftool` and generates shell completions at install time by running
  `moraine completion <shell>`, so:
  - `archives[].formats` must stay `tar.gz`. A `binary` archive stages the file
    under its versioned name, GoReleaser can only name the executable `moraine`
    (it is not templated), and `brew` resets `PATH` during install -- every shell
    then fails with exit 127 and Homebrew only *warns*.
  - the quarantine-stripping hook must stay in `hooks.pre.install`
    (-> cask `preflight`). Homebrew sorts cask artifacts by class, not by stanza
    order: `preflight` -> `binary` -> `generate_completions_from_executable` ->
    `postflight`, so a postflight hook runs too late.
  - never add `args: [completion]` alongside `shell_parameter_format: cobra` --
    `:cobra` already appends `completion <shell>`, and a second one makes
    Homebrew write cobra's *help text* into the completion file.
- Run `goreleaser check` when touching `.goreleaser.yml`; it fails on deprecated
  properties.
