// Package hades is a thin client for the public Hades surface: POST /build to
// submit a job and the LogManager status endpoint for reconciliation. Momos
// uses only this documented surface — zero changes to Hades (plan.md §1, §10).
package hades

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Step mirrors shared/payload.Step in Hades. Field tags match the wire format
// exactly (verified in plan.md §10.2). CPULimit is millicores.
type Step struct {
	ID              int               `json:"id"`
	Name            string            `json:"name"`
	Image           string            `json:"image"`
	Script          string            `json:"script"`
	ContinueOnError bool              `json:"continue_on_error"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CPULimit        uint              `json:"cpu_limit,omitempty"`
	MemoryLimit     string            `json:"memory_limit,omitempty"`
}

// Payload mirrors shared/payload.RESTPayload. Hades assigns the job ID
// server-side (plan.md §10.3); any ID here is ignored.
type Payload struct {
	Name     string            `json:"name"`
	Priority int               `json:"priority"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Steps    []Step            `json:"steps"`
}

// Client talks to the Hades API and LogManager.
type Client struct {
	apiURL        string
	logManagerURL string
	authKey       string
	http          *http.Client
}

// New constructs a Hades client. authKey is the AUTH_KEY used for Basic Auth
// with user "hades" (plan.md §10.3).
func New(apiURL, logManagerURL, authKey string) *Client {
	return &Client{
		apiURL:        apiURL,
		logManagerURL: logManagerURL,
		authKey:       authKey,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

type buildResponse struct {
	Message string `json:"message"`
	JobID   string `json:"job_id"`
}

// Submit posts a job to POST /build and returns the Hades-assigned job ID.
func (c *Client) Submit(ctx context.Context, p Payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/build", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authKey != "" {
		req.SetBasicAuth("hades", c.authKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("post /build: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hades /build returned %d: %s", resp.StatusCode, string(data))
	}
	var br buildResponse
	if err := json.Unmarshal(data, &br); err != nil {
		return "", fmt.Errorf("decode build response: %w", err)
	}
	if br.JobID == "" {
		return "", fmt.Errorf("hades /build returned empty job_id")
	}
	return br.JobID, nil
}

// JobState is a coarse job state derived from the LogManager status endpoint.
type JobState string

const (
	StateUnknown   JobState = "unknown"
	StateQueued    JobState = "queued"
	StateRunning   JobState = "running"
	StateSucceeded JobState = "succeeded"
	StateFailed    JobState = "failed"
)

// Status queries the LogManager for a job's status. LogManager state is
// in-memory (plan.md §12.3), so a missing job is reported as StateUnknown
// rather than an error, and callers treat status polling as best-effort
// reconciliation behind the authoritative callback.
func (c *Client) Status(ctx context.Context, jobID string) (JobState, error) {
	if c.logManagerURL == "" {
		return StateUnknown, nil
	}
	url := fmt.Sprintf("%s/jobs/%s/status", c.logManagerURL, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StateUnknown, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return StateUnknown, fmt.Errorf("get status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return StateUnknown, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return StateUnknown, fmt.Errorf("logmanager status %d: %s", resp.StatusCode, string(data))
	}
	// LogManager's exact shape is not part of the payload contract; extract a
	// status/state field defensively.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return StateUnknown, nil
	}
	for _, key := range []string{"status", "state"} {
		if v, ok := raw[key].(string); ok {
			return normalizeState(v), nil
		}
	}
	return StateUnknown, nil
}

func normalizeState(s string) JobState {
	switch {
	case containsAny(s, "success", "succeed", "complete", "done"):
		return StateSucceeded
	case containsAny(s, "fail", "error"):
		return StateFailed
	case containsAny(s, "run", "active", "progress"):
		return StateRunning
	case containsAny(s, "queue", "pending", "wait"):
		return StateQueued
	default:
		return StateUnknown
	}
}

func containsAny(s string, subs ...string) bool {
	ls := toLower(s)
	for _, sub := range subs {
		if indexOf(ls, sub) >= 0 {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func indexOf(s, sub string) int {
	return bytesIndex([]byte(s), []byte(sub))
}

func bytesIndex(s, sub []byte) int {
	return bytes.Index(s, sub)
}
