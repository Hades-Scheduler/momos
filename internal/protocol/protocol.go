// Package protocol defines the metadata contract between the Momos job builder
// (which sets these) and the reviewer/publisher step binaries (which read
// them). Keeping the names in one place stops the two sides drifting.
//
// Secrets are always set at STEP level (never job level) so they reach only the
// step that needs them (plan.md §10.2, §11.4). The review step deliberately
// carries no forge token.
package protocol

// Shared filesystem paths on the /shared volume (plan.md §10.2).
const (
	RepoDir    = "/shared/repo"
	OutDir     = "/shared/out"
	ReviewJSON = "/shared/out/review.json"
)

// Job-level metadata: dashboard/correlation labels only. Injected into every
// step in both supported executors (plan.md §10.2, corrected §12.1).
const (
	EnvRunID   = "MOMOS_RUN_ID"
	EnvRepo    = "MOMOS_REPO"
	EnvHeadSHA = "MOMOS_HEAD_SHA"
	EnvPR      = "MOMOS_PR"
)

// Clone step (step 1) metadata. Named to match shared/redact's denylist where
// sensitive (GIT_TOKEN), so values are masked on the dashboard (plan.md §10.2).
const (
	EnvGitURL     = "GIT_URL"      // origin (base repo) clone URL
	EnvGitToken   = "GIT_TOKEN"    // short-lived read token (scrubbed from .git/config by clone.sh)
	EnvGitHeadSHA = "GIT_HEAD_SHA" // head commit to check out
	EnvGitHeadRef = "GIT_HEAD_REF" // refs/pull/N/head (PR) or branch ref (push)
	EnvGitBaseSHA = "GIT_BASE_SHA" // base commit for the diff
	EnvGitBaseRef = "GIT_BASE_REF" // base branch ref
	EnvGitIsPR    = "GIT_IS_PR"    // "true" for pull requests
	EnvCloneDepth = "CLONE_DEPTH"  // optional shallow depth; empty = full
)

// Review step (step 2) metadata. LLM credentials only — no forge token.
const (
	EnvReviewStrategy  = "REVIEW_STRATEGY" // oneshot | agentic
	EnvLLMBaseURL      = "LLM_BASE_URL"
	EnvLLMModel        = "LLM_MODEL"
	EnvLLMAPIKey       = "LLM_API_KEY"
	EnvPromptB64       = "PROMPT_B64"
	EnvPromptVersion   = "PROMPT_VERSION"
	EnvMaxOutputTokens = "MAX_OUTPUT_TOKENS"
	EnvMaxTurns        = "MAX_TURNS"
	EnvMaxDiffBytes    = "MAX_DIFF_BYTES"
	EnvMaxChangedFiles = "MAX_CHANGED_FILES"
	EnvMaxCostUSD      = "MAX_COST_USD"
	EnvInputPrice      = "INPUT_PRICE_PER_MTOK"
	EnvOutputPrice     = "OUTPUT_PRICE_PER_MTOK"
	EnvBaseSHA         = "BASE_SHA"
	EnvHeadSHA2        = "HEAD_SHA"
	EnvRepoID          = "REPO_ID"
	// EnvExistingThreadsB64 carries the existing PR review threads (human,
	// Momos's own filtered out) as a base64 text block, so the reviewer can
	// avoid duplicating open change requests and respect resolved ones. It is
	// data, not a credential — the review step stays token-free (plan.md §11.4).
	EnvExistingThreadsB64 = "EXISTING_THREADS_B64"
)

// Publish step (step 3) metadata. Forge write token + callback credentials.
const (
	EnvForge          = "FORGE"
	EnvForgeAPI       = "FORGE_API"
	EnvForgeToken     = "FORGE_TOKEN"
	EnvPublishMode    = "PUBLISH_MODE"
	EnvInlineComments = "INLINE_COMMENTS"
	EnvCheckRun       = "CHECK_RUN"
	EnvApprovals      = "APPROVALS" // "true" to submit APPROVE/REQUEST_CHANGES per verdict
	EnvIsFork         = "IS_FORK"   // "true" for fork PRs — never auto-approved (plan.md §12.4)
	EnvExpectedHead   = "EXPECTED_HEAD_SHA"
	EnvPRNumber       = "PR_NUMBER"
	EnvCallbackURL    = "MOMOS_CALLBACK_URL"
	EnvCallbackToken  = "MOMOS_CALLBACK_TOKEN"
)
