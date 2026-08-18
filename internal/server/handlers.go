package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Hades-Scheduler/momos/internal/config"
	"github.com/Hades-Scheduler/momos/internal/event"
	"github.com/Hades-Scheduler/momos/internal/job"
	"github.com/Hades-Scheduler/momos/internal/metrics"
	"github.com/Hades-Scheduler/momos/internal/store"
	"github.com/Hades-Scheduler/momos/internal/token"
)

// handleGitHubWebhook verifies, dedupes, and processes a webhook. It responds
// 202 immediately and processes asynchronously (plan.md §3①).
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	// The forge id is chosen by matching the configured webhook secret against
	// the signature. For simplicity we try each github forge until one parses.
	body := http.MaxBytesReader(w, r.Body, 5<<20)
	r.Body = body

	forgeID, ev, err := s.parseAnyGitHub(r)
	if err != nil {
		metrics.HooksTotal.WithLabelValues("rejected").Inc()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ev == nil {
		metrics.HooksTotal.WithLabelValues("ignored").Inc()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if s.dedupe(ev.DeliveryID) {
		metrics.HooksTotal.WithLabelValues("duplicate").Inc()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	ev.ForgeID = forgeID
	metrics.HooksTotal.WithLabelValues("accepted").Inc()
	w.WriteHeader(http.StatusAccepted)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.process(ctx, ev); err != nil {
			s.log.Error("process event failed", "repo", ev.RepoID, "pr", ev.PRNumber, "err", err)
		}
	}()
}

// parseAnyGitHub tries each configured github forge's secret until one verifies.
func (s *Server) parseAnyGitHub(r *http.Request) (string, *event.ReviewEvent, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}
	var lastErr error
	for _, fc := range s.cfg.Forges {
		if fc.Type != "github" {
			continue
		}
		f := s.forges[fc.ID]
		req := r.Clone(r.Context())
		req.Body = io.NopCloser(bytes.NewReader(raw))
		ev, perr := f.ParseWebhook(req, fc.WebhookSecret)
		if perr != nil {
			lastErr = perr
			continue
		}
		return fc.ID, ev, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no github forge configured")
	}
	return "", nil, lastErr
}

// process turns a normalized event into a submitted Hades job.
func (s *Server) process(ctx context.Context, ev *event.ReviewEvent) error {
	pol := s.cfg.Resolve(ev.RepoID)
	if pol.Forge == "" {
		pol.Forge = ev.ForgeID
	}
	if !triggered(pol, ev) {
		s.log.Info("event not triggered by policy", "repo", ev.RepoID, "action", ev.Action)
		return nil
	}
	if ev.IsFork {
		switch pol.ForkPolicy {
		case "none":
			s.log.Info("fork PR skipped by policy", "repo", ev.RepoID)
			return nil
		case "summary_only":
			pol.Publish.InlineComments = false
		}
	}

	f, fc, ok := s.forge(pol.Forge)
	if !ok {
		return errors.New("forge not configured: " + pol.Forge)
	}

	rendered, err := s.prompts.Render(pol.Prompt, ev)
	if err != nil {
		return err
	}
	policyHash := job.PolicyHash(pol, rendered.Version)
	runID := newRunID()

	run := &store.Run{
		RunID: runID, Forge: pol.Forge, RepoID: ev.RepoID, PRNumber: ev.PRNumber,
		HeadSHA: ev.HeadSHA, BaseSHA: ev.BaseSHA, PolicyHash: policyHash,
		PromptVersion: rendered.Version, Strategy: orDefault(pol.Reviewer.Strategy, "oneshot"),
		Model: pol.Reviewer.Model, Status: store.StatusSubmitted,
	}
	if _, err := s.store.Create(ctx, run); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.log.Info("idempotent skip", "repo", ev.RepoID, "head", ev.HeadSHA)
			return nil
		}
		return err
	}

	// Embed mode (plan.md §11.5): mint scoped tokens at submission.
	cloneTok, err := f.MintToken(ctx, ev.RepoID, token.ScopeRead)
	if err != nil {
		return s.fail(ctx, runID, err)
	}
	publishTok, err := f.MintToken(ctx, ev.RepoID, token.ScopeWrite)
	if err != nil {
		return s.fail(ctx, runID, err)
	}
	timeout := pol.Timeout.Std()
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	callbackTok := s.minter.Mint(runID, token.StepPublish, token.ScopeWrite, timeout+30*time.Minute)

	payload := job.Build(job.Inputs{
		RunID: runID, Event: ev, Policy: pol,
		ForgeType: fc.Type, ForgeAPI: fc.API,
		PromptText: rendered.Text, PromptVersion: rendered.Version,
		CloneToken: cloneTok.Token, PublishToken: publishTok.Token,
		CallbackURL: s.cfg.Server.ExternalURL, CallbackToken: callbackTok,
	})

	jobID, err := s.hades.Submit(ctx, payload)
	if err != nil {
		return s.fail(ctx, runID, err)
	}
	if err := s.store.SetHadesJob(ctx, runID, jobID); err != nil {
		s.log.Warn("set hades job id failed", "run", runID, "err", err)
	}
	s.log.Info("submitted review job", "run", runID, "job", jobID, "repo", ev.RepoID, "pr", ev.PRNumber)
	return nil
}

func (s *Server) fail(ctx context.Context, runID string, cause error) error {
	_ = s.store.SaveResult(ctx, runID, store.StatusFailed, "", 0, 0, 0, 0, "", cause.Error())
	metrics.RunsTotal.WithLabelValues(string(store.StatusFailed)).Inc()
	return cause
}

// handleCallback receives the publisher's result. The token is verified
// idempotently per run_id, so the publisher's retries never race a burn
// (plan.md §12.3).
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	claims, err := s.minter.Verify(bearer(r))
	if err != nil || claims.RunID != runID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var result event.RunResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&result); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	status := store.Status(result.Status)
	var verdict string
	var findings, inTok, outTok int
	var cost float64
	if result.Review != nil {
		verdict = string(result.Review.Verdict)
		findings = len(result.Review.Findings)
		inTok = result.Review.Usage.InputTokens
		outTok = result.Review.Usage.OutputTokens
		cost = result.Review.Usage.CostUSD
	}
	if err := s.store.SaveResult(r.Context(), runID, status, verdict, findings, inTok, outTok, cost, result.CommentURL, result.Error); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}

	metrics.RunsTotal.WithLabelValues(string(status)).Inc()
	metrics.CostUSD.Add(cost)
	if run, err := s.store.Get(r.Context(), runID); err == nil {
		metrics.HookToComment.Observe(time.Since(run.CreatedAt).Seconds())
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleStepToken implements fetch-at-step-start (plan.md §11.5): the clone or
// publish step presents its bootstrap token and receives a fresh scoped forge
// token. Embed mode does not use this, but the endpoint completes the seam.
func (s *Server) handleStepToken(step token.Step, scope token.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("run_id")
		claims, err := s.minter.Verify(bearer(r))
		if err != nil || claims.RunID != runID || claims.Step != step {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		run, err := s.store.Get(r.Context(), runID)
		if err != nil {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		f, _, ok := s.forge(run.Forge)
		if !ok {
			http.Error(w, "forge not configured", http.StatusInternalServerError)
			return
		}
		tok, err := f.MintToken(r.Context(), run.RepoID, scope)
		if err != nil {
			http.Error(w, "mint failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": tok.Token, "expiry": tok.Expiry})
	}
}

// handleListRuns returns recent runs as JSON (minimal status view, plan.md §3⑩).
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.List(r.Context(), 100)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func triggered(pol config.Policy, ev *event.ReviewEvent) bool {
	switch ev.Kind {
	case event.KindPullRequest:
		return contains(pol.Triggers.PullRequest, ev.Action)
	case event.KindPush:
		return contains(pol.Triggers.Push, ev.HeadRef) || contains(pol.Triggers.Push, ev.BaseRef)
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
