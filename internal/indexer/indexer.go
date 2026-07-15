package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/exitmesh/skills/internal/audit"
	"github.com/exitmesh/skills/internal/model"
)

type Candidate struct {
	ID, Source, Slug, Name, Description, Contents string
	Stars                                         int
	Official                                      bool
	Fresh                                         bool
	Files                                         []model.File
}
type Source interface {
	Discover(context.Context) ([]Candidate, error)
}
type FreshnessAwareSource interface {
	DiscoverSkipping(context.Context, map[string]struct{}) ([]Candidate, error)
}
type Auditor interface {
	Audit(context.Context, string) (audit.Result, error)
}
type Store interface {
	UpsertSkill(context.Context, model.Skill) error
	DeleteSkill(context.Context, string) error
	FreshSkillIDs(context.Context, time.Time) (map[string]struct{}, error)
}
type Stats struct {
	Discovered, Stored, SkippedWeak, SkippedStars, SkippedFresh, Failed int
}

const skillFreshness = 24 * time.Hour

type Indexer struct {
	source        Source
	auditor       Auditor
	store         Store
	now           func() time.Time
	publicBaseURL string
	logger        *slog.Logger
	concurrency   int
}

func New(source Source, auditor Auditor, store Store, now func() time.Time) *Indexer {
	if now == nil {
		now = time.Now
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Indexer{source: source, auditor: auditor, store: store, now: now, publicBaseURL: "https://skills.exitmesh.com", logger: logger, concurrency: 8}
}
func (i *Indexer) WithPublicBaseURL(url string) *Indexer { i.publicBaseURL = url; return i }
func (i *Indexer) WithConcurrency(concurrency int) *Indexer {
	if concurrency > 0 {
		i.concurrency = concurrency
	}
	return i
}
func (i *Indexer) WithLogger(logger *slog.Logger) *Indexer {
	if logger != nil {
		i.logger = logger
	}
	return i
}

func (i *Indexer) Run(ctx context.Context) (Stats, error) {
	i.logger.Info("skill discovery started")
	cutoff := i.now().UTC().Add(-skillFreshness)
	freshIDs, err := i.store.FreshSkillIDs(ctx, cutoff)
	if err != nil {
		return Stats{}, fmt.Errorf("load fresh skills: %w", err)
	}
	var candidates []Candidate
	if source, aware := i.source.(FreshnessAwareSource); aware {
		candidates, err = source.DiscoverSkipping(ctx, freshIDs)
	} else {
		candidates, err = i.source.Discover(ctx)
	}
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{Discovered: len(candidates)}
	eligible := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		_, storedFresh := freshIDs[candidate.ID]
		if candidate.Fresh || storedFresh {
			stats.SkippedFresh++
			i.logger.Debug("skill skipped", "skill_id", candidate.ID, "reason", "fresh", "freshness_cutoff", cutoff)
			continue
		}
		if candidate.Stars <= 10 {
			stats.SkippedStars++
			i.logger.Debug("skill skipped", "skill_id", candidate.ID, "reason", "stars", "stars", candidate.Stars)
			continue
		}
		eligible = append(eligible, candidate)
	}
	i.logger.Info("skill discovery complete", "discovered", len(candidates), "eligible", len(eligible), "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "freshness_cutoff", cutoff)
	i.logger.Info("skill auditing phase started", "eligible", len(eligible), "concurrency", min(i.concurrency, len(eligible)))
	logProgress := func(processed int) {
		if processed%10 == 0 || processed == len(candidates) {
			i.logger.Info("indexing progress", "processed", processed, "total", len(candidates), "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "failed", stats.Failed)
		}
	}
	if len(eligible) == 0 {
		logProgress(len(candidates))
		return stats, nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan Candidate, len(eligible))
	results := make(chan indexResult, len(eligible))
	for _, candidate := range eligible {
		jobs <- candidate
	}
	close(jobs)
	workers := min(i.concurrency, len(eligible))
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for candidate := range jobs {
				if runCtx.Err() != nil {
					return
				}
				results <- i.indexCandidate(runCtx, candidate)
			}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()

	processed := stats.SkippedStars + stats.SkippedFresh
	var runErr error
	for result := range results {
		processed++
		switch result.status {
		case indexStored:
			stats.Stored++
		case indexWeak:
			stats.SkippedWeak++
		case indexFailed:
			stats.Failed++
		}
		if result.err != nil && runErr == nil {
			runErr = result.err
			cancel()
		}
		logProgress(processed)
	}
	if runErr != nil {
		return stats, runErr
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

type indexStatus uint8

const (
	indexStored indexStatus = iota + 1
	indexWeak
	indexFailed
)

type indexResult struct {
	status indexStatus
	err    error
}

func (i *Indexer) indexCandidate(ctx context.Context, candidate Candidate) indexResult {
	i.logger.Debug("skill audit started", "skill_id", candidate.ID, "stars", candidate.Stars, "official", candidate.Official)
	result, err := i.auditor.Audit(ctx, candidate.Contents)
	if err != nil {
		if ctx.Err() != nil {
			return indexResult{err: ctx.Err()}
		}
		if err := i.store.DeleteSkill(ctx, candidate.ID); err != nil {
			return indexResult{err: fmt.Errorf("remove unaudited skill %s: %w", candidate.ID, err)}
		}
		i.logger.Warn("skill audit failed", "skill_id", candidate.ID, "error", err)
		return indexResult{status: indexFailed}
	}
	if result.Score < 5 {
		if err := i.store.DeleteSkill(ctx, candidate.ID); err != nil {
			return indexResult{err: fmt.Errorf("remove weak skill %s: %w", candidate.ID, err)}
		}
		i.logger.Debug("skill skipped", "skill_id", candidate.ID, "reason", "security_score", "score", result.Score, "risk_level", result.RiskLevel)
		return indexResult{status: indexWeak}
	}
	filesJSON, _ := json.Marshal(candidate.Files)
	sum := sha256.Sum256(filesJSON)
	now := i.now().UTC()
	skill := model.Skill{ID: candidate.ID, Source: candidate.Source, Slug: candidate.Slug, Name: candidate.Name, Description: candidate.Description, Stars: candidate.Stars, Installs: candidate.Stars, SourceType: "github", InstallURL: "https://github.com/" + candidate.Source, URL: i.publicBaseURL + "/" + candidate.ID, SecurityScore: result.Score, Official: candidate.Official, Hash: hex.EncodeToString(sum[:]), Files: candidate.Files, UpdatedAt: now, Audit: model.Audit{Provider: "ExitMesh AI", Slug: "exitmesh-ai", Status: result.Status, Summary: result.Summary, RiskLevel: result.RiskLevel, Categories: result.Categories, AuditedAt: now}}
	if err := i.store.UpsertSkill(ctx, skill); err != nil {
		return indexResult{err: fmt.Errorf("store skill %s: %w", candidate.ID, err)}
	}
	i.logger.Debug("skill indexed", "skill_id", candidate.ID, "score", result.Score, "risk_level", result.RiskLevel, "official", candidate.Official, "files", len(candidate.Files))
	return indexResult{status: indexStored}
}
