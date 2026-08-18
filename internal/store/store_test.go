package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newRun(id, head, policy string) *Run {
	return &Run{
		RunID: id, Forge: "github-main", RepoID: "o/r", PRNumber: 7,
		HeadSHA: head, BaseSHA: "base", PolicyHash: policy,
		PromptVersion: "v", Strategy: "oneshot", Model: "gpt-4o",
	}
}

func TestCreateAndGet(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, newRun("run1", "head1", "pol1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Get(ctx, "run1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusSubmitted || got.HeadSHA != "head1" {
		t.Fatalf("unexpected run: %+v", got)
	}
}

func TestIdempotency(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, newRun("run1", "head1", "pol1")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	existing, err := s.Create(ctx, newRun("run2", "head1", "pol1"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if existing.RunID != "run1" {
		t.Fatalf("duplicate should return existing run1, got %s", existing.RunID)
	}
	// A different policy hash for the same head is a distinct run (A/B).
	if _, err := s.Create(ctx, newRun("run3", "head1", "pol2")); err != nil {
		t.Fatalf("distinct policy must be allowed: %v", err)
	}
}

func TestSupersede(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_, _ = s.Create(ctx, newRun("run1", "head1", "pol1"))
	// New head for same PR supersedes the old submitted run.
	if _, err := s.Create(ctx, newRun("run2", "head2", "pol1")); err != nil {
		t.Fatalf("create run2: %v", err)
	}
	old, _ := s.Get(ctx, "run1")
	if old.Status != StatusSuperseded {
		t.Fatalf("expected run1 superseded, got %s", old.Status)
	}
}

func TestSaveResultAndReconcile(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_, _ = s.Create(ctx, newRun("run1", "head1", "pol1"))
	if err := s.SaveResult(ctx, "run1", StatusSucceeded, "comment", 3, 100, 50, 0.12, "http://x", ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ := s.Get(ctx, "run1")
	if got.Status != StatusSucceeded || got.Findings != 3 || got.CostUSD != 0.12 {
		t.Fatalf("result not saved: %+v", got)
	}

	// ActiveOlderThan only returns still-submitted runs.
	_, _ = s.Create(ctx, newRun("run2", "head2", "pol1"))
	active, err := s.ActiveOlderThan(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 || active[0].RunID != "run2" {
		t.Fatalf("expected only run2 active, got %+v", active)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
