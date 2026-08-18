# Architecture

Momos turns a forge webhook into a review posted on a pull request, using Hades
as an unmodified execution platform. This document explains the components, the
job topology, the result path, and the security model. Rationale and the
code-verified Hades contract are in [`../plan.md`](../plan.md) §§10–12.

## Components (the service)

The `momos` service (`cmd/momos`, `internal/server`) is stateless except for the
run store. Ingress flow:

1. **Webhook receiver** (`internal/server`, `internal/forge`) — one endpoint per
   forge type. Verifies the HMAC signature, dedupes on the delivery ID, responds
   `202` immediately, and processes asynchronously.
2. **Event normalizer** (`internal/forge`) — the only ingress code that knows a
   forge; produces a neutral `event.ReviewEvent`.
3. **Policy resolver** (`internal/config`) — maps `repo_id` to a resolved
   `Policy` (defaults + first matching glob rule).
4. **Prompt store** (`internal/prompt`) — loads and renders the policy's prompt
   with the event; records a prompt version (`path@contenthash`).
5. **Secret broker** (`internal/forge`, `internal/token`) — mints scoped forge
   tokens (App installation tokens or a PAT) and signs the callback token.
6. **Job builder** (`internal/job`) — assembles the three-step Hades payload.
7. **Hades client** (`internal/hades`) — `POST /build`; reads back the
   server-assigned job ID.
8. **Run store** (`internal/store`) — one `Run` per job; idempotency key
   (`repo + head_sha + policy_hash`), supersede index, evaluation dataset.
9. **Status tracker / reconciler** (`internal/server/reconcile.go`) — polls
   LogManager for runs whose callback never arrived.
10. **Metrics** (`internal/metrics`) — Prometheus at `/metrics`.

## Job topology (three steps)

```
Step 1 Clone     momos-clone       → /shared/repo         continue_on_error
Step 2 Review    momos-reviewer    → /shared/out/review.json  continue_on_error, git, NO forge token
Step 3 Publish   momos-publisher   → GitHub review + check + callback
```

All steps share Hades's hardcoded `/shared` volume. Steps run in array order.
`continue_on_error` on steps 1–2 guarantees the publisher (the *universal
reporter*) always runs and reports — even when the review failed. This requires a
non-empty `script` on those steps (see `plan.md` §12.1).

The **clone step** fetches both base and head (via `refs/pull/N/head` for PRs so
fork heads resolve) so the reviewer can compute `git diff base...head`. It scrubs
the token so `/shared/repo/.git/config` carries no credential into the next step.

The **review step** carries only LLM credentials — no `GIT_TOKEN`, no
`FORGE_TOKEN`. It computes its own diff, and in agentic mode navigates the tree
with read-only tools. This is the isolation boundary.

The **publish step** validates `review.json`, runs a freshness check against the
current PR head, posts a marker-tagged summary comment (idempotent upsert) plus
an inline review (dismissing the prior Momos review), a check run, and calls
back to Momos.

## Result path

Hades has no result callback and no cancel API. Momos closes this with:

- **Primary (fast path):** the publish step POSTs a `RunResult` envelope to
  `POST /v1/runs/{id}/result`, retrying with backoff. The callback token is
  verified idempotently, so retries never 401.
- **Fallback (reconciliation):** a background loop polls LogManager status for
  still-submitted runs past a global timeout and finalizes them. LogManager state
  is in-memory, so this is best-effort; the callback is authoritative for the
  review detail.

```
publish ──(retry backoff)──► POST /v1/runs/{id}/result ──► run store
   fail-all-callbacks?                                        ▲
        └──────────── reconciler polls Hades status ──────────┘
```

## Security model

- **Credentials-only isolation.** The review step has no forge/git token, so it
  cannot push, access private repos, or post — only produce `review.json`. There
  is *no* per-step network policy (Hades cannot express one; `plan.md` §11.4).
- **Prompt-injection containment.** Repository content is untrusted. Oneshot has
  no tools. Agentic has only read-only tools (no write, no shell), so injection
  can at most influence the review text, never take actions. The publisher
  sanitizes model output and never self-approves.
- **git hardening.** `safe.directory`, `--no-ext-diff`, disabled hooks
  (`plan.md` §12.4).
- **Accepted residual:** egress is open, so an injected agentic reviewer could
  exfiltrate the cloned tree; and the pod's IMDS/SA-token exposure is mitigated
  out-of-band ([Hades-Scheduler/hades#482]).

## Forge abstraction

`forge.Forge` is a narrow, forge-neutral interface compiled into both the service
(webhook parse, token mint) and the publisher binary (post, current head).
GitHub is the only implementation; adding GitLab/Gitea is additive.

## Run lifecycle

```
submitted ─► succeeded | review_failed | no_changes | stale | failed | timeout
     └─► superseded (a newer head for the same PR arrived)
```
