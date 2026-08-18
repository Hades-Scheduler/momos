// Package server wires the Momos HTTP surface (plan.md §3): webhook receiver,
// the result callback endpoint, the step-start token endpoints (fetch mode),
// health, metrics, and a minimal status view. It also runs the reconciler that
// polls Hades job status for runs whose callback never arrived (plan.md §11.6).
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ls1intum/momos/internal/config"
	"github.com/ls1intum/momos/internal/forge"
	"github.com/ls1intum/momos/internal/hades"
	"github.com/ls1intum/momos/internal/prompt"
	"github.com/ls1intum/momos/internal/store"
	"github.com/ls1intum/momos/internal/token"
)

// Server holds the wired dependencies.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	hades   *hades.Client
	prompts prompt.Store
	minter  *token.Minter
	forges  map[string]*forge.GitHub
	log     *slog.Logger

	mu         sync.Mutex
	deliveries map[string]time.Time // webhook delivery-ID dedupe (plan.md §3①)
}

// New builds a Server from config and dependencies.
func New(cfg *config.Config, st *store.Store, prompts prompt.Store, minter *token.Minter, log *slog.Logger) *Server {
	forges := map[string]*forge.GitHub{}
	for _, fc := range cfg.Forges {
		if fc.Type != "github" {
			log.Warn("unsupported forge type; skipping", "id", fc.ID, "type", fc.Type)
			continue
		}
		if fc.App.AppID != 0 && fc.App.PrivateKey != "" {
			forges[fc.ID] = forge.NewGitHubApp(fc.API, fc.App.AppID, fc.App.InstallationID, []byte(fc.App.PrivateKey))
		} else {
			forges[fc.ID] = forge.NewGitHub(fc.API, fc.Token, "momos")
		}
	}
	return &Server{
		cfg:        cfg,
		store:      st,
		hades:      hades.New(cfg.Hades.URL, cfg.Hades.LogManagerURL, cfg.Hades.AuthKey),
		prompts:    prompts,
		minter:     minter,
		forges:     forges,
		log:        log,
		deliveries: map[string]time.Time{},
	}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/github", s.handleGitHubWebhook)
	mux.HandleFunc("POST /v1/runs/{run_id}/result", s.handleCallback)
	mux.HandleFunc("GET /v1/runs/{run_id}/clone-token", s.handleStepToken(token.StepClone, token.ScopeRead))
	mux.HandleFunc("GET /v1/runs/{run_id}/publish-token", s.handleStepToken(token.StepPublish, token.ScopeWrite))
	mux.HandleFunc("GET /v1/runs", s.handleListRuns)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

// Start launches background workers (the reconciler) and blocks on ctx.
func (s *Server) Start(ctx context.Context) {
	go s.reconcileLoop(ctx)
	go s.gcDeliveries(ctx)
	<-ctx.Done()
}

func (s *Server) forge(id string) (*forge.GitHub, config.ForgeConfig, bool) {
	fc, ok := s.cfg.Forge(id)
	if !ok {
		return nil, config.ForgeConfig{}, false
	}
	f, ok := s.forges[id]
	return f, fc, ok
}

// dedupe returns true if the delivery ID was seen before (replay protection).
func (s *Server) dedupe(deliveryID string) bool {
	if deliveryID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.deliveries[deliveryID]; seen {
		return true
	}
	s.deliveries[deliveryID] = time.Now()
	return false
}

func (s *Server) gcDeliveries(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-time.Hour)
			s.mu.Lock()
			for id, ts := range s.deliveries {
				if ts.Before(cutoff) {
					delete(s.deliveries, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

func newRunID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "run_" + hex.EncodeToString(b)
}
