package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/exitmesh/skills/internal/indexer"
)

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r blockingRunner) Run(ctx context.Context) (indexer.Stats, error) {
	close(r.started)
	select {
	case <-r.release:
		return indexer.Stats{}, nil
	case <-ctx.Done():
		return indexer.Stats{}, ctx.Err()
	}
}

func TestRunLogsStartupJobBeforeExecutingIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	done := make(chan struct{})
	go func() {
		Run(ctx, time.Hour, true, runner, logger)
		close(done)
	}()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("startup indexing job did not run")
	}
	output := logs.String()
	if !strings.Contains(output, `"msg":"indexing started"`) {
		t.Fatalf("missing indexing-started log: %s", output)
	}
	if !strings.Contains(output, `"trigger":"startup"`) {
		t.Fatalf("missing startup trigger: %s", output)
	}

	close(runner.release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
