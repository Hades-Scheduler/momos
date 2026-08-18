# Install & demo guide

This walks you from nothing to a live review on a **demo repository**: open a
pull request, and Momos posts an AI review back onto it. Budget ~20 minutes.

There are two checkpoints:

- **A. Smoke test** — submit a hand-written job straight to Hades to prove the
  images, the LLM call, and posting to GitHub all work. No webhook needed.
- **B. Full demo** — run the Momos service and let a real pull request drive
  everything end to end.

Do A first; it isolates most failures before you add webhooks.

---

## Prerequisites

- **A running Hades** with the **Docker executor**, on the same host you'll run
  Momos on (so it can pull images from a local registry). See the Hades repo.
- **Docker + Docker Compose** and **Go 1.26+** (to build the images).
- A **demo GitHub repository** you own — ideally with a couple of commits and a
  branch you can open a PR from. A throwaway repo is perfect.
- A **GitHub token** with access to the demo repo:
  - Fine-grained PAT: *Contents: Read*, *Pull requests: Read and write*,
    *Checks: Read and write*, *Metadata: Read*.
  - Or a classic PAT with the `repo` scope (covers all of the above).
- An **OpenAI API key** (`sk-...`).
- A **public tunnel** for the full demo (B), so GitHub and the job containers can
  reach Momos: [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/),
  `ngrok`, or `smee.io`. Not needed for the smoke test (A).

Clone this repo and `cd` into it before running anything below.

---

## Step 1 — Build the images into a registry Hades can pull

Hades `ImagePull`s every step image, so the `momos-*` images must live in a
registry its Docker daemon can reach. The Compose file ships a local registry on
`localhost:5000` for exactly this.

```bash
# Start the local registry (and Momos, though we don't need it yet for A).
docker compose -f deploy/compose.yml up -d registry

# Build the four images and push them to the local registry.
make push REGISTRY=localhost:5000
```

`localhost:5000/momos-{clone,reviewer,publisher,momos}:latest` now exist, which
is what `deploy/config.example.yaml` already points the steps at.

> If Hades runs on a **different** host, push to a registry both can reach
> (e.g. `make push REGISTRY=ghcr.io/Hades-Scheduler` after `docker login ghcr.io`)
> and set the image paths in your config accordingly.

---

## Step 2 — Checkpoint A: smoke test with a hand-written job

This proves clone → review → publish without the Momos service or webhooks.

1. Pick a **pull request** on your demo repo (open a trivial one if needed). Note
   its number, and grab the head and base commit SHAs:

   ```bash
   # From a clone of your demo repo:
   git rev-parse origin/main            # -> BASE_SHA (the PR's base branch tip)
   git rev-parse origin/<your-pr-branch> # -> HEAD_SHA
   ```

2. Copy the sample payload and fill in the placeholders:

   ```bash
   cp deploy/sample-payload.json /tmp/demo-job.json
   # Edit /tmp/demo-job.json and replace:
   #   Hades-Scheduler/hades  -> <owner>/<demo-repo>   (name, MOMOS_REPO, GIT_URL, REPO_ID x2)
   #   refs/pull/1/head        -> refs/pull/<PR>/head
   #   PR_NUMBER "1"           -> your PR number
   #   REPLACE_HEAD_SHA        -> HEAD_SHA        (appears 3x)
   #   REPLACE_BASE_SHA        -> BASE_SHA        (appears 2x)
   #   REPLACE_READ_PAT        -> your GitHub token
   #   REPLACE_WRITE_PAT       -> your GitHub token
   #   REPLACE_OPENAI_KEY      -> your OpenAI key
   ```

3. Submit it to Hades:

   ```bash
   curl -u hades:$HADES_AUTH_KEY -H 'Content-Type: application/json' \
        --data @/tmp/demo-job.json \
        $HADES_URL/build
   # -> {"message":"Successfully enqueued job","job_id":"..."}
   ```

Within a minute a **Momos review comment** should appear on the PR. If it
doesn't, watch the job in the Hades dashboard/logs and see
[Troubleshooting](#troubleshooting).

---

## Step 3 — Checkpoint B: run the service for a webhook-driven demo

### 3a. Point Momos at your demo repo

Edit the `repositories:` block in `deploy/config.example.yaml` so it matches your
demo repo (leave the rest as-is):

```yaml
repositories:
  - match: "<owner>/<demo-repo>"
    forge: github-main
    prompt: default.md
    triggers:
      pull_request: [opened, synchronize, reopened]
```

### 3b. Expose Momos publicly

Momos must be reachable by GitHub (for webhooks) **and** by the Hades job
containers (for the result callback). One public URL covers both — job
containers have internet egress.

```bash
# Example with cloudflared (prints a https URL like https://abc-def.trycloudflare.com)
cloudflared tunnel --url http://localhost:8080
```

Keep this running and note the URL — call it `$PUB`.

### 3c. Configure secrets and endpoints, then start Momos

Create `deploy/.env` (next to the compose file):

```dotenv
MOMOS_TOKEN_SECRET=<run: openssl rand -hex 32>
MOMOS_EXTERNAL_URL=https://abc-def.trycloudflare.com   # your $PUB
HADES_URL=http://host.docker.internal:8080             # how Momos reaches Hades
HADES_LOGMANAGER_URL=http://host.docker.internal:8081
HADES_AUTH_KEY=<your hades AUTH_KEY, or empty>
GH_WEBHOOK_SECRET=<pick a random string>
GH_TOKEN=<your GitHub token>
LLM_API_KEY=<your OpenAI key>
```

> `HADES_URL` is how the **Momos container** reaches the Hades API. On Docker
> Desktop, `host.docker.internal` resolves to the host; on Linux, use the host
> IP or attach Momos to the Hades Compose network. Adjust to your setup.

Start the service:

```bash
docker compose -f deploy/compose.yml up -d momos registry
curl -s http://localhost:8080/healthz && echo " momos up"
```

### 3d. Register the GitHub webhook

On the demo repo: **Settings → Webhooks → Add webhook**.

- **Payload URL:** `$PUB/hooks/github`
- **Content type:** `application/json`
- **Secret:** the same value as `GH_WEBHOOK_SECRET`
- **Events:** *Let me select individual events* → **Pull requests** only.

GitHub sends a `ping`; Momos answers `202` and ignores it (that's expected).

### 3e. Trigger a review

Open (or reopen, or push to) a pull request on the demo repo. Within a minute:

- a **summary comment** appears on the PR,
- **inline comments** on changed lines,
- a **check run** named *Momos review*,
- and the run shows up in Momos:

```bash
curl -s http://localhost:8080/v1/runs | jq '.[0]'   # most recent run + result
curl -s http://localhost:8080/metrics | grep momos_ # counters
```

---

## Trying the sovereignty / local-model variant (optional)

Point the reviewer at a local OpenAI-compatible endpoint instead of OpenAI — same
code, no data leaves your machine. In `deploy/config.example.yaml`:

```yaml
    reviewer:
      base_url: http://host.docker.internal:11434/v1   # e.g. Ollama
      model: qwen2.5-coder:7b
```

`api_key` can be any non-empty string for local servers. Note: the **agentic**
strategy needs a tool-calling-capable model; `oneshot` works anywhere.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| Job fails immediately at **clone** with an image-pull error | Images not in a registry Hades can reach. Re-run `make push REGISTRY=localhost:5000`; if Hades is remote, use a shared registry. |
| `git diff` **bad revision** in the review step | The base commit wasn't fetched — check `GIT_BASE_SHA`/`GIT_BASE_REF` and that the PR head SHA is correct. |
| **No comment** appears but the job succeeded | Check the token scopes (Pull requests + Checks write) and that `REPO_ID`/PR number are correct. |
| Review step returns a **400 from the model** | Model/endpoint mismatch — some providers need different params or don't support JSON mode. Try `gpt-4o` on OpenAI first. |
| Run stays **`submitted`** in `/v1/runs` | The callback didn't reach Momos. Confirm `MOMOS_EXTERNAL_URL` is your public tunnel URL and the tunnel is up; the reconciler will time it out after `defaults.timeout`. |
| Webhook shows **red** in GitHub's *Recent Deliveries* | Wrong URL/secret, or Momos not reachable. The path is `/hooks/github`; the secret must match `GH_WEBHOOK_SECRET`. |

More operational detail: [operations.md](operations.md). Architecture:
[architecture.md](architecture.md).

---

## Cleanup

```bash
docker compose -f deploy/compose.yml down -v   # stops Momos + registry, drops volumes
# Remove the GitHub webhook from the demo repo's settings, and revoke the PAT.
```
