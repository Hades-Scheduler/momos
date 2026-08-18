// Package review defines the versioned review.json contract produced by the
// reviewer step and consumed by the publisher step and the Momos run store.
//
// The schema is deliberately stable and versioned: it doubles as the raw
// dataset for the paper's evaluation, so every field a run produces is captured
// here. See plan.md §4 "Result schema".
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SchemaVersion is the current review.json schema version.
const SchemaVersion = "1.0"

// Verdict is the reviewer's overall assessment. It is advisory only: the
// publisher never wires a verdict to an auto-merge or a GitHub "approve"
// review, because review.json is untrusted LLM output (plan.md §12.4).
type Verdict string

const (
	VerdictComment        Verdict = "comment"
	VerdictApprove        Verdict = "approve"
	VerdictRequestChanges Verdict = "request_changes"
)

// Severity ranks a finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityMinor    Severity = "minor"
	SeverityMajor    Severity = "major"
	SeverityCritical Severity = "critical"
)

// Finding is a single review comment. Line/EndLine are 1-indexed and refer to
// the head revision; findings whose line is not part of the diff are moved to
// the summary block by the publisher.
type Finding struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	EndLine    int      `json:"end_line,omitempty"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// Usage records model, token, and cost figures for the run.
type Usage struct {
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Turns        int     `json:"turns"`
}

// Meta records provenance for reproducibility and the evaluation dataset.
type Meta struct {
	Strategy      string `json:"strategy"`
	PromptVersion string `json:"prompt_version"`
	DurationMS    int64  `json:"duration_ms"`
}

// Review is the top-level review.json document.
type Review struct {
	SchemaVersion string    `json:"schema_version"`
	Verdict       Verdict   `json:"verdict"`
	Summary       string    `json:"summary"`
	Findings      []Finding `json:"findings"`
	Truncated     bool      `json:"truncated"`
	Usage         Usage     `json:"usage"`
	Meta          Meta      `json:"meta"`
}

var validVerdicts = map[Verdict]bool{
	VerdictComment: true, VerdictApprove: true, VerdictRequestChanges: true,
}

var validSeverities = map[Severity]bool{
	SeverityInfo: true, SeverityMinor: true, SeverityMajor: true, SeverityCritical: true,
}

// Validate checks the document against the schema contract. It is strict about
// structure but lenient about optional fields so a best-effort model response
// still passes once repaired.
func (r *Review) Validate() error {
	if r == nil {
		return errors.New("review is nil")
	}
	if r.SchemaVersion == "" {
		r.SchemaVersion = SchemaVersion
	}
	if !validVerdicts[r.Verdict] {
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	if strings.TrimSpace(r.Summary) == "" && len(r.Findings) == 0 {
		return errors.New("review has neither summary nor findings")
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.File == "" {
			return fmt.Errorf("finding %d: empty file", i)
		}
		if f.Line < 0 {
			return fmt.Errorf("finding %d: negative line", i)
		}
		if f.Severity == "" {
			f.Severity = SeverityInfo
		}
		if !validSeverities[f.Severity] {
			return fmt.Errorf("finding %d: invalid severity %q", i, f.Severity)
		}
	}
	return nil
}

// Parse decodes review JSON, repairing the malformations models commonly emit:
// leading/trailing prose, ```json code fences, and trailing commas. It then
// validates against the schema. Callers use this on the reviewer's own output
// and again in the publisher, so a strict-schema server is never required
// (plan.md §11.1: structured output is not uniform across OpenAI-compatible
// providers).
func Parse(raw []byte) (*Review, error) {
	cleaned := repair(raw)
	var r Review
	dec := json.NewDecoder(bytes.NewReader(cleaned))
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("decode review json: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// repair extracts the most plausible JSON object from a possibly-noisy string.
func repair(raw []byte) []byte {
	s := string(raw)
	// Strip a leading ```json / ``` fence and its closing fence.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		if j := strings.LastIndex(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = rest
	}
	// Trim to the outermost { ... } object.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	s = removeTrailingCommas(s)
	return []byte(strings.TrimSpace(s))
}

// removeTrailingCommas deletes commas that immediately precede a closing } or ]
// (ignoring whitespace), a common model mistake that breaks encoding/json.
func removeTrailingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inString {
			b.WriteRune(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			b.WriteRune(c)
			continue
		}
		if c == ',' {
			// Look ahead past whitespace for a closing bracket.
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\n' || runes[j] == '\t' || runes[j] == '\r') {
				j++
			}
			if j < len(runes) && (runes[j] == '}' || runes[j] == ']') {
				continue // drop this comma
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Marshal renders the review as indented JSON.
func (r *Review) Marshal() ([]byte, error) {
	if r.SchemaVersion == "" {
		r.SchemaVersion = SchemaVersion
	}
	return json.MarshalIndent(r, "", "  ")
}
