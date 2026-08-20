package forge

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

// The App private key may be provided as a raw PEM or, so it survives ${ENV}
// substitution into inline YAML, as a base64-encoded PEM (plan.md §11.5).
func TestAppJWTAcceptsRawAndBase64Key(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	t.Run("raw pem", func(t *testing.T) {
		g := NewGitHubApp("", 123, 456, pemBytes)
		if _, err := g.appJWT(); err != nil {
			t.Fatalf("raw pem: %v", err)
		}
	})

	t.Run("base64 pem", func(t *testing.T) {
		b64 := base64.StdEncoding.EncodeToString(pemBytes)
		g := NewGitHubApp("", 123, 456, []byte(b64))
		if _, err := g.appJWT(); err != nil {
			t.Fatalf("base64 pem: %v", err)
		}
	})

	t.Run("garbage key fails", func(t *testing.T) {
		g := NewGitHubApp("", 123, 456, []byte("not-a-key"))
		if _, err := g.appJWT(); err == nil {
			t.Fatal("expected failure for a non-key")
		}
	})
}
