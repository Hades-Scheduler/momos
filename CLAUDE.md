# CLAUDE.md — Momos developer & agent guide

Momos is **AI code review as a service** built on top of the **unmodified** Hades
job scheduler. It receives forge webhooks, resolves a per-repository review
policy, builds a three-step Hades job, and writes the review back into the pull
request from inside the job — using only Hades's public `POST /build` API.

> **Read `plan.md` first.** Sections **10–12** are the binding decisions and the
> code-verified Hades contract every component is built against. Precedence on
> conflict: **§12 > §11 > §10 > §§1–9.** This file summarizes what you must not
> break; `plan.md` explains why.

## The one hard rule

**Zero changes to Hades.** Momos uses only `POST /build`, the payload schema, the
shared `/shared` volume, and the LogManager status endpoint. If you find yourself
wanting to change Hades, stop — the design exists specifically to avoid that
(`plan.md` §10.7 confirms even the metadata path needs no patch).

## Architecture at a glance

```
GitHub webhook ─► momos service ─► POST /build ─► Hades
                     │                               │
                     │ (mint tokens, build payload)  ▼
                     │                    ┌── Step 1 clone  (momos-clone)  ─► /shared/repo
                     │                    ├── Step 2 review (momos-reviewer, git) ─► /shared/out/review.json
                     │                    └── Step 3 publish(momos-publisher) ─► GitHub review + check
                     ▼                               │
             run store (SQLite) ◄── callback ◄───────┘
                     ▲
             reconciler polls LogManager status (fallback)
```

Three step images + one service binary. See `plan.md` §10.8 / §11.

## Repository map

| Path | What it is |
|---|---|
| `cmd/momos` | the service (webhook → job, callback, reconciler) |
| `cmd/reviewer` | the review step binary (oneshot + agentic) |
| `cmd/publisher` | the publish step binary (universal reporter) |
| `internal/review` | `review.json` schema + parse-with-repair (the evaluation contract) |
| `internal/event` | forge-neutral `ReviewEvent` + callback `RunResult` envelope |
| `internal/config` | YAML config, `${ENV}`/`${ENV:-default}` substitution, policy resolution |
| `internal/forge` | thin `Forge` interface + GitHub impl (webhook, mint, post) |
| `internal/prompt` | filesystem prompt store + `text/template` rendering |
| `internal/job` | builds the 3-step Hades payload |
| `internal/hades` | `POST /build` + status client |
| `internal/store` | SQLite run store (idempotency, supersede, reconcile) |
| `internal/token` | signed step/callback bootstrap tokens |
| `internal/protocol` | env-var names shared between builder and step binaries |
| `internal/diff` | unified-diff parser (added-line classification) |
| `internal/llm` | OpenAI-compatible chat client |
| `internal/reviewer` | review logic (git diff, oneshot, agentic loop, cost limits) |
| `internal/publisher` | publish logic (freshness, split summary/inline, callback) |
| `internal/server` | HTTP handlers + reconciler |
| `internal/metrics` | Prometheus instruments |
| `images/` | Dockerfiles + `clone.sh` |
| `deploy/` | Compose, Helm, example config, sample payload |
| `prompts/` | review prompts (templated with `ReviewEvent`) |

## Invariants you must not break

These are load-bearing decisions from `plan.md` §§10–12. Tests guard several of them (`internal/job`, `internal/token`, `internal/reviewer`).

1. **The review step holds no forge/git credentials.** That is the *entire*
   isolation mechanism — no network policy (§11.4). `internal/job` must never put
   `GIT_TOKEN` or `FORGE_TOKEN` in the review step metadata. Guarded by
   `TestReviewStepHasNoForgeCredentials`.
2. **`continue_on_error` steps must set a non-empty `script`.** In operator mode
   the `... || true` wrapping only applies to a non-empty script; an entrypoint
   step is not wrapped and a failure aborts the pod (§12.1). Clone and review use
   `continue_on_error: true` with `script` set.
3. **All step images carry `/bin/bash`.** The Docker executor runs `/bin/bash -c`
   (§12.1.3). Alpine images must `apk add bash`.
4. **The clone step fetches BOTH base and head** so `git diff base...head` works,
   including fork PRs via `refs/pull/N/head` (§12.2). Do not assume a single-branch
   clone is enough.
5. **`review.json` is untrusted output.** The publisher sanitizes it and never
   self-approves/merges (§12.4). Keep `sanitize()` on every model-provided string.
6. **Structured JSON is not server-guaranteed.** Always parse with
   `review.Parse` (repair + validate + one retry), never assume the endpoint
   enforced the schema (§11.1).
7. **Callback token verification is idempotent** (pure `token.Verify`), so the
   publisher's retries don't 401 (§12.3).
8. **git runs hardened**: `safe.directory=*`, `--no-ext-diff`, hooks disabled
   (§12.4). See `internal/reviewer/git.go`.

## Common tasks

```bash
make build           # binaries into ./bin
make test            # full suite
make images          # build the 4 images (REGISTRY=localhost:5000 by default)
make push            # build + push to $(REGISTRY)
make run             # run the service against deploy/config.example.yaml
```

### Add a new forge (e.g. GitLab)

Implement `forge.Forge` (and `forge.TokenMinter`) in `internal/forge/gitlab.go`,
register it in `server.New`, and add a `type: gitlab` branch. The `Forge`
interface is deliberately narrow (`plan.md` §11.8) — resist adding methods.

### Add/adjust a review strategy

The reviewer is one binary switched by `REVIEW_STRATEGY` (`internal/reviewer`).
Agentic tools are **read-only** by design (`strategy.go: agentTools`) — never add
a write or shell tool (§11.3 blast-radius argument).

### Change the step metadata contract

Edit `internal/protocol` (the single source of truth) and both sides that use it
(`internal/job` sets, `internal/reviewer`/`internal/publisher` read).

## Style

- Standard Go; `gofmt`; keep packages small and dependency-light.
- Prefer stdlib. Current external deps: `yaml.v3`, `golang-jwt/jwt/v5`,
  `modernc.org/sqlite` (pure Go, no cgo), `prometheus/client_golang`.
- Every exported symbol has a doc comment; comments explain *why*, citing
  `plan.md` sections for non-obvious decisions.
- Update `plan.md` and these docs when you change a load-bearing decision.

## Known interim shortcuts (tracked)

- **Clone image is bespoke `momos-clone`**, not `git-container`, until
  [ls1intum/git-container#20] adds base+head fetch (§12.2).
- **Credentials are embed-at-submission**, not fetch-at-step-start, until #20
  lands; the token endpoints (`/v1/runs/{id}/clone-token`, `/publish-token`) and
  `token` package already implement the fetch seam (§11.5).
- **Pod hardening** (SA-token, IMDS) is out-of-band, filed as
  [ls1intum/hades#482] (§12.4).
