package job

import (
	"encoding/base64"
	"testing"

	"github.com/ls1intum/momos/internal/config"
	"github.com/ls1intum/momos/internal/event"
	"github.com/ls1intum/momos/internal/protocol"
)

func sampleInputs() Inputs {
	ev := &event.ReviewEvent{
		Forge: event.ForgeGitHub, RepoID: "o/r", CloneURL: "https://github.com/o/r.git",
		Kind: event.KindPullRequest, Action: "opened",
		BaseRef: "main", BaseSHA: "basesha", HeadRef: "feat", HeadSHA: "headsha", PRNumber: 7,
	}
	pol := config.Policy{
		Priority: 3,
		Reviewer: config.ReviewerConfig{Strategy: "oneshot", Model: "gpt-4o", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
		Publish:  config.PublishConfig{Mode: "pr_review", InlineComments: true, CheckRun: true},
		Limits:   config.Limits{MaxDiffBytes: 400000, MaxChangedFiles: 200, MaxCostUSD: 1.0},
	}
	return Inputs{
		RunID: "run1", Event: ev, Policy: pol, ForgeType: "github", ForgeAPI: "https://api.github.com",
		PromptText: "PROMPT", PromptVersion: "prompts/x@abc",
		CloneToken: "CTOK", PublishToken: "PTOK",
		CallbackURL: "https://momos", CallbackToken: "CBTOK",
	}
}

func TestBuildTopology(t *testing.T) {
	p := Build(sampleInputs())
	if len(p.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(p.Steps))
	}
	if p.Metadata[protocol.EnvRunID] != "run1" {
		t.Fatalf("job metadata missing run id")
	}

	clone, review, publish := p.Steps[0], p.Steps[1], p.Steps[2]

	// continue_on_error rule (plan.md §12.1): steps 1&2 true with non-empty script.
	if !clone.ContinueOnError || clone.Script == "" {
		t.Fatalf("clone must have continue_on_error and non-empty script")
	}
	if !review.ContinueOnError || review.Script == "" {
		t.Fatalf("review must have continue_on_error and non-empty script")
	}
	if publish.ContinueOnError {
		t.Fatalf("publish must NOT continue_on_error")
	}
}

// The review step must carry no forge/git credentials — the isolation boundary
// (plan.md §11.4).
func TestReviewStepHasNoForgeCredentials(t *testing.T) {
	p := Build(sampleInputs())
	review := p.Steps[1]
	if _, ok := review.Metadata[protocol.EnvGitToken]; ok {
		t.Fatal("review step must not have GIT_TOKEN")
	}
	if _, ok := review.Metadata[protocol.EnvForgeToken]; ok {
		t.Fatal("review step must not have FORGE_TOKEN")
	}
	// Clone has the read token; publish has the write token + callback.
	if p.Steps[0].Metadata[protocol.EnvGitToken] != "CTOK" {
		t.Fatal("clone step must carry the clone token")
	}
	if p.Steps[2].Metadata[protocol.EnvForgeToken] != "PTOK" {
		t.Fatal("publish step must carry the publish token")
	}
	if p.Steps[2].Metadata[protocol.EnvCallbackToken] != "CBTOK" {
		t.Fatal("publish step must carry the callback token")
	}
}

func TestPromptEncodedInReviewStep(t *testing.T) {
	p := Build(sampleInputs())
	b64 := p.Steps[1].Metadata[protocol.EnvPromptB64]
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || string(decoded) != "PROMPT" {
		t.Fatalf("prompt not encoded correctly: %q %v", b64, err)
	}
}

func TestPolicyHashSensitivity(t *testing.T) {
	pol := sampleInputs().Policy
	h1 := PolicyHash(pol, "v1")
	pol.Reviewer.Strategy = "agentic"
	h2 := PolicyHash(pol, "v1")
	if h1 == h2 {
		t.Fatal("policy hash must change with strategy (A/B re-runs must not dedupe)")
	}
	h3 := PolicyHash(pol, "v2")
	if h2 == h3 {
		t.Fatal("policy hash must change with prompt version")
	}
}
