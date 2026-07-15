package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://skills:secret@db/skills")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_API_KEY", "ai-token")
	t.Setenv("AI_MODEL", "audit-model")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.IndexInterval != 24*time.Hour {
		t.Fatalf("IndexInterval = %v, want 24h", cfg.IndexInterval)
	}
	if cfg.RateLimit != 600 || cfg.ListenAddress != ":8080" {
		t.Fatalf("unexpected defaults: rate=%d listen=%q", cfg.RateLimit, cfg.ListenAddress)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.IndexConcurrency != 8 {
		t.Fatalf("IndexConcurrency = %d, want 8", cfg.IndexConcurrency)
	}
	if cfg.MasterToken != "master-token" {
		t.Fatalf("MasterToken = %q, want configured value", cfg.MasterToken)
	}
}

func TestLoadRequiresMasterToken(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want MASTER_TOKEN required error")
	}
}

func TestLoadAcceptsOAuthAppClientCredentials(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_CLIENT_ID", "client-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "client-secret")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAuthMode != "oauth_app" || cfg.GitHubClientID != "client-id" || cfg.GitHubClientSecret != "client-secret" {
		t.Fatalf("unexpected OAuth App configuration: %+v", cfg)
	}
}

func TestLoadRejectsPartialGitHubAuthentication(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{name: "partial oauth", env: map[string]string{"GITHUB_CLIENT_ID": "client-id"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearOptionalConfigEnv(t)
			t.Setenv("DATABASE_URL", "sqlite://:memory:")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("MASTER_TOKEN", "master-token")
			t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
			t.Setenv("AI_BASE_URL", "https://ai.example/v1")
			t.Setenv("AI_MODEL", "audit-model")
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want GitHub authentication validation error")
			}
		})
	}
}

func TestLoadPrefersOAuthAppCredentialsOverLegacyToken(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "legacy-token")
	t.Setenv("GITHUB_CLIENT_ID", "client-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "client-secret")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAuthMode != "oauth_app" || cfg.GitHubToken != "" {
		t.Fatalf("GitHub auth mode = %q token retained = %t, want OAuth with ignored token", cfg.GitHubAuthMode, cfg.GitHubToken != "")
	}
}

func TestLoadRejectsInvalidIndexConcurrency(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
	t.Setenv("INDEX_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want INDEX_CONCURRENCY validation error")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want LOG_LEVEL validation error")
	}
}

func TestLoadRejectsShortEncryptionKey(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://db")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_API_KEY", "token")
	t.Setenv("AI_MODEL", "model")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want encryption-key validation error")
	}
}

func TestLoadDerivesLocalDevelopmentPassphrase(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", "test")
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
	t.Setenv("EXITMESH_LOCAL_DEVELOPMENT", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := sha256.Sum256([]byte("test"))
	if !bytes.Equal(cfg.EncryptionKey, want[:]) {
		t.Fatal("local development passphrase was not derived with SHA-256")
	}
}

func TestLoadKeepsValidKeyInLocalDevelopment(t *testing.T) {
	clearOptionalConfigEnv(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", "master-token")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
	t.Setenv("EXITMESH_LOCAL_DEVELOPMENT", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !bytes.Equal(cfg.EncryptionKey, key) {
		t.Fatal("valid base64 key was unexpectedly derived")
	}
}

func clearOptionalConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AI_API_KEY",
		"EXITMESH_LOCAL_DEVELOPMENT",
		"GITHUB_API_BASE_URL",
		"GITHUB_CLIENT_ID",
		"GITHUB_CLIENT_SECRET",
		"INDEX_INTERVAL",
		"INDEX_CONCURRENCY",
		"INDEX_ON_START",
		"LISTEN_ADDRESS",
		"LOG_LEVEL",
		"MASTER_TOKEN",
		"OFFICIAL_SKILLS_URL",
		"PUBLIC_BASE_URL",
		"RATE_LIMIT_REQUESTS",
		"RATE_LIMIT_WINDOW",
		"REQUEST_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}
