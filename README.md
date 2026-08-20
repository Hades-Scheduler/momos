# Momos

**AI code review as a service, on top of [Hades](https://github.com/Hades-Scheduler/hades).**

Momos receives webhooks from GitHub, resolves a per-repository review policy
(prompt, model, limits), builds a multi-step Hades job, submits it through the
public `POST /build` API, and writes the review comment back into the pull
request from inside the job container — **without a single change to Hades.**

- **One protocol for models:** an OpenAI-compatible client, so OpenAI, a local
  vLLM/Ollama, or any compatible endpoint is a one-line config swap.
- **Two review strategies, one image:** `oneshot` (deterministic, cheap) and
  `agentic` (navigates the repo with read-only tools), selected per repository.
- **Secure by construction:** the review step holds no forge credentials, so it
  cannot push or post — it can only produce a `review.json`.

> Momos is **Μῶμος**, the Greek god of criticism. Install guide in
> [`docs/install.md`](docs/install.md); design and rationale in
> [`plan.md`](plan.md); developer/agent orientation in [`CLAUDE.md`](CLAUDE.md);
> deep docs in [`docs/`](docs/).

## How it works

```
GitHub PR ─webhook─► momos ─POST /build─► Hades job:
                                    clone ─► review (LLM) ─► publish ─► PR review + check run
                                                                 └─callback─► momos run store
```

Three steps share Hades's `/shared` volume. The publisher is the *universal
reporter*: it runs even if the review failed, and always reports back. See
[`docs/architecture.md`](docs/architecture.md).

## Quickstart

> Full step-by-step walkthrough: [`docs/install.md`](docs/install.md).

Prerequisites: a running Hades (Docker executor) that can pull from GHCR, Docker,
a GitHub repo you own, and a key for any OpenAI-compatible LLM endpoint (OpenAI, a
self-hosted server, or an org gateway - the `base_url` must match the key). Hades
pulls the step images from `ghcr.io/hades-scheduler/momos-*` — nothing to build.

1. Point Momos at your repo in the `repositories:` block of
   `deploy/config.example.yaml`.
2. Put secrets + your public URL in `deploy/.env` (`MOMOS_TOKEN_SECRET`,
   `MOMOS_EXTERNAL_URL`, `HADES_URL`, `HADES_AUTH_KEY`, `GH_WEBHOOK_SECRET`,
   `GH_TOKEN`, `LLM_API_KEY`).
3. Start the service:

   ```bash
   docker compose -f deploy/compose.yml up -d momos
   ```
4. Add a GitHub webhook for pull-request events pointing at
   `<your-public-url>/hooks/github`, then open a PR.

Developing Momos and want to test locally built images? See the smoke test in
[`docs/development.md`](docs/development.md#smoke-test-the-pipeline-by-hand).

## Configuration

A single YAML file (`deploy/config.example.yaml`, [plan.md §5](plan.md)) with
`${ENV}` / `${ENV:-default}` substitution: defaults plus per-repository
overrides with glob matching (`Hades-Scheduler/*`). The `reviewer.base_url` field is the
sovereignty knob — same code, cloud model or self-hosted model.

## Deploy

- **Docker Compose:** `deploy/compose.yml` (service + local registry).
- **Helm:** `deploy/helm/momos` — `helm install momos oci://ghcr.io/hades-scheduler/charts/momos`
  (or from the local path: `helm install momos deploy/helm/momos -f my-values.yaml`).

See [`docs/operations.md`](docs/operations.md).

## Continuous integration

GitHub Actions (`.github/workflows/ci.yml`) runs `go vet`/`build`/`test` on every
push and pull request, then publishes on success:

- **Images** to `ghcr.io/hades-scheduler/momos-{clone,reviewer,publisher,momos}`
  — tagged `latest` on `main`, `pr-<number>` on pull requests, and the release
  tag on releases.
- **Helm chart** to `oci://ghcr.io/hades-scheduler/charts` (SemVer version on
  releases, a `-main` prerelease on `main`).

Runs for the same branch/PR cancel their predecessors.

## Endpoints

| Route | Purpose |
|---|---|
| `POST /hooks/github` | webhook receiver (HMAC-verified, deduped) |
| `POST /v1/runs/{id}/result` | job result callback (bearer-token auth) |
| `GET /v1/runs/{id}/clone-token`, `/publish-token` | step-start token fetch (fetch mode) |
| `GET /v1/runs` | recent runs (status view) |
| `GET /healthz`, `GET /metrics` | health + Prometheus |

## Development

```bash
make test    # unit + integration tests
make build   # binaries
make run     # service against the example config
```

More in [`docs/development.md`](docs/development.md) and [`CLAUDE.md`](CLAUDE.md).

## Tracked follow-ups

Momos runs end to end: a pull request triggers a Hades job that clones, reviews,
posts the result, and reports back to the run store. A few implementation details
are deliberately interim, each tracked upstream:

- Clone uses a purpose-built `momos-clone` image until [Hades-Scheduler/git-container#20](https://github.com/Hades-Scheduler/git-container/issues/20) adds base+head fetch to the shared clone container.
- Forge tokens are embedded at job submission; the fetch-at-step-start path (endpoints and token signer) is already implemented and switches on once #20 lands.
- Per-pod hardening for untrusted step code (ServiceAccount token, IMDS egress) is filed as [Hades-Scheduler/hades#482](https://github.com/Hades-Scheduler/hades/issues/482).

## License

MIT (matching the Hades ecosystem).
