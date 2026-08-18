package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hades-Scheduler/momos/internal/event"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestParseWebhookPullRequest(t *testing.T) {
	body := `{
		"action":"opened","number":7,
		"pull_request":{
			"head":{"ref":"feat","sha":"headsha","repo":{"full_name":"fork/r","clone_url":"https://github.com/fork/r.git","fork":true}},
			"base":{"ref":"main","sha":"basesha"},
			"user":{"login":"alice"}
		},
		"repository":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"}
	}`
	g := NewGitHub("https://api.github.com", "", "momos")
	req := httptest.NewRequest("POST", "/hooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d-1")
	req.Header.Set("X-Hub-Signature-256", sign("whsecret", body))

	ev, err := g.ParseWebhook(req, "whsecret")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.RepoID != "o/r" || ev.PRNumber != 7 || ev.HeadSHA != "headsha" || ev.BaseSHA != "basesha" {
		t.Fatalf("bad normalization: %+v", ev)
	}
	if ev.Kind != event.KindPullRequest || ev.Action != "opened" {
		t.Fatalf("bad kind/action: %+v", ev)
	}
	if !ev.IsFork {
		t.Fatal("expected fork detection")
	}
	if ev.HeadCloneURL != "https://github.com/fork/r.git" {
		t.Fatalf("head clone url wrong: %q", ev.HeadCloneURL)
	}
	if ev.DeliveryID != "d-1" {
		t.Fatalf("delivery id wrong: %q", ev.DeliveryID)
	}
}

func TestParseWebhookRejectsBadSignature(t *testing.T) {
	body := `{"action":"opened"}`
	g := NewGitHub("", "", "")
	req := httptest.NewRequest("POST", "/hooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", sign("wrong", body))
	if _, err := g.ParseWebhook(req, "whsecret"); err == nil {
		t.Fatal("expected signature rejection")
	}
}

func TestParseWebhookIgnoresUnknownEvent(t *testing.T) {
	g := NewGitHub("", "", "")
	req := httptest.NewRequest("POST", "/hooks/github", strings.NewReader("{}"))
	req.Header.Set("X-GitHub-Event", "ping")
	ev, err := g.ParseWebhook(req, "")
	if err != nil || ev != nil {
		t.Fatalf("expected (nil,nil) for ignorable event, got %+v %v", ev, err)
	}
}
