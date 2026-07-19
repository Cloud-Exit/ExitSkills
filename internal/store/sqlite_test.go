package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
)

func TestSQLiteCreatesDatabaseFileAndMigratesIdempotently(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".sqlite-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "skills.db")
	db, err := Open(context.Background(), "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, ok := db.(*SQLite); !ok {
		t.Fatalf("Open() returned %T, want *SQLite", db)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate() failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("SQLite database was not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("SQLite database mode = %o, want 600", mode)
	}
}

func TestSQLiteMigrationAddsAssessmentStateToExistingCatalog(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := sqliteMigrations.ReadFile("sqlite_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(schema), "    quality_score INTEGER NOT NULL CHECK (quality_score BETWEEN 5 AND 10),\n", "", 1)
	legacy = strings.Replace(legacy, "    llm_checked INTEGER NOT NULL DEFAULT 0,\n", "", 1)
	if _, err := db.db.ExecContext(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.QueryContext(ctx, "PRAGMA table_info(skills)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	qualityFound, checkedFound := false, false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		qualityFound = qualityFound || name == "quality_score"
		checkedFound = checkedFound || name == "llm_checked"
	}
	if !qualityFound || !checkedFound {
		t.Fatalf("assessment columns missing: quality=%t checked=%t", qualityFound, checkedFound)
	}
	legacySkill := testSkill()
	legacySkill.LLMChecked = false
	if err := db.UpsertSkill(ctx, legacySkill); err != nil {
		t.Fatal(err)
	}
	unchecked, err := db.UnassessedSkills(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchecked) != 1 || unchecked[0].ID != legacySkill.ID {
		t.Fatalf("unchecked legacy skills = %+v", unchecked)
	}
	listed, total, err := db.ListSkills(ctx, model.ListOptions{PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(listed) != 1 || listed[0].SecurityScore != 0 || listed[0].QualityScore != 0 {
		t.Fatalf("disabled-LLM skill representation: total=%d skills=%+v", total, listed)
	}
	published, publishedTotal, err := db.ListSkills(ctx, model.ListOptions{PerPage: 10, LLMCheckedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if publishedTotal != 0 || len(published) != 0 {
		t.Fatalf("AI-enforced catalog exposed unchecked skill: total=%d skills=%+v", publishedTotal, published)
	}
	searched, err := db.SearchSkills(ctx, legacySkill.Name, "", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(searched) != 0 {
		t.Fatalf("AI-enforced search exposed unchecked skill: %+v", searched)
	}
	if _, err := db.GetSkill(ctx, legacySkill.ID, true); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("AI-enforced detail error = %v, want not found", err)
	}
	curated, err := db.CuratedSkills(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(curated) != 0 {
		t.Fatalf("AI-enforced curated catalog exposed unchecked skill: %+v", curated)
	}
}

func TestSQLiteLoadsCompactUncheckedSkillBatches(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	for position := range 12 {
		skill := testSkill()
		skill.ID = fmt.Sprintf("org/repo/skill-%02d", position)
		skill.Slug = fmt.Sprintf("skill-%02d", position)
		skill.LLMChecked = false
		skill.Files = []model.File{
			{Path: "SKILL.md", Contents: skill.ID},
			{Path: "large-reference.md", Contents: strings.Repeat("x", 256<<10)},
		}
		if err := db.UpsertSkill(ctx, skill); err != nil {
			t.Fatal(err)
		}
	}
	count, err := db.UnassessedSkillCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("unchecked count = %d, want 12", count)
	}
	pending, err := db.UnassessedSkills(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 10 {
		t.Fatalf("unchecked batch = %d, want 10", len(pending))
	}
	for _, skill := range pending {
		if skill.Contents != skill.ID {
			t.Fatalf("pending skill %q loaded unexpected content", skill.ID)
		}
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	skillAudit := model.Audit{Provider: "test", Slug: "test", Status: "pass", Summary: "checked", RiskLevel: "LOW", AuditedAt: now}
	if err := db.UpdateSkillAssessment(ctx, pending[0].ID, 9, 8, skillAudit, now); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetSkill(ctx, pending[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LLMChecked || updated.SecurityScore != 9 || updated.QualityScore != 8 || len(updated.Files) != 2 {
		t.Fatalf("updated compact assessment = %+v", updated)
	}
}

func TestSQLiteRejectsNULSkillContents(t *testing.T) {
	db := openTestSQLite(t)
	skill := testSkill()
	skill.Files = []model.File{{Path: "SKILL.md", Contents: "valid\x00invalid"}}
	err := db.UpsertSkill(context.Background(), skill)
	if !errors.Is(err, model.ErrInvalidSkillContents) {
		t.Fatalf("UpsertSkill() error = %v, want ErrInvalidSkillContents", err)
	}
}

func TestSQLiteMigrationRemovesRowsWithoutCanonicalSkillFile(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	invalid := testSkill()
	invalid.ID = "Canner/WrenAI/reference"
	invalid.Slug = "reference"
	invalid.Name = "Skills"
	invalid.Files = []model.File{{Path: "skills.md", Contents: "# Skills documentation"}}
	if err := db.UpsertSkill(ctx, invalid); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSkill(ctx, invalid.ID, false); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("invalid documentation row survived migration: %v", err)
	}
}

func TestSQLiteSkillRepositoryCompatibility(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	skill := testSkill()
	if err := db.UpsertSkill(ctx, skill); err != nil {
		t.Fatal(err)
	}
	listed, total, err := db.ListSkills(ctx, model.ListOptions{Page: 0, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(listed) != 1 || listed[0].ID != skill.ID {
		t.Fatalf("unexpected list: total=%d skills=%+v", total, listed)
	}

	found, err := db.GetSkill(ctx, skill.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if found.Hash != skill.Hash || found.Audit.Summary != skill.Audit.Summary || found.QualityScore != skill.QualityScore || len(found.Files) != 1 {
		t.Fatalf("unexpected stored skill: %+v", found)
	}
	fresh, err := db.FreshSkillIDs(ctx, skill.UpdatedAt.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := fresh[skill.ID]; !exists {
		t.Fatalf("fresh IDs = %v, want %q", fresh, skill.ID)
	}
	atBoundary, err := db.FreshSkillIDs(ctx, skill.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := atBoundary[skill.ID]; exists {
		t.Fatalf("skill updated exactly at cutoff was marked fresh: %v", atBoundary)
	}

	searched, err := db.SearchSkills(ctx, "demo", "acme", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(searched) != 1 || searched[0].ID != skill.ID {
		t.Fatalf("unexpected search: %+v", searched)
	}

	curated, err := db.CuratedSkills(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(curated) != 1 || !curated[0].Official {
		t.Fatalf("unexpected curated skills: %+v", curated)
	}

	skill.Stars = 99
	skill.SecurityScore = 10
	if err := db.UpsertSkill(ctx, skill); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetSkill(ctx, skill.ID, false)
	if err != nil || updated.Stars != 99 || updated.SecurityScore != 10 {
		t.Fatalf("upsert did not update skill: %+v, %v", updated, err)
	}

	if err := db.DeleteSkill(ctx, skill.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSkill(ctx, skill.ID, false); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("GetSkill() error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteAPIKeyCompatibility(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	manager, err := auth.NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, record, err := manager.Generate("local", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateAPIKey(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.LookupAPIKey(ctx, manager.Hash(token)); err != nil || got != id {
		t.Fatalf("LookupAPIKey() = %q, %v, want %q", got, err, id)
	}
	if _, err := db.LookupAPIKey(ctx, manager.Hash("wrong")); !errors.Is(err, model.ErrUnauthorized) {
		t.Fatalf("wrong-token error = %v, want ErrUnauthorized", err)
	}

	expired := record
	expired.Name = "expired"
	expired.TokenHash = manager.Hash("expired")
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := db.CreateAPIKey(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LookupAPIKey(ctx, expired.TokenHash); !errors.Is(err, model.ErrUnauthorized) {
		t.Fatalf("expired-token error = %v, want ErrUnauthorized", err)
	}
}

func TestSQLiteAdminTokenRevocationAndDownloadStats(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	manager, err := auth.NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, record, err := manager.Generate("analytics-client", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := db.CreateAPIKey(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	skill := testSkill()
	if err := db.UpsertSkill(ctx, skill); err != nil {
		t.Fatal(err)
	}
	undownloaded := testSkill()
	undownloaded.ID = "acme/tools/unused"
	undownloaded.Slug = "unused"
	undownloaded.Name = "Unused Skill"
	if err := db.UpsertSkill(ctx, undownloaded); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.RecordSkillDownload(ctx, skill.ID, keyID); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := db.AdminStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalSkills != 2 || stats.TotalDownloads != 2 || stats.UniqueClients != 1 || len(stats.Skills) != 2 || len(stats.Skills[0].Clients) != 1 || stats.Skills[1].Downloads != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.Skills[0].QualityScore != skill.QualityScore {
		t.Fatalf("stats quality score = %d, want %d", stats.Skills[0].QualityScore, skill.QualityScore)
	}
	client := stats.Skills[0].Clients[0]
	if client.ID != keyID || client.Name != "analytics-client" || client.Downloads != 2 || client.FirstDownloadedAt.IsZero() || client.LastDownloadedAt.IsZero() {
		t.Fatalf("unexpected client stats: %+v", client)
	}

	revoked, err := db.RevokeAPIKey(ctx, keyID)
	if err != nil || !revoked {
		t.Fatalf("RevokeAPIKey() = %v, %v", revoked, err)
	}
	if _, err := db.LookupAPIKey(ctx, manager.Hash(token)); !errors.Is(err, model.ErrUnauthorized) {
		t.Fatalf("revoked token lookup error = %v, want unauthorized", err)
	}
	revoked, err = db.RevokeAPIKey(ctx, "key_missing")
	if err != nil || revoked {
		t.Fatalf("missing RevokeAPIKey() = %v, %v", revoked, err)
	}
}

func TestOpenRejectsUnsupportedDatabaseScheme(t *testing.T) {
	if _, err := Open(context.Background(), "mysql://localhost/skills"); err == nil {
		t.Fatal("Open() error = nil, want unsupported scheme error")
	}
}

func openTestSQLite(t *testing.T) Backend {
	t.Helper()
	db, err := Open(context.Background(), "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func testSkill() model.Skill {
	now := time.Unix(1_700_000_000, 0).UTC()
	return model.Skill{
		ID: "acme/tools/demo", Slug: "demo", Name: "Demo Skill", Description: "A demo skill",
		Source: "acme/tools", Installs: 42, Stars: 42, SourceType: "github",
		InstallURL: "https://github.com/acme/tools", URL: "https://skills.exitmesh.com/acme/tools/demo",
		SecurityScore: 9, QualityScore: 8, LLMChecked: true, Official: true, Hash: "abc123", UpdatedAt: now,
		Files: []model.File{{Path: "SKILL.md", Contents: "# Demo"}},
		Audit: model.Audit{Provider: "ExitMesh AI", Slug: "exitmesh-ai", Status: "pass", Summary: "safe", AuditedAt: now, RiskLevel: "LOW", Categories: []string{"SAFE"}},
	}
}
