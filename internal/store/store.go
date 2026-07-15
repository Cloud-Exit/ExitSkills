package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
)

// Backend is the complete persistence contract used by the server and admin
// binaries. PostgreSQL and SQLite intentionally implement the same behavior.
type Backend interface {
	Close()
	Ping(context.Context) error
	Migrate(context.Context) error
	UpsertSkill(context.Context, model.Skill) error
	DeleteSkill(context.Context, string) error
	ListSkills(context.Context, model.ListOptions) ([]model.Skill, int, error)
	SearchSkills(context.Context, string, string, int) ([]model.Skill, error)
	GetSkill(context.Context, string) (model.Skill, error)
	FreshSkillIDs(context.Context, time.Time) (map[string]struct{}, error)
	CuratedSkills(context.Context) ([]model.Skill, error)
	CreateAPIKey(context.Context, auth.KeyRecord) (string, error)
	RevokeAPIKey(context.Context, string) (bool, error)
	LookupAPIKey(context.Context, []byte) (string, error)
	RecordSkillDownload(context.Context, string, string) error
	AdminStats(context.Context) (model.AdminStats, error)
}

// Open selects SQLite for sqlite: and file: URLs and PostgreSQL for postgres
// URLs or pgx keyword/value connection strings.
func Open(ctx context.Context, databaseURL string) (Backend, error) {
	value := strings.TrimSpace(databaseURL)
	switch {
	case strings.HasPrefix(value, "sqlite:"), strings.HasPrefix(value, "file:"):
		return OpenSQLite(ctx, value)
	case strings.HasPrefix(value, "postgres://"), strings.HasPrefix(value, "postgresql://"):
		return OpenPostgres(ctx, value)
	case strings.Contains(value, "://"):
		return nil, fmt.Errorf("unsupported database URL scheme")
	case value == "":
		return nil, fmt.Errorf("database URL is empty")
	default:
		return OpenPostgres(ctx, value)
	}
}
