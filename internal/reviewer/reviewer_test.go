package reviewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hades-Scheduler/momos/internal/diff"
	"github.com/Hades-Scheduler/momos/internal/llm"
	"github.com/Hades-Scheduler/momos/internal/review"
)

// stubLLM returns a fixed chat completion so we can exercise the oneshot path
// end to end (llm client -> parse -> validate) without a real model.
func stubLLM(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + jsonString(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
}

func TestOneshotParsesReview(t *testing.T) {
	content := `{"verdict":"comment","summary":"looks ok","findings":[{"file":"foo.go","line":3,"severity":"major","category":"correctness","message":"nil deref"}]}`
	srv := stubLLM(t, content)
	defer srv.Close()

	c := &Config{Model: "test", MaxOutputTokens: 1000, InputPrice: 1, OutputPrice: 2}
	client := llm.New(srv.URL, "")
	rev, usage, err := c.oneshot(context.Background(), client, "unified diff here")
	if err != nil {
		t.Fatalf("oneshot: %v", err)
	}
	if rev.Verdict != review.VerdictComment || len(rev.Findings) != 1 {
		t.Fatalf("bad review: %+v", rev)
	}
	if usage.PromptTokens != 100 || usage.CompletionTokens != 50 {
		t.Fatalf("usage not captured: %+v", usage)
	}
	if got := c.cost(usage); got == 0 {
		t.Fatalf("cost should be non-zero: %v", got)
	}
}

func TestClassifySplitsByDiff(t *testing.T) {
	d := diff.Parse("--- a/foo.go\n+++ b/foo.go\n@@ -1,1 +1,2 @@\n old\n+new line\n")
	findings := []review.Finding{
		{File: "foo.go", Line: 2, Severity: review.SeverityMinor, Message: "on added line"},
		{File: "foo.go", Line: 1, Severity: review.SeverityMinor, Message: "on context line"},
		{File: "other.go", Line: 9, Severity: review.SeverityMinor, Message: "not in diff"},
	}
	inline, summary := classify(findings, "base summary", d)
	if len(inline) != 1 || inline[0].Line != 2 {
		t.Fatalf("expected only the added-line finding inline, got %+v", inline)
	}
	if !contains(summary, "context line") || !contains(summary, "not in diff") {
		t.Fatalf("out-of-diff findings not folded into summary: %q", summary)
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	if _, err := safePath("../../etc/passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := safePath("src/main.go"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// jsonString quotes a string as a JSON literal.
func jsonString(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, string(r)...)
		}
	}
	b = append(b, '"')
	return string(b)
}
