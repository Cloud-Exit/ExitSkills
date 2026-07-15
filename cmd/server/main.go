package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exitmesh/skills/internal/audit"
	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/config"
	githubclient "github.com/exitmesh/skills/internal/github"
	"github.com/exitmesh/skills/internal/httpapi"
	"github.com/exitmesh/skills/internal/indexer"
	"github.com/exitmesh/skills/internal/scheduler"
	"github.com/exitmesh/skills/internal/store"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		logger.Error("invalid log level", "error", err)
		os.Exit(1)
	}
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	keys, err := auth.NewKeyManager(cfg.EncryptionKey)
	if err != nil {
		logger.Error("key manager startup failed", "error", err)
		os.Exit(1)
	}
	httpClient := newOutboundHTTPClient(cfg.RequestTimeout)
	githubAuth, err := githubAuthenticator(cfg, httpClient)
	if err != nil {
		logger.Error("github authentication startup failed", "error", err)
		os.Exit(1)
	}
	logger.Info("github authentication configured", "mode", githubAuth.Name())
	source := githubclient.NewClientWithAuthenticator(cfg.GitHubAPIBaseURL, githubAuth, cfg.OfficialURL, httpClient).WithLogger(logger).WithConcurrency(cfg.IndexConcurrency)
	var auditor indexer.Auditor
	if cfg.AIEnabled {
		auditor = audit.NewClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel, httpClient)
	}
	worker := indexer.New(source, auditor, db, time.Now).WithPublicBaseURL(cfg.PublicBaseURL).WithLogger(logger).WithConcurrency(cfg.IndexConcurrency)
	handler := httpapi.NewHandler(db, auth.NewVerifier(keys, db), httpapi.NewLimiter(cfg.RateLimit, cfg.RateWindow), httpapi.WithAdmin(cfg.MasterToken, keys, db), httpapi.WithLLMEnforcement(cfg.AIEnabled))
	server := newHTTPServer(cfg.ListenAddress, handler)
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		logger.Error("HTTP server startup failed", "error", err)
		os.Exit(1)
	}
	defer server.Close()
	logger.Info("server listening", "address", cfg.ListenAddress, "version", version, "log_level", cfg.LogLevel, "index_concurrency", cfg.IndexConcurrency)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()

	if cfg.AIEnabled {
		logger.Info("boot-time stored skill LLM assessment started")
		if err := worker.Reconcile(ctx); err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				logger.Info("boot-time stored skill LLM assessment interrupted", "reason", ctx.Err())
				return
			}
			logger.Error("boot-time stored skill LLM assessment failed", "error", err)
			os.Exit(1)
		}
		logger.Info("boot-time stored skill LLM assessment complete")
	} else {
		logger.Warn("LLM assessment disabled", "reason", "AI_BASE_URL and AI_MODEL are not configured")
	}
	go scheduler.Run(ctx, cfg.IndexInterval, cfg.IndexOnStart, worker, logger)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP shutdown failed", "error", err)
	}
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

func newOutboundHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Outbound requests can carry GitHub or AI credentials. Requiring the
			// configured endpoint to be canonical prevents redirects from moving
			// those credentials to another origin or downgrading HTTPS.
			return http.ErrUseLastResponse
		},
	}
}

func githubAuthenticator(cfg config.Config, _ *http.Client) (githubclient.RequestAuthenticator, error) {
	switch cfg.GitHubAuthMode {
	case "token":
		return githubclient.NewTokenAuthenticator(cfg.GitHubToken), nil
	case "oauth_app":
		return githubclient.NewOAuthAppAuthenticator(cfg.GitHubClientID, cfg.GitHubClientSecret), nil
	default:
		return nil, fmt.Errorf("unsupported GitHub authentication mode %q", cfg.GitHubAuthMode)
	}
}
