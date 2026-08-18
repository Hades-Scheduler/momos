package token

import (
	"testing"
	"time"
)

func TestMintVerifyRoundtrip(t *testing.T) {
	m := NewMinter([]byte("s3cr3t"))
	tok := m.Mint("run_abc", StepPublish, ScopeWrite, time.Hour)
	claims, err := m.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.RunID != "run_abc" || claims.Step != StepPublish || claims.Scope != ScopeWrite {
		t.Fatalf("claims wrong: %+v", claims)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	tok := NewMinter([]byte("a")).Mint("run", StepClone, ScopeRead, time.Hour)
	if _, err := NewMinter([]byte("b")).Verify(tok); err == nil {
		t.Fatal("expected signature failure across secrets")
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	m := NewMinter([]byte("s"))
	tok := m.Mint("run", StepClone, ScopeRead, time.Hour)
	if _, err := m.Verify(tok + "x"); err == nil {
		t.Fatal("expected tamper failure")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	m := NewMinter([]byte("s"))
	tok := m.Mint("run", StepClone, ScopeRead, -time.Second)
	if _, err := m.Verify(tok); err == nil {
		t.Fatal("expected expiry failure")
	}
}

// Verification is a pure function (idempotent) so publisher retries never race
// a burn (plan.md §12.3): verifying twice must both succeed.
func TestVerifyIsIdempotent(t *testing.T) {
	m := NewMinter([]byte("s"))
	tok := m.Mint("run", StepPublish, ScopeWrite, time.Hour)
	if _, err := m.Verify(tok); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := m.Verify(tok); err != nil {
		t.Fatalf("second verify should also pass: %v", err)
	}
}
