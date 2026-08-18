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

## Smoke-test the pipeline by hand

To exercise the whole clone → review → publish pipeline with your **locally
built** images — without the service or a webhook — submit a hand-written job
straight to Hades. This is the fastest way to validate a change to a step image.

1. Build your images and push them where Hades can pull them. The Compose file
   ships a local registry on `localhost:5000` for this:

   ```bash
   docker compose -f deploy/compose.yml up -d registry
   make push REGISTRY=localhost:5000
   ```

2. Get the head and base SHAs of a pull request on a test repo:

   ```bash
   git rev-parse origin/main             # -> BASE_SHA
   git rev-parse origin/<your-pr-branch> # -> HEAD_SHA
   ```

3. Fill in the sample payload (it already points its images at
   `localhost:5000/momos-*`):

   ```bash
   cp deploy/sample-payload.json /tmp/job.json
   # In /tmp/job.json replace:
   #   Hades-Scheduler/hades -> <owner>/<repo>   (name, MOMOS_REPO, GIT_URL, REPO_ID x2)
   #   refs/pull/1/head       -> refs/pull/<PR>/head, and PR_NUMBER "1" -> your PR number
   #   REPLACE_HEAD_SHA (x3)  -> HEAD_SHA
   #   REPLACE_BASE_SHA (x2)  -> BASE_SHA
   #   REPLACE_READ_PAT / REPLACE_WRITE_PAT -> a GitHub token
   #   REPLACE_OPENAI_KEY     -> your OpenAI key
   ```

4. Submit it:

   ```bash
   curl -u hades:$HADES_AUTH_KEY -H 'Content-Type: application/json' \
        --data @/tmp/job.json $HADES_URL/build
   # -> {"message":"Successfully enqueued job","job_id":"..."}
   ```

A review comment should appear on the PR within a minute. `MOMOS_CALLBACK_TOKEN`
is empty in the sample, so the publisher skips the callback — that's fine for a
smoke test.

For setting Momos up against a real repo (service + webhook, images pulled from
GHCR), see [install.md](install.md).

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
2. `make push REGISTRY=ghcr.io/hades-scheduler TAG=<version>`.
3. Bump `deploy/helm/momos/Chart.yaml` `version`/`appVersion` and image tags.
4. Update `plan.md` §12 / `CLAUDE.md` if invariants changed.
