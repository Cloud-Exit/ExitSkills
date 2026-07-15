package auth

import (
	"testing"
	"time"
)

func TestKeyManagerRoundTrip(t *testing.T) {
	m, err := NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	token, record, err := m.Generate("automation", expires)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 20 || token[:6] != "em_sk_" {
		t.Fatalf("unexpected token format %q", token)
	}
	if record.Name != "automation" || !record.ExpiresAt.Equal(expires) {
		t.Fatalf("unexpected record: %+v", record)
	}
	if !m.Matches(token, record.TokenHash) {
		t.Fatal("generated token does not match hash")
	}
	plaintext, err := m.Decrypt(record.EncryptedToken)
	if err != nil || plaintext != token {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
}

func TestKeyManagerUsesRandomTokens(t *testing.T) {
	m, _ := NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	a, _, _ := m.Generate("a", time.Now().Add(time.Hour))
	b, _, _ := m.Generate("b", time.Now().Add(time.Hour))
	if a == b {
		t.Fatal("Generate() returned duplicate tokens")
	}
}
