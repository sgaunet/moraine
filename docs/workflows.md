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
- Honor the project constitution (`.specify/memory/constitution.md`, v1.0.0):
  pure Go / no CGo / single binary; decoupled logic; test-first; copy-only;
  never overwrite or lose a photo; machine-readable CLI errors.

## Testing Strategy

- **Unit tests**: co-located `internal/**/*_test.go`, written as black-box
  external packages (`package foo_test`).
- **Style**: table-driven with `t.Run` subtests; HTTP dependencies faked with
  `net/http/httptest` rather than a mock framework.
- **Coverage**: both happy and failure paths are required (constitution).
- **Command**: `CGO_ENABLED=1 go test ./... -race -count=1`. CGo is enabled
  *only* for the race detector; production builds keep `CGO_ENABLED=0`.

## Verification Suite (run before every push)

```bash
gofmt -l .                                   # must print nothing
go vet ./...
CGO_ENABLED=1 go test ./... -race -count=1
golangci-lint run
```

## Release Process

- CI (`.github/workflows/ci.yml`) runs two jobs on push/PR: `build-test`
  (gofmt check, `go vet`, race tests) and `lint` (golangci-lint).
- No automated release/tag workflow is configured yet; releases are manual.
  Add a GoReleaser workflow if/when binaries need publishing.
