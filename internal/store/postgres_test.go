package store

import (
	"errors"
	"testing"

	"github.com/exitmesh/skills/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresClassifiesUnsupportedUnicodeAsInvalidSkillContents(t *testing.T) {
	err := classifyPostgresSkillError(&pgconn.PgError{Code: "22P05", Message: "unsupported Unicode escape sequence"})
	if !errors.Is(err, model.ErrInvalidSkillContents) {
		t.Fatalf("classified error = %v, want ErrInvalidSkillContents", err)
	}
}
