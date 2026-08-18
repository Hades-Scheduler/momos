// Package event defines the forge-neutral internal model. It is the only place
// on the ingress side that carries forge knowledge (plan.md §2), plus the
// callback envelope the publisher posts back to Momos (plan.md §11.6).
package event

import "github.com/Hades-Scheduler/momos/internal/review"

// Forge identifies the source forge.
type Forge string

const (
	ForgeGitHub Forge = "github"
	ForgeGitLab Forge = "gitlab"
	ForgeGitea  Forge = "gitea"
)

// Kind is the kind of trigger.
type Kind string

const (
	KindPullRequest Kind = "pull_request"
	KindPush        Kind = "push"
)

// ReviewEvent is the normalized, forge-neutral event produced by the Event
// Normalizer from a forge-specific webhook payload.
type ReviewEvent struct {
	Forge      Forge  `json:"forge"`
	ForgeID    string `json:"forge_id"` // configured forge instance id (e.g. "github-main")
	RepoID     string `json:"repo_id"`  // "Hades-Scheduler/hades"
	CloneURL   string `json:"clone_url"`
	Kind       Kind   `json:"kind"`
	Action     string `json:"action"` // opened | synchronize | reopened | ...
	BaseRef    string `json:"base_ref"`
	BaseSHA    string `json:"base_sha"`
	HeadRef    string `json:"head_ref"`
	HeadSHA    string `json:"head_sha"`
	PRNumber   int    `json:"pr_number"`
	IsFork     bool   `json:"is_fork"`
	Author     string `json:"author"`
	DeliveryID string `json:"delivery_id"`
	// HeadCloneURL is the clone URL of the head repository. For fork PRs it
	// differs from CloneURL (which points at the base repo). See git-container#20.
	HeadCloneURL string `json:"head_clone_url"`
}

// RunStatus is the terminal outcome the publisher reports.
type RunStatus string

const (
	StatusSucceeded    RunStatus = "succeeded"
	StatusReviewFailed RunStatus = "review_failed"
	StatusNoChanges    RunStatus = "no_changes"
	StatusStale        RunStatus = "stale"
)

// RunResult is the callback envelope POSTed by the publisher to
// POST /v1/runs/{run_id}/result (plan.md §11.6).
type RunResult struct {
	RunID      string         `json:"run_id"`
	Status     RunStatus      `json:"status"`
	Review     *review.Review `json:"review,omitempty"`
	CommentURL string         `json:"comment_url,omitempty"`
	Error      string         `json:"error,omitempty"`
}
