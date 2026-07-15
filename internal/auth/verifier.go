package auth

import (
	"context"

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
	if len(token) < len("em_sk_")+1 {
		return "", model.ErrUnauthorized
	}
	return v.store.LookupAPIKey(ctx, v.keys.Hash(token))
}
