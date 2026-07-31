package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

const testMasterToken = "0123456789abcdef0123456789abcdef"

func TestLoadDefaults(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://skills:secret@db/skills")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", testMasterToken)
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_API_KEY", "ai-token")
	t.Setenv("AI_MODEL", "audit-model")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.IndexInterval != 7*24*time.Hour {
		t.Fatalf("IndexInterval = %v, want 168h", cfg.IndexInterval)
	}
	if cfg.RateLimit != 600 || cfg.ListenAddress != ":8080" {
		t.Fatalf("unexpected defaults: rate=%d listen=%q", cfg.RateLimit, cfg.ListenAddress)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.IndexConcurrency != 2 {
		t.Fatalf("IndexConcurrency = %d, want 2", cfg.IndexConcurrency)
	}
	if cfg.AIAuditInterval != time.Minute {
		t.Fatalf("AIAuditInterval = %v, want 1m", cfg.AIAuditInterval)
	}
	if cfg.AIRequestTimeout != 10*time.Minute {
		t.Fatalf("AIRequestTimeout = %v, want 10m", cfg.AIRequestTimeout)
	}
	if cfg.MasterToken != testMasterToken {
		t.Fatalf("MasterToken = %q, want configured value", cfg.MasterToken)
	}
}

func TestLoadClampsIndexIntervalToSevenDays(t *testing.T) {
	setValidConfigEnv(t)
	t.Setenv("INDEX_INTERVAL", "24h")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexInterval != 7*24*time.Hour {
		t.Fatalf("IndexInterval = %v, want seven-day minimum", cfg.IndexInterval)
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

func TestLoadAllowsLLMAssessmentToBeDisabled(t *testing.T) {
	setValidConfigEnv(t)
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_MODEL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIEnabled {
		t.Fatal("AIEnabled = true with no AI endpoint or model")
	}
}

func TestLoadRejectsPartialLLMConfiguration(t *testing.T) {
	for _, variable := range []string{"AI_BASE_URL", "AI_MODEL"} {
		setValidConfigEnv(t)
		if variable == "AI_BASE_URL" {
			t.Setenv("AI_MODEL", "")
		} else {
			t.Setenv("AI_BASE_URL", "")
		}
		if _, err := Load(); err == nil {
			t.Fatalf("Load() error = nil with partial %s configuration", variable)
		}
	}
}

func TestLoadAcceptsOAuthAppClientCredentials(t *testing.T) {
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_CLIENT_ID", "client-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "client-secret")
	t.Setenv("MASTER_TOKEN", testMasterToken)
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
	t.Setenv("MASTER_TOKEN", testMasterToken)
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

func TestLoadRejectsInsecureOutboundURLsOutsideLocalDevelopment(t *testing.T) {
	for _, test := range []struct {
		name     string
		variable string
		value    string
	}{
		{name: "AI endpoint", variable: "AI_BASE_URL", value: "http://ai.example/v1"},
		{name: "GitHub API", variable: "GITHUB_API_BASE_URL", value: "http://github.example"},
		{name: "official catalog", variable: "OFFICIAL_SKILLS_URL", value: "http://skills.example/official"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidConfigEnv(t)
			t.Setenv(test.variable, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() error = nil, want insecure %s rejection", test.variable)
			}
		})
	}
}

func TestLoadAllowsHTTPOutboundURLsForExplicitLocalDevelopment(t *testing.T) {
	setValidConfigEnv(t)
	t.Setenv("EXITMESH_LOCAL_DEVELOPMENT", "true")
	t.Setenv("AI_BASE_URL", "http://127.0.0.1:9000/v1")
	t.Setenv("GITHUB_API_BASE_URL", "http://127.0.0.1:9001")
	t.Setenv("OFFICIAL_SKILLS_URL", "http://127.0.0.1:9002/official")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want local HTTP endpoints accepted", err)
	}
}

func TestLoadRejectsMalformedPublicBaseURL(t *testing.T) {
	for _, value := range []string{"javascript:alert(1)", "https://user:password@skills.example"} {
		setValidConfigEnv(t)
		t.Setenv("PUBLIC_BASE_URL", value)
		if _, err := Load(); err == nil {
			t.Fatalf("Load() error = nil, want invalid PUBLIC_BASE_URL %q rejection", value)
		}
	}
}

func TestLoadRejectsURLsThatCouldExposeEmbeddedCredentials(t *testing.T) {
	for _, value := range []string{
		"https://user:password@ai.example/v1",
		"https://ai.example/v1?api_key=secret",
		"https://ai.example/v1#secret",
	} {
		setValidConfigEnv(t)
		t.Setenv("AI_BASE_URL", value)
		if _, err := Load(); err == nil {
			t.Fatalf("Load() error = nil, want unsafe AI_BASE_URL %q rejection", value)
		}
	}
}

func TestLoadRejectsWeakMasterTokenOutsideLocalDevelopment(t *testing.T) {
	setValidConfigEnv(t)
	t.Setenv("MASTER_TOKEN", "test")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want weak MASTER_TOKEN rejection")
	}
}

func setValidConfigEnv(t *testing.T) {
	t.Helper()
	clearOptionalConfigEnv(t)
	t.Setenv("DATABASE_URL", "sqlite://:memory:")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("MASTER_TOKEN", testMasterToken)
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("AI_MODEL", "audit-model")
}

func clearOptionalConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AI_API_KEY",
		"AI_AUDIT_INTERVAL",
		"AI_REQUEST_TIMEOUT",
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
