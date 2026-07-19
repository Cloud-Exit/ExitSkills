package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	SearchSkills(context.Context, string, string, int, bool) ([]model.Skill, error)
	GetSkill(context.Context, string, bool) (model.Skill, error)
	FreshSkillIDs(context.Context, time.Time) (map[string]struct{}, error)
	UnassessedSkillCount(context.Context) (int, error)
	UnassessedSkills(context.Context, int) ([]model.PendingSkillAssessment, error)
	UpdateSkillAssessment(context.Context, string, int, int, model.Audit, time.Time) error
	CuratedSkills(context.Context, bool) ([]model.Skill, error)
	CreateAPIKey(context.Context, auth.KeyRecord) (string, error)
	RevokeAPIKey(context.Context, string) (bool, error)
	LookupAPIKey(context.Context, []byte) (string, error)
	RecordSkillDownload(context.Context, string, string) error
	AdminStats(context.Context) (model.AdminStats, error)
}

func marshalSkillFiles(files []model.File) ([]byte, error) {
	for _, file := range files {
		if strings.IndexByte(file.Contents, 0) >= 0 {
			return nil, fmt.Errorf("%w: %s contains NUL", model.ErrInvalidSkillContents, file.Path)
		}
		if !utf8.ValidString(file.Contents) {
			return nil, fmt.Errorf("%w: %s is not UTF-8", model.ErrInvalidSkillContents, file.Path)
		}
	}
	return json.Marshal(files)
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
