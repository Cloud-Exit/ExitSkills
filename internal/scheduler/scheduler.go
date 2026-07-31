package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/exitmesh/skills/internal/indexer"
)

type Runner interface {
	Run(context.Context) (indexer.Stats, error)
}

func Run(ctx context.Context, interval time.Duration, runOnStart bool, runner Runner, logger *slog.Logger) {
	run := func(trigger string) {
		logger.Info("indexing started", "trigger", trigger)
		started := time.Now()
		stats, err := runner.Run(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("indexing failed", "trigger", trigger, "error", err, "duration", time.Since(started))
			}
			return
		}
		logger.Info("indexing complete", "trigger", trigger, "discovered", stats.Discovered, "stored", stats.Stored, "skipped_weak", stats.SkippedWeak, "skipped_stars", stats.SkippedStars, "skipped_fresh", stats.SkippedFresh, "skipped_unchanged", stats.SkippedUnchanged, "failed", stats.Failed, "duration", time.Since(started))
	}
	if runOnStart {
		run("startup")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run("schedule")
		}
	}
}
