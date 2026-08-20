package forge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Hades-Scheduler/momos/internal/event"
	"github.com/Hades-Scheduler/momos/internal/token"
)

// GitHub implements Forge and TokenMinter against the GitHub REST API.
type GitHub struct {
	apiBase string
	token   string // REST token (installation token or PAT); may be empty for a mint-only client
	app     *appCreds
	http    *http.Client
	botName string // author login used to identify our own comments (e.g. "momos[bot]")
}

type appCreds struct {
	appID          int64
	installationID int64
	privateKeyPEM  []byte
}

// NewGitHub builds a REST client authenticated with a token (used by the
// publisher). botName identifies Momos's own comments for dismiss-on-rerun.
func NewGitHub(apiBase, tok, botName string) *GitHub {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	if botName == "" {
		botName = "momos"
	}
	return &GitHub{
		apiBase: strings.TrimRight(apiBase, "/"),
		token:   tok,
		http:    &http.Client{Timeout: 30 * time.Second},
		botName: botName,
	}
}

// NewGitHubApp builds a mint-capable client from GitHub App credentials (used
// by the Momos service to mint installation tokens on demand).
func NewGitHubApp(apiBase string, appID, installationID int64, privateKeyPEM []byte) *GitHub {
	g := NewGitHub(apiBase, "", "")
	g.app = &appCreds{appID: appID, installationID: installationID, privateKeyPEM: privateKeyPEM}
	return g
}

// ---- Webhook parsing ----------------------------------------------------

type ghPullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head ghRef `json:"head"`
		Base ghRef `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository ghRepo `json:"repository"`
}

type ghRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo ghRepo `json:"repo"`
}

type ghRepo struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	Fork     bool   `json:"fork"`
}

type ghPushEvent struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Repository ghRepo `json:"repository"`
	Pusher     struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

// ParseWebhook verifies X-Hub-Signature-256 and normalizes the payload.
func (g *GitHub) ParseWebhook(r *http.Request, secret string) (*event.ReviewEvent, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("read webhook body: %w", err)
	}
	if secret != "" {
		if err := verifyGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
			return nil, err
		}
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	switch r.Header.Get("X-GitHub-Event") {
	case "pull_request":
		var pe ghPullRequestEvent
		if err := json.Unmarshal(body, &pe); err != nil {
			return nil, fmt.Errorf("decode pull_request: %w", err)
		}
		ev := &event.ReviewEvent{
			Forge:        event.ForgeGitHub,
			RepoID:       pe.Repository.FullName,
			CloneURL:     pe.Repository.CloneURL,
			Kind:         event.KindPullRequest,
			Action:       pe.Action,
			BaseRef:      pe.PullRequest.Base.Ref,
			BaseSHA:      pe.PullRequest.Base.SHA,
			HeadRef:      pe.PullRequest.Head.Ref,
			HeadSHA:      pe.PullRequest.Head.SHA,
			HeadCloneURL: pe.PullRequest.Head.Repo.CloneURL,
			PRNumber:     pe.Number,
			IsFork:       pe.PullRequest.Head.Repo.FullName != "" && pe.PullRequest.Head.Repo.FullName != pe.Repository.FullName,
			Author:       pe.PullRequest.User.Login,
			DeliveryID:   delivery,
		}
		if ev.HeadCloneURL == "" {
			ev.HeadCloneURL = ev.CloneURL
		}
		return ev, nil
	case "push":
		var pe ghPushEvent
		if err := json.Unmarshal(body, &pe); err != nil {
			return nil, fmt.Errorf("decode push: %w", err)
		}
		return &event.ReviewEvent{
			Forge:        event.ForgeGitHub,
			RepoID:       pe.Repository.FullName,
			CloneURL:     pe.Repository.CloneURL,
			HeadCloneURL: pe.Repository.CloneURL,
			Kind:         event.KindPush,
			Action:       pe.Ref,
			BaseRef:      pe.Ref,
			BaseSHA:      pe.Before,
			HeadRef:      pe.Ref,
			HeadSHA:      pe.After,
			Author:       pe.Pusher.Name,
			DeliveryID:   delivery,
		}, nil
	default:
		return nil, nil // ignorable event
	}
}

func verifyGitHubSignature(secret string, body []byte, header string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return fmt.Errorf("missing or malformed signature header")
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return fmt.Errorf("bad signature hex")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(want, mac.Sum(nil)) {
		return fmt.Errorf("webhook signature mismatch")
	}
	return nil
}

// ---- Token minting ------------------------------------------------------

// MintToken mints a scoped installation token. With a PAT-only client (M0) it
// returns the configured token unchanged.
func (g *GitHub) MintToken(ctx context.Context, repo string, scope token.Scope) (MintedToken, error) {
	if g.app == nil {
		if g.token == "" {
			return MintedToken{}, fmt.Errorf("no app credentials and no token configured")
		}
		return MintedToken{Token: g.token, Expiry: time.Now().Add(time.Hour)}, nil
	}
	jwtStr, err := g.appJWT()
	if err != nil {
		return MintedToken{}, err
	}
	perms := map[string]string{}
	switch scope {
	case token.ScopeRead:
		perms["contents"] = "read"
	case token.ScopeWrite:
		perms["pull_requests"] = "write"
		perms["checks"] = "write"
		perms["contents"] = "read"
	}
	name := repoName(repo)
	reqBody, _ := json.Marshal(map[string]any{
		"repositories": []string{name},
		"permissions":  perms,
	})
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", g.apiBase, g.app.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return MintedToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.http.Do(req)
	if err != nil {
		return MintedToken{}, fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return MintedToken{}, fmt.Errorf("mint token status %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return MintedToken{}, err
	}
	return MintedToken{Token: out.Token, Expiry: out.ExpiresAt}, nil
}

func (g *GitHub) appJWT() (string, error) {
	keyPEM := g.app.privateKeyPEM
	// Accept a base64-encoded PEM as well as a raw one. The base64 form is a
	// single line, so it survives ${ENV} substitution into the inline YAML
	// config; a raw multi-line PEM would not (plan.md §11.5).
	if !bytes.Contains(keyPEM, []byte("-----BEGIN")) {
		if decoded, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyPEM))); derr == nil {
			keyPEM = decoded
		}
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(keyPEM)
	if err != nil {
		return "", fmt.Errorf("parse app private key: %w", err)
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", g.app.appID),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// ---- Posting ------------------------------------------------------------

// CurrentHead returns the PR's current head SHA.
func (g *GitHub) CurrentHead(ctx context.Context, repo string, pr int) (string, error) {
	var out struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, pr), nil, &out); err != nil {
		return "", err
	}
	return out.Head.SHA, nil
}

// ListReviewThreads reads existing PR review threads via the GraphQL API. REST
// does not expose isResolved/isOutdated, which are exactly the flags we need to
// respect a human's resolution, so GraphQL is required here.
func (g *GitHub) ListReviewThreads(ctx context.Context, repo string, pr int, authToken string) ([]ReviewThread, error) {
	const query = `query($owner:String!,$name:String!,$pr:Int!){repository(owner:$owner,name:$name){pullRequest(number:$pr){reviewThreads(first:100){nodes{isResolved isOutdated path line comments(first:20){nodes{body author{login}}}}}}}}`
	reqBody, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"owner": repoOwner(repo), "name": repoName(repo), "pr": pr},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.graphqlEndpoint(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "momos") // GitHub GraphQL rejects requests without one.
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list review threads: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list review threads: status %d: %s", resp.StatusCode, string(data))
	}
	return parseReviewThreads(data)
}

// parseReviewThreads decodes the GraphQL reviewThreads response. author and line
// are nullable (deleted user / outdated thread), so both are pointers.
func parseReviewThreads(data []byte) ([]ReviewThread, error) {
	var out struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Path       string `json:"path"`
							Line       *int   `json:"line"`
							Comments   struct {
								Nodes []struct {
									Body   string `json:"body"`
									Author *struct {
										Login string `json:"login"`
									} `json:"author"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode review threads: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", out.Errors[0].Message)
	}
	nodes := out.Data.Repository.PullRequest.ReviewThreads.Nodes
	threads := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		t := ReviewThread{Path: n.Path, Line: n.Line, IsResolved: n.IsResolved, IsOutdated: n.IsOutdated}
		for _, c := range n.Comments.Nodes {
			author := "unknown"
			if c.Author != nil && c.Author.Login != "" {
				author = c.Author.Login
			}
			t.Comments = append(t.Comments, ThreadComment{Author: author, Body: c.Body})
		}
		threads = append(threads, t)
	}
	return threads, nil
}

// graphqlEndpoint derives the GraphQL URL from the REST apiBase. GitHub.com uses
// <base>/graphql; GHES exposes it at .../api/graphql rather than .../api/v3.
func (g *GitHub) graphqlEndpoint() string {
	if strings.HasSuffix(g.apiBase, "/api/v3") {
		return strings.TrimSuffix(g.apiBase, "/api/v3") + "/api/graphql"
	}
	return g.apiBase + "/graphql"
}

type ghComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	HTMLURL string `json:"html_url"`
}

// PostSummary upserts the marker-tagged issue comment.
func (g *GitHub) PostSummary(ctx context.Context, repo string, pr int, marker, body string) (string, error) {
	full := marker + "\n" + body
	var comments []ghComment
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, pr), nil, &comments); err != nil {
		return "", err
	}
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			var out ghComment
			if err := g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, c.ID),
				map[string]string{"body": full}, &out); err != nil {
				return "", err
			}
			return out.HTMLURL, nil
		}
	}
	var out ghComment
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, pr),
		map[string]string{"body": full}, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

// PostReview deletes any prior Momos inline comments (found by marker) then
// posts a fresh COMMENT review, so inline comments do not stack across pushes.
func (g *GitHub) PostReview(ctx context.Context, repo string, pr int, marker, body string, comments []InlineComment) error {
	// Remove prior Momos review comments.
	var prior []ghComment
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", repo, pr), nil, &prior); err != nil {
		return err
	}
	for _, c := range prior {
		if strings.Contains(c.Body, marker) {
			_ = g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/pulls/comments/%d", repo, c.ID), nil, nil)
		}
	}
	if len(comments) == 0 {
		return nil
	}
	type rc struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	revComments := make([]rc, 0, len(comments))
	for _, c := range comments {
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		revComments = append(revComments, rc{
			Path: c.Path, Line: c.Line, Side: side,
			Body: marker + "\n" + c.Body,
		})
	}
	payload := map[string]any{
		"event":    "COMMENT",
		"body":     body,
		"comments": revComments,
	}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, pr), payload, nil)
}

// PostCheckRun creates a completed check run.
func (g *GitHub) PostCheckRun(ctx context.Context, repo, headSHA, name, conclusion, title, summary string) error {
	payload := map[string]any{
		"name":         name,
		"head_sha":     headSHA,
		"status":       "completed",
		"conclusion":   conclusion,
		"completed_at": time.Now().UTC().Format(time.RFC3339),
		"output": map[string]string{
			"title":   title,
			"summary": summary,
		},
	}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/check-runs", repo), payload, nil)
}

// ---- REST plumbing ------------------------------------------------------

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.apiBase+path, body)
	if err != nil {
		return err
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func repoName(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func repoOwner(repo string) string {
	if i := strings.Index(repo, "/"); i >= 0 {
		return repo[:i]
	}
	return repo
}
