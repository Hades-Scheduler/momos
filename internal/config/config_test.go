package config

import (
	"testing"
	"time"
)

const sample = `
hades:
  url: https://hades.example.com
  auth_key: ${TEST_AUTH_KEY}
server:
  addr: ":8080"
  external_url: https://momos.example.com
forges:
  - id: github-main
    type: github
    api: https://api.github.com
    webhook_secret: ${TEST_WH}
defaults:
  priority: 3
  timeout: 15m
  limits:
    max_changed_files: 200
    max_diff_bytes: 400000
    max_cost_usd: 1.0
  reviewer:
    strategy: oneshot
    model: gpt-4o
    base_url: https://api.openai.com/v1
  publish:
    mode: pr_review
    inline_comments: true
repositories:
  - match: "ls1intum/hades"
    prompt: prompts/go.md
    reviewer:
      strategy: agentic
      model: gpt-4o-mini
  - match: "ls1intum/*"
    prompt: prompts/org.md
`

func TestParseEnvSubstitutionAndResolve(t *testing.T) {
	t.Setenv("TEST_AUTH_KEY", "secret123")
	t.Setenv("TEST_WH", "whsecret")
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Hades.AuthKey != "secret123" {
		t.Fatalf("env substitution failed: %q", c.Hades.AuthKey)
	}
	if c.Defaults.Timeout.Std() != 15*time.Minute {
		t.Fatalf("duration parse failed: %v", c.Defaults.Timeout.Std())
	}

	// Exact match wins and overrides strategy/model.
	p := c.Resolve("ls1intum/hades")
	if p.Reviewer.Strategy != "agentic" || p.Reviewer.Model != "gpt-4o-mini" {
		t.Fatalf("override not applied: %+v", p.Reviewer)
	}
	if p.Reviewer.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("inherited base_url lost: %q", p.Reviewer.BaseURL)
	}
	if p.Prompt != "prompts/go.md" {
		t.Fatalf("prompt override wrong: %q", p.Prompt)
	}

	// Glob match.
	p2 := c.Resolve("ls1intum/other")
	if p2.Prompt != "prompts/org.md" || p2.Reviewer.Strategy != "oneshot" {
		t.Fatalf("glob resolve wrong: %+v", p2)
	}

	// No match: defaults only.
	p3 := c.Resolve("someone/else")
	if p3.Reviewer.Strategy != "oneshot" || p3.Prompt != "" {
		t.Fatalf("default resolve wrong: %+v", p3)
	}
}

func TestMatchRepo(t *testing.T) {
	cases := []struct {
		pattern, repo string
		want          bool
	}{
		{"*", "a/b", true},
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"ls1intum/*", "ls1intum/hades", true},
		{"ls1intum/*", "other/hades", false},
	}
	for _, tc := range cases {
		if got := matchRepo(tc.pattern, tc.repo); got != tc.want {
			t.Errorf("matchRepo(%q,%q)=%v want %v", tc.pattern, tc.repo, got, tc.want)
		}
	}
}

func TestEnvDefaultSubstitution(t *testing.T) {
	t.Setenv("TEST_AUTH_KEY", "k")
	t.Setenv("TEST_WH", "w")
	t.Setenv("HADES_URL", "") // unset/empty -> use default
	c, err := Parse([]byte(sample + "\n# note\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_ = c
	// Direct check of the substitution helper via a tiny doc.
	doc := []byte("hades:\n  url: ${MISSING:-http://fallback}\n  auth_key: x\nserver:\n  external_url: e\nforges: [{id: a, type: github}]")
	c2, err := Parse(doc)
	if err != nil {
		t.Fatalf("parse default doc: %v", err)
	}
	if c2.Hades.URL != "http://fallback" {
		t.Fatalf("default substitution failed: %q", c2.Hades.URL)
	}
}

func TestValidateRequiresFields(t *testing.T) {
	_, err := Parse([]byte("server:\n  external_url: x\nforges: [{id: a, type: github}]"))
	if err == nil {
		t.Fatal("expected error for missing hades.url")
	}
}
