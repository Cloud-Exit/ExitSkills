package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/exitmesh/skills/internal/model"
)

type countingHashLookup struct {
	calls int
	id    string
}

func (lookup *countingHashLookup) LookupAPIKey(context.Context, []byte) (string, error) {
	lookup.calls++
	if lookup.id == "" {
		return "", model.ErrUnauthorized
	}
	return lookup.id, nil
}

func TestVerifierRejectsMalformedTokensWithoutDatabaseLookup(t *testing.T) {
	manager, err := NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	lookup := &countingHashLookup{id: "key_valid"}
	verifier := NewVerifier(manager, lookup)
	for _, token := range []string{"", "em_sk_x", "wrong_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", "em_sk_!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, model.ErrUnauthorized) {
			t.Fatalf("Verify(%q) error = %v, want unauthorized", token, err)
		}
	}
	if lookup.calls != 0 {
		t.Fatalf("malformed tokens caused %d database lookups", lookup.calls)
	}
}

func TestVerifierAcceptsGeneratedTokenShape(t *testing.T) {
	manager, err := NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Generate("test", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	lookup := &countingHashLookup{id: "key_valid"}
	id, err := NewVerifier(manager, lookup).Verify(context.Background(), token)
	if err != nil || id != "key_valid" || lookup.calls != 1 {
		t.Fatalf("Verify() = %q, %v; calls=%d", id, err, lookup.calls)
	}
}
