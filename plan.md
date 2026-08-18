# Hades Use Case 2: AI Code Review as a Service

**Working title: Momos** (Μῶμος, the Greek god of criticism and blame; fits the ls1intum naming convention and is still unused)

Status: 2026-08-17. Based on an analysis of `github.com/Hades-Scheduler/hades` (HadesAPI, HadesScheduler, HadesOperator, HadesLogManager, `shared/payload`, `shared/redact`).

---

## 1. The goal in one sentence

A standalone, configurable service that receives webhooks from GitHub, GitLab, or Gitea, resolves an out-of-band review policy per repository (prompt, image, model, limits), builds a multi-step Hades job from it, submits it through the public `POST /build` API, and writes the review comment back into the pull request from inside the container.

> **Read Sections 10-12 first, in reverse-precedence order.** [§10 Locked Decisions & Verified Hades Contract](#10-locked-decisions--verified-hades-contract) is the code-verified Hades contract; [§11 Grilling Session Decisions](#11-grilling-session-decisions-2026-08-17) is the design-interview log; [§12 Review Corrections & Hardening](#12-review-corrections--hardening-agy-pass-2026-08-17) folds in an independent `agy` review that corrected two claims and added the hardening set. **On any conflict the later section wins: §12 > §11 > §10 > §§1-9.** Zero changes to Hades are required (§10.7, corrected). Two Hades-ecosystem improvements are filed as issues: [Hades-Scheduler/hades#482](https://github.com/Hades-Scheduler/hades/issues/482) and [Hades-Scheduler/git-container#20](https://github.com/Hades-Scheduler/git-container/issues/20).

**Hard constraint: zero changes to Hades.** Momos uses only the documented public surface of Hades:

| Hades surface used | Purpose |
|---|---|
| `POST /build` (Basic Auth, user `hades`, password `AUTH_KEY`) | Submit a job |
| `payload.RESTPayload` (`name`, `priority`, `metadata`, `steps[]`) | Job description |
| Shared volume `/shared` between steps | Data handover Clone -> Review -> Publish |
| LogManager `GET /jobs/{id}/status`, `GET /jobs/{id}/logs` | Observability, timeout detection, evaluation data |

This is also the central argument of the paper: the second use case emerges without a single commit in the scheduler.

---

## 2. What the Hades analysis implies for the design

Five findings from the code that shape the architecture directly:

1. **No callback, no webhook.** Hades never reports results back actively. There are only status and log queries against the LogManager. Consequence: the result path must originate from the job itself (the chosen variant) or be polled.
2. **Job-level `metadata` is injected as an environment variable into *every* step, step-level `metadata` only into the respective step.** Consequence: always set secrets at step level. The LLM key must not end up in the clone step, and the forge write token must not end up in the reviewer step.
3. **There is no built-in clone logic.** The `GIT_*` conventions in `shared/payload/example.json` are not interpreted by the scheduler; they are plain environment variables for a clone image. Momos therefore brings its own lean clone image (or uses `alpine/git` plus a script).
4. **`shared/redact` masks metadata values** based on a key denylist (`token|password|secret|api[_-]?key|...`) and an entropy heuristic before they surface in the dashboard or logs. Momos should deliberately name its secret keys so that this matches (for example `LLM_API_KEY`, `FORGE_TOKEN`).
5. **No cancel endpoint.** Once submitted, a job runs to completion. Consequence: superseding (a new commit makes an older review obsolete) must be solved by Momos itself, specifically in the publish step via a freshness check against the current head.

---

## 3. Component architecture

```
   GitHub / GitLab / Gitea
            │  Webhook (PR opened/synchronize, push)
            ▼
┌──────────────────────────────────────────────────────────┐
│ MOMOS (Go, single binary)                                │
│                                                          │
│  ① Webhook Receiver     HMAC check, delivery-ID dedupe   │
│  ② Event Normalizer     -> ReviewEvent (forge-neutral)   │
│  ③ Policy Resolver      repo -> ReviewPolicy (YAML)      │
│  ④ Prompt Store         versioned prompts, templating    │
│  ⑤ Secret Broker        short-lived installation tokens  │
│  ⑥ Job Builder          ReviewEvent+Policy -> payload    │
│  ⑦ Hades Client         POST /build                      │
│  ⑧ Run Store            SQLite/Postgres, idempotency     │
│  ⑨ Status Tracker       polls LogManager, timeouts       │
│  ⑩ Metrics/UI           Prometheus, /runs, /healthz      │
└──────────────────────────────────────────────────────────┘
            │  POST /build
            ▼
        HADES (unmodified)
            │
      ┌─────┴──────────────────────────────────┐
      │ Step 1 Clone   Step 2 Review   Step 3 Publish
      │   /shared/repo   /shared/out     forge API
      └────────────────────────────────────────┘
                                          │
                                          ▼
                              PR comment / check run
```

### Responsibilities in detail

**① Webhook Receiver.** One HTTP endpoint per forge type (`/hooks/github`, `/hooks/gitlab`, `/hooks/gitea`). Signature verification (GitHub: HMAC-SHA256 over `X-Hub-Signature-256`; GitLab: `X-Gitlab-Token`; Gitea: analogous). Body size limit, constant-time comparison, delivery ID stored in the run store for replay protection. Responds immediately with `202 Accepted` and processes asynchronously so the forge does not run into timeouts.

**② Event Normalizer.** Translates the forge-specific payload into an internal, neutral model. This is the only place on the ingress side that carries forge knowledge:

```go
type ReviewEvent struct {
    Forge       Forge        // github | gitlab | gitea
    RepoID      string       // "Hades-Scheduler/hades"
    Kind        EventKind    // pull_request | push
    Action      string       // opened | synchronize | reopened
    CloneURL    string
    BaseRef     string       // "main"
    BaseSHA     string
    HeadRef     string
    HeadSHA     string
    PRNumber    int
    IsFork      bool
    Author      string
    DeliveryID  string
}
```

**③ Policy Resolver.** Maps `RepoID` to a `ReviewPolicy`: trigger filters, prompt reference, reviewer strategy, image, model, resource and diff limits, publish mode. Defaults plus per-repository overrides, glob matching for org-wide rules (`ls1intum/*`).

**④ Prompt Store.** Prompts deliberately do **not** live in the repository. Two backends behind the same interface:
- *Filesystem/ConfigMap*: `prompts/*.md`, versioned in the Momos deployment repository.
- *Git*: a separate prompt repository that Momos pulls periodically (`prompt_repo`, `ref`, `path`). This allows review prompts to have their own review process and gives the paper clean prompt versioning (the prompt commit SHA is recorded per run).

Templating via Go `text/template` with the `ReviewEvent` as context, so a prompt can reference `{{ .BaseRef }}` or `{{ .RepoID }}`.

**⑤ Secret Broker.** Mints short-lived credentials per job instead of long-lived PATs. On GitHub: an app installation token (valid for 1 h, scoped to the repository). On GitLab: a project access token or a CI-job-style token. Separate tokens for reading (clone) and writing (publish) wherever the forge supports it.

**⑥ Job Builder.** The core (see section 4).

**⑦ Hades Client.** A thin wrapper around `POST /build`. Priority mapping: interactive PR reviews at 3, batch or nightly reviews at 1. This makes Momos visibly exercise the Hades priority feature, which is easy to substantiate in the paper.

**⑧ Run Store.** SQLite for single-node, Postgres for HA. One `Run` record per submitted job: event, policy hash, prompt version, Hades job ID, status, timestamps, result metrics. It doubles as the idempotency key (`repo + head_sha + policy_hash`), the supersede index, and the raw dataset for the evaluation.

**⑨ Status Tracker.** Polls `GET /jobs/{id}/status` with backoff, marks runs as `Succeeded`/`Failed`/`Timeout`, and pulls `GET /jobs/{id}/logs` when needed for debugging and cost data. Result posting does not depend on it; the tracker is pure observation and reconciliation.

**⑩ Metrics/UI.** Prometheus metrics (runs per status, latency from hook to comment, token cost, queue wait time) plus a minimal status page. Sufficient for the measurements in the paper.

---

## 4. Job design: three steps

The three-step split is deliberate because it separates secrets, keeps the LLM call swappable, and lets the publish step run even when the review fails.

```json
{
  "name": "momos: review Hades-Scheduler/hades#412 @ 8f3a1c2",
  "priority": 3,
  "metadata": {
    "MOMOS_RUN_ID": "01J...",
    "MOMOS_REPO": "Hades-Scheduler/hades",
    "MOMOS_HEAD_SHA": "8f3a1c2...",
    "MOMOS_PR": "412"
  },
  "steps": [
    {
      "id": 1,
      "name": "Clone & Context",
      "image": "ghcr.io/Hades-Scheduler/momos-clone:1.0",
      "script": "/usr/local/bin/prepare-context.sh",
      "cpu_limit": 1000,
      "memory_limit": "1G",
      "metadata": {
        "GIT_URL": "https://github.com/Hades-Scheduler/hades.git",
        "GIT_TOKEN": "ghs_… (short-lived, read-only)",
        "GIT_BASE_SHA": "…",
        "GIT_HEAD_SHA": "…",
        "CLONE_DEPTH": "50",
        "MAX_DIFF_BYTES": "400000"
      }
    },
    {
      "id": 2,
      "name": "AI Review",
      "image": "ghcr.io/Hades-Scheduler/momos-reviewer-oneshot:1.0",
      "script": "/usr/local/bin/review.sh",
      "cpu_limit": 2000,
      "memory_limit": "4G",
      "continue_on_error": true,
      "metadata": {
        "REVIEW_STRATEGY": "oneshot",
        "LLM_BASE_URL": "https://api.anthropic.com",
        "LLM_MODEL": "claude-sonnet-4-5",
        "LLM_API_KEY": "sk-… (this step only)",
        "PROMPT_B64": "…base64 of the rendered prompt…",
        "PROMPT_VERSION": "prompts/go-backend@a91f0c3",
        "MAX_OUTPUT_TOKENS": "8000"
      }
    },
    {
      "id": 3,
      "name": "Publish",
      "image": "ghcr.io/Hades-Scheduler/momos-publisher:1.0",
      "script": "/usr/local/bin/publish.sh",
      "cpu_limit": 500,
      "memory_limit": "512M",
      "metadata": {
        "FORGE": "github",
        "FORGE_API": "https://api.github.com",
        "FORGE_TOKEN": "ghs_… (write, short-lived)",
        "PUBLISH_MODE": "pr_review",
        "INLINE_COMMENTS": "true",
        "CHECK_RUN": "true",
        "EXPECTED_HEAD_SHA": "8f3a1c2…"
      }
    }
  ]
}
```

### Step 1: Clone & Context
Shallow clone into `/shared/repo`, then context preparation into `/shared/context/`:
- `diff.patch` (diff `base...head`; for push events, against the predecessor commit)
- `changed_files.json` (paths, line ranges, additions/deletions)
- `event.json` (the normalized `ReviewEvent`)
- optionally `tree.txt`, `README.md`, language detection for repository context

The step enforces the limits: if the diff exceeds `MAX_DIFF_BYTES` or touches more than N files, it writes a truncation flag that the reviewer and later the publisher make transparent. Afterwards the token is removed from the environment and `.git/config` is scrubbed so it cannot leak into the next step.

### Step 2: AI Review
Reads `/shared/context/*` and the decoded prompt, and writes exactly one file: `/shared/out/review.json`. **`continue_on_error: true`**, so that even an LLM timeout still gets a publish run that surfaces the failure instead of staying silent.

Two interchangeable strategies behind an identical contract (same input paths, same output schema), configurable per repository:

| Strategy | Image | Flow | Character |
|---|---|---|---|
| `oneshot` | `momos-reviewer-oneshot` | Diff plus context plus prompt in a single API call, structured JSON response enforced | deterministic, cheap, easy to measure, fixed latency |
| `agentic` | `momos-reviewer-agentic` | Agent CLI inside the container, may navigate the repository, read tests and neighbouring files, multiple turns | finds context-dependent defects, more expensive, variable runtime, needs hard turn and cost limits |

The fact that both satisfy the same contract is the methodological gift for the paper: identical trigger, identical repository, identical prompt, only the strategy varies. That is a clean A/B comparison within one and the same platform.

### Step 3: Publish
1. Validates `review.json` against the schema. An invalid or missing file leads to a short error comment plus a failed check run, never a silent abort.
2. **Freshness check**: queries the current head of the PR. If it differs from `EXPECTED_HEAD_SHA`, only a summary comment is posted, or nothing at all (configurable). This substitutes for the missing cancel API in Hades.
3. Maps findings onto diff positions. Findings on lines that are not part of the diff move into the summary block instead of going inline.
4. Posts: one PR review with inline comments plus a summary, along with a check run / commit status carrying the `verdict` and the cost figure. Idempotent via a marker line (`<!-- momos:run=… -->`), so repeated runs update the previous comment instead of duplicating it.

### Result schema (`review.json`)
Deliberately designed as a stable, versioned contract, because it doubles as the raw dataset of the evaluation:

```json
{
  "schema_version": "1.0",
  "verdict": "comment",
  "summary": "Three potential nil pointer dereferences in the new scheduler path …",
  "findings": [
    {
      "file": "HadesScheduler/docker/scheduler.go",
      "line": 102,
      "end_line": 108,
      "severity": "major",
      "category": "correctness",
      "message": "The volume is created before the error path but is not cleaned up if CreateVolume partially fails.",
      "suggestion": "Register the defer immediately after successful creation."
    }
  ],
  "truncated": false,
  "usage": {
    "model": "claude-sonnet-4-5",
    "input_tokens": 41233,
    "output_tokens": 1877,
    "cost_usd": 0.19,
    "turns": 1
  },
  "meta": {
    "strategy": "oneshot",
    "prompt_version": "prompts/go-backend@a91f0c3",
    "duration_ms": 18422
  }
}
```

### How does the prompt get into the container?
Recommendation: **base64-encoded as step metadata (`PROMPT_B64`)**. No additional infrastructure, no escaping problems in the shell script, and the prompt is part of the versioned job payload and therefore reproducible. For very large prompt libraries or prompt sets with attachments, the alternative is a fetch in the clone step against `GET /prompts/{id}` on Momos using a one-time job token. The practical bound is the NATS message size (1 MB by default), which applies to the whole payload; a prompt of a few tens of kilobytes is unproblematic.

---

## 5. Configuration model

A single YAML file (or ConfigMap), hot-reloadable, with environment variable substitution for secrets.

```yaml
hades:
  url: https://hades.example.com
  auth_key: ${HADES_AUTH_KEY}
  log_manager_url: https://hades-logs.example.com

forges:
  - id: github-main
    type: github
    api: https://api.github.com
    webhook_secret: ${GH_WEBHOOK_SECRET}
    app:
      app_id: 123456
      private_key: ${GH_APP_KEY}

defaults:
  priority: 3
  timeout: 15m
  limits:
    max_changed_files: 200
    max_diff_bytes: 400000
    max_cost_usd: 1.00
  reviewer:
    strategy: oneshot
    image: ghcr.io/Hades-Scheduler/momos-reviewer-oneshot:1.0
    model: claude-sonnet-4-5
    base_url: https://api.anthropic.com
    api_key: ${LLM_API_KEY}
    cpu_limit: 2000
    memory_limit: 4G
  publish:
    mode: pr_review
    inline_comments: true
    check_run: true
  fork_policy: summary_only     # no secret access for fork PRs

repositories:
  - match: "Hades-Scheduler/hades"
    forge: github-main
    prompt: prompts/go-backend-review.md
    triggers:
      pull_request: [opened, synchronize, reopened]
    reviewer:
      strategy: agentic
      image: ghcr.io/Hades-Scheduler/momos-reviewer-agentic:1.0
      model: claude-opus-4-1

  - match: "ls1intum/artemis-exercise-*"
    forge: github-main
    prompt: prompts/student-project-review.md
    triggers:
      pull_request: [opened, synchronize]
      push: ["refs/heads/main"]
    priority: 1
    reviewer:
      base_url: http://vllm.internal:8000/v1   # local model, no data leaving the cluster
      model: qwen3-coder-30b
```

The `reviewer.base_url` field is the single line the sovereignty argument of the paper hangs on: same path, same image, once with a cloud model and once with a self-hosted one.

---

## 6. Security

Critical, because untrusted third-party code, an LLM, and secrets all meet here.

| Risk | Mitigation |
|---|---|
| Forged webhooks | HMAC signature verification, constant-time comparison, delivery-ID dedupe, body size limit |
| Fork PRs with secret access (the classic CI hole) | `fork_policy`: read-only clone by default, publish as a summary comment via an app token the PR author cannot influence. Never expose repository secrets to fork jobs |
| Secret leakage into logs | Secrets exclusively at step level; choose key names that `shared/redact` matches (`*_TOKEN`, `*_API_KEY`); `set +x` in scripts; strip the token from the environment and `.git/config` after cloning |
| Prompt injection from the repository | Repository content is untrusted input and is clearly delimited as such in the prompt. The decisive part: the model has no tools that write. Publishing happens in a **separate step without an LLM** that processes validated JSON only. An injected instruction can therefore at most influence the comment text, not the API calls |
| Escape from the reviewer container | Hades container isolation, CPU and memory limits per step, no Docker socket passthrough, dedicated namespaces via the operator in K8s mode |
| Egress from the reviewer | Network policy: only the LLM endpoint and package mirrors reachable. With a local model, no internet egress at all |
| Runaway cost | `max_cost_usd` per run, turn limit for the agentic strategy, per-repository budget in the run store, hard timeouts |
| Stale reviews | Freshness check in the publish step against the current head |

---

## 7. Implementation plan

| Milestone | Content | Outcome | Effort |
|---|---|---|---|
| **M0 Spike** | Hand-written payload against a local Hades instance, three minimal container images, one real PR comment | Proof that the approach works without changing Hades | approx. 1 week |
| **M1 Skeleton** | Go service: webhook receiver (GitHub), normalizer, policy resolver, job builder, Hades client, run store, idempotency, callback endpoint (`POST /v1/runs/{id}/result`), plus Docker Compose and Helm chart (10.9) | Fully automatic end to end: open a PR, get a comment; one-command deploy | 2 to 3 weeks |
| **M2 Publish maturity** | Inline comments with diff position mapping, check runs, verdict, marker-based idempotency, freshness check, error paths | Usable in production on real repositories | 1 to 2 weeks |
| **M3 Multi-forge** | GitLab adapter (and optionally Gitea), extract a clean forge interface | Evidence of forge independence | 1 to 2 weeks |
| **M4 Strategies** | Agentic strategy as a second image, turn and cost limits, per-repository strategy selection | An A/B-capable setup | 2 weeks |
| **M5 Evaluation** | Instrumentation, load generator, dataset, analysis scripts, local model via vLLM/Ollama | Numbers for the paper | 2 to 3 weeks |

Realistically that is about three months alongside other work until you have defensible measurements. M0 through M2 are the critical path; everything after that can run in parallel (and is assignable to student work: the GitLab adapter and the agentic strategy are each a clean bachelor's thesis).

---

## 8. Evaluation design for the paper

Aligned with the two chosen focal points (generalizability of Hades, operating model and sovereignty).

### Claim 1: Hades generalizes beyond the Artemis context
- **Zero-diff evidence:** Momos runs against an unmodified Hades release. Metric: 0 changed lines in Hades, with the API surface used listed explicitly as a table (section 1).
- **Integration effort:** LOC in Momos, of which the share of Hades-specific integration code (expected to be a few hundred lines: build the payload, POST it, poll status). Contrasted with what building a dedicated execution layer would have cost (queue, isolation, log aggregation, K8s scheduling).
- **Reuse matrix:** which Hades capabilities use case 1 (Artemis) and use case 2 (Momos) each rely on: multi-step, shared volume, prioritization, resource limits, secret redaction, log aggregation, Docker and K8s operating modes. This is the key table of the paper.
- **Contrasting load profiles:** Artemis jobs are short, CPU-bound, and high-frequency (submission peaks); review jobs are longer, I/O- and network-bound, and latency-sensitive. That the same scheduler serves both without one starving the other can be demonstrated directly with priorities and mixed load.

### Claim 2: Self-hosted, sovereign AI review is practical
- **Deployment variants:** (a) cloud LLM, (b) self-hosted model in your own cluster. Identical code, one changed configuration field.
- **Metrics per variant:** latency from hook to comment (p50/p95), cost per review, findings per review, share of accepted comments, data egress (in variant (b) measurably zero, enforced by egress policy).
- **Comparison baselines:** GitHub Copilot code review and/or CodeRabbit on the same PRs, plus human reviews as a reference. Important for context, but to be framed honestly: Momos is platform work, not model work.
- **Qualitative analysis:** a dataset of N (suggested: 60 to 100) real PRs from ls1intum repositories and student projects. Two annotators rate findings as correct / irrelevant / wrong. From that, precision and a rough recall estimate against human review comments. Report inter-rater agreement.
- **Scaling experiment:** M concurrent PR events, queue wait time and throughput against the concurrency setting, once with the Docker executor and once with the K8s operator. Shows that the platform holds up without Momos needing to know anything about scheduling.
- **Threats to validity:** model nondeterminism (multiple runs per PR), selection bias in the repositories, drifting model versions, small annotator count.

### Data that is essentially free to collect
The run store plus `review.json` already yield per run: prompt version, strategy, model, token and cost figures, runtime, queue time, findings with severity and category, PR metadata. This should be logged from the very beginning so the evaluation does not have to be retrofitted later.

---

## 9. Open points for the next round

Deliberately left undecided because they do not block the core:

1. **Incremental reviews.** On `synchronize`, look only at the delta diff since the last review, or always at the full PR diff? The former saves cost, the latter is more consistent. Proposal: delta by default, full diff on `opened` and on demand (`/momos review full`).
2. **Command interface in the PR.** Should a comment such as `/momos review` trigger a run? Cheap to build (one more webhook event type), helpful for user acceptance and for controlled experiments.
3. **Large repositories.** Sparse checkout, a repository cache volume, or always a fresh shallow clone? With the Docker executor a persistent cache volume is feasible; in K8s operator mode it is more involved.
4. **Multi-tenancy.** Who may change prompts and policies? With several chairs or courses you need tenant separation in the prompt store and separate LLM budgets.
5. **The prompt repository as its own review subject.** Versioning prompts and changing them via PRs would be an elegant self-application: Momos reviews the changes to its own prompts.
6. **Relationship to Athena.** Athena generates feedback suggestions for submissions, Momos performs reviews on repositories. The delineation should be explicit in the paper, otherwise the question will certainly come up in peer review.
7. **The naming question.** Momos is my favourite. Alternatives in the same register: Aristarchus (the critic), Kritias, Argos (the many-eyed).

---

## 10. Locked Decisions & Verified Hades Contract

This section records the binding decisions and the facts we verified directly against the Hades source at `github.com/Hades-Scheduler/hades` (local checkout `../hades`). Every file:line reference was read, not assumed. When the earlier sections and this section disagree, **this section wins**.

### 10.1 Supported executors: Docker and K8s operator only

Hades ships three execution backends with **divergent semantics**. Momos targets exactly two and treats the third as unsupported.

| Backend | Supported | Reason |
|---|---|---|
| Docker executor (`HadesScheduler/docker/`) | **Yes** (M0-M2 reference) | Full `continue_on_error` and metadata support |
| K8s operator mode (`HadesScheduler/HadesOperator/`) | **Yes** (scaling / production) | `continue_on_error` honoured (script wrapped in `... \|\| true`, `buildjob_controller.go:365`) |
| K8s legacy direct mode (`HadesScheduler/k8s/`) | **No, explicitly unsupported** | `continue_on_error` is silently ignored; a failed step aborts the pod and the publish step never runs, breaking the reporting invariant |

Deployments MUST run the Docker executor or the operator. Legacy direct mode is a documented non-target.

### 10.2 Step semantics (verified)

- **Shared volume:** all steps share one writable volume mounted at the hardcoded path `/shared` (Docker: named volume `shared-<jobID>`, `docker/step.go:57-64`; operator: `emptyDir` named `shared`, `buildjob_controller.go:347-360`). The Clone -> Review -> Publish handover over `/shared/{repo,context,out}` is sound.
- **Step ordering is array order, not the `id` field.** No backend sorts by `id` (the CRD doc comment claiming "by ID" is wrong). Momos MUST emit `steps[]` in intended execution order; `id` is cosmetic.
- **Default failure behaviour = stop.** A non-zero step exit aborts the remaining steps unless `continue_on_error: true` is set on that step (`docker/job.go:59-63`).
- **Metadata -> env vars.** Job-level `metadata` is injected into every step and step-level `metadata` only into its own step, with step-level overriding on key collision. This holds identically in **both** supported executors: Docker (`docker/job.go:39-45`) and the **operator** (`buildjob_controller.go:357-361`: `maps.Copy(env, bj.Spec.Metadata)` then `maps.Copy(env, s.Metadata)`, reserved keys last). **No Hades change is required** - an earlier draft wrongly claimed the operator dropped job metadata (see the correction in 10.7). Corrected via the agy review pass (§12) and confirmed against the source.
- **`continue_on_error` requires a non-empty `script`.** In operator mode the `... || true` wrapping is applied **only when `script` is non-empty** (`buildjob_controller.go:370-386`). A step that relies on the image **entrypoint** (empty `script`) is *not* wrapped, so a non-zero exit aborts the pod and skips later steps. **Rule:** every `continue_on_error` step MUST set a non-empty `script` (e.g. `/app/reviewer`). See §12.
- **Shell differs by executor.** Docker runs the script under `/bin/bash -c` (`docker/env.go:6`), the operator under `/bin/sh -c`. Scripts must be POSIX-`sh`-safe **and** the image must carry the shell the executor uses. See §12.
- **Payload schema is exactly as used in Section 4.** Every field in the sample payload exists in `shared/payload/payload.go`: job `priority` (`:18`, default 3), job `metadata` (`:28`), `steps[]` (`:29`); per step `id` (`:35`), `name` (`:36`), `image` (`:37`), `script` (`:38`), `continue_on_error` (`:39`), `metadata` (`:40`), `cpu_limit` uint millicores (`:41`), `memory_limit` string e.g. `512M` (`:42`).

### 10.3 `POST /build` contract (verified)

- **Auth:** HTTP Basic Auth, user `hades`, password `AUTH_KEY` (`HadesAPI/router.go:91`). Auth is only enforced when the server was started with a non-empty `AUTH_KEY`.
- **Body:** `payload.RESTPayload`. Priority defaults to 3 if omitted (`router.go:162`).
- **Hades assigns the job ID server-side.** Any `id` in the payload is overwritten by `uuid.New()` at `router.go:180`. Momos CANNOT pre-choose the Hades job ID.
- **Response:** `200 OK` with `{"message": "...", "job_id": "<uuid>"}` (`router.go:208-211`).
- **Correlation:** Momos generates its own `run_id`, submits the job, then stores the `run_id <-> job_id` mapping from the response. The callback and all correlation key off **Momos's `run_id`** (carried in step metadata), never the Hades job ID.

### 10.4 Image resolution: images must be registry-pullable

The Docker executor **unconditionally `ImagePull`s every step image** and fails the whole job if any pull fails - there is no "use local image if present" fallback (`docker/docker.go:77-89`). Consequence:

- All `momos-*` images MUST be resolvable from a registry the Hades daemon can reach.
- **Local development:** run a local registry (`registry:2` on `localhost:5000`) and tag images `localhost:5000/momos-*`, or use public GHCR images. A plain local `docker build` tag will not work.
- **Production:** publish to `ghcr.io/Hades-Scheduler/momos-*`; ensure the daemon has pull credentials for private images.

### 10.5 Result path: explicit job callback, no log scraping

Hades never reports results back (no callback, no cancel API). We do **not** reconstruct results from logs - `redact.Drop` strips all metadata from logs (`shared/redact/redact.go:182-191`) and the text redactor mangles high-entropy substrings, so log scraping is rejected. Instead **the job calls back to Momos explicitly.**

- **Endpoint:** `POST /v1/runs/{run_id}/result` on Momos.
- **Reachability:** confirmed that step containers have outbound internet egress, so the callback targets a routable Momos URL (public or cluster-internal service DNS). `MOMOS_CALLBACK_URL` is passed in the publish step's metadata.
- **Auth:** at job-build time Momos mints a single-use bearer token, stores its hash under `run_id`, and passes it as `MOMOS_CALLBACK_TOKEN` in the **publish step's** metadata only (step-level, so it never touches the clone or review steps). The publish step sends `Authorization: Bearer <token>`; Momos validates and burns it.
- **Envelope:** `{ run_id, status: succeeded | review_failed | no_changes | stale, review: <review.json | null>, comment_url, error }`.

### 10.6 Failure-coverage invariant: publish is the universal reporter

To guarantee a callback on (almost) every path despite Hades having no result callback:

- Steps 1 (Clone & Context) and 2 (AI Review) carry **`continue_on_error: true`**.
- The review step **guards against empty input**: if there is no diff / context (e.g. clone failed), it exits early WITHOUT calling the LLM (no wasted API spend) and writes no `review.json`.
- Step 3 (Publish) **always runs** and is the sole reporter. It inspects `/shared/out/review.json`: if a valid file is present it posts the review; otherwise it posts a short "review did not run" notice. In both cases it fires the callback.
- The only unreported case is the **publish container itself dying** (infra failure) or the job never starting. This is caught by a **run-store timeout** in Momos (reconciliation), not by log polling. LogManager status polling is an optional later safety net, never on the critical path.

### 10.7 Zero-diff status: 100%, no Hades change (corrected)

An earlier draft of this section claimed one operator bugfix was needed (job-level metadata injection). **That was wrong** - the operator already injects job-level metadata into every step (`buildjob_controller.go:357-361`, verified in the agy review pass, §12). **Momos requires zero changes to Hades** and the paper's Section 1 claim stands unqualified. (Two *separate* Hades-ecosystem improvements were filed as issues - operator pod hardening [Hades-Scheduler/hades#482] and git-container base+head fetch [Hades-Scheduler/git-container#20] - but neither is a change to the Hades *scheduler* Momos depends on, and Momos works without them via documented workarounds; see §12.)

### 10.8 Container images

> **Superseded in part by [Section 11](#11-grilling-session-decisions-2026-08-17).** The step topology and image set below were revised during the grilling session: the clone step **reuses `git-container`** (no bespoke `momos-clone`), context generation folds **into** the reviewer, and oneshot + agentic are **one image**. See §11.1-11.3. The table here is kept for the field-level detail; §11 wins on shape.

Job topology is **three steps** (revised from the four-step draft):

| Step | Image | Purpose |
|---|---|---|
| 1. Clone | `git-container` (reused, `ghcr.io/Hades-Scheduler/git-container`) | Full clone of the head branch into `/shared/<repo>` (`REPOSITORY_DIR=/shared` to avoid the fallback quirk). Read token via `HADES_0_USERNAME`/`HADES_0_PASSWORD`. go-git auth is transport-only, so no token persists in `.git/config`. |
| 2. Review | `momos-reviewer` (Go, **carries `git`**) | Reads the clone, runs its own `git diff <base>...<head>` (and, in agentic mode, navigates the tree), decodes `PROMPT_B64`, calls the LLM over the OpenAI-compatible client, writes `/shared/out/review.json`. `continue_on_error: true`. **No forge credentials** (the isolation boundary, 11.4). |
| 3. Publish | `momos-publisher` (Go) | Validate `review.json`, freshness check vs `EXPECTED_HEAD_SHA`, post via the modern GitHub reviews API, callback to Momos. Universal reporter (10.6). |

All Momos images published to `ghcr.io/Hades-Scheduler/momos-*` and mirrored to a local registry for development (10.4).

**Clone-container reuse (reversed from the draft).** The earlier draft said build a bespoke `momos-clone` because `git-container` produces no diff and has the `REPOSITORY_DIR` fallback quirk (`env.go` + `main.go:27`). **Decision reversed:** reuse `git-container` for the clone and move all diff/context work into the reviewer step (which carries `git`). The quirk is sidestepped by setting `REPOSITORY_DIR=/shared` (the mount root always exists). `git-container` may be extended if the clone step needs more (e.g. the fetch-at-step-start token flow, 11.4) - we own that project.

### 10.9 Deployment artefacts (deliverables)

Momos ships with both, kept in sync, so it is trivial to stand up:

- **Docker Compose** (`deploy/compose.yml`): Momos service + its datastore (SQLite volume for single-node) + a local `registry:2`, wired to talk to an existing Hades. Used for M0/M1 and local dev.
- **Helm chart** (`deploy/helm/momos/`): Deployment, Service, Ingress (for the webhook receiver and the `/v1/runs/{id}/result` callback endpoint), ConfigMap for the policy YAML (Section 5), Secrets for `HADES_AUTH_KEY` / forge app keys / LLM keys, optional Postgres for HA. Targets the same cluster as the Hades operator.

Both must expose the webhook endpoints and the callback endpoint, and inject the Momos config from Section 5.

---

## 11. Grilling Session Decisions (2026-08-17)

Decisions locked in a design-interview pass over the whole tree. Where these conflict with earlier sections, **Section 11 wins**.

### 11.1 LLM client: OpenAI-compatible protocol only

The reviewer speaks **exactly one** wire protocol: OpenAI Chat Completions (`POST {base_url}/chat/completions`). No native Anthropic protocol. Reach each provider through its OpenAI-compatible surface:

- Anthropic → its OpenAI-compatibility endpoint (`base_url: https://api.anthropic.com/v1/`).
- OpenAI → `https://api.openai.com/v1/`.
- Local / self-hosted (vLLM, Ollama, etc.) → the cluster-internal `…/v1/` endpoint.

`base_url` + `model` + `api_key` (+ `provider` is **not** needed - one protocol) select the target. The single-line sovereignty swap of §5 (`reviewer.base_url`) is preserved.

**Consequence - structured JSON is not uniformly enforceable.** OpenAI supports strict `response_format: json_schema`; vLLM via guided decoding; Ollama via `format`; Anthropic's compat layer's `json_schema` support is unreliable. So the reviewer MUST NOT depend on server-side schema enforcement: prompt for JSON, request `response_format: {type: "json_object"}` when the endpoint supports it, and **always parse-with-repair, validate against the `review.json` schema, and retry once** on failure.

### 11.2 Implementation languages

- **Clone step:** reuse `git-container` (Go, already exists).
- **Reviewer + publisher:** **Go** static binaries. Rationale: real `encoding/json` parse-with-repair, schema validation, retry, and GitHub posting are miserable in shell; Go shares the `review.json` struct and Hades payload structs across Momos, reviewer, and publisher as one codebase.

### 11.3 Reviewer: one image, oneshot + agentic behind a strategy switch

- **One `momos-reviewer` image**, `REVIEW_STRATEGY` env selecting `oneshot` | `agentic`. Same binary, same `/shared` → `review.json` contract - which makes the paper's A/B provably "same artefact, only strategy varies."
- **Context is computed in-reviewer, not in a separate step.** The image carries `git`; it runs its own `git diff <base>...<head>` and, in agentic mode, navigates the tree.
- **Agentic driver = a custom Go agent loop** using OpenAI-compatible **tool-calling**, with a **fixed, read-only tool set**: `read_file`, `list_dir`, `grep`, `git_show` / `git_log` / `git_diff`. **No write tool, no arbitrary-shell tool** - the agent navigates but cannot execute injected commands, bounding prompt-injection to "influence the review text." **Hard turn limit + running cost budget checked each turn**, aborting to whatever `review.json` exists.
- **Constraint:** agentic requires a tool-calling-capable OpenAI-compatible endpoint (OpenAI, or vLLM with a function-calling model). **Agentic-on-Anthropic-compat is unsupported** (weak tool-calling); oneshot works there. Agentic's home is the local-model / sovereignty deployment.

### 11.4 Security: credentials-only isolation of the review step

"No remote access after clone" is enforced **solely by the review step holding no forge credentials** - no NetworkPolicies (Hades sets zero network config anywhere, and in operator mode all steps share one pod's network identity, so per-step egress is impossible without changing Hades; verified against the scheduler source). This prevents every *authenticated* action (push, private-repo access, comment posting) from the review step. **Accepted residual:** egress stays open, so an injected agentic reviewer could exfiltrate the cloned tree to an arbitrary endpoint; for public repos this is near-zero value, for private repos it is a knowingly accepted risk. True per-step "clone-open / review-closed" isolation is a **non-goal** under zero-diff.

### 11.5 Credential delivery

- **Real build: fetch-at-step-start.** Momos puts only a **Momos-scoped bootstrap token** in the *clone* and *publish* step metadata (step-level; the review step gets none, so its inability to obtain a forge token is structural). At step start each calls Momos, which mints a **fresh, scoped GitHub App installation token** on demand - `contents:read` for clone, `pull_requests:write` + `checks:write` for publish. Survives arbitrary queue delay and keeps **real forge tokens entirely out of Hades / NATS / dashboard**. Requires a small `git-container` extension to fetch its token.
- **M0 spike: PAT-embed.** A classic PAT embedded in step metadata at submission - no App, no fetch, fastest path to a comment.

### 11.6 Result path

- **Publisher** posts the PR review, then **retries the callback** to Momos with bounded exponential backoff.
- **Momos** treats the callback as the fast path and **polls `GET /jobs/{id}/status`**, reacting to terminal status, bounded by a **global configurable timeout** (§5 `defaults.timeout`, now global). No log-scraping.
- **Accepted:** if every callback for a job is lost, the run is marked `Succeeded`/`Failed` from status but carries no `review.json` result (the comment still posted). Rare given the retry; acceptable while evaluation is out of scope.

### 11.7 Publisher posting behaviour

- **Modern GitHub reviews API** with `path` + `line` + `side` - no diff-position arithmetic. Findings on lines outside the diff spill into the summary.
- **Split surfaces:** the **summary** (verdict + cost + out-of-diff findings) is a single marker-tagged (`<!-- momos:run=… -->`) **issue comment**, upserted (find-by-marker → PATCH, else POST) → fully idempotent. The **inline findings** are a **PR review** (`event: COMMENT`); on re-run, **dismiss/minimize the prior Momos review** (found by bot author + marker) before posting the new one, so inline comments don't stack across pushes.
- **Freshness-gated:** fetch current head first; if ≠ `EXPECTED_HEAD_SHA`, update the summary comment only, skip the inline review.
- **Check run** carries `verdict` + cost as the machine-readable status.

### 11.8 Forge abstraction

Define a **thin `Forge` interface now, GitHub as the sole implementation**, as shared Go code compiled into **both** the Momos service and the publisher binary. Minimal surface: `ParseWebhook(req) → ReviewEvent`, `MintToken(scope) → token`, `PostReview` / `PostSummary` / `PostCheckRun`, `CurrentHead(pr) → sha`. Resist speculative methods. Adding GitLab/Gitea (Claim 1 evidence, §7 M3) is then additive, not a refactor that reopens the publisher image.

### 11.9 First-light target & M0 definition of done

- **First light:** a throwaway repo you own on **github.com** + **real Anthropic** via its OpenAI-compat endpoint (`claude-sonnet-4-5` class, pennies per PR) + the existing Hades Docker-compose stack. **Local-model support is built from the start** (the OpenAI-compatible client makes it a config swap), even though first light uses Anthropic.
- **M0 = option (b):** three hand-written-payload steps + one real PR comment **and** a callback to a ~50-line Go Momos stub (`POST /v1/runs/{id}/result` + trivial run store), PAT-embedded tokens, on the local Hades. Definition of done: PR event → hand-crafted 3-step payload → `POST /build` → **comment appears on the PR** *and* **the stub run store shows a completed run with the parsed `review.json`** (proves the callback-reachability unknown early). Milestones are otherwise soft - the full system gets built regardless.

---

## 12. Review Corrections & Hardening (agy pass, 2026-08-17)

An independent `agy` review, cross-checked against the Hades source, corrected two earlier claims and surfaced several real gaps. **Section 12 supersedes conflicting earlier text.**

### 12.1 Corrections to the verified contract (code-checked)

1. **No Hades patch needed - 100% zero-diff.** The operator already injects job-level metadata into every step (`buildjob_controller.go:357-361`). The earlier "one accepted change" (old §10.7) was based on a misread and is withdrawn. §10.2 and §10.7 are corrected.
2. **`continue_on_error` needs a non-empty `script`.** Operator wraps `... || true` only when `script` is non-empty (`buildjob_controller.go:370-386`). **Invariant:** the review step (and any `continue_on_error` step) MUST set a non-empty `script` pointing at the binary (e.g. `/app/reviewer`), *not* rely on the image entrypoint. Otherwise a failed review aborts the pod and publish never runs - silently breaking the "publish is the universal reporter" guarantee (10.6).
3. **Executor shell differs:** Docker `/bin/bash -c`, operator `/bin/sh -c`. Momos scripts must be POSIX-`sh`-safe and the images must carry the executor's shell (don't assume `bash` in an Alpine image under the Docker executor).

### 12.2 Real bug fixed: base/head diff handover

`git diff <base>...<head>` in the review step **fails as originally drawn**: a clone of only the head branch does not contain `base_sha` (the base branch advances independently; for fork PRs base and head are different repos entirely), and the review step has no credentials to fetch it. **Fix:** the **clone step must fetch both base and head** into `/shared`. This needs a `git-container` extension - filed as **[Hades-Scheduler/git-container#20]**. Until merged, the M0 spike can hand-fetch both refs in a shell clone step; the real build depends on the extended git-container. This reinforces §11.3 (reviewer computes the diff) but moves the *ref-fetching* responsibility firmly into the clone step.

### 12.3 Result-path correctness fixes

- **Idempotent callback auth.** The publisher retries the callback (11.6); the callback token must be verified **idempotently per `run_id`** - do not burn a single-use token on attempt 1 and then 401 the publisher's own retry.
- **LogManager is in-memory** (`sync.Map`) - a restart loses job history, so status-poll reconciliation (11.6) can emit false `Timeout`/`Failed` for runs whose callback was also dropped during the restart window. Treat reconciliation verdicts as best-effort; prefer the callback as source of truth.
- **Freshness TOCTOU:** re-check the PR head SHA **immediately before** the inline write (11.7), not only at step start; concurrent jobs for rapid pushes can otherwise post a stale review over a newer commit (no cancel API in Hades).

### 12.4 Hardening the review/publish steps

- **`/shared` and git safety:** review and publish set `git config --global --add safe.directory '*'` (steps run as different UIDs → "dubious ownership" otherwise). Run all diffs with `--no-ext-diff -c diff.external=''` (a malicious `.gitattributes` external-diff driver = arbitrary exec on the untrusted repo). The **publisher must not run git operations that trigger repo hooks** over `/shared` (`.git/hooks` poisoning); if it touches git at all, disable hooks.
- **`review.json` is untrusted LLM output.** The publisher sanitizes the summary markdown (strip/escape links and HTML) and **must not let the model self-`approve`/merge** - poisoned repo content can steer verdict text. Treat `verdict` as advisory; never wire it to an auto-merge.
- **IMDS / K8s SA-token exposure (accepted, mitigation filed).** Credentials-only isolation (11.4) does not stop the review container from reaching cloud IMDS (`169.254.169.254`) or reading the mounted K8s ServiceAccount token - cluster-credential theft beyond the accepted "exfiltrate the tree" residual. Decision: **accept for now**, mitigate out-of-band at the operator/cluster level (`automountServiceAccountToken: false`, IMDS egress block) - filed as **[Hades-Scheduler/hades#482]**. Not a blocker for Docker-mode M0.

### 12.5 Empirical checks before building

- **Anthropic OpenAI-compat endpoint (11.1).** The review flagged it as a blocker; it is believed to exist (`/v1/chat/completions` at `https://api.anthropic.com/v1/`) but is a **limited compatibility shim**. **De-risk with a single `curl` before M0.** If too limited (esp. for `response_format`/`json_object` and tool-calling), the fallback is a LiteLLM proxy or a thin native adapter - which reintroduces a second protocol path and partially reopens 11.1. Confirm empirically, don't assume.
- Confirm the M0 images carry the correct shell for the Docker executor (12.1.3) and that a step container can reach the Momos stub callback URL on the Hades network (the one infra unknown, 11.9).
