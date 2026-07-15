package auth

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/exitmesh/skills/internal/model"
)

type HashLookup interface {
	LookupAPIKey(context.Context, []byte) (string, error)
}
type Verifier struct {
	keys  *KeyManager
	store HashLookup
}

func NewVerifier(keys *KeyManager, store HashLookup) *Verifier {
	return &Verifier{keys: keys, store: store}
}
func (v *Verifier) Verify(ctx context.Context, token string) (string, error) {
	const prefix = "em_sk_"
	if !strings.HasPrefix(token, prefix) {
		return "", model.ErrUnauthorized
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, prefix))
	if err != nil || len(random) != 32 {
		return "", model.ErrUnauthorized
	}
	return v.store.LookupAPIKey(ctx, v.keys.Hash(token))
}
