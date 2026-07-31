package indexer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exitmesh/skills/internal/audit"
	"github.com/exitmesh/skills/internal/model"
)

type fakeSource struct{ candidates []Candidate }

func (f fakeSource) Discover(context.Context) ([]Candidate, error) { return f.candidates, nil }

type boundedStreamingSource struct {
	total       int
	storedCount func() int
}

func (boundedStreamingSource) Discover(context.Context) ([]Candidate, error) {
	return nil, errors.New("unbounded discovery path used")
}

func (source boundedStreamingSource) DiscoverStream(_ context.Context, _ map[string]struct{}, yield func(Candidate) error) error {
	for position := range source.total {
		if position > 0 && position%10 == 0 {
			if stored := source.storedCount(); stored != position {
				return fmt.Errorf("discovery continued after %d candidates with only %d stored", position, stored)
			}
		}
		id := fmt.Sprintf("org/repo/skill-%d", position)
		if err := yield(Candidate{ID: id, Source: "org/repo", Slug: fmt.Sprintf("skill-%d", position), Name: id, Stars: 11, Contents: id}); err != nil {
			return err
		}
	}
	return nil
}

type fakeAuditor struct {
	scores        map[string]int
	qualityScores map[string]int
	statuses      map[string]string
}

func (f fakeAuditor) Audit(_ context.Context, contents string) (audit.Result, error) {
	qualityScore := 8
	if score, exists := f.qualityScores[contents]; exists {
		qualityScore = score
	}
	status := "pass"
	if configured, exists := f.statuses[contents]; exists {
		status = configured
	}
	return audit.Result{Score: f.scores[contents], QualityScore: qualityScore, Status: status, Summary: "checked", RiskLevel: "LOW"}, nil
}

type failingAuditor struct{}

func (failingAuditor) Audit(context.Context, string) (audit.Result, error) {
	return audit.Result{}, errors.New("AI unavailable")
}

type fakeStore struct {
	mu         sync.Mutex
	upserted   []model.Skill
	deleted    []string
	fresh      map[string]struct{}
	hashes     map[string]string
	unassessed []model.Skill
	cutoff     time.Time
	batchLoads []int
	upsertErrs map[string]error
}

func TestRunFailsClosedWhenAuditIsUnavailable(t *testing.T) {
	source := fakeSource{candidates: []Candidate{{
		ID: "org/stale/stale", Source: "org/stale", Slug: "stale", Name: "Stale", Stars: 20, Contents: "changed",
	}}}
	store := &fakeStore{}
	i := New(source, failingAuditor{}, store, time.Now)

	stats, err := i.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 || len(store.upserted) != 0 {
		t.Fatalf("unexpected stats/store: %+v %+v", stats, store.upserted)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "org/stale/stale" {
		t.Fatalf("stale approval was not removed: %+v", store.deleted)
	}
}

func (f *fakeStore) UpsertSkill(_ context.Context, skill model.Skill) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.upsertErrs[skill.ID]; err != nil {
		return err
	}
	f.upserted = append(f.upserted, skill)
	return nil
}
func (f *fakeStore) DeleteSkill(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	for position, skill := range f.unassessed {
		if skill.ID == id {
			f.unassessed = append(f.unassessed[:position], f.unassessed[position+1:]...)
			break
		}
	}
	return nil
}
func (f *fakeStore) FreshSkillIDs(_ context.Context, cutoff time.Time) (map[string]struct{}, error) {
	f.mu.Lock()
	f.cutoff = cutoff
	f.mu.Unlock()
	return f.fresh, nil
}
func (f *fakeStore) SkillContentHashes(context.Context) (map[string]string, error) {
	return f.hashes, nil
}
func (f *fakeStore) UnassessedSkillCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, skill := range f.unassessed {
		if !skill.LLMChecked {
			count++
		}
	}
	return count, nil
}
func (f *fakeStore) UnassessedSkills(_ context.Context, limit int) ([]model.PendingSkillAssessment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchLoads = append(f.batchLoads, len(f.upserted)+len(f.deleted))
	pending := make([]model.PendingSkillAssessment, 0, limit)
	for _, skill := range f.unassessed {
		if skill.LLMChecked {
			continue
		}
		contents, _ := primarySkillContents(skill.Files)
		pending = append(pending, model.PendingSkillAssessment{ID: skill.ID, Contents: contents})
		if len(pending) == limit {
			break
		}
	}
	return pending, nil
}
func (f *fakeStore) UpdateSkillAssessment(_ context.Context, id string, securityScore, qualityScore int, skillAudit model.Audit, updatedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for position := range f.unassessed {
		skill := &f.unassessed[position]
		if skill.ID != id {
			continue
		}
		skill.SecurityScore = securityScore
		skill.QualityScore = qualityScore
		skill.LLMChecked = true
		skill.Audit = skillAudit
		skill.UpdatedAt = updatedAt
		f.upserted = append(f.upserted, *skill)
		return nil
	}
	return model.ErrNotFound
}

func TestRunFlushesEveryTenStreamedCandidatesBeforeDiscoveryContinues(t *testing.T) {
	store := &fakeStore{}
	source := boundedStreamingSource{total: 25, storedCount: func() int {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.upserted)
	}}
	stats, err := New(source, nil, store, time.Now).WithConcurrency(8).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Discovered != 25 || stats.Stored != 25 {
		t.Fatalf("stats = %+v, want 25 candidates discovered and stored", stats)
	}
}

func TestRunContinuesAfterInvalidCandidateContents(t *testing.T) {
	candidates := make([]Candidate, 11)
	for position := range candidates {
		id := fmt.Sprintf("org/repo/skill-%d", position)
		candidates[position] = Candidate{ID: id, Source: "org/repo", Slug: fmt.Sprintf("skill-%d", position), Name: id, Stars: 11, Contents: id}
	}
	store := &fakeStore{upsertErrs: map[string]error{
		candidates[2].ID: fmt.Errorf("%w: contains NUL", model.ErrInvalidSkillContents),
	}}
	stats, err := New(fakeSource{candidates: candidates}, nil, store, time.Now).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 10 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 10 stored and 1 failed", stats)
	}
	if len(store.upserted) != 10 || store.upserted[len(store.upserted)-1].ID != candidates[10].ID {
		t.Fatalf("indexing did not continue into the next batch: %+v", store.upserted)
	}
}

func TestReconcileLoadsAndStoresUncheckedSkillsInBatchesOfTen(t *testing.T) {
	store := &fakeStore{}
	for position := range 25 {
		id := fmt.Sprintf("org/repo/legacy-%d", position)
		store.unassessed = append(store.unassessed, model.Skill{
			ID: id, Source: "org/repo", Slug: fmt.Sprintf("legacy-%d", position), Name: id, Stars: 11,
			Files: []model.File{{Path: "SKILL.md", Contents: id}},
		})
	}
	if err := New(fakeSource{}, &recordingAuditor{}, store, time.Now).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.batchLoads, []int{0, 10, 20}) {
		t.Fatalf("batch loads happened after processed counts %v, want [0 10 20]", store.batchLoads)
	}
	if len(store.upserted) != 25 {
		t.Fatalf("assessed skills = %d, want 25", len(store.upserted))
	}
}

func TestRunPublishesWithoutLLMWhenAssessmentIsDisabled(t *testing.T) {
	store := &fakeStore{}
	stats, err := New(fakeSource{candidates: []Candidate{{
		ID: "org/repo/unchecked", Source: "org/repo", Slug: "unchecked", Name: "Unchecked", Stars: 11, Contents: "unchecked",
	}}}, nil, store, time.Now).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 1 || len(store.upserted) != 1 || store.upserted[0].LLMChecked {
		t.Fatalf("disabled assessment result: stats=%+v skills=%+v", stats, store.upserted)
	}
}

type orderingSource struct {
	beforeDiscover func() error
}

func (source orderingSource) Discover(context.Context) ([]Candidate, error) {
	if err := source.beforeDiscover(); err != nil {
		return nil, err
	}
	return nil, nil
}

func TestRunAssessesStoredUncheckedSkillsBeforeGitHubDiscovery(t *testing.T) {
	store := &fakeStore{unassessed: []model.Skill{{
		ID: "org/repo/legacy", Source: "org/repo", Slug: "legacy", Name: "Legacy", Stars: 11,
		Files: []model.File{{Path: "SKILL.md", Contents: "legacy contents"}},
	}}}
	auditor := &recordingAuditor{}
	source := orderingSource{beforeDiscover: func() error {
		auditor.mu.Lock()
		defer auditor.mu.Unlock()
		if !slices.Contains(auditor.calls, "legacy contents") {
			return errors.New("GitHub discovery started before stored unchecked skill assessment")
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.upserted) != 1 || !store.upserted[0].LLMChecked {
			return errors.New("stored unchecked skill was not marked as passed before discovery")
		}
		return nil
	}}
	if _, err := New(source, auditor, store, time.Now).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileLogsEveryStoredSkillAssessment(t *testing.T) {
	store := &fakeStore{unassessed: []model.Skill{{
		ID: "org/repo/legacy", Source: "org/repo", Slug: "legacy", Name: "Legacy", Stars: 11,
		Files: []model.File{{Path: "SKILL.md", Contents: "legacy contents"}},
	}}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	index := New(fakeSource{}, &recordingAuditor{}, store, time.Now).WithLogger(logger)
	if err := index.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{
		`"msg":"stored skill LLM assessment started"`,
		`"msg":"stored skill LLM assessment passed"`,
		`"skill_id":"org/repo/legacy"`,
		`"position":1`,
		`"total":1`,
		`"security_score":8`,
		`"quality_score":8`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %s in reconciliation logs:\n%s", expected, output)
		}
	}
}

func TestPrimarySkillContentsRequiresCanonicalFilenameCase(t *testing.T) {
	if _, ok := primarySkillContents([]model.File{{Path: "skills.md", Contents: "# Documentation"}}); ok {
		t.Fatal("lowercase documentation file was accepted as a canonical skill file")
	}
	if contents, ok := primarySkillContents([]model.File{{Path: "SKILL.md", Contents: "# Actual skill"}}); !ok || contents != "# Actual skill" {
		t.Fatalf("canonical skill file = %q, %t", contents, ok)
	}
}

func TestReconcileRetriesCanceledAssessmentWithoutDeletingSkill(t *testing.T) {
	store := &fakeStore{unassessed: []model.Skill{{
		ID: "org/repo/legacy", Source: "org/repo", Slug: "legacy", Name: "Legacy", Stars: 11,
		Files: []model.File{{Path: "SKILL.md", Contents: "legacy contents"}},
	}}}
	auditor := &cancelOnceAuditor{}
	index := New(fakeSource{}, auditor, store, time.Now)
	index.assessmentRetryDelay = 0
	if err := index.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auditor.calls.Load() != 2 {
		t.Fatalf("assessment calls = %d, want 2", auditor.calls.Load())
	}
	if len(store.deleted) != 0 {
		t.Fatalf("canceled assessment deleted skills: %v", store.deleted)
	}
	if len(store.upserted) != 1 || !store.upserted[0].LLMChecked {
		t.Fatalf("retried assessment was not stored as checked: %+v", store.upserted)
	}
}

func TestReconcileLeavesSkillUncheckedWhenServiceContextIsCanceled(t *testing.T) {
	store := &fakeStore{unassessed: []model.Skill{{
		ID: "org/repo/legacy", Source: "org/repo", Slug: "legacy", Name: "Legacy", Stars: 11,
		Files: []model.File{{Path: "SKILL.md", Contents: "legacy contents"}},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := New(fakeSource{}, cancelingAuditor{}, store, time.Now).Reconcile(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want context canceled", err)
	}
	if len(store.deleted) != 0 || len(store.upserted) != 0 {
		t.Fatalf("interrupted assessment mutated store: deleted=%v upserted=%v", store.deleted, store.upserted)
	}
}

type cancelingAuditor struct{}

func (cancelingAuditor) Audit(context.Context, string) (audit.Result, error) {
	return audit.Result{}, context.Canceled
}

type cancelOnceAuditor struct {
	calls atomic.Int32
}

func (a *cancelOnceAuditor) Audit(context.Context, string) (audit.Result, error) {
	if a.calls.Add(1) == 1 {
		return audit.Result{}, context.Canceled
	}
	return audit.Result{Score: 8, QualityScore: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

type recordingAuditor struct {
	mu    sync.Mutex
	calls []string
	times []time.Time
}

func (a *recordingAuditor) Audit(_ context.Context, contents string) (audit.Result, error) {
	a.mu.Lock()
	a.calls = append(a.calls, contents)
	a.times = append(a.times, time.Now())
	a.mu.Unlock()
	return audit.Result{Score: 8, QualityScore: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

func TestRunPacesAssessmentStarts(t *testing.T) {
	auditor := &recordingAuditor{}
	source := fakeSource{candidates: []Candidate{
		{ID: "org/repo/one", Source: "org/repo", Slug: "one", Stars: 20, Contents: "one"},
		{ID: "org/repo/two", Source: "org/repo", Slug: "two", Stars: 20, Contents: "two"},
	}}
	if _, err := New(source, auditor, &fakeStore{}, time.Now).
		WithConcurrency(2).
		WithAssessmentInterval(30 * time.Millisecond).
		Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(auditor.times) != 2 || auditor.times[1].Sub(auditor.times[0]) < 25*time.Millisecond {
		t.Fatalf("assessment starts were not paced: %v", auditor.times)
	}
}

func TestRunSkipsStoredSkillsUpdatedLessThanSevenDaysAgo(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	source := fakeSource{candidates: []Candidate{
		{ID: "org/repo/fresh", Source: "org/repo", Slug: "fresh", Name: "Fresh", Stars: 11, Contents: "fresh"},
		{ID: "org/repo/boundary", Source: "org/repo", Slug: "boundary", Name: "Boundary", Stars: 11, Contents: "boundary"},
		{ID: "org/repo/new", Source: "org/repo", Slug: "new", Name: "New", Stars: 11, Contents: "new"},
	}}
	store := &fakeStore{fresh: map[string]struct{}{"org/repo/fresh": {}}}
	auditor := &recordingAuditor{}
	stats, err := New(source, auditor, store, func() time.Time { return now }).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 2 || stats.SkippedFresh != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(store.upserted) != 2 {
		t.Fatalf("upserted = %d, want 2", len(store.upserted))
	}
	if !store.cutoff.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("freshness cutoff = %v, want %v", store.cutoff, now.Add(-7*24*time.Hour))
	}
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	if len(auditor.calls) != 2 || slices.Contains(auditor.calls, "fresh") {
		t.Fatalf("audit calls = %v, want boundary and new only", auditor.calls)
	}
}

func TestRunDoesNotAuditOrStoreUnchangedStaleSkill(t *testing.T) {
	files := []model.File{{Path: "SKILL.md", Contents: "unchanged"}}
	source := fakeSource{candidates: []Candidate{{
		ID: "org/repo/demo", Source: "org/repo", Slug: "demo", Name: "Demo", Stars: 20,
		Contents: "unchanged", Files: files,
	}}}
	store := &fakeStore{hashes: map[string]string{"org/repo/demo": hashFiles(files)}}
	auditor := &recordingAuditor{}

	stats, err := New(source, auditor, store, time.Now).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.SkippedUnchanged != 1 || stats.Stored != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(auditor.calls) != 0 || len(store.upserted) != 0 || len(store.deleted) != 0 {
		t.Fatalf("unchanged skill caused action: audits=%v upserts=%d deletes=%v", auditor.calls, len(store.upserted), store.deleted)
	}
}

func TestRunAuditsAndStoresChangedStaleSkill(t *testing.T) {
	files := []model.File{{Path: "SKILL.md", Contents: "changed"}}
	source := fakeSource{candidates: []Candidate{{
		ID: "org/repo/demo", Source: "org/repo", Slug: "demo", Name: "Demo", Stars: 20,
		Contents: "changed", Files: files,
	}}}
	store := &fakeStore{hashes: map[string]string{"org/repo/demo": "old-hash"}}
	auditor := &recordingAuditor{}

	stats, err := New(source, auditor, store, time.Now).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 1 || stats.SkippedUnchanged != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(auditor.calls) != 1 || auditor.calls[0] != "changed" || len(store.upserted) != 1 {
		t.Fatalf("changed skill was not refreshed: audits=%v upserts=%d", auditor.calls, len(store.upserted))
	}
}

type concurrentAuditor struct {
	active atomic.Int32
	max    atomic.Int32
}

func (a *concurrentAuditor) Audit(context.Context, string) (audit.Result, error) {
	active := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		maximum := a.max.Load()
		if active <= maximum || a.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return audit.Result{Score: 8, QualityScore: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

func TestRunAuditsEligibleSkillsConcurrently(t *testing.T) {
	candidates := make([]Candidate, 4)
	for position := range candidates {
		id := fmt.Sprintf("org/repo/skill-%d", position)
		candidates[position] = Candidate{ID: id, Source: "org/repo", Slug: fmt.Sprintf("skill-%d", position), Name: id, Stars: 11, Contents: id}
	}
	auditor := &concurrentAuditor{}
	store := &fakeStore{}
	stats, err := New(fakeSource{candidates: candidates}, auditor, store, time.Now).WithConcurrency(4).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != len(candidates) {
		t.Fatalf("Stored = %d, want %d", stats.Stored, len(candidates))
	}
	if auditor.max.Load() < 2 {
		t.Fatalf("maximum concurrent audits = %d, want at least 2", auditor.max.Load())
	}
}

func TestRunStoresOnlyAuditsScoringAtLeastFiveForSecurityAndQuality(t *testing.T) {
	source := fakeSource{candidates: []Candidate{
		{ID: "org/good/good", Source: "org/good", Slug: "good", Name: "Good", Stars: 11, Contents: "good", Files: []model.File{{Path: "SKILL.md", Contents: "good"}}},
		{ID: "org/weak/weak", Source: "org/weak", Slug: "weak", Name: "Weak", Stars: 500, Contents: "weak"},
		{ID: "org/poor/poor", Source: "org/poor", Slug: "poor", Name: "Poor", Stars: 500, Contents: "poor"},
		{ID: "org/failed/failed", Source: "org/failed", Slug: "failed", Name: "Failed", Stars: 500, Contents: "failed"},
		{ID: "org/tiny/tiny", Source: "org/tiny", Slug: "tiny", Name: "Tiny", Stars: 10, Contents: "tiny"},
	}}
	store := &fakeStore{}
	i := New(source, fakeAuditor{
		scores:        map[string]int{"good": 8, "weak": 4, "poor": 9, "failed": 9, "tiny": 10},
		qualityScores: map[string]int{"poor": 4},
		statuses:      map[string]string{"failed": "fail"},
	}, store, func() time.Time { return time.Unix(100, 0).UTC() })

	stats, err := i.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 1 || stats.SkippedWeak != 3 || stats.SkippedStars != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(store.upserted) != 1 || store.upserted[0].SecurityScore != 8 || store.upserted[0].QualityScore != 8 {
		t.Fatalf("unexpected stored skills: %+v", store.upserted)
	}
	if len(store.deleted) != 3 || !slices.Contains(store.deleted, "org/weak/weak") || !slices.Contains(store.deleted, "org/poor/poor") || !slices.Contains(store.deleted, "org/failed/failed") {
		t.Fatalf("non-passing skills were not removed: %+v", store.deleted)
	}
}

type blockingAuditor struct {
	started chan struct{}
	release chan struct{}
}

func (auditor blockingAuditor) Audit(context.Context, string) (audit.Result, error) {
	close(auditor.started)
	<-auditor.release
	return audit.Result{Score: 8, QualityScore: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

func TestRunDoesNotPublishCandidateBeforeLLMAssessmentPasses(t *testing.T) {
	store := &fakeStore{}
	auditor := blockingAuditor{started: make(chan struct{}), release: make(chan struct{})}
	index := New(fakeSource{candidates: []Candidate{{
		ID: "org/repo/pending", Source: "org/repo", Slug: "pending", Name: "Pending", Stars: 11, Contents: "pending",
	}}}, auditor, store, time.Now)
	done := make(chan error, 1)
	go func() {
		_, err := index.Run(context.Background())
		done <- err
	}()

	<-auditor.started
	store.mu.Lock()
	publishedWhilePending := len(store.upserted)
	store.mu.Unlock()
	if publishedWhilePending != 0 {
		t.Fatalf("%d skills were published before the LLM assessment completed", publishedWhilePending)
	}
	close(auditor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserted) != 1 {
		t.Fatalf("published skills after passing assessment = %d, want 1", len(store.upserted))
	}
}

func TestRunLogsDiscoveryAuditAndPerSkillProgress(t *testing.T) {
	source := fakeSource{candidates: []Candidate{{
		ID: "org/good/good", Source: "org/good", Slug: "good", Name: "Good", Stars: 11, Contents: "good",
	}}}
	store := &fakeStore{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	i := New(source, fakeAuditor{scores: map[string]int{"good": 8}}, store, time.Now).WithLogger(logger)

	if _, err := i.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{
		`"msg":"skill discovery started"`,
		`"msg":"skill discovery complete"`,
		`"msg":"skill audit started"`,
		`"msg":"skill indexed"`,
		`"msg":"indexing progress"`,
		`"skill_id":"org/good/good"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %s in logs:\n%s", expected, output)
		}
	}
}
