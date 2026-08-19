# Operations

How to configure, deploy, run, and monitor Momos.

## Prerequisites

- A running **Hades** using the **Docker executor** or the **K8s operator** —
  never K8s "legacy direct" mode (`plan.md` §10.1).
- A registry the Hades daemon can pull the `momos-*` step images from (Hades
  `ImagePull`s every step image; `plan.md` §10.4). Locally, the registry in
  `deploy/compose.yml`.
- A model endpoint reachable from the review step (OpenAI, or an in-cluster
  vLLM/Ollama).

## Configuration reference

Single YAML file (`deploy/config.example.yaml`), `${ENV}` / `${ENV:-default}`
substitution.

| Key | Meaning |
|---|---|
| `hades.url` | Hades API base (for `POST /build`) |
| `hades.auth_key` | Basic-auth password (user `hades`) |
| `hades.log_manager_url` | LogManager base (status reconciliation) |
| `server.addr` | listen address |
| `server.external_url` | **base URL the job containers call back to** — must be routable from the Hades network |
| `forges[].webhook_secret` | HMAC secret for webhook verification |
| `forges[].token` | PAT (simplest) — or configure `forges[].app` for installation tokens |
| `defaults.*` | fallback policy; `reviewer.base_url`/`model` are the model knobs |
| `defaults.limits` | `max_changed_files`, `max_diff_bytes`, `max_cost_usd` |
| `repositories[]` | glob `match` + overrides + `triggers` |

Required env: `MOMOS_TOKEN_SECRET` (signs step + callback tokens). Without it the
service refuses to start.

## GitHub setup

1. **Webhook:** repo/org settings → Webhooks → payload URL
   `https://<host>/hooks/github`, content type `application/json`, secret =
   `GH_WEBHOOK_SECRET`, events = *Pull requests* (and *Pushes* if you use push
   triggers).
2. **Credentials:**
   - **Quick start:** a PAT with `contents:read`, `pull_requests:write`, `checks:write`
     → `GH_TOKEN`.
   - **Production:** a GitHub App (`app_id`, `installation_id`, `private_key`);
     Momos mints per-run scoped installation tokens. Set `forges[].app` and drop
     `token`.

## Deploy — Docker Compose

```bash
export MOMOS_TOKEN_SECRET=$(openssl rand -hex 32)
export HADES_AUTH_KEY=... GH_WEBHOOK_SECRET=... GH_TOKEN=... LLM_API_KEY=...
docker compose -f deploy/compose.yml up --build
# build + push the step images to the local registry:
make push REGISTRY=localhost:5000
```

## Deploy — Helm

```bash
helm install momos deploy/helm/momos \
  --set-string secrets.MOMOS_TOKEN_SECRET=$(openssl rand -hex 32) \
  --set-string secrets.GH_TOKEN=... \
  --set-string secrets.GH_WEBHOOK_SECRET=... \
  --set-string secrets.LLM_API_KEY=... \
  --set ingress.enabled=true --set ingress.host=momos.example.com \
  -f my-values.yaml
```

Put the full config under `config:` and prompts under `prompts:` in your values
file. `server.external_url` must equal the ingress host. Use `existingSecret` to
reference a pre-created Secret instead of inlining values.

**K8s operator hardening** (out-of-band, [Hades-Scheduler/hades#482]): set
`automountServiceAccountToken: false` and a default-deny egress NetworkPolicy on
the `hades-executor` namespace to block IMDS.

## Deploying to Kubernetes

CI publishes the images and the Helm chart to GHCR on every `main` push and
release (`.github/workflows/ci.yml`); it does **not** deploy. Roll the release
out to your cluster with the chart — either `helm upgrade --install` (see
[Deploy — Helm](#deploy--helm)) or a GitOps controller (Argo CD / Flux) tracking
`oci://ghcr.io/hades-scheduler/charts/momos`.

## Running a job by hand

Edit `deploy/sample-payload.json` (replace `REPLACE_*`), then:

```bash
curl -u hades:$HADES_AUTH_KEY -H 'Content-Type: application/json' \
     --data @deploy/sample-payload.json $HADES_URL/build
```

A review comment should appear on the PR. If you also run a Momos stub/service
and set `MOMOS_CALLBACK_URL`/`MOMOS_CALLBACK_TOKEN`, the run store records the
result.

## Monitoring

- `GET /metrics` — `momos_runs_total{status}`, `momos_hooks_total{result}`,
  `momos_hook_to_comment_seconds`, `momos_review_cost_usd_total`.
- `GET /v1/runs` — recent runs as JSON.
- `GET /healthz` — liveness/readiness.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Job fails at clone: image pull error | image not in a registry Hades can reach (`plan.md` §10.4) |
| Publish never runs after a review failure | review step used an entrypoint (empty `script`) in operator mode (`plan.md` §12.1) — set `script` |
| `git diff` fails `bad revision` | base not fetched; check clone step base/head fetch (`plan.md` §12.2) |
| Inline comment rejected by GitHub | finding not on an added line — should be folded into summary by the reviewer's classify step |
| Reviewer 400 from model | provider needs a different param (e.g. `max_completion_tokens`) or lacks `json_object` — model/endpoint mismatch (`plan.md` §11.1) |
| Run stuck `submitted` | callback lost and LogManager restarted; reconciler times it out after `defaults.timeout` |
