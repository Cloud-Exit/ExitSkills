package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	apidocs "github.com/exitmesh/skills/docs"
	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/model"
)

type Repository interface {
	ListSkills(context.Context, model.ListOptions) ([]model.Skill, int, error)
	SearchSkills(context.Context, string, string, int, bool) ([]model.Skill, error)
	GetSkill(context.Context, string, bool) (model.Skill, error)
	CuratedSkills(context.Context, bool) ([]model.Skill, error)
}
type Verifier interface {
	Verify(context.Context, string) (string, error)
}
type AdminRepository interface {
	CreateAPIKey(context.Context, auth.KeyRecord) (string, error)
	RevokeAPIKey(context.Context, string) (bool, error)
	RecordSkillDownload(context.Context, string, string) error
	AdminStats(context.Context) (model.AdminStats, error)
}
type Handler struct {
	repo           Repository
	verifier       Verifier
	limiter        *Limiter
	adminRepo      AdminRepository
	keys           *auth.KeyManager
	masterToken    string
	llmCheckedOnly bool
}

const masterKeyID = "__master__"

type Option func(*Handler)

func WithAdmin(masterToken string, keys *auth.KeyManager, repo AdminRepository) Option {
	return func(handler *Handler) {
		handler.masterToken = masterToken
		handler.keys = keys
		handler.adminRepo = repo
	}
}

func WithLLMEnforcement(enabled bool) Option {
	return func(handler *Handler) {
		handler.llmCheckedOnly = enabled
	}
}

func NewHandler(repo Repository, verifier Verifier, limiter *Limiter, options ...Option) http.Handler {
	handler := &Handler{repo: repo, verifier: verifier, limiter: limiter}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	path, validAPIPath := apiPath(r.URL.Path)
	if validAPIPath && path == "/docs" {
		h.docs(w, r)
		return
	}
	if validAPIPath && (path == "/admin" || strings.HasPrefix(path, "/admin/")) {
		h.admin(w, r, path)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported.")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "A valid bearer API token is required.")
		return
	}
	keyID := masterKeyID
	if !constantTokenEqual(token, h.masterToken) {
		var err error
		keyID, err = h.verifier.Verify(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "The bearer API token is invalid or expired.")
			return
		}
	}
	result := h.limiter.allow(keyID)
	h.limiter.headers(w.Header().Set, result)
	if !result.allowed {
		w.Header().Set("Retry-After", strconv.Itoa(result.reset))
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit exceeded. Retry after the indicated interval.")
		return
	}

	if !validAPIPath {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found.")
		return
	}
	switch {
	case path == "/skills":
		h.list(w, r)
	case path == "/skills/search":
		h.search(w, r)
	case path == "/skills/curated":
		h.curated(w, r)
	case strings.HasPrefix(path, "/skills/audit/"):
		h.audit(w, r, strings.TrimPrefix(path, "/skills/audit/"))
	case strings.HasPrefix(path, "/skills/"):
		h.detail(w, r, strings.TrimPrefix(path, "/skills/"), keyID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found.")
	}
}

func (h *Handler) docs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported.")
		return
	}
	page, err := apidocs.HTML()
	if err != nil {
		internalError(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' https://cdn.redoc.ly; style-src 'unsafe-inline'; img-src data: https:; font-src data: https:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "all-time"
	}
	if view != "all-time" && view != "trending" && view != "hot" {
		writeError(w, http.StatusBadRequest, "invalid_request", "view must be all-time, trending, or hot.")
		return
	}
	page, ok := queryInt(r, "page", 0, 0, 1_000_000)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "page must be a non-negative integer.")
		return
	}
	perPage, ok := queryInt(r, "per_page", 100, 1, 500)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "per_page must be between 1 and 500.")
		return
	}
	skills, total, err := h.repo.ListSkills(r.Context(), model.ListOptions{View: view, Page: page, PerPage: perPage, LLMCheckedOnly: h.llmCheckedOnly})
	if err != nil {
		internalError(w)
		return
	}
	if view == "hot" {
		for n := range skills {
			zero := 0
			skills[n].InstallsYesterday = &zero
			skills[n].Change = &zero
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": skills, "pagination": model.Pagination{Page: page, PerPage: perPage, Total: total, HasMore: (page+1)*perPage < total}})
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 || len(query) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "q must contain between 2 and 256 characters.")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if len(owner) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "owner must not exceed 100 characters.")
		return
	}
	limit, ok := queryInt(r, "limit", 50, 1, 200)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200.")
		return
	}
	skills, err := h.repo.SearchSkills(r.Context(), query, owner, limit, h.llmCheckedOnly)
	if err != nil {
		internalError(w)
		return
	}
	searchType := "fuzzy"
	if len(strings.Fields(query)) > 1 {
		searchType = "semantic"
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": skills, "query": query, "searchType": searchType, "count": len(skills), "durationMs": time.Since(started).Milliseconds()})
}

func (h *Handler) curated(w http.ResponseWriter, r *http.Request) {
	skills, err := h.repo.CuratedSkills(r.Context(), h.llmCheckedOnly)
	if err != nil {
		internalError(w)
		return
	}
	type ownerGroup struct {
		Owner         string        `json:"owner"`
		TotalInstalls int           `json:"totalInstalls"`
		FeaturedRepo  string        `json:"featuredRepo"`
		FeaturedSkill string        `json:"featuredSkill"`
		Skills        []model.Skill `json:"skills"`
	}
	groups, positions := make([]ownerGroup, 0), map[string]int{}
	for _, skill := range skills {
		owner, repo := sourceParts(skill.Source)
		pos, exists := positions[owner]
		if !exists {
			pos = len(groups)
			positions[owner] = pos
			groups = append(groups, ownerGroup{Owner: owner, FeaturedRepo: repo, FeaturedSkill: skill.Slug})
		}
		groups[pos].Skills = append(groups[pos].Skills, skill)
		groups[pos].TotalInstalls += skill.Installs
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": groups, "totalOwners": len(groups), "totalSkills": len(skills), "generatedAt": time.Now().UTC()})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request, id, keyID string) {
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Skill not found.")
		return
	}
	skill, err := h.repo.GetSkill(r.Context(), id, h.llmCheckedOnly)
	if errors.Is(err, model.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Skill not found.")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	if h.adminRepo != nil && keyID != masterKeyID {
		if err := h.adminRepo.RecordSkillDownload(r.Context(), skill.ID, keyID); err != nil {
			internalError(w)
			return
		}
	}
	payload := map[string]any{"id": skill.ID, "source": skill.Source, "slug": skill.Slug, "installs": skill.Installs, "hash": skill.Hash, "files": skill.Files, "llmChecked": skill.LLMChecked}
	if skill.LLMChecked {
		payload["securityScore"] = skill.SecurityScore
		payload["qualityScore"] = skill.QualityScore
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) admin(w http.ResponseWriter, r *http.Request, path string) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !constantTokenEqual(token, h.masterToken) || h.adminRepo == nil || h.keys == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "A valid master bearer token is required.")
		return
	}
	switch {
	case path == "/admin/token" && r.Method == http.MethodPost:
		h.createToken(w, r)
	case path == "/admin/token" && r.Method == http.MethodDelete:
		h.revokeToken(w, r)
	case path == "/admin/stats" && r.Method == http.MethodGet:
		h.adminStats(w, r)
	case path == "/admin/token" || path == "/admin/stats":
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The method is not supported for this admin endpoint.")
	default:
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found.")
	}
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		ValidFor string `json:"validFor"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "A JSON body with name and optional validFor is required.")
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must contain between 1 and 100 characters.")
		return
	}
	if request.ValidFor == "" {
		request.ValidFor = "720h"
	}
	validFor, err := time.ParseDuration(request.ValidFor)
	if err != nil || validFor <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "validFor must be a positive Go duration such as 24h or 720h.")
		return
	}
	expiresAt := time.Now().UTC().Add(validFor)
	token, record, err := h.keys.Generate(request.Name, expiresAt)
	if err != nil {
		internalError(w)
		return
	}
	id, err := h.adminRepo.CreateAPIKey(r.Context(), record)
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": request.Name, "expiresAt": expiresAt, "token": token})
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if err := decodeRequest(w, r, &request); err != nil || !strings.HasPrefix(request.ID, "key_") {
		writeError(w, http.StatusBadRequest, "invalid_request", "A valid API-key id is required.")
		return
	}
	revoked, err := h.adminRepo.RevokeAPIKey(r.Context(), request.ID)
	if err != nil {
		internalError(w)
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "not_found", "API token not found or already revoked.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) adminStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.adminRepo.AdminStats(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	stats.GeneratedAt = time.Now().UTC()
	writeJSON(w, http.StatusOK, stats)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func constantTokenEqual(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return expected != "" && subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func (h *Handler) audit(w http.ResponseWriter, r *http.Request, id string) {
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "No audits exist for this skill.")
		return
	}
	skill, err := h.repo.GetSkill(r.Context(), id, h.llmCheckedOnly)
	if errors.Is(err, model.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No audits exist for this skill.")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	if !skill.LLMChecked {
		writeError(w, http.StatusNotFound, "not_found", "No audits exist for this skill.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": skill.ID, "source": skill.Source, "slug": skill.Slug, "audits": []model.Audit{skill.Audit}, "securityScore": skill.SecurityScore, "qualityScore": skill.QualityScore, "llmChecked": true})
}

func apiPath(path string) (string, bool) {
	for _, prefix := range []string{"/api/v1", "/v1"} {
		if path == prefix {
			return "/", true
		}
		if strings.HasPrefix(path, prefix+"/") {
			return strings.TrimPrefix(path, prefix), true
		}
	}
	return "", false
}
func bearerToken(value string) (string, bool) {
	parts := strings.SplitN(value, " ", 2)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = strings.TrimSpace(parts[1])
	}
	return returnValue, returnValue != ""
}
func queryInt(r *http.Request, name string, fallback, min, max int) (int, bool) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(value)
	return n, err == nil && n >= min && n <= max
}
func validID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > 100 {
			return false
		}
		for _, r := range p {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}
func sourceParts(source string) (string, string) {
	parts := strings.SplitN(source, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return source, source
}
func internalError(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "The service is temporarily unavailable. Retry with backoff.")
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
}
