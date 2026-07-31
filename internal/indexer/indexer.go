package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
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
type StreamingSource interface {
	DiscoverStream(context.Context, map[string]struct{}, func(Candidate) error) error
}
type Auditor interface {
	Audit(context.Context, string) (audit.Result, error)
}
type Store interface {
	UpsertSkill(context.Context, model.Skill) error
	DeleteSkill(context.Context, string) error
	FreshSkillIDs(context.Context, time.Time) (map[string]struct{}, error)
	SkillContentHashes(context.Context) (map[string]string, error)
	UnassessedSkillCount(context.Context) (int, error)
	UnassessedSkills(context.Context, int) ([]model.PendingSkillAssessment, error)
	UpdateSkillAssessment(context.Context, string, int, int, model.Audit, time.Time) error
}
type Stats struct {
	Discovered, Stored, SkippedWeak, SkippedStars, SkippedFresh, SkippedUnchanged, Failed int
}

var ErrAssessmentDeferred = errors.New("AI assessment deferred")

const (
	skillFreshness = 7 * 24 * time.Hour
	indexBatchSize = 10
)

type Indexer struct {
	source              Source
	auditor             Auditor
	store               Store
	now                 func() time.Time
	publicBaseURL       string
	logger              *slog.Logger
	concurrency         int
	assessmentInterval  time.Duration
	assessmentGate      sync.Mutex
	nextAssessmentStart time.Time
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
func (i *Indexer) WithAssessmentInterval(interval time.Duration) *Indexer {
	if interval >= 0 {
		i.assessmentInterval = interval
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
	if err := i.Reconcile(ctx); err != nil {
		return Stats{}, err
	}
	i.logger.Info("skill discovery started")
	cutoff := i.now().UTC().Add(-skillFreshness)
	freshIDs, err := i.store.FreshSkillIDs(ctx, cutoff)
	if err != nil {
		return Stats{}, fmt.Errorf("load fresh skills: %w", err)
	}
	storedHashes, err := i.store.SkillContentHashes(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("load stored skill hashes: %w", err)
	}
	stats := Stats{}
	batchNumber := 0
	batch := make([]Candidate, 0, indexBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batchNumber++
		i.logger.Info("indexing batch started", "batch", batchNumber, "candidates", len(batch), "batch_size_limit", indexBatchSize)
		batchStats, err := i.indexBatch(ctx, batch, freshIDs, storedHashes, cutoff)
		stats.add(batchStats)
		for position := range batch {
			batch[position] = Candidate{}
		}
		batch = batch[:0]
		i.logger.Info("indexing batch complete", "batch", batchNumber, "discovered", stats.Discovered, "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "skipped_unchanged", stats.SkippedUnchanged, "failed", stats.Failed)
		return err
	}
	yield := func(candidate Candidate) error {
		batch = append(batch, candidate)
		if len(batch) == indexBatchSize {
			return flush()
		}
		return nil
	}
	if source, streaming := i.source.(StreamingSource); streaming {
		err = source.DiscoverStream(ctx, freshIDs, yield)
	} else {
		var candidates []Candidate
		if source, aware := i.source.(FreshnessAwareSource); aware {
			candidates, err = source.DiscoverSkipping(ctx, freshIDs)
		} else {
			candidates, err = i.source.Discover(ctx)
		}
		if err == nil {
			for _, candidate := range candidates {
				if err = yield(candidate); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		return stats, err
	}
	if err := flush(); err != nil {
		return stats, err
	}
	i.logger.Info("skill discovery complete", "discovered", stats.Discovered, "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "skipped_unchanged", stats.SkippedUnchanged, "failed", stats.Failed, "freshness_cutoff", cutoff, "batches", batchNumber)
	return stats, nil
}

func (stats *Stats) add(batch Stats) {
	stats.Discovered += batch.Discovered
	stats.Stored += batch.Stored
	stats.SkippedWeak += batch.SkippedWeak
	stats.SkippedStars += batch.SkippedStars
	stats.SkippedFresh += batch.SkippedFresh
	stats.SkippedUnchanged += batch.SkippedUnchanged
	stats.Failed += batch.Failed
}

func (i *Indexer) indexBatch(ctx context.Context, candidates []Candidate, freshIDs map[string]struct{}, storedHashes map[string]string, cutoff time.Time) (Stats, error) {
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
		if storedHash, exists := storedHashes[candidate.ID]; exists && storedHash == hashFiles(candidate.Files) {
			stats.SkippedUnchanged++
			i.logger.Debug("skill skipped", "skill_id", candidate.ID, "reason", "unchanged")
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		i.logger.Info("indexing progress", "processed", len(candidates), "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "skipped_unchanged", stats.SkippedUnchanged, "failed", stats.Failed)
		return stats, nil
	}
	i.logger.Info("skill auditing batch started", "eligible", len(eligible), "concurrency", min(i.concurrency, len(eligible)))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan Candidate, len(eligible))
	workers := min(i.concurrency, len(eligible))
	results := make(chan indexResult, workers)
	for _, candidate := range eligible {
		jobs <- candidate
	}
	close(jobs)
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

	processed := stats.SkippedStars + stats.SkippedFresh + stats.SkippedUnchanged
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
		if processed%indexBatchSize == 0 || processed == len(candidates) {
			i.logger.Info("indexing progress", "processed", processed, "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "skipped_unchanged", stats.SkippedUnchanged, "failed", stats.Failed)
		}
	}
	if runErr != nil {
		return stats, runErr
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

func (i *Indexer) Reconcile(ctx context.Context) error {
	if i.auditor == nil {
		i.logger.Info("stored skill LLM assessment skipped", "reason", "ai_disabled")
		return nil
	}
	total, err := i.store.UnassessedSkillCount(ctx)
	if err != nil {
		return fmt.Errorf("count unchecked skills: %w", err)
	}
	i.logger.Info("stored unchecked skill assessment started", "skills", total, "batch_size", indexBatchSize)
	processed := 0
	batchNumber := 0
	for processed < total {
		skills, err := i.store.UnassessedSkills(ctx, indexBatchSize)
		if err != nil {
			return fmt.Errorf("load unchecked skill batch: %w", err)
		}
		if len(skills) == 0 {
			return fmt.Errorf("unchecked skill count reported %d but batch was empty after %d processed", total, processed)
		}
		batchNumber++
		i.logger.Info("stored skill LLM assessment batch started", "batch", batchNumber, "skills", len(skills), "processed", processed, "total", total)
		for _, skill := range skills {
			position := processed + 1
			assessmentStarted := time.Now()
			i.logger.Info("stored skill LLM assessment started", "skill_id", skill.ID, "position", position, "total", total)
			if skill.Contents == "" {
				i.logger.Warn("stored skill LLM assessment rejected", "skill_id", skill.ID, "position", position, "total", total, "reason", "skill_file_missing", "duration_ms", time.Since(assessmentStarted).Milliseconds())
				if err := i.store.DeleteSkill(ctx, skill.ID); err != nil {
					return fmt.Errorf("remove unchecked skill without skill file %s: %w", skill.ID, err)
				}
				processed++
				continue
			}
			result, err := i.auditWithCooldown(ctx, skill.Contents)
			if err != nil {
				if ctx.Err() != nil {
					i.logger.Info("stored skill LLM assessment interrupted", "skill_id", skill.ID, "position", position, "total", total, "reason", ctx.Err(), "duration_ms", time.Since(assessmentStarted).Milliseconds())
					return ctx.Err()
				}
				i.logger.Warn("stored skill LLM assessment deferred", "skill_id", skill.ID, "position", position, "total", total, "error", err, "duration_ms", time.Since(assessmentStarted).Milliseconds())
				return err
			}
			if result.Status != "pass" || result.Score < 5 || result.QualityScore < 5 {
				i.logger.Info("stored skill LLM assessment rejected", "skill_id", skill.ID, "position", position, "total", total, "status", result.Status, "security_score", result.Score, "quality_score", result.QualityScore, "risk_level", result.RiskLevel, "duration_ms", time.Since(assessmentStarted).Milliseconds())
				if err := i.store.DeleteSkill(ctx, skill.ID); err != nil {
					return fmt.Errorf("remove non-passing stored skill %s: %w", skill.ID, err)
				}
				processed++
				continue
			}
			now := i.now().UTC()
			skillAudit := model.Audit{Provider: "ExitMesh AI", Slug: "exitmesh-ai", Status: result.Status, Summary: result.Summary, RiskLevel: result.RiskLevel, Categories: result.Categories, AuditedAt: now}
			if err := i.store.UpdateSkillAssessment(ctx, skill.ID, result.Score, result.QualityScore, skillAudit, now); err != nil {
				return fmt.Errorf("store assessed skill %s: %w", skill.ID, err)
			}
			processed++
			i.logger.Info("stored skill LLM assessment passed", "skill_id", skill.ID, "position", position, "total", total, "security_score", result.Score, "quality_score", result.QualityScore, "risk_level", result.RiskLevel, "duration_ms", time.Since(assessmentStarted).Milliseconds())
		}
		i.logger.Info("stored skill LLM assessment batch complete", "batch", batchNumber, "processed", processed, "total", total)
	}
	i.logger.Info("stored unchecked skill assessment complete", "skills", processed, "batches", batchNumber)
	return nil
}

func primarySkillContents(files []model.File) (string, bool) {
	for _, file := range files {
		name := path.Base(file.Path)
		if name == "SKILL.md" || name == "SKILLS.md" {
			return file.Contents, true
		}
	}
	return "", false
}

func (i *Indexer) auditWithCooldown(ctx context.Context, contents string) (audit.Result, error) {
	i.assessmentGate.Lock()
	defer i.assessmentGate.Unlock()
	if delay := time.Until(i.nextAssessmentStart); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	result, err := i.auditor.Audit(ctx, contents)
	i.nextAssessmentStart = time.Now().Add(i.assessmentInterval)
	if err != nil && ctx.Err() == nil {
		return audit.Result{}, fmt.Errorf("%w: %v", ErrAssessmentDeferred, err)
	}
	return result, err
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
	result := audit.Result{Score: 5, QualityScore: 5, Status: "pass"}
	llmChecked := i.auditor != nil
	if llmChecked {
		var err error
		result, err = i.auditWithCooldown(ctx, candidate.Contents)
		if err != nil {
			if ctx.Err() != nil {
				return indexResult{err: ctx.Err()}
			}
			i.logger.Warn("skill audit deferred; indexing run will stop without changing stored data", "skill_id", candidate.ID, "error", err)
			return indexResult{status: indexFailed, err: err}
		}
		if result.Status != "pass" || result.Score < 5 || result.QualityScore < 5 {
			if err := i.store.DeleteSkill(ctx, candidate.ID); err != nil {
				return indexResult{err: fmt.Errorf("remove weak skill %s: %w", candidate.ID, err)}
			}
			i.logger.Debug("skill skipped", "skill_id", candidate.ID, "reason", "assessment_score", "security_score", result.Score, "quality_score", result.QualityScore, "risk_level", result.RiskLevel)
			return indexResult{status: indexWeak}
		}
	}
	now := i.now().UTC()
	skill := model.Skill{ID: candidate.ID, Source: candidate.Source, Slug: candidate.Slug, Name: candidate.Name, Description: candidate.Description, Stars: candidate.Stars, Installs: candidate.Stars, SourceType: "github", InstallURL: "https://github.com/" + candidate.Source, URL: i.publicBaseURL + "/" + candidate.ID, SecurityScore: result.Score, QualityScore: result.QualityScore, LLMChecked: llmChecked, Official: candidate.Official, Hash: hashFiles(candidate.Files), Files: candidate.Files, UpdatedAt: now, Audit: model.Audit{Provider: "ExitMesh AI", Slug: "exitmesh-ai", Status: result.Status, Summary: result.Summary, RiskLevel: result.RiskLevel, Categories: result.Categories, AuditedAt: now}}
	if err := i.store.UpsertSkill(ctx, skill); err != nil {
		if errors.Is(err, model.ErrInvalidSkillContents) {
			i.logger.Warn("skill storage skipped", "skill_id", candidate.ID, "reason", "invalid_contents", "error", err)
			return indexResult{status: indexFailed}
		}
		return indexResult{err: fmt.Errorf("store skill %s: %w", candidate.ID, err)}
	}
	i.logger.Debug("skill indexed", "skill_id", candidate.ID, "security_score", result.Score, "quality_score", result.QualityScore, "risk_level", result.RiskLevel, "official", candidate.Official, "files", len(candidate.Files))
	return indexResult{status: indexStored}
}

func hashFiles(files []model.File) string {
	filesJSON, _ := json.Marshal(files)
	sum := sha256.Sum256(filesJSON)
	return hex.EncodeToString(sum[:])
}
