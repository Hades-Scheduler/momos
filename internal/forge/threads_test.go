package forge

import (
	"strings"
	"testing"
)

// A GraphQL response exercising the nullable fields agy flagged: an outdated
// thread with null line, and a comment whose author is null (deleted user).
const threadsJSON = `{
  "data": { "repository": { "pullRequest": { "reviewThreads": { "nodes": [
    {
      "isResolved": true, "isOutdated": false, "path": "a.go", "line": 12,
      "comments": { "nodes": [
        {"body": "please guard the nil deref", "author": {"login": "alice"}},
        {"body": "done, thanks", "author": {"login": "bob"}}
      ]}
    },
    {
      "isResolved": false, "isOutdated": true, "path": "b.go", "line": null,
      "comments": { "nodes": [
        {"body": "leftover from a deleted account", "author": null}
      ]}
    },
    {
      "isResolved": false, "isOutdated": false, "path": "c.go", "line": 3,
      "comments": { "nodes": [
        {"body": "**major** (correctness): off-by-one\n` + MomosInlineMarker + `", "author": {"login": "momos-reviewer[bot]"}}
      ]}
    }
  ]}}}}
}`

func TestParseReviewThreadsNullableFields(t *testing.T) {
	threads, err := parseReviewThreads([]byte(threadsJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(threads) != 3 {
		t.Fatalf("want 3 threads, got %d", len(threads))
	}
	if !threads[0].IsResolved || threads[0].Line == nil || *threads[0].Line != 12 {
		t.Fatalf("thread 0 wrong: %+v", threads[0])
	}
	// Outdated thread: null line stays nil, null author becomes "unknown".
	if !threads[1].IsOutdated || threads[1].Line != nil {
		t.Fatalf("thread 1 should be outdated with nil line: %+v", threads[1])
	}
	if threads[1].Comments[0].Author != "unknown" {
		t.Fatalf("null author should render as unknown, got %q", threads[1].Comments[0].Author)
	}
}

func TestParseReviewThreadsGraphQLError(t *testing.T) {
	_, err := parseReviewThreads([]byte(`{"errors":[{"message":"Bad credentials"}]}`))
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("want graphql error surfaced, got %v", err)
	}
}

func TestFilterOutMomosThreads(t *testing.T) {
	threads, _ := parseReviewThreads([]byte(threadsJSON))
	human := FilterOutMomosThreads(threads)
	if len(human) != 2 {
		t.Fatalf("want 2 human threads (Momos's own dropped), got %d", len(human))
	}
	for _, tr := range human {
		for _, c := range tr.Comments {
			if strings.Contains(c.Body, MomosInlineMarker) {
				t.Fatal("a Momos-authored thread survived the filter")
			}
		}
	}
}

func TestRenderReviewThreads(t *testing.T) {
	threads, _ := parseReviewThreads([]byte(threadsJSON))
	out := RenderReviewThreads(FilterOutMomosThreads(threads))
	if !strings.Contains(out, "a.go:12 [resolved]") {
		t.Fatalf("resolved thread not rendered with location/state:\n%s", out)
	}
	if !strings.Contains(out, "b.go [outdated]") {
		t.Fatalf("outdated thread (null line) not rendered without a line:\n%s", out)
	}
	if strings.Contains(out, MomosInlineMarker) {
		t.Fatal("rendered block leaked Momos's own thread")
	}
	if RenderReviewThreads(nil) != "" {
		t.Fatal("no threads should render to empty string")
	}
}

func TestRenderReviewThreadsCapsCommentBody(t *testing.T) {
	long := strings.Repeat("x", maxCommentChars+50)
	out := RenderReviewThreads([]ReviewThread{{
		Path: "a.go", Comments: []ThreadComment{{Author: "a", Body: long}},
	}})
	if strings.Contains(out, long) {
		t.Fatal("comment body should be truncated to the cap")
	}
	if !strings.Contains(out, "…") {
		t.Fatal("truncation marker missing")
	}
}

func TestGraphQLEndpoint(t *testing.T) {
	if got := NewGitHub("https://api.github.com", "", "").graphqlEndpoint(); got != "https://api.github.com/graphql" {
		t.Fatalf("github.com graphql endpoint wrong: %s", got)
	}
	if got := NewGitHub("https://ghes.example.com/api/v3", "", "").graphqlEndpoint(); got != "https://ghes.example.com/api/graphql" {
		t.Fatalf("GHES graphql endpoint wrong: %s", got)
	}
}
