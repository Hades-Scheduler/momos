// Package job builds the three-step Hades payload from a ReviewEvent, a
// resolved Policy, a rendered prompt, and per-step tokens (plan.md §4, §10.8,
// §11). Topology: clone (momos-clone) -> review (momos-reviewer, git,
// continue_on_error) -> publish (momos-publisher).
package job

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Hades-Scheduler/momos/internal/config"
	"github.com/Hades-Scheduler/momos/internal/event"
	"github.com/Hades-Scheduler/momos/internal/hades"
	"github.com/Hades-Scheduler/momos/internal/protocol"
)

// Inputs bundles everything the builder needs.
type Inputs struct {
	RunID         string
	Event         *event.ReviewEvent
	Policy        config.Policy
	ForgeType     string
	ForgeAPI      string
	PromptText    string
	PromptVersion string

	// ExistingThreads is the rendered block of existing (human) PR review
	// threads, so the reviewer avoids duplicating open change requests and
	// respects resolved ones. Data, not a credential — kept out of PolicyHash.
	ExistingThreads string

	// Tokens embedded at submission (M0 embed mode, plan.md §11.5). CloneToken
	// is contents:read; PublishToken is pull_requests:write + checks:write.
	CloneToken   string
	PublishToken string

	// Callback wiring for the publisher (plan.md §11.6).
	CallbackURL   string
	CallbackToken string
}

// Build assembles the Hades payload.
func Build(in Inputs) hades.Payload {
	ev := in.Event
	pol := in.Policy

	jobMeta := map[string]string{
		protocol.EnvRunID:   in.RunID,
		protocol.EnvRepo:    ev.RepoID,
		protocol.EnvHeadSHA: ev.HeadSHA,
		protocol.EnvPR:      strconv.Itoa(ev.PRNumber),
	}

	steps := []hades.Step{
		cloneStep(in),
		reviewStep(in),
		publishStep(in),
	}

	name := fmt.Sprintf("momos: review %s#%d @ %s", ev.RepoID, ev.PRNumber, shortSHA(ev.HeadSHA))
	return hades.Payload{
		Name:     name,
		Priority: pol.Priority,
		Metadata: jobMeta,
		Steps:    steps,
	}
}

func cloneStep(in Inputs) hades.Step {
	ev := in.Event
	headRef := ev.HeadRef
	if ev.Kind == event.KindPullRequest {
		// GitHub mirrors the PR head (even from forks) under refs/pull/N/head on
		// the base repo, so a single origin fetch reaches it (plan.md §12.2).
		headRef = fmt.Sprintf("refs/pull/%d/head", ev.PRNumber)
	}
	meta := map[string]string{
		protocol.EnvGitURL:     ev.CloneURL,
		protocol.EnvGitToken:   in.CloneToken,
		protocol.EnvGitHeadSHA: ev.HeadSHA,
		protocol.EnvGitHeadRef: headRef,
		protocol.EnvGitBaseSHA: ev.BaseSHA,
		protocol.EnvGitBaseRef: ev.BaseRef,
		protocol.EnvGitIsPR:    strconv.FormatBool(ev.Kind == event.KindPullRequest),
	}
	return hades.Step{
		ID:              1,
		Name:            "Clone",
		Image:           orDefault(in.Policy.Clone.Image, "ghcr.io/hades-scheduler/momos-clone:latest"),
		Script:          "/app/clone.sh", // non-empty so continue_on_error is honored (plan.md §12.1)
		ContinueOnError: true,
		Metadata:        meta,
		CPULimit:        in.Policy.Clone.CPULimit,
		MemoryLimit:     in.Policy.Clone.MemoryLimit,
	}
}

func reviewStep(in Inputs) hades.Step {
	ev := in.Event
	r := in.Policy.Reviewer
	meta := map[string]string{
		protocol.EnvReviewStrategy:  orDefault(r.Strategy, "oneshot"),
		protocol.EnvLLMBaseURL:      r.BaseURL,
		protocol.EnvLLMModel:        r.Model,
		protocol.EnvLLMAPIKey:       r.APIKey,
		protocol.EnvPromptB64:       base64.StdEncoding.EncodeToString([]byte(in.PromptText)),
		protocol.EnvPromptVersion:   in.PromptVersion,
		protocol.EnvMaxOutputTokens: strconv.Itoa(r.MaxOutputTokens),
		protocol.EnvMaxTurns:        strconv.Itoa(r.MaxTurns),
		protocol.EnvMaxDiffBytes:    strconv.Itoa(in.Policy.Limits.MaxDiffBytes),
		protocol.EnvMaxChangedFiles: strconv.Itoa(in.Policy.Limits.MaxChangedFiles),
		protocol.EnvMaxCostUSD:      strconv.FormatFloat(in.Policy.Limits.MaxCostUSD, 'f', -1, 64),
		protocol.EnvInputPrice:      strconv.FormatFloat(r.InputPricePerMTok, 'f', -1, 64),
		protocol.EnvOutputPrice:     strconv.FormatFloat(r.OutputPricePerMTok, 'f', -1, 64),
		protocol.EnvBaseSHA:         ev.BaseSHA,
		protocol.EnvHeadSHA2:        ev.HeadSHA,
		protocol.EnvRepoID:          ev.RepoID,
	}
	if in.ExistingThreads != "" {
		meta[protocol.EnvExistingThreadsB64] = base64.StdEncoding.EncodeToString([]byte(in.ExistingThreads))
	}
	return hades.Step{
		ID:              2,
		Name:            "AI Review",
		Image:           orDefault(r.Image, "ghcr.io/hades-scheduler/momos-reviewer:latest"),
		Script:          "/app/reviewer",
		ContinueOnError: true, // publish still runs to surface failures (plan.md §10.6)
		Metadata:        meta,
		CPULimit:        r.CPULimit,
		MemoryLimit:     r.MemoryLimit,
	}
}

func publishStep(in Inputs) hades.Step {
	ev := in.Event
	p := in.Policy.Publish
	meta := map[string]string{
		protocol.EnvForge:          in.ForgeType,
		protocol.EnvForgeAPI:       in.ForgeAPI,
		protocol.EnvForgeToken:     in.PublishToken,
		protocol.EnvPublishMode:    orDefault(p.Mode, "pr_review"),
		protocol.EnvInlineComments: strconv.FormatBool(p.InlineComments),
		protocol.EnvCheckRun:       strconv.FormatBool(p.CheckRun),
		protocol.EnvApprovals:      strconv.FormatBool(p.Approvals),
		protocol.EnvIsFork:         strconv.FormatBool(ev.IsFork),
		protocol.EnvExpectedHead:   ev.HeadSHA,
		protocol.EnvRepoID:         ev.RepoID,
		protocol.EnvPRNumber:       strconv.Itoa(ev.PRNumber),
		protocol.EnvCallbackURL:    in.CallbackURL,
		protocol.EnvCallbackToken:  in.CallbackToken,
		protocol.EnvRunID:          in.RunID,
	}
	return hades.Step{
		ID:          3,
		Name:        "Publish",
		Image:       orDefault(p.Image, "ghcr.io/hades-scheduler/momos-publisher:latest"),
		Script:      "/app/publisher",
		Metadata:    meta,
		CPULimit:    p.CPULimit,
		MemoryLimit: p.MemoryLimit,
	}
}

// PolicyHash is a stable hash of the fields that make a policy meaningfully
// distinct for idempotency (plan.md §3⑧). Prompt version, strategy, and model
// are included so an A/B re-run with a different strategy is not deduped.
func PolicyHash(pol config.Policy, promptVersion string) string {
	h := struct {
		Prompt   string
		Strategy string
		Model    string
		BaseURL  string
		Limits   config.Limits
	}{promptVersion, pol.Reviewer.Strategy, pol.Reviewer.Model, pol.Reviewer.BaseURL, pol.Limits}
	b, _ := json.Marshal(h)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
