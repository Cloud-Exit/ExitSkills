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

type fakeAuditor struct{ scores map[string]int }

func (f fakeAuditor) Audit(_ context.Context, contents string) (audit.Result, error) {
	return audit.Result{Score: f.scores[contents], Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

type failingAuditor struct{}

func (failingAuditor) Audit(context.Context, string) (audit.Result, error) {
	return audit.Result{}, errors.New("AI unavailable")
}

type fakeStore struct {
	mu       sync.Mutex
	upserted []model.Skill
	deleted  []string
	fresh    map[string]struct{}
	cutoff   time.Time
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
	f.upserted = append(f.upserted, skill)
	return nil
}
func (f *fakeStore) DeleteSkill(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeStore) FreshSkillIDs(_ context.Context, cutoff time.Time) (map[string]struct{}, error) {
	f.mu.Lock()
	f.cutoff = cutoff
	f.mu.Unlock()
	return f.fresh, nil
}

type recordingAuditor struct {
	mu    sync.Mutex
	calls []string
}

func (a *recordingAuditor) Audit(_ context.Context, contents string) (audit.Result, error) {
	a.mu.Lock()
	a.calls = append(a.calls, contents)
	a.mu.Unlock()
	return audit.Result{Score: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
}

func TestRunSkipsStoredSkillsUpdatedLessThanTwentyFourHoursAgo(t *testing.T) {
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
	if !store.cutoff.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("freshness cutoff = %v, want %v", store.cutoff, now.Add(-24*time.Hour))
	}
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	if len(auditor.calls) != 2 || slices.Contains(auditor.calls, "fresh") {
		t.Fatalf("audit calls = %v, want boundary and new only", auditor.calls)
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
	return audit.Result{Score: 8, Status: "pass", Summary: "checked", RiskLevel: "LOW"}, nil
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

func TestRunStoresOnlyAuditsScoringAtLeastFive(t *testing.T) {
	source := fakeSource{candidates: []Candidate{
		{ID: "org/good/good", Source: "org/good", Slug: "good", Name: "Good", Stars: 11, Contents: "good", Files: []model.File{{Path: "SKILL.md", Contents: "good"}}},
		{ID: "org/weak/weak", Source: "org/weak", Slug: "weak", Name: "Weak", Stars: 500, Contents: "weak"},
		{ID: "org/tiny/tiny", Source: "org/tiny", Slug: "tiny", Name: "Tiny", Stars: 10, Contents: "tiny"},
	}}
	store := &fakeStore{}
	i := New(source, fakeAuditor{scores: map[string]int{"good": 8, "weak": 4, "tiny": 10}}, store, func() time.Time { return time.Unix(100, 0).UTC() })

	stats, err := i.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Stored != 1 || stats.SkippedWeak != 1 || stats.SkippedStars != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(store.upserted) != 1 || store.upserted[0].SecurityScore != 8 {
		t.Fatalf("unexpected stored skills: %+v", store.upserted)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "org/weak/weak" {
		t.Fatalf("weak skill was not removed: %+v", store.deleted)
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
