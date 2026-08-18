// Package reviewer implements the review step (plan.md §11.3): one binary,
// oneshot or agentic, sharing the /shared -> review.json contract. It carries
// git and computes its own diff. It holds no forge credentials.
package reviewer

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ls1intum/momos/internal/diff"
	"github.com/ls1intum/momos/internal/llm"
	"github.com/ls1intum/momos/internal/protocol"
	"github.com/ls1intum/momos/internal/review"
)

// Config is read from the step metadata (env).
type Config struct {
	Strategy        string
	BaseURL         string
	Model           string
	APIKey          string
	PromptText      string
	PromptVersion   string
	MaxOutputTokens int
	MaxTurns        int
	MaxDiffBytes    int
	MaxChangedFiles int
	MaxCostUSD      float64
	InputPrice      float64
	OutputPrice     float64
	BaseSHA         string
	HeadSHA         string
	RepoID          string
}

// FromEnv builds a Config from the step environment.
func FromEnv() (*Config, error) {
	promptB64 := os.Getenv(protocol.EnvPromptB64)
	promptBytes, err := base64.StdEncoding.DecodeString(promptB64)
	if err != nil {
		return nil, fmt.Errorf("decode PROMPT_B64: %w", err)
	}
	c := &Config{
		Strategy:        envDefault(protocol.EnvReviewStrategy, "oneshot"),
		BaseURL:         os.Getenv(protocol.EnvLLMBaseURL),
		Model:           os.Getenv(protocol.EnvLLMModel),
		APIKey:          os.Getenv(protocol.EnvLLMAPIKey),
		PromptText:      string(promptBytes),
		PromptVersion:   os.Getenv(protocol.EnvPromptVersion),
		MaxOutputTokens: envInt(protocol.EnvMaxOutputTokens, 8000),
		MaxTurns:        envInt(protocol.EnvMaxTurns, 12),
		MaxDiffBytes:    envInt(protocol.EnvMaxDiffBytes, 400000),
		MaxChangedFiles: envInt(protocol.EnvMaxChangedFiles, 200),
		MaxCostUSD:      envFloat(protocol.EnvMaxCostUSD, 1.0),
		InputPrice:      envFloat(protocol.EnvInputPrice, 0),
		OutputPrice:     envFloat(protocol.EnvOutputPrice, 0),
		BaseSHA:         os.Getenv(protocol.EnvBaseSHA),
		HeadSHA:         os.Getenv(protocol.EnvHeadSHA2),
		RepoID:          os.Getenv(protocol.EnvRepoID),
	}
	if c.BaseURL == "" || c.Model == "" {
		return nil, fmt.Errorf("LLM_BASE_URL and LLM_MODEL are required")
	}
	return c, nil
}

// Run executes the review and writes /shared/out/review.json. It always writes
// a valid document (even on empty diff) so the publisher has something to
// report; a nil error means a review was produced.
func (c *Config) Run(ctx context.Context) error {
	start := time.Now()
	if err := os.MkdirAll(protocol.OutDir, 0o755); err != nil {
		return err
	}

	unified, err := computeDiff(ctx, c.BaseSHA, c.HeadSHA)
	if err != nil {
		return fmt.Errorf("compute diff: %w", err)
	}
	parsed := diff.Parse(unified)

	// Empty diff: no LLM call (plan.md §10.6), write a minimal review.
	if parsed.ChangedFiles() == 0 {
		return writeReview(&review.Review{
			SchemaVersion: review.SchemaVersion,
			Verdict:       review.VerdictComment,
			Summary:       "No reviewable changes in this diff.",
			Meta:          c.meta(start),
		})
	}

	// Enforce limits: truncate the diff sent to the model.
	truncated := false
	if len(unified) > c.MaxDiffBytes {
		unified = unified[:c.MaxDiffBytes]
		truncated = true
	}
	if parsed.ChangedFiles() > c.MaxChangedFiles {
		truncated = true
	}

	client := llm.New(c.BaseURL, c.APIKey)
	var rev *review.Review
	var usage llm.Usage

	switch c.Strategy {
	case "agentic":
		rev, usage, err = c.agentic(ctx, client, unified)
	default:
		rev, usage, err = c.oneshot(ctx, client, unified)
	}
	if err != nil {
		return err
	}

	// Classify findings: only added-line findings become inline comments; the
	// rest are folded into the summary (plan.md §11.7).
	rev.Findings, rev.Summary = classify(rev.Findings, rev.Summary, parsed)
	rev.Truncated = truncated || rev.Truncated
	rev.Usage = c.usage(usage)
	rev.Meta = c.meta(start)
	return writeReview(rev)
}

func (c *Config) meta(start time.Time) review.Meta {
	return review.Meta{
		Strategy:      c.Strategy,
		PromptVersion: c.PromptVersion,
		DurationMS:    time.Since(start).Milliseconds(),
	}
}

func (c *Config) usage(u llm.Usage) review.Usage {
	return review.Usage{
		Model:        c.Model,
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		CostUSD:      c.cost(u),
		Turns:        1,
	}
}

func (c *Config) cost(u llm.Usage) float64 {
	return float64(u.PromptTokens)/1e6*c.InputPrice + float64(u.CompletionTokens)/1e6*c.OutputPrice
}

func writeReview(r *review.Review) error {
	b, err := r.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(protocol.ReviewJSON, b, 0o644)
}

// classify keeps only findings on added lines as inline; others are appended to
// the summary as a bulleted list.
func classify(findings []review.Finding, summary string, d *diff.Diff) ([]review.Finding, string) {
	var inline []review.Finding
	var extra []string
	for _, f := range findings {
		if d.IsAddedLine(f.File, f.Line) {
			inline = append(inline, f)
		} else {
			extra = append(extra, fmt.Sprintf("- `%s:%d` (%s): %s", f.File, f.Line, f.Severity, f.Message))
		}
	}
	if len(extra) > 0 {
		summary = strings.TrimRight(summary, "\n") +
			"\n\n**Additional findings (outside the diff):**\n" + strings.Join(extra, "\n")
	}
	return inline, summary
}

const schemaInstruction = `
Respond with a single JSON object and nothing else, matching this schema:
{
  "verdict": "comment" | "approve" | "request_changes",
  "summary": string,
  "findings": [
    {
      "file": string,          // repo-relative path
      "line": number,          // 1-indexed line in the NEW (head) revision
      "end_line": number,      // optional
      "severity": "info" | "minor" | "major" | "critical",
      "category": string,      // e.g. "correctness", "security", "style"
      "message": string,
      "suggestion": string     // optional
    }
  ]
}
Only report findings you are confident about. Prefer findings on lines that were
added or changed in the diff. Output JSON only — no prose, no code fences.`

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
