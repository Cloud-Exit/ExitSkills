package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
	_ "modernc.org/sqlite"
)

//go:embed sqlite_schema.sql
var sqliteMigrations embed.FS

type SQLite struct{ db *sql.DB }

func OpenSQLite(ctx context.Context, databaseURL string) (*SQLite, error) {
	dsn, filePath, err := normalizeSQLiteURL(databaseURL)
	if err != nil {
		return nil, err
	}
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return nil, fmt.Errorf("create SQLite directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	// A single connection gives in-memory databases stable semantics and avoids
	// lock churn for the small local catalog workload.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect sqlite: %w", err)
	}
	if filePath != "" {
		if err := os.Chmod(filePath, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure SQLite database permissions: %w", err)
		}
	}
	for _, pragma := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return &SQLite{db: db}, nil
}

func normalizeSQLiteURL(databaseURL string) (dsn string, filePath string, err error) {
	raw := strings.TrimSpace(databaseURL)
	switch {
	case strings.HasPrefix(raw, "sqlite://"):
		raw = strings.TrimPrefix(raw, "sqlite://")
	case strings.HasPrefix(raw, "sqlite:"):
		raw = strings.TrimPrefix(raw, "sqlite:")
	case strings.HasPrefix(raw, "file:"):
		// Already a database/sql SQLite URI.
	default:
		return "", "", fmt.Errorf("invalid SQLite URL %q", databaseURL)
	}
	if raw == "" {
		return "", "", errors.New("SQLite database path is empty")
	}
	if raw == ":memory:" {
		name, err := randomID("exitmesh_mem_")
		if err != nil {
			return "", "", err
		}
		return "file:" + name + "?mode=memory&cache=shared", "", nil
	}
	if strings.HasPrefix(raw, "file:") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("parse SQLite URL: %w", err)
		}
		if parsed.Query().Get("mode") == "memory" {
			return raw, "", nil
		}
		path, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, "//"))
		if err != nil {
			return "", "", fmt.Errorf("parse SQLite path: %w", err)
		}
		if path == "" {
			path = parsed.Opaque
		}
		return raw, path, nil
	}
	pathPart := raw
	if query := strings.IndexByte(pathPart, '?'); query >= 0 {
		pathPart = pathPart[:query]
	}
	decoded, err := url.PathUnescape(pathPart)
	if err != nil {
		return "", "", fmt.Errorf("parse SQLite path: %w", err)
	}
	return "file:" + raw, decoded, nil
}

func (s *SQLite) Close()                         { _ = s.db.Close() }
func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *SQLite) Migrate(ctx context.Context) error {
	schema, err := sqliteMigrations.ReadFile("sqlite_schema.sql")
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, string(schema)); err != nil {
		return err
	}
	return s.ensureAssessmentColumns(ctx)
}

func (s *SQLite) ensureAssessmentColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(skills)`)
	if err != nil {
		return err
	}
	qualityFound, checkedFound := false, false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		qualityFound = qualityFound || name == "quality_score"
		checkedFound = checkedFound || name == "llm_checked"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !qualityFound {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE skills ADD COLUMN quality_score INTEGER CHECK (quality_score BETWEEN 5 AND 10)`); err != nil {
			return fmt.Errorf("add SQLite quality score: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE skills SET quality_score=5 WHERE quality_score IS NULL`); err != nil {
		return fmt.Errorf("initialize SQLite quality score: %w", err)
	}
	if !checkedFound {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE skills ADD COLUMN llm_checked INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add SQLite LLM status: %w", err)
		}
	}
	return nil
}

func (s *SQLite) UpsertSkill(ctx context.Context, skill model.Skill) error {
	files, err := json.Marshal(skill.Files)
	if err != nil {
		return err
	}
	categories, err := json.Marshal(skill.Audit.Categories)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO skills (`+columns+`) VALUES
(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET slug=excluded.slug,name=excluded.name,description=excluded.description,
source=excluded.source,installs=excluded.installs,stars=excluded.stars,source_type=excluded.source_type,
install_url=excluded.install_url,public_url=excluded.public_url,is_duplicate=excluded.is_duplicate,
security_score=excluded.security_score,quality_score=excluded.quality_score,llm_checked=excluded.llm_checked,official=excluded.official,content_hash=excluded.content_hash,
files=excluded.files,audit_provider=excluded.audit_provider,audit_slug=excluded.audit_slug,
audit_status=excluded.audit_status,audit_summary=excluded.audit_summary,audit_risk_level=excluded.audit_risk_level,
audit_categories=excluded.audit_categories,audited_at=excluded.audited_at,updated_at=excluded.updated_at`,
		skill.ID, skill.Slug, skill.Name, skill.Description, skill.Source, skill.Installs, skill.Stars, skill.SourceType,
		skill.InstallURL, skill.URL, skill.IsDuplicate, skill.SecurityScore, skill.QualityScore, skill.LLMChecked, skill.Official, skill.Hash, files,
		skill.Audit.Provider, skill.Audit.Slug, skill.Audit.Status, skill.Audit.Summary, skill.Audit.RiskLevel,
		categories, skill.Audit.AuditedAt, skill.UpdatedAt)
	return err
}

func (s *SQLite) DeleteSkill(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE id=?`, id)
	return err
}

func (s *SQLite) ListSkills(ctx context.Context, options model.ListOptions) ([]model.Skill, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM skills WHERE quality_score IS NOT NULL AND (?=0 OR llm_checked=1)`, options.LLMCheckedOnly).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM skills WHERE quality_score IS NOT NULL AND (?=0 OR llm_checked=1) ORDER BY stars DESC, id ASC LIMIT ? OFFSET ?`, options.LLMCheckedOnly, options.PerPage, options.Page*options.PerPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	skills, err := scanSQLiteSkills(rows)
	return skills, total, err
}

func (s *SQLite) SearchSkills(ctx context.Context, query, owner string, limit int, llmCheckedOnly bool) ([]model.Skill, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM skills WHERE quality_score IS NOT NULL AND (?=0 OR llm_checked=1) AND
(lower(name) LIKE lower(?) OR lower(source) LIKE lower(?) OR lower(description) LIKE lower(?))
AND (?='' OR source LIKE ?||'/%')
ORDER BY CASE WHEN lower(name)=lower(?) THEN 0 WHEN lower(name) LIKE lower(?)||'%' THEN 1 ELSE 2 END,
stars DESC LIMIT ?`, llmCheckedOnly, pattern, pattern, pattern, owner, owner, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteSkills(rows)
}

func (s *SQLite) GetSkill(ctx context.Context, id string, llmCheckedOnly bool) (model.Skill, error) {
	skill, err := scanSkill(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM skills WHERE id=? AND quality_score IS NOT NULL AND (?=0 OR llm_checked=1)`, id, llmCheckedOnly))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Skill{}, model.ErrNotFound
	}
	return skill, err
}

func (s *SQLite) FreshSkillIDs(ctx context.Context, cutoff time.Time) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM skills WHERE quality_score IS NOT NULL AND updated_at>?`, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}
func (s *SQLite) UnassessedSkills(ctx context.Context) ([]model.Skill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM skills WHERE llm_checked=0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteSkills(rows)
}

func (s *SQLite) CuratedSkills(ctx context.Context, llmCheckedOnly bool) ([]model.Skill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM skills WHERE official=1 AND quality_score IS NOT NULL AND (?=0 OR llm_checked=1) ORDER BY source, stars DESC, id`, llmCheckedOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteSkills(rows)
}

func scanSQLiteSkills(rows *sql.Rows) ([]model.Skill, error) {
	skills := make([]model.Skill, 0)
	for rows.Next() {
		skill, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, rows.Err()
}

func (s *SQLite) CreateAPIKey(ctx context.Context, record auth.KeyRecord) (string, error) {
	id, err := randomID("key_")
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO api_keys (id,name,token_hash,encrypted_token,expires_at) VALUES (?,?,?,?,?)`, id, record.Name, record.TokenHash, record.EncryptedToken, record.ExpiresAt)
	return id, err
}

func (s *SQLite) RevokeAPIKey(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().UTC(), id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *SQLite) LookupAPIKey(ctx context.Context, hash []byte) (string, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, hash, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, now, id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *SQLite) RecordSkillDownload(ctx context.Context, skillID, keyID string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO skill_downloads (skill_id,api_key_id,downloads,first_downloaded_at,last_downloaded_at)
VALUES (?,?,1,?,?) ON CONFLICT (skill_id,api_key_id) DO UPDATE SET
downloads=skill_downloads.downloads+1,last_downloaded_at=excluded.last_downloaded_at`, skillID, keyID, now, now)
	return err
}

func (s *SQLite) AdminStats(ctx context.Context) (model.AdminStats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,source,slug,security_score,quality_score,llm_checked FROM skills WHERE quality_score IS NOT NULL ORDER BY id`)
	if err != nil {
		return model.AdminStats{}, err
	}
	stats, positions, err := scanSkillStats(rows)
	_ = rows.Close()
	if err != nil {
		return model.AdminStats{}, err
	}
	clientRows, err := s.db.QueryContext(ctx, `SELECT d.skill_id,k.id,k.name,d.downloads,d.first_downloaded_at,d.last_downloaded_at
FROM skill_downloads d JOIN api_keys k ON k.id=d.api_key_id ORDER BY d.skill_id,d.downloads DESC,k.id`)
	if err != nil {
		return model.AdminStats{}, err
	}
	defer clientRows.Close()
	uniqueClients := make(map[string]struct{})
	for clientRows.Next() {
		var skillID string
		var client model.ClientStats
		if err := clientRows.Scan(&skillID, &client.ID, &client.Name, &client.Downloads, &client.FirstDownloadedAt, &client.LastDownloadedAt); err != nil {
			return model.AdminStats{}, err
		}
		addClientStats(&stats, positions, uniqueClients, skillID, client)
	}
	if err := clientRows.Err(); err != nil {
		return model.AdminStats{}, err
	}
	finishAdminStats(&stats, uniqueClients)
	return stats, nil
}

func randomID(prefix string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

var _ Backend = (*SQLite)(nil)
var _ Backend = (*Postgres)(nil)
