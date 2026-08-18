#!/usr/bin/env bash
# momos-clone: clone the repository and fetch BOTH base and head commits into
# /shared/repo so the reviewer can compute `git diff <base>...<head>`
# (plan.md §12.2). Interim shell clone until ls1intum/git-container#20 lands.
#
# Security: the read token is used transport-only via -c http.extraheader (never
# persisted), the remote URL carries no token, and repo hooks are disabled so a
# malicious repo cannot execute code in a later step (plan.md §12.4).
set -euo pipefail

: "${GIT_URL:?GIT_URL required}"
: "${GIT_HEAD_SHA:?GIT_HEAD_SHA required}"

REPO=/shared/repo
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"

git init -q
git config --local core.hooksPath /dev/null
git remote add origin "$GIT_URL"

AUTH=""
if [ -n "${GIT_TOKEN:-}" ]; then
  AUTH="AUTHORIZATION: basic $(printf 'x-access-token:%s' "$GIT_TOKEN" | base64 | tr -d '\n')"
fi

DEPTH_ARG=""
[ -n "${CLONE_DEPTH:-}" ] && DEPTH_ARG="--depth ${CLONE_DEPTH}"

fetch() { # $1 = ref or sha
  if [ -n "$AUTH" ]; then
    git -c http.extraheader="$AUTH" fetch -q $DEPTH_ARG origin "$1"
  else
    git fetch -q $DEPTH_ARG origin "$1"
  fi
}

# Head: prefer the ref (refs/pull/N/head reaches fork heads via the base repo),
# fall back to the raw SHA.
if [ -n "${GIT_HEAD_REF:-}" ]; then
  fetch "$GIT_HEAD_REF" || fetch "$GIT_HEAD_SHA"
else
  fetch "$GIT_HEAD_SHA"
fi

# Base: needed for the three-dot diff. Fall back to the base branch ref.
if [ -n "${GIT_BASE_SHA:-}" ]; then
  fetch "$GIT_BASE_SHA" || { [ -n "${GIT_BASE_REF:-}" ] && fetch "${GIT_BASE_REF#refs/heads/}"; } || true
fi

git checkout -q "$GIT_HEAD_SHA"

# Scrub credential traces before the next step sees /shared/repo.
git remote set-url origin "https://removed.invalid/repo.git" || true
git config --local --unset-all http.extraheader 2>/dev/null || true

echo "clone: HEAD=$(git rev-parse HEAD) base=${GIT_BASE_SHA:-none}"
