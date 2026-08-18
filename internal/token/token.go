// Package token mints and verifies the Momos-scoped bootstrap tokens carried in
// the clone and publish step metadata (plan.md §11.5). The review step gets no
// bootstrap token, so its inability to obtain a forge token is structural.
//
// Tokens are HMAC-signed and time-bound, binding (run_id, step, scope). They
// are NOT single-use: verification is a pure function of the token and the
// signing key, so the publisher's callback/token-fetch retries never race a
// "burn" (plan.md §12.3 — idempotent per run_id).
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Step identifies which step a bootstrap token authorizes.
type Step string

const (
	StepClone   Step = "clone"
	StepPublish Step = "publish"
)

// Scope is the forge permission the token can be exchanged for.
type Scope string

const (
	ScopeRead  Scope = "read"  // contents:read (clone)
	ScopeWrite Scope = "write" // pull_requests:write + checks:write (publish)
)

// Claims are the verified contents of a bootstrap token.
type Claims struct {
	RunID string
	Step  Step
	Scope Scope
	Exp   time.Time
}

// Minter mints and verifies bootstrap tokens with a shared secret.
type Minter struct {
	secret []byte
}

// NewMinter creates a Minter. The secret must be kept server-side; it never
// leaves Momos.
func NewMinter(secret []byte) *Minter {
	return &Minter{secret: secret}
}

// Mint produces a signed bootstrap token valid for ttl.
func (m *Minter) Mint(runID string, step Step, scope Scope, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := strings.Join([]string{runID, string(step), string(scope), strconv.FormatInt(exp, 10)}, "|")
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	sig := m.sign(enc)
	return enc + "." + sig
}

// Verify checks a token's signature and expiry and returns its claims.
func (m *Minter) Verify(tok string) (*Claims, error) {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed token")
	}
	enc, sig := parts[0], parts[1]
	expected := m.sign(enc)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return nil, errors.New("bad token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, errors.New("bad token encoding")
	}
	fields := strings.Split(string(raw), "|")
	if len(fields) != 4 {
		return nil, errors.New("bad token payload")
	}
	expUnix, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return nil, errors.New("bad token expiry")
	}
	exp := time.Unix(expUnix, 0)
	if time.Now().After(exp) {
		return nil, fmt.Errorf("token expired at %s", exp.Format(time.RFC3339))
	}
	return &Claims{
		RunID: fields[0],
		Step:  Step(fields[1]),
		Scope: Scope(fields[2]),
		Exp:   exp,
	}, nil
}

func (m *Minter) sign(enc string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
