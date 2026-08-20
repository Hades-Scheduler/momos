// Package publisher implements the publish step (plan.md §11.7): validate
// review.json, freshness-gate, post the split summary/inline review + check run
// via the forge, then call back to Momos. It is the universal reporter — it
// runs even when the review step failed, and always reports something
// (plan.md §10.6).
package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Hades-Scheduler/momos/internal/event"
	"github.com/Hades-Scheduler/momos/internal/forge"
	"github.com/Hades-Scheduler/momos/internal/protocol"
	"github.com/Hades-Scheduler/momos/internal/review"
)

// Config is read from the publish step environment.
type Config struct {
	Forge         string
	ForgeAPI      string
	ForgeToken    string
	Mode          string
	Inline        bool
	CheckRun      bool
	Approvals     bool
	IsFork        bool
	ExpectedHead  string
	RepoID        string
	PRNumber      int
	CallbackURL   string
	CallbackToken string
	RunID         string
}

// FromEnv builds a Config from the environment.
func FromEnv() *Config {
	return &Config{
		Forge:         os.Getenv(protocol.EnvForge),
		ForgeAPI:      os.Getenv(protocol.EnvForgeAPI),
		ForgeToken:    os.Getenv(protocol.EnvForgeToken),
		Mode:          os.Getenv(protocol.EnvPublishMode),
		Inline:        os.Getenv(protocol.EnvInlineComments) == "true",
		CheckRun:      os.Getenv(protocol.EnvCheckRun) == "true",
		Approvals:     os.Getenv(protocol.EnvApprovals) == "true",
		IsFork:        os.Getenv(protocol.EnvIsFork) == "true",
		ExpectedHead:  os.Getenv(protocol.EnvExpectedHead),
		RepoID:        os.Getenv(protocol.EnvRepoID),
		PRNumber:      atoi(os.Getenv(protocol.EnvPRNumber)),
		CallbackURL:   os.Getenv(protocol.EnvCallbackURL),
		CallbackToken: os.Getenv(protocol.EnvCallbackToken),
		RunID:         os.Getenv(protocol.EnvRunID),
	}
}

func (c *Config) marker() string { return fmt.Sprintf("<!-- momos:run=%s -->", c.RunID) }

// Run publishes the review and reports back. It always attempts a callback.
func (c *Config) Run(ctx context.Context) error {
	f := forge.NewGitHub(c.ForgeAPI, c.ForgeToken, "momos")

	rev, readErr := readReview()
	if readErr != nil {
		// Review step failed or produced no output: report it, do not stay silent.
		body := c.marker() + "\n### 🤖 Momos review did not run\n\n" +
			"The review step did not produce a valid result:\n\n> " + sanitize(readErr.Error())
		url, _ := f.PostSummary(ctx, c.RepoID, c.PRNumber, c.marker(), body)
		if c.CheckRun {
			_ = f.PostCheckRun(ctx, c.RepoID, c.ExpectedHead, "Momos review", "neutral", "Review failed", sanitize(readErr.Error()))
		}
		return c.callback(ctx, event.RunResult{
			RunID: c.RunID, Status: event.StatusReviewFailed, CommentURL: url, Error: readErr.Error(),
		})
	}

	// Freshness check (plan.md §11.7).
	stale := false
	if current, err := f.CurrentHead(ctx, c.RepoID, c.PRNumber); err == nil && current != "" && current != c.ExpectedHead {
		stale = true
	}

	summary := c.buildSummary(rev, stale)
	url, err := f.PostSummary(ctx, c.RepoID, c.PRNumber, c.marker(), summary)
	if err != nil {
		return c.callback(ctx, event.RunResult{RunID: c.RunID, Status: event.StatusReviewFailed, Error: err.Error()})
	}

	status := event.StatusSucceeded
	if stale {
		status = event.StatusStale
	} else {
		var inline []forge.InlineComment
		if c.Inline && len(rev.Findings) > 0 {
			inline = toInline(rev.Findings)
		}
		reviewEvent := c.reviewEvent(rev)
		// Submit a review when there is a verdict to register (APPROVE /
		// REQUEST_CHANGES) or inline comments to attach; a bare COMMENT with no
		// comments is a no-op the summary already covers.
		if reviewEvent != "COMMENT" || len(inline) > 0 {
			reviewBody := c.marker() + "\nMomos automated review"
			if perr := f.PostReview(ctx, c.RepoID, c.PRNumber, c.marker(), reviewEvent, reviewBody, inline); perr != nil {
				// The review failed but the summary succeeded; still report the
				// summary as the result.
				status = event.StatusSucceeded
			}
		}
	}

	if c.CheckRun {
		title := fmt.Sprintf("Momos: %s (%d findings, $%.2f)", rev.Verdict, len(rev.Findings), rev.Usage.CostUSD)
		// Conservative: always "neutral" — never let model output gate a merge (plan.md §12.4).
		_ = f.PostCheckRun(ctx, c.RepoID, c.ExpectedHead, "Momos review", "neutral", title, truncate(sanitize(rev.Summary), 60000))
	}

	return c.callback(ctx, event.RunResult{
		RunID:      c.RunID,
		Status:     status,
		Review:     rev,
		CommentURL: url,
	})
}

func (c *Config) buildSummary(rev *review.Review, stale bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n### 🤖 Momos review\n\n", c.marker())
	if stale {
		b.WriteString("> ⚠️ The head moved since this review started; inline comments were skipped. Summary only.\n\n")
	}
	fmt.Fprintf(&b, "**Verdict:** %s  \n", rev.Verdict)
	fmt.Fprintf(&b, "**Findings:** %d  \n", len(rev.Findings))
	if rev.Usage.Model != "" {
		fmt.Fprintf(&b, "**Model:** `%s`  \n", sanitize(rev.Usage.Model))
	}
	if rev.Usage.CostUSD > 0 {
		fmt.Fprintf(&b, "**Cost:** $%.4f  \n", rev.Usage.CostUSD)
	}
	if rev.Truncated {
		b.WriteString("\n> ⚠️ The diff was truncated to fit review limits.\n")
	}
	b.WriteString("\n" + sanitize(rev.Summary) + "\n")
	return b.String()
}

// reviewEvent turns the model's advisory verdict into a GitHub review event,
// applying safety guardrails (plan.md §12.4). When Approvals is off it always
// returns "COMMENT" (the verdict stays advisory, shown only in the summary).
// It never auto-approves a fork PR (untrusted diff → prompt-injection risk), and
// any major/critical finding forces REQUEST_CHANGES regardless of the stated
// verdict, so a model can't approve over its own blocking findings. Momos still
// never merges — approval only satisfies branch protection if the repo requires
// it.
func (c *Config) reviewEvent(rev *review.Review) string {
	if !c.Approvals {
		return "COMMENT"
	}
	if hasBlocking(rev) {
		return "REQUEST_CHANGES"
	}
	switch rev.Verdict {
	case review.VerdictApprove:
		if c.IsFork {
			return "COMMENT" // never auto-approve untrusted fork content
		}
		return "APPROVE"
	case review.VerdictRequestChanges:
		return "REQUEST_CHANGES"
	default:
		return "COMMENT"
	}
}

// hasBlocking reports whether any finding is severe enough to block a merge.
func hasBlocking(rev *review.Review) bool {
	for _, f := range rev.Findings {
		if f.Severity == review.SeverityMajor || f.Severity == review.SeverityCritical {
			return true
		}
	}
	return false
}

func toInline(findings []review.Finding) []forge.InlineComment {
	out := make([]forge.InlineComment, 0, len(findings))
	for _, f := range findings {
		body := fmt.Sprintf("**%s** (%s): %s", f.Severity, sanitize(f.Category), sanitize(f.Message))
		if f.Suggestion != "" {
			body += "\n\n_Suggestion:_ " + sanitize(f.Suggestion)
		}
		// Tag with the invisible marker so a later run recognizes and drops
		// Momos's own threads before feeding existing threads to the reviewer.
		body += "\n" + forge.MomosInlineMarker
		out = append(out, forge.InlineComment{Path: f.File, Line: f.Line, Side: "RIGHT", Body: body})
	}
	return out
}

// callback POSTs the result to Momos, retrying with bounded backoff so a
// transient Momos outage doesn't orphan the run (plan.md §11.6). The token is
// verified idempotently on the Momos side, so retries never race a burn.
func (c *Config) callback(ctx context.Context, result event.RunResult) error {
	if c.CallbackURL == "" {
		return nil // M0 without a service target
	}
	body, _ := json.Marshal(result)
	url := strings.TrimRight(c.CallbackURL, "/") + "/v1/runs/" + c.RunID + "/result"

	var lastErr error
	delay := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if c.CallbackToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.CallbackToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return nil
			}
			lastErr = fmt.Errorf("callback status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if attempt < 4 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay *= 2
		}
	}
	return fmt.Errorf("callback failed after retries: %w", lastErr)
}

func readReview() (*review.Review, error) {
	data, err := os.ReadFile(protocol.ReviewJSON)
	if err != nil {
		return nil, fmt.Errorf("read review.json: %w", err)
	}
	return review.Parse(data)
}

// sanitize neutralizes untrusted model output before it is posted under the
// bot's identity (plan.md §12.4): HTML-escape angle brackets so no raw HTML or
// script survives, and strip our own marker so injected content can't forge it.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "<!-- momos:run=", "<!-- (removed) ")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
