package model

import (
	"errors"
	"time"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
)

type File struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type Audit struct {
	Provider   string    `json:"provider"`
	Slug       string    `json:"slug"`
	Status     string    `json:"status"`
	Summary    string    `json:"summary"`
	AuditedAt  time.Time `json:"auditedAt"`
	RiskLevel  string    `json:"riskLevel,omitempty"`
	Categories []string  `json:"categories,omitempty"`
}

type Skill struct {
	ID                string `json:"id"`
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Source            string `json:"source"`
	Installs          int    `json:"installs"`
	Stars             int    `json:"stars,omitempty"`
	SourceType        string `json:"sourceType"`
	InstallURL        string `json:"installUrl"`
	URL               string `json:"url"`
	IsDuplicate       bool   `json:"isDuplicate,omitempty"`
	SecurityScore     int    `json:"securityScore,omitempty"`
	QualityScore      int    `json:"qualityScore,omitempty"`
	LLMChecked        bool   `json:"llmChecked"`
	Official          bool   `json:"official,omitempty"`
	InstallsYesterday *int   `json:"installsYesterday,omitempty"`
	Change            *int   `json:"change,omitempty"`

	Hash      string    `json:"-"`
	Files     []File    `json:"-"`
	Audit     Audit     `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// PendingSkillAssessment intentionally contains only the canonical skill file.
// Reconciliation must not load every supporting file into memory.
type PendingSkillAssessment struct {
	ID       string
	Contents string
}

type ListOptions struct {
	View           string
	Page           int
	PerPage        int
	LLMCheckedOnly bool
}

type Pagination struct {
	Page    int  `json:"page"`
	PerPage int  `json:"perPage"`
	Total   int  `json:"total"`
	HasMore bool `json:"hasMore"`
}

type AdminStats struct {
	GeneratedAt    time.Time    `json:"generatedAt"`
	TotalSkills    int          `json:"totalSkills"`
	TotalDownloads int64        `json:"totalDownloads"`
	UniqueClients  int          `json:"uniqueClients"`
	Skills         []SkillStats `json:"skills"`
}

type SkillStats struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Source           string        `json:"source"`
	Slug             string        `json:"slug"`
	SecurityScore    int           `json:"securityScore,omitempty"`
	QualityScore     int           `json:"qualityScore,omitempty"`
	LLMChecked       bool          `json:"llmChecked"`
	Downloads        int64         `json:"downloads"`
	UniqueClients    int           `json:"uniqueClients"`
	LastDownloadedAt *time.Time    `json:"lastDownloadedAt"`
	Clients          []ClientStats `json:"clients"`
}

type ClientStats struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Downloads         int64     `json:"downloads"`
	FirstDownloadedAt time.Time `json:"firstDownloadedAt"`
	LastDownloadedAt  time.Time `json:"lastDownloadedAt"`
}
