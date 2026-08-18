package review

import "testing"

func TestParseValid(t *testing.T) {
	raw := `{"schema_version":"1.0","verdict":"comment","summary":"ok","findings":[{"file":"a.go","line":5,"severity":"major","category":"correctness","message":"bug"}]}`
	r, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Verdict != VerdictComment || len(r.Findings) != 1 || r.Findings[0].Line != 5 {
		t.Fatalf("unexpected parse result: %+v", r)
	}
}

func TestParseRepairsFencesAndProse(t *testing.T) {
	raw := "Here is your review:\n```json\n{\"verdict\":\"comment\",\"summary\":\"s\",\"findings\":[]}\n```\nThanks!"
	r, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if r.Summary != "s" {
		t.Fatalf("got summary %q", r.Summary)
	}
}

func TestParseRepairsTrailingCommas(t *testing.T) {
	raw := `{"verdict":"comment","summary":"s","findings":[{"file":"a","line":1,"severity":"info","category":"c","message":"m",},],}`
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatalf("trailing-comma repair failed: %v", err)
	}
}

func TestParseTrailingCommaInsideStringPreserved(t *testing.T) {
	raw := `{"verdict":"comment","summary":"a, b, c","findings":[]}`
	r, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Summary != "a, b, c" {
		t.Fatalf("string content damaged: %q", r.Summary)
	}
}

func TestValidateRejectsBadVerdict(t *testing.T) {
	_, err := Parse([]byte(`{"verdict":"lgtm","summary":"s","findings":[]}`))
	if err == nil {
		t.Fatal("expected error for invalid verdict")
	}
}

func TestValidateRejectsEmpty(t *testing.T) {
	_, err := Parse([]byte(`{"verdict":"comment","summary":"","findings":[]}`))
	if err == nil {
		t.Fatal("expected error for empty review")
	}
}

func TestValidateDefaultsSeverity(t *testing.T) {
	r := &Review{Verdict: VerdictComment, Summary: "s", Findings: []Finding{{File: "a", Line: 1, Category: "c", Message: "m"}}}
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if r.Findings[0].Severity != SeverityInfo {
		t.Fatalf("expected default severity info, got %q", r.Findings[0].Severity)
	}
}
