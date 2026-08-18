# Development

## Prerequisites

- Go 1.26+
- Docker (for images and the local registry)
- Optional: `helm` (to lint/render the chart)

## Build & test

```bash
make build            # binaries into ./bin
make test             # full suite (unit + httptest integration)
go test -run TestX ./internal/pkg   # a single test
make vet
make images           # build the four images
```

The build is cgo-free (`modernc.org/sqlite` is pure Go), so binaries are static
and images are minimal.

## Layout & dependencies

See the repository map in [`../CLAUDE.md`](../CLAUDE.md). Packages are small and
single-purpose. External deps are intentionally few: `yaml.v3`,
`golang-jwt/jwt/v5`, `modernc.org/sqlite`, `prometheus/client_golang`. Prefer the
standard library before adding a dependency.

## The step-metadata contract

`internal/protocol` is the single source of truth for the env-var names that flow
from the job builder into the step binaries. If you add a field:

1. add the constant in `internal/protocol`,
2. set it in `internal/job`,
3. read it in `internal/reviewer` or `internal/publisher`.

Keep secrets at **step** level, never job level, and never give the review step a
forge token (guarded by `internal/job` tests).

## Testing approach

- **Pure logic** (`review`, `diff`, `config`, `token`, `job`, `store`,
  `publisher`) has table/white-box tests.
- **Integration without external services:** the reviewer's oneshot path is
  tested against an `httptest` stub LLM (`internal/reviewer/reviewer_test.go`);
  the GitHub webhook parser is tested with a signed request
  (`internal/forge/github_test.go`).
- When you touch a load-bearing invariant (see `CLAUDE.md`), add or update the
  guarding test.

## Extending

### Add a forge

Create `internal/forge/<name>.go` implementing `forge.Forge` and
`forge.TokenMinter`. Register it in `server.New` and add a `type: <name>` branch
in `parseAnyGitHub`'s equivalent. Keep the `Forge` interface narrow.

### Add a reviewer capability

The reviewer is one binary (`internal/reviewer`) switched by `REVIEW_STRATEGY`.
Agentic tools live in `strategy.go: agentTools()` and are **read-only** — do not
add a write or shell tool (prompt-injection blast radius, `plan.md` §11.3).

### Change the review.json schema

Bump `review.SchemaVersion`, update `internal/review` (types + `Validate`), and
the schema instruction in `internal/reviewer/reviewer.go`. The schema is the
evaluation dataset contract — keep it backward-compatible where possible.

## Conventions

- `gofmt`; doc comments on exported symbols; explain *why*, citing `plan.md`.
- Update `plan.md` and the docs when a load-bearing decision changes.
- Don't reach for a Hades change — the design forbids it (`plan.md` §10.7).

## Release checklist

1. `make test && make vet` green.
2. `make push REGISTRY=ghcr.io/Hades-Scheduler TAG=<version>`.
3. Bump `deploy/helm/momos/Chart.yaml` `version`/`appVersion` and image tags.
4. Update `plan.md` §12 / `CLAUDE.md` if invariants changed.
