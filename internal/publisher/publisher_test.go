package publisher

import (
	"strings"
	"testing"

	"github.com/Hades-Scheduler/momos/internal/forge"
	"github.com/Hades-Scheduler/momos/internal/review"
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
	// Every inline comment carries the invisible marker so a later run can
	// recognize and drop Momos's own threads before feeding the reviewer.
	if !strings.Contains(c.Body, forge.MomosInlineMarker) {
		t.Fatalf("inline comment must carry the Momos marker: %q", c.Body)
	}
}

func rev(verdict review.Verdict, sev ...review.Severity) *review.Review {
	r := &review.Review{Verdict: verdict}
	for i, s := range sev {
		r.Findings = append(r.Findings, review.Finding{File: "a.go", Line: i + 1, Severity: s, Message: "x"})
	}
	return r
}

func TestReviewEventOffIsAlwaysComment(t *testing.T) {
	c := &Config{Approvals: false}
	if got := c.reviewEvent(rev(review.VerdictApprove)); got != "COMMENT" {
		t.Fatalf("approvals off must stay COMMENT, got %s", got)
	}
	if got := c.reviewEvent(rev(review.VerdictRequestChanges, review.SeverityCritical)); got != "COMMENT" {
		t.Fatalf("approvals off must stay COMMENT, got %s", got)
	}
}

func TestReviewEventVerdictMapping(t *testing.T) {
	c := &Config{Approvals: true}
	cases := []struct {
		name string
		rev  *review.Review
		want string
	}{
		{"clean approve", rev(review.VerdictApprove), "APPROVE"},
		{"approve with only minor findings", rev(review.VerdictApprove, review.SeverityMinor), "APPROVE"},
		{"approve over a major finding is forced to request_changes",
			rev(review.VerdictApprove, review.SeverityMajor), "REQUEST_CHANGES"},
		{"explicit request_changes", rev(review.VerdictRequestChanges), "REQUEST_CHANGES"},
		{"comment verdict, no blocking", rev(review.VerdictComment), "COMMENT"},
		{"critical finding forces request_changes even on comment verdict",
			rev(review.VerdictComment, review.SeverityCritical), "REQUEST_CHANGES"},
	}
	for _, tc := range cases {
		if got := c.reviewEvent(tc.rev); got != tc.want {
			t.Fatalf("%s: want %s, got %s", tc.name, tc.want, got)
		}
	}
}

func TestReviewEventNeverApprovesFork(t *testing.T) {
	c := &Config{Approvals: true, IsFork: true}
	if got := c.reviewEvent(rev(review.VerdictApprove)); got != "COMMENT" {
		t.Fatalf("fork approve must downgrade to COMMENT, got %s", got)
	}
	// A fork with a blocking finding may still request changes (only blocks).
	if got := c.reviewEvent(rev(review.VerdictApprove, review.SeverityMajor)); got != "REQUEST_CHANGES" {
		t.Fatalf("fork blocking finding should REQUEST_CHANGES, got %s", got)
	}
}
