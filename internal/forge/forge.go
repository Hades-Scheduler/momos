// Package forge is the thin, forge-neutral seam (plan.md §11.8). GitHub is the
// only implementation today; GitLab/Gitea are additive. The interface is shared
// Go code compiled into both the Momos service (ParseWebhook, MintToken) and
// the publisher binary (CurrentHead, Post*).
package forge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// ThreadComment is one comment inside an existing review thread.
type ThreadComment struct {
	Author string // login; "unknown" if the author is a deleted user/integration
	Body   string
}

// ReviewThread is an existing PR review thread. Momos feeds these to the
// reviewer so it does not duplicate open change requests and honors ones a human
// has resolved. Line is a pointer because GitHub leaves it null on outdated
// threads (only the original line survives there).
type ReviewThread struct {
	Path       string
	Line       *int
	IsResolved bool
	IsOutdated bool
	Comments   []ThreadComment
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

	// ListReviewThreads returns existing review threads on the PR so the
	// reviewer can avoid duplicating open change requests and respect resolved
	// ones. Unlike the REST Post* methods (which use the client-embedded token),
	// it takes an explicit authToken: the service-side client is built with
	// NewGitHubApp and an empty token and mints per call, and a pull_requests
	// read scope (satisfied by the publish token) is enough. Callers treat it as
	// best-effort — an error means "assume no threads", never fail the review.
	ListReviewThreads(ctx context.Context, repo string, pr int, authToken string) ([]ReviewThread, error)

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

// MomosInlineMarker is the invisible HTML-comment tag Momos appends to every
// inline review comment it posts. The ingress side uses it to drop Momos's own
// threads from the set fed back to the reviewer, so the model never suppresses
// (and the publisher then erases) an unfixed finding Momos raised on a previous
// run. It is invisible in rendered markdown.
const MomosInlineMarker = "<!-- momos:inline -->"

// Caps on the serialized threads block, keeping the review-step metadata well
// under Hades's ~1MB message bound (agy review).
const (
	maxThreads       = 50
	maxThreadComment = 20
	maxCommentChars  = 500
	maxThreadsBytes  = 64 * 1024
)

// FilterOutMomosThreads drops threads carrying MomosInlineMarker in any comment,
// leaving only human threads to drive deduplication.
func FilterOutMomosThreads(threads []ReviewThread) []ReviewThread {
	out := make([]ReviewThread, 0, len(threads))
	for _, t := range threads {
		mine := false
		for _, c := range t.Comments {
			if strings.Contains(c.Body, MomosInlineMarker) {
				mine = true
				break
			}
		}
		if !mine {
			out = append(out, t)
		}
	}
	return out
}

// RenderReviewThreads serializes threads into a compact block for the reviewer's
// context, applying the caps above. Returns "" when there is nothing to show.
// The result is untrusted data — the reviewer wraps it in explicit delimiters
// and the prompt treats it as such.
func RenderReviewThreads(threads []ReviewThread) string {
	if len(threads) == 0 {
		return ""
	}
	var b strings.Builder
	for n, t := range threads {
		if n >= maxThreads || b.Len() >= maxThreadsBytes {
			b.WriteString("... (more threads omitted)\n")
			break
		}
		loc := t.Path
		if loc == "" {
			loc = "(unknown file)"
		}
		if t.Line != nil {
			loc = fmt.Sprintf("%s:%d", loc, *t.Line)
		}
		state := "open"
		switch {
		case t.IsResolved:
			state = "resolved"
		case t.IsOutdated:
			state = "outdated"
		}
		fmt.Fprintf(&b, "- thread on %s [%s]\n", loc, state)
		for i, c := range t.Comments {
			if i >= maxThreadComment {
				fmt.Fprintf(&b, "    ... (%d more comments)\n", len(t.Comments)-i)
				break
			}
			body := strings.TrimSpace(c.Body)
			if len(body) > maxCommentChars {
				body = body[:maxCommentChars] + "…"
			}
			author := c.Author
			if author == "" {
				author = "unknown"
			}
			fmt.Fprintf(&b, "    %s: %s\n", author, strings.ReplaceAll(body, "\n", " "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
