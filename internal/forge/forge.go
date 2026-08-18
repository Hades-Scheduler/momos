// Package forge is the thin, forge-neutral seam (plan.md §11.8). GitHub is the
// only implementation today; GitLab/Gitea are additive. The interface is shared
// Go code compiled into both the Momos service (ParseWebhook, MintToken) and
// the publisher binary (CurrentHead, Post*).
package forge

import (
	"context"
	"net/http"
	"time"

	"github.com/Hades-Scheduler/momos/internal/event"
	"github.com/Hades-Scheduler/momos/internal/token"
)

// InlineComment is a single inline review comment positioned by path+line+side
// (the modern GitHub reviews API — no diff-offset math, plan.md §11.7).
type InlineComment struct {
	Path string // repo-relative path
	Line int    // 1-indexed line in the head revision
	Side string // "RIGHT" (default) or "LEFT"
	Body string
}

// MintedToken is a short-lived forge token plus its expiry.
type MintedToken struct {
	Token  string
	Expiry time.Time
}

// Forge is the forge-neutral operation set.
type Forge interface {
	// ParseWebhook verifies the signature and normalizes the request into a
	// ReviewEvent. Returns (nil, nil) for a well-formed but ignorable event
	// (e.g. an unhandled action), and an error for a bad signature or payload.
	ParseWebhook(r *http.Request, secret string) (*event.ReviewEvent, error)

	// CurrentHead returns the current head SHA of a pull request (freshness
	// check, plan.md §11.7).
	CurrentHead(ctx context.Context, repo string, pr int) (string, error)

	// PostSummary upserts a marker-tagged issue comment (idempotent summary,
	// plan.md §11.7). Returns the comment URL.
	PostSummary(ctx context.Context, repo string, pr int, marker, body string) (string, error)

	// PostReview posts an inline review, dismissing/removing any prior Momos
	// review (found by marker) first so comments don't stack on re-runs.
	PostReview(ctx context.Context, repo string, pr int, marker, body string, comments []InlineComment) error

	// PostCheckRun creates a completed check run carrying the verdict + cost.
	PostCheckRun(ctx context.Context, repo, headSHA, name, conclusion, title, summary string) error
}

// TokenMinter mints short-lived, scoped forge tokens on demand (plan.md §11.5).
type TokenMinter interface {
	MintToken(ctx context.Context, repo string, scope token.Scope) (MintedToken, error)
}
