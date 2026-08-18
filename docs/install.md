# Install guide

Set up Momos to review pull requests on a GitHub repository. Momos submits jobs
to your Hades instance, and Hades pulls the `momos-*` step images straight from
GHCR — there is nothing to build. Budget ~15 minutes.

By the end: open a pull request on your repo, and Momos posts an AI review back
onto it (a summary comment, inline comments, and a check run).

---

## Prerequisites

- **A running Hades** with the **Docker executor**, able to pull images from
  `ghcr.io/hades-scheduler/momos-*`. The images are published there by CI; make
  sure they are **accessible to Hades** — either the packages are public, or the
  Hades Docker daemon is logged in to GHCR (`docker login ghcr.io`).
- **Docker + Docker Compose** to run the Momos service.
- A **GitHub repository** you own, with a branch you can open a PR from.
- A **GitHub token** for that repo:
  - Fine-grained PAT: *Contents: Read*, *Pull requests: Read and write*,
    *Checks: Read and write*, *Metadata: Read*.
  - Or a classic PAT with the `repo` scope (covers all of the above).
- An **OpenAI API key** (`sk-...`).
- A **public URL** for Momos, reachable by GitHub (webhooks) and by the Hades job
  containers (result callback). For a quick setup, a tunnel works:
  [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/),
  `ngrok`, or `smee.io`. For a permanent install, deploy behind an ingress
  (see [operations.md](operations.md) for the Helm chart).

Clone this repo and `cd` into it for the config and Compose file.

---

## Step 1 — Point Momos at your repository

Edit the `repositories:` block in `deploy/config.example.yaml` so it matches your
repo (leave everything else as defaults — the step images already point at GHCR):

```yaml
repositories:
  - match: "<owner>/<repo>"
    forge: github-main
    prompt: default.md
    triggers:
      pull_request: [opened, synchronize, reopened]
```

You can add more rules or an org-wide glob (`<owner>/*`); the first match wins.

---

## Step 2 — Expose Momos publicly

Momos needs one public URL that both GitHub and the Hades job containers can
reach. Start a tunnel to Momos's port `8080`:

```bash
# cloudflared prints an https URL, e.g. https://abc-def.trycloudflare.com
cloudflared tunnel --url http://localhost:8080
```

Keep it running; call the URL `$PUB`.

---

## Step 3 — Configure secrets and start the service

Create `deploy/.env` (next to the Compose file):

```dotenv
MOMOS_TOKEN_SECRET=<run: openssl rand -hex 32>
MOMOS_EXTERNAL_URL=https://abc-def.trycloudflare.com   # your $PUB
HADES_URL=http://host.docker.internal:8080             # how Momos reaches Hades
HADES_LOGMANAGER_URL=http://host.docker.internal:8081
HADES_AUTH_KEY=<your Hades AUTH_KEY, or leave empty>
GH_WEBHOOK_SECRET=<pick a random string>
GH_TOKEN=<your GitHub token>
LLM_API_KEY=<your OpenAI key>
```

> `HADES_URL` is how the **Momos container** reaches the Hades API. On Docker
> Desktop, `host.docker.internal` resolves to the host; on Linux use the host IP
> or attach Momos to the Hades Compose network.

Start Momos (it pulls its own image from GHCR):

```bash
docker compose -f deploy/compose.yml up -d momos
curl -s http://localhost:8080/healthz && echo " momos up"
```

---

## Step 4 — Register the GitHub webhook

On the repo: **Settings → Webhooks → Add webhook**.

- **Payload URL:** `$PUB/hooks/github`
- **Content type:** `application/json`
- **Secret:** the same value as `GH_WEBHOOK_SECRET`
- **Events:** *Let me select individual events* → **Pull requests** only.

GitHub sends a `ping`; Momos answers `202` and ignores it — that's expected.

For an **organization-wide** install, add the webhook at the org level instead
and use an org glob (`<owner>/*`) in the config.

---

## Step 5 — Open a pull request

Open (or reopen, or push to) a pull request on the repo. Within a minute you
should see:

- a **summary comment** on the PR,
- **inline comments** on changed lines,
- a **check run** named *Momos review*.

Confirm from the service side:

```bash
curl -s http://localhost:8080/v1/runs | jq '.[0]'   # most recent run + result
curl -s http://localhost:8080/metrics | grep momos_ # counters
```

---

## Using a self-hosted model (optional)

Point the reviewer at any OpenAI-compatible endpoint — same code, no data leaves
your network. In `deploy/config.example.yaml`:

```yaml
    reviewer:
      base_url: http://host.docker.internal:11434/v1   # e.g. Ollama
      model: qwen2.5-coder:7b
```

`api_key` can be any non-empty string for local servers. The **agentic** strategy
needs a tool-calling-capable model; `oneshot` works anywhere.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| Job fails at **clone** with an image-pull error | Hades can't pull `ghcr.io/hades-scheduler/momos-*`. Make the packages public, or `docker login ghcr.io` on the Hades host. |
| Webhook shows **red** in GitHub's *Recent Deliveries* | Wrong URL/secret or Momos unreachable. Path is `/hooks/github`; the secret must match `GH_WEBHOOK_SECRET`. |
| **No comment** but the job succeeded | Token scopes (Pull requests + Checks write), or the repo `match:` doesn't cover this repo. |
| Review step returns a **400 from the model** | Model/endpoint mismatch. Try `gpt-4o` on OpenAI first. |
| Run stays **`submitted`** in `/v1/runs` | The callback didn't reach Momos — confirm `MOMOS_EXTERNAL_URL` is your public URL and the tunnel is up. The reconciler times it out after `defaults.timeout`. |

More detail: [operations.md](operations.md). Architecture: [architecture.md](architecture.md).
Testing the pipeline while developing Momos: [development.md](development.md#smoke-test-the-pipeline-by-hand).

---

## Cleanup

```bash
docker compose -f deploy/compose.yml down     # stop Momos
# Remove the webhook from the repo settings, and revoke the PAT.
```
