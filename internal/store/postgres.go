package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var migrations embed.FS

type Postgres struct{ pool *pgxpool.Pool }

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}
func (p *Postgres) Close()                         { p.pool.Close() }
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Postgres) Migrate(ctx context.Context) error {
	sql, err := migrations.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, string(sql))
	return err
}

const columns = `id, slug, name, description, source, installs, stars, source_type, install_url, public_url,
 is_duplicate, security_score, official, content_hash, files, audit_provider, audit_slug, audit_status,
 audit_summary, audit_risk_level, audit_categories, audited_at, updated_at`

func (p *Postgres) UpsertSkill(ctx context.Context, skill model.Skill) error {
	files, err := json.Marshal(skill.Files)
	if err != nil {
		return err
	}
	categories, err := json.Marshal(skill.Audit.Categories)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO skills (`+columns+`) VALUES
($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
ON CONFLICT (id) DO UPDATE SET slug=EXCLUDED.slug,name=EXCLUDED.name,description=EXCLUDED.description,
source=EXCLUDED.source,installs=EXCLUDED.installs,stars=EXCLUDED.stars,source_type=EXCLUDED.source_type,
install_url=EXCLUDED.install_url,public_url=EXCLUDED.public_url,is_duplicate=EXCLUDED.is_duplicate,
security_score=EXCLUDED.security_score,official=EXCLUDED.official,content_hash=EXCLUDED.content_hash,
files=EXCLUDED.files,audit_provider=EXCLUDED.audit_provider,audit_slug=EXCLUDED.audit_slug,
audit_status=EXCLUDED.audit_status,audit_summary=EXCLUDED.audit_summary,audit_risk_level=EXCLUDED.audit_risk_level,
audit_categories=EXCLUDED.audit_categories,audited_at=EXCLUDED.audited_at,updated_at=EXCLUDED.updated_at`,
		skill.ID, skill.Slug, skill.Name, skill.Description, skill.Source, skill.Installs, skill.Stars, skill.SourceType,
		skill.InstallURL, skill.URL, skill.IsDuplicate, skill.SecurityScore, skill.Official, skill.Hash, files,
		skill.Audit.Provider, skill.Audit.Slug, skill.Audit.Status, skill.Audit.Summary, skill.Audit.RiskLevel,
		categories, skill.Audit.AuditedAt, skill.UpdatedAt)
	return err
}

func (p *Postgres) DeleteSkill(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM skills WHERE id=$1`, id)
	return err
}

func (p *Postgres) ListSkills(ctx context.Context, options model.ListOptions) ([]model.Skill, int, error) {
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM skills`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM skills ORDER BY stars DESC, id ASC LIMIT $1 OFFSET $2`, options.PerPage, options.Page*options.PerPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	skills, err := scanPostgresSkills(rows)
	return skills, total, err
}

func (p *Postgres) SearchSkills(ctx context.Context, query, owner string, limit int) ([]model.Skill, error) {
	pattern := "%" + query + "%"
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM skills WHERE
(name ILIKE $1 OR source ILIKE $1 OR description ILIKE $1) AND ($2='' OR split_part(source,'/',1)=$2)
ORDER BY CASE WHEN lower(name)=lower($3) THEN 0 WHEN name ILIKE $3||'%' THEN 1 ELSE 2 END, stars DESC LIMIT $4`, pattern, owner, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPostgresSkills(rows)
}
func (p *Postgres) GetSkill(ctx context.Context, id string) (model.Skill, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+columns+` FROM skills WHERE id=$1`, id)
	skill, err := scanSkill(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Skill{}, model.ErrNotFound
	}
	return skill, err
}
func (p *Postgres) FreshSkillIDs(ctx context.Context, cutoff time.Time) (map[string]struct{}, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM skills WHERE updated_at>$1`, cutoff.UTC())
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
func (p *Postgres) CuratedSkills(ctx context.Context) ([]model.Skill, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM skills WHERE official=TRUE ORDER BY source, stars DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPostgresSkills(rows)
}

type scanner interface{ Scan(...any) error }

func scanSkill(row scanner) (model.Skill, error) {
	var skill model.Skill
	var files, categories []byte
	err := row.Scan(&skill.ID, &skill.Slug, &skill.Name, &skill.Description, &skill.Source, &skill.Installs, &skill.Stars, &skill.SourceType, &skill.InstallURL, &skill.URL, &skill.IsDuplicate, &skill.SecurityScore, &skill.Official, &skill.Hash, &files, &skill.Audit.Provider, &skill.Audit.Slug, &skill.Audit.Status, &skill.Audit.Summary, &skill.Audit.RiskLevel, &categories, &skill.Audit.AuditedAt, &skill.UpdatedAt)
	if err != nil {
		return model.Skill{}, err
	}
	if err := json.Unmarshal(files, &skill.Files); err != nil {
		return model.Skill{}, err
	}
	if err := json.Unmarshal(categories, &skill.Audit.Categories); err != nil {
		return model.Skill{}, err
	}
	return skill, nil
}
func scanPostgresSkills(rows pgx.Rows) ([]model.Skill, error) {
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

func (p *Postgres) CreateAPIKey(ctx context.Context, record auth.KeyRecord) (string, error) {
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := "key_" + hex.EncodeToString(idBytes)
	_, err := p.pool.Exec(ctx, `INSERT INTO api_keys (id,name,token_hash,encrypted_token,expires_at) VALUES ($1,$2,$3,$4,$5)`, id, record.Name, record.TokenHash, record.EncryptedToken, record.ExpiresAt)
	return id, err
}
func (p *Postgres) RevokeAPIKey(ctx context.Context, id string) (bool, error) {
	result, err := p.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=NOW() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, err
}
func (p *Postgres) LookupAPIKey(ctx context.Context, hash []byte) (string, error) {
	var id string
	err := p.pool.QueryRow(ctx, `UPDATE api_keys SET last_used_at=NOW() WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>NOW() RETURNING id`, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", model.ErrUnauthorized
	}
	return id, err
}

func (p *Postgres) RecordSkillDownload(ctx context.Context, skillID, keyID string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO skill_downloads (skill_id,api_key_id,downloads,first_downloaded_at,last_downloaded_at)
VALUES ($1,$2,1,NOW(),NOW()) ON CONFLICT (skill_id,api_key_id) DO UPDATE SET
downloads=skill_downloads.downloads+1,last_downloaded_at=EXCLUDED.last_downloaded_at`, skillID, keyID)
	return err
}

func (p *Postgres) AdminStats(ctx context.Context) (model.AdminStats, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,name,source,slug,security_score FROM skills ORDER BY id`)
	if err != nil {
		return model.AdminStats{}, err
	}
	stats, positions, err := scanSkillStats(rows)
	rows.Close()
	if err != nil {
		return model.AdminStats{}, err
	}
	clientRows, err := p.pool.Query(ctx, `SELECT d.skill_id,k.id,k.name,d.downloads,d.first_downloaded_at,d.last_downloaded_at
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
