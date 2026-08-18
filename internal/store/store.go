// Package store is the run store (plan.md §3⑧): one Run record per submitted
// job. It is the idempotency key (repo + head_sha + policy_hash), the supersede
// index (latest run per PR), the reconciliation source, and the evaluation
// dataset. Backed by SQLite (modernc.org/sqlite, pure Go — no cgo).
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// Status is the run lifecycle state.
type Status string

const (
	StatusSubmitted    Status = "submitted"
	StatusSucceeded    Status = "succeeded"
	StatusFailed       Status = "failed"
	StatusTimeout      Status = "timeout"
	StatusReviewFailed Status = "review_failed"
	StatusNoChanges    Status = "no_changes"
	StatusStale        Status = "stale"
	StatusSuperseded   Status = "superseded"
)

// Run is one submitted review job.
type Run struct {
	RunID         string
	Forge         string
	RepoID        string
	PRNumber      int
	HeadSHA       string
	BaseSHA       string
	PolicyHash    string
	PromptVersion string
	Strategy      string
	Model         string
	HadesJobID    string
	Status        Status
	Verdict       string
	Findings      int
	InputTokens   int
	OutputTokens  int
	CostUSD       float64
	CommentURL    string
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store is the SQLite-backed run store.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the run store at dsn (a file path or ":memory:").
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite single-writer
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS runs (
	run_id         TEXT PRIMARY KEY,
	forge          TEXT NOT NULL,
	repo_id        TEXT NOT NULL,
	pr_number      INTEGER NOT NULL,
	head_sha       TEXT NOT NULL,
	base_sha       TEXT NOT NULL,
	policy_hash    TEXT NOT NULL,
	prompt_version TEXT NOT NULL,
	strategy       TEXT NOT NULL,
	model          TEXT NOT NULL,
	hades_job_id   TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL,
	verdict        TEXT NOT NULL DEFAULT '',
	findings       INTEGER NOT NULL DEFAULT 0,
	input_tokens   INTEGER NOT NULL DEFAULT 0,
	output_tokens  INTEGER NOT NULL DEFAULT 0,
	cost_usd       REAL NOT NULL DEFAULT 0,
	comment_url    TEXT NOT NULL DEFAULT '',
	error          TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMP NOT NULL,
	updated_at     TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_repo_pr ON runs(repo_id, pr_number);
CREATE INDEX IF NOT EXISTS idx_runs_status  ON runs(status);
CREATE INDEX IF NOT EXISTS idx_idem ON runs(repo_id, head_sha, policy_hash);
`)
	return err
}

// ErrDuplicate indicates an idempotent create hit an existing active run.
var ErrDuplicate = errors.New("duplicate active run for idempotency key")

// Create inserts a run, enforcing idempotency on (repo, head_sha, policy_hash).
// If an active run already exists for that key, it returns that run and
// ErrDuplicate. It also marks any older active run for the same PR as
// superseded (the supersede index; Hades has no cancel API, plan.md §10.5).
func (s *Store) Create(ctx context.Context, r *Run) (*Run, error) {
	now := time.Now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now
	if r.Status == "" {
		r.Status = StatusSubmitted
	}

	// Idempotency check.
	if existing := s.findActiveByKey(ctx, r.RepoID, r.HeadSHA, r.PolicyHash); existing != nil {
		return existing, ErrDuplicate
	}

	// Supersede older active runs for the same PR.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, updated_at=? WHERE repo_id=? AND pr_number=? AND status IN (?,?)`,
		StatusSuperseded, now, r.RepoID, r.PRNumber, StatusSubmitted, StatusReviewFailed)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO runs (run_id, forge, repo_id, pr_number, head_sha, base_sha, policy_hash,
	prompt_version, strategy, model, hades_job_id, status, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.Forge, r.RepoID, r.PRNumber, r.HeadSHA, r.BaseSHA, r.PolicyHash,
		r.PromptVersion, r.Strategy, r.Model, r.HadesJobID, r.Status, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) findActiveByKey(ctx context.Context, repo, head, policy string) *Run {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+cols+` FROM runs WHERE repo_id=? AND head_sha=? AND policy_hash=? AND status=? LIMIT 1`,
		repo, head, policy, StatusSubmitted)
	r, err := scan(row)
	if err != nil {
		return nil
	}
	return r
}

// SetHadesJob records the Hades job ID for a run.
func (s *Store) SetHadesJob(ctx context.Context, runID, jobID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET hades_job_id=?, updated_at=? WHERE run_id=?`,
		jobID, time.Now().UTC(), runID)
	return err
}

// SaveResult persists the terminal result of a run.
func (s *Store) SaveResult(ctx context.Context, runID string, status Status,
	verdict string, findings, inTok, outTok int, cost float64, commentURL, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE runs SET status=?, verdict=?, findings=?, input_tokens=?, output_tokens=?,
	cost_usd=?, comment_url=?, error=?, updated_at=? WHERE run_id=?`,
		status, verdict, findings, inTok, outTok, cost, commentURL, errMsg,
		time.Now().UTC(), runID)
	return err
}

// SetStatus updates only the status (used by the reconciler).
func (s *Store) SetStatus(ctx context.Context, runID string, status Status) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, updated_at=? WHERE run_id=?`,
		status, time.Now().UTC(), runID)
	return err
}

// Get returns a run by ID.
func (s *Store) Get(ctx context.Context, runID string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM runs WHERE run_id=?`, runID)
	return scan(row)
}

// ActiveOlderThan returns submitted runs whose updated_at is older than cutoff
// (reconciliation candidates for status polling / timeout, plan.md §11.6).
func (s *Store) ActiveOlderThan(ctx context.Context, cutoff time.Time) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM runs WHERE status=? AND updated_at < ?`, StatusSubmitted, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List returns the most recent runs (for the status UI).
func (s *Store) List(ctx context.Context, limit int) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const cols = `run_id, forge, repo_id, pr_number, head_sha, base_sha, policy_hash,
	prompt_version, strategy, model, hades_job_id, status, verdict, findings,
	input_tokens, output_tokens, cost_usd, comment_url, error, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (*Run, error) {
	var r Run
	err := row.Scan(&r.RunID, &r.Forge, &r.RepoID, &r.PRNumber, &r.HeadSHA, &r.BaseSHA,
		&r.PolicyHash, &r.PromptVersion, &r.Strategy, &r.Model, &r.HadesJobID, &r.Status,
		&r.Verdict, &r.Findings, &r.InputTokens, &r.OutputTokens, &r.CostUSD,
		&r.CommentURL, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
