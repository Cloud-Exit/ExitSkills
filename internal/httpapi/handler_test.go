package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
)

type fakeRepository struct{ skills []model.Skill }

func (f fakeRepository) ListSkills(_ context.Context, _ model.ListOptions) ([]model.Skill, int, error) {
	return f.skills, len(f.skills), nil
}
func (f fakeRepository) SearchSkills(_ context.Context, _ string, _ string, limit int) ([]model.Skill, error) {
	if limit < len(f.skills) {
		return f.skills[:limit], nil
	}
	return f.skills, nil
}
func (f fakeRepository) GetSkill(_ context.Context, id string) (model.Skill, error) {
	for _, skill := range f.skills {
		if skill.ID == id {
			return skill, nil
		}
	}
	return model.Skill{}, model.ErrNotFound
}
func (f fakeRepository) CuratedSkills(_ context.Context) ([]model.Skill, error) { return f.skills, nil }

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, token string) (string, error) {
	if token == "test" {
		return "key-1", nil
	}
	return "", model.ErrUnauthorized
}

func testHandler(limit int) http.Handler {
	skill := model.Skill{ID: "exitmesh/skills/audit", Slug: "audit", Name: "Audit", Source: "exitmesh/skills", Stars: 42, Installs: 42, SourceType: "github", InstallURL: "https://github.com/exitmesh/skills", URL: "https://skills.exitmesh.com/exitmesh/skills/audit", SecurityScore: 9, Official: true, Audit: model.Audit{Provider: "ExitMesh AI", Slug: "exitmesh-ai", Status: "pass", Summary: "safe", AuditedAt: time.Unix(1, 0).UTC(), RiskLevel: "LOW"}}
	return NewHandler(fakeRepository{skills: []model.Skill{skill}}, fakeVerifier{}, NewLimiter(limit, time.Minute))
}

func TestListSupportsDocumentedAndShortRoutes(t *testing.T) {
	for _, path := range []string{"/api/v1/skills", "/v1/skills"} {
		req := httptest.NewRequest(http.MethodGet, path+"?page=0&per_page=10", nil)
		req.Header.Set("Authorization", "Bearer test")
		res := httptest.NewRecorder()
		testHandler(10).ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, res.Code, res.Body.String())
		}
		var body struct {
			Data       []model.Skill    `json:"data"`
			Pagination model.Pagination `json:"pagination"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Data) != 1 || body.Pagination.Page != 0 || body.Pagination.Total != 1 {
			t.Fatalf("unexpected body: %+v", body)
		}
		if res.Header().Get("X-RateLimit-Limit") != "10" {
			t.Fatalf("missing rate limit headers")
		}
	}
}

func TestDocsSupportsDocumentedAndShortRoutesWithoutAuthentication(t *testing.T) {
	for _, route := range []string{"/api/v1/docs", "/v1/docs"} {
		res := httptest.NewRecorder()
		testHandler(10).ServeHTTP(res, httptest.NewRequest(http.MethodGet, route, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", route, res.Code, res.Body.String())
		}
		if contentType := res.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("%s content type = %q, want text/html", route, contentType)
		}
		for _, expected := range []string{"ExitMesh Skills API", "Redoc.init(spec", `"/v1/skills"`} {
			if !strings.Contains(res.Body.String(), expected) {
				t.Fatalf("%s body is missing %q", route, expected)
			}
		}
	}
}

func TestAuthenticationAndErrors(t *testing.T) {
	res := httptest.NewRecorder()
	testHandler(10).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/skills", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["error"] != "unauthorized" || body["message"] == "" {
		t.Fatalf("unexpected error: %v", body)
	}
}

func TestRateLimit(t *testing.T) {
	h := testHandler(1)
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
		req.Header.Set("Authorization", "Bearer test")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("request %d status = %d, want %d", i, res.Code, want)
		}
		if want == http.StatusTooManyRequests && res.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
}

func TestDetailAndAuditCompatibility(t *testing.T) {
	for _, path := range []string{"/api/v1/skills/exitmesh/skills/audit", "/api/v1/skills/audit/exitmesh/skills/audit"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test")
		res := httptest.NewRecorder()
		testHandler(10).ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, res.Code, res.Body.String())
		}
	}
}

func TestHotViewIncludesComparisonFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills?view=hot", nil)
	req.Header.Set("Authorization", "Bearer test")
	res := httptest.NewRecorder()
	testHandler(10).ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Data[0]["installsYesterday"]; !ok {
		t.Fatal("hot response is missing installsYesterday")
	}
	if _, ok := body.Data[0]["change"]; !ok {
		t.Fatal("hot response is missing change")
	}
}

type fakeAdminRepository struct {
	createdRecord auth.KeyRecord
	createdID     string
	revokedID     string
	downloadSkill string
	downloadKey   string
	stats         model.AdminStats
}

func (f *fakeAdminRepository) CreateAPIKey(_ context.Context, record auth.KeyRecord) (string, error) {
	f.createdRecord = record
	f.createdID = "key_created"
	return f.createdID, nil
}
func (f *fakeAdminRepository) RevokeAPIKey(_ context.Context, id string) (bool, error) {
	f.revokedID = id
	return id == f.createdID, nil
}
func (f *fakeAdminRepository) RecordSkillDownload(_ context.Context, skillID, keyID string) error {
	f.downloadSkill, f.downloadKey = skillID, keyID
	return nil
}
func (f *fakeAdminRepository) AdminStats(context.Context) (model.AdminStats, error) {
	return f.stats, nil
}

func adminTestHandler(t *testing.T, admin *fakeAdminRepository) (http.Handler, *auth.KeyManager) {
	t.Helper()
	keys, err := auth.NewKeyManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	skill := model.Skill{ID: "exitmesh/skills/audit", Slug: "audit", Name: "Audit", Source: "exitmesh/skills", Stars: 42, Installs: 42, SecurityScore: 9}
	handler := NewHandler(fakeRepository{skills: []model.Skill{skill}}, fakeVerifier{}, NewLimiter(10, time.Minute), WithAdmin("master-secret", keys, admin))
	return handler, keys
}

func TestAdminTokenLifecycleRequiresMasterToken(t *testing.T) {
	admin := &fakeAdminRepository{}
	handler, keys := adminTestHandler(t, admin)

	for _, authorization := range []string{"", "Bearer test", "Bearer wrong-master"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/token", strings.NewReader(`{"name":"automation","validFor":"24h"}`))
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, res.Code)
		}
	}

	create := httptest.NewRequest(http.MethodPost, "/v1/admin/token", strings.NewReader(`{"name":"automation","validFor":"24h"}`))
	create.Header.Set("Authorization", "Bearer master-secret")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	var body struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(created.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "key_created" || body.Name != "automation" || !keys.Matches(body.Token, admin.createdRecord.TokenHash) || time.Until(body.ExpiresAt) < 23*time.Hour {
		t.Fatalf("unexpected token response: id=%q name=%q expires=%v", body.ID, body.Name, body.ExpiresAt)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/v1/admin/token", strings.NewReader(`{"id":"key_created"}`))
	remove.Header.Set("Authorization", "Bearer master-secret")
	removed := httptest.NewRecorder()
	handler.ServeHTTP(removed, remove)
	if removed.Code != http.StatusNoContent || admin.revokedID != "key_created" {
		t.Fatalf("delete status = %d revoked=%q body=%s", removed.Code, admin.revokedID, removed.Body.String())
	}
}

func TestAdminStatsAndSkillDownloadTracking(t *testing.T) {
	lastDownload := time.Unix(1_800_000_000, 0).UTC()
	admin := &fakeAdminRepository{stats: model.AdminStats{
		TotalSkills: 1, TotalDownloads: 3, UniqueClients: 1,
		Skills: []model.SkillStats{{ID: "exitmesh/skills/audit", Name: "Audit", Downloads: 3, UniqueClients: 1, LastDownloadedAt: &lastDownload, Clients: []model.ClientStats{{ID: "key-1", Name: "client", Downloads: 3}}}},
	}}
	handler, _ := adminTestHandler(t, admin)

	detail := httptest.NewRequest(http.MethodGet, "/v1/skills/exitmesh/skills/audit", nil)
	detail.Header.Set("Authorization", "Bearer test")
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detail)
	if detailResponse.Code != http.StatusOK || admin.downloadSkill != "exitmesh/skills/audit" || admin.downloadKey != "key-1" {
		t.Fatalf("detail status=%d tracked skill=%q key=%q", detailResponse.Code, admin.downloadSkill, admin.downloadKey)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/stats", nil)
	request.Header.Set("Authorization", "Bearer master-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stats status = %d: %s", response.Code, response.Body.String())
	}
	var stats model.AdminStats
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.TotalSkills != 1 || stats.TotalDownloads != 3 || stats.UniqueClients != 1 || len(stats.Skills) != 1 || stats.GeneratedAt.IsZero() {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestMasterTokenCanReadCatalogWithoutRecordingClientDownload(t *testing.T) {
	admin := &fakeAdminRepository{}
	handler, _ := adminTestHandler(t, admin)
	request := httptest.NewRequest(http.MethodGet, "/v1/skills/exitmesh/skills/audit", nil)
	request.Header.Set("Authorization", "Bearer master-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if admin.downloadSkill != "" || admin.downloadKey != "" {
		t.Fatalf("master request was recorded as client download: skill=%q key=%q", admin.downloadSkill, admin.downloadKey)
	}
}
