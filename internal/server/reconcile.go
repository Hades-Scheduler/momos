package server

import (
	"context"
	"time"

	"github.com/ls1intum/momos/internal/hades"
	"github.com/ls1intum/momos/internal/metrics"
	"github.com/ls1intum/momos/internal/store"
)

// reconcileLoop polls Hades job status for runs still in "submitted" past a
// timeout, so a lost callback does not orphan a run (plan.md §11.6). This is
// best-effort: LogManager state is in-memory (plan.md §12.3), so an unknown
// status past the hard timeout is treated as a timeout.
func (s *Server) reconcileLoop(ctx context.Context) {
	timeout := s.cfg.Defaults.Timeout.Std()
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOnce(ctx, timeout)
		}
	}
}

func (s *Server) reconcileOnce(ctx context.Context, timeout time.Duration) {
	// Poll runs older than one poll interval (the callback is the fast path).
	runs, err := s.store.ActiveOlderThan(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		s.log.Warn("reconcile query failed", "err", err)
		return
	}
	for _, run := range runs {
		age := time.Since(run.CreatedAt)
		var state hades.JobState
		if run.HadesJobID != "" {
			state, _ = s.hades.Status(ctx, run.HadesJobID)
		}
		switch {
		case state == hades.StateSucceeded:
			// The publisher's callback is authoritative for the result; if the
			// job succeeded but no callback arrived, mark succeeded without
			// review detail (plan.md §11.6 accepted consequence).
			s.finalize(ctx, run.RunID, store.StatusSucceeded)
		case state == hades.StateFailed:
			s.finalize(ctx, run.RunID, store.StatusFailed)
		case age > timeout:
			s.log.Warn("run timed out", "run", run.RunID, "age", age.String())
			s.finalize(ctx, run.RunID, store.StatusTimeout)
		}
	}
}

func (s *Server) finalize(ctx context.Context, runID string, status store.Status) {
	if err := s.store.SetStatus(ctx, runID, status); err != nil {
		s.log.Warn("finalize failed", "run", runID, "err", err)
		return
	}
	metrics.RunsTotal.WithLabelValues(string(status)).Inc()
}
