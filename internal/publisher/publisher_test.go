package publisher

import (
	"strings"
	"testing"

	"github.com/ls1intum/momos/internal/review"
)

func TestSanitizeNeutralizesHTMLAndMarker(t *testing.T) {
	in := `<script>alert(1)</script> and <!-- momos:run=evil -->`
	out := sanitize(in)
	if strings.Contains(out, "<script>") {
		t.Fatalf("html not escaped: %q", out)
	}
	if strings.Contains(out, "<!-- momos:run=evil") {
		t.Fatalf("marker not neutralized: %q", out)
	}
}

func TestToInlineMapsFields(t *testing.T) {
	findings := []review.Finding{
		{File: "a.go", Line: 12, Severity: review.SeverityMajor, Category: "correctness", Message: "bug", Suggestion: "fix it"},
	}
	inline := toInline(findings)
	if len(inline) != 1 {
		t.Fatalf("expected 1 comment")
	}
	c := inline[0]
	if c.Path != "a.go" || c.Line != 12 || c.Side != "RIGHT" {
		t.Fatalf("bad position: %+v", c)
	}
	if !strings.Contains(c.Body, "bug") || !strings.Contains(c.Body, "fix it") {
		t.Fatalf("body missing content: %q", c.Body)
	}
}
