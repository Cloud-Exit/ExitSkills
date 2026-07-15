package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL        string
	GitHubToken        string
	GitHubAuthMode     string
	GitHubClientID     string
	GitHubClientSecret string
	MasterToken        string
	EncryptionKey      []byte
	AIBaseURL          string
	AIAPIKey           string
	AIModel            string
	AIEnabled          bool
	ListenAddress      string
	PublicBaseURL      string
	IndexInterval      time.Duration
	IndexConcurrency   int
	IndexOnStart       bool
	RateLimit          int
	RateWindow         time.Duration
	GitHubAPIBaseURL   string
	OfficialURL        string
	RequestTimeout     time.Duration
	LogLevel           string
}

func Load() (Config, error) {
	cfg, err := loadCommon()
	if err != nil {
		return Config{}, err
	}
	required := map[string]string{
		"DATABASE_URL": cfg.DatabaseURL,
		"MASTER_TOKEN": cfg.MasterToken,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("%s is required", name)
		}
	}
	if err := validateGitHubAuthentication(&cfg); err != nil {
		return Config{}, err
	}
	hasAIBaseURL := strings.TrimSpace(cfg.AIBaseURL) != ""
	hasAIModel := strings.TrimSpace(cfg.AIModel) != ""
	if hasAIBaseURL != hasAIModel {
		return Config{}, errors.New("AI_BASE_URL and AI_MODEL must be configured together or both omitted")
	}
	cfg.AIEnabled = hasAIBaseURL && hasAIModel
	localDevelopment, _ := boolEnv("EXITMESH_LOCAL_DEVELOPMENT", false)
	if !localDevelopment && len(cfg.MasterToken) < 32 {
		return Config{}, errors.New("MASTER_TOKEN must contain at least 32 bytes outside local development")
	}
	urls := map[string]string{
		"GITHUB_API_BASE_URL": cfg.GitHubAPIBaseURL,
		"OFFICIAL_SKILLS_URL": cfg.OfficialURL,
	}
	if cfg.AIEnabled {
		urls["AI_BASE_URL"] = cfg.AIBaseURL
	}
	for name, value := range urls {
		if err := validateHTTPURL(name, value, localDevelopment); err != nil {
			return Config{}, err
		}
	}
	if err := validatePublicURL(cfg.PublicBaseURL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateHTTPURL(name, value string, allowHTTP bool) error {
	if strings.ContainsAny(value, "?#") {
		return fmt.Errorf("%s must not contain a query or fragment", name)
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if allowHTTP && parsed.Scheme == "http" {
		return nil
	}
	return fmt.Errorf("%s must use HTTPS outside local development", name)
}

func validatePublicURL(value string) error {
	if strings.ContainsAny(value, "?#") {
		return errors.New("PUBLIC_BASE_URL must not contain a query or fragment")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("PUBLIC_BASE_URL must be an absolute HTTP(S) URL")
	}
	return nil
}

func validateGitHubAuthentication(cfg *Config) error {
	token := strings.TrimSpace(cfg.GitHubToken)
	clientID := strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("GITHUB_CLIENT_SECRET"))
	oauthValues := 0
	for _, value := range []string{clientID, clientSecret} {
		if value != "" {
			oauthValues++
		}
	}
	if oauthValues != 0 && oauthValues != 2 {
		return errors.New("GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET must be configured together")
	}
	if oauthValues == 2 {
		cfg.GitHubAuthMode = "oauth_app"
		cfg.GitHubClientID = clientID
		cfg.GitHubClientSecret = clientSecret
		cfg.GitHubToken = ""
		return nil
	}
	if token != "" {
		cfg.GitHubToken = token
		cfg.GitHubAuthMode = "token"
		return nil
	}
	return errors.New("GitHub authentication is required: configure GITHUB_TOKEN or OAuth App client credentials")
}

func LoadAdmin() (Config, error) {
	cfg, err := loadCommon()
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}

func loadCommon() (Config, error) {
	localDevelopment, err := boolEnv("EXITMESH_LOCAL_DEVELOPMENT", false)
	if err != nil {
		return Config{}, err
	}
	keyText := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	if keyText == "" {
		return Config{}, errors.New("ENCRYPTION_KEY is required")
	}
	key, decodeErr := base64.StdEncoding.DecodeString(keyText)
	if decodeErr != nil || len(key) != 32 {
		if localDevelopment {
			derived := sha256.Sum256([]byte(keyText))
			key = derived[:]
		} else if decodeErr != nil {
			return Config{}, fmt.Errorf("ENCRYPTION_KEY must be base64: %w", decodeErr)
		} else {
			return Config{}, fmt.Errorf("ENCRYPTION_KEY must decode to exactly 32 bytes, got %d", len(key))
		}
	}

	indexInterval, err := durationEnv("INDEX_INTERVAL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	rateWindow, err := durationEnv("RATE_LIMIT_WINDOW", time.Minute)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := durationEnv("REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	rateLimit, err := intEnv("RATE_LIMIT_REQUESTS", 600)
	if err != nil || rateLimit < 1 {
		if err == nil {
			err = errors.New("must be at least 1")
		}
		return Config{}, fmt.Errorf("RATE_LIMIT_REQUESTS: %w", err)
	}
	indexConcurrency, err := intEnv("INDEX_CONCURRENCY", 8)
	if err != nil || indexConcurrency < 1 || indexConcurrency > 32 {
		if err == nil {
			err = errors.New("must be between 1 and 32")
		}
		return Config{}, fmt.Errorf("INDEX_CONCURRENCY: %w", err)
	}
	indexOnStart, err := boolEnv("INDEX_ON_START", true)
	if err != nil {
		return Config{}, err
	}
	logLevel := strings.ToLower(envOr("LOG_LEVEL", "info"))
	switch logLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}

	return Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), GitHubToken: os.Getenv("GITHUB_TOKEN"), MasterToken: strings.TrimSpace(os.Getenv("MASTER_TOKEN")), EncryptionKey: key,
		AIBaseURL: strings.TrimRight(os.Getenv("AI_BASE_URL"), "/"), AIAPIKey: os.Getenv("AI_API_KEY"), AIModel: os.Getenv("AI_MODEL"),
		ListenAddress: envOr("LISTEN_ADDRESS", ":8080"), PublicBaseURL: strings.TrimRight(envOr("PUBLIC_BASE_URL", "https://skills.exitmesh.com"), "/"),
		IndexInterval: indexInterval, IndexConcurrency: indexConcurrency, IndexOnStart: indexOnStart, RateLimit: rateLimit, RateWindow: rateWindow,
		GitHubAPIBaseURL: strings.TrimRight(envOr("GITHUB_API_BASE_URL", "https://api.github.com"), "/"),
		OfficialURL:      envOr("OFFICIAL_SKILLS_URL", "https://www.skills.sh/official"), RequestTimeout: requestTimeout, LogLevel: logLevel,
	}, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}
func intEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
func boolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}
