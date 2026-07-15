package main

import (
	"net/http"
	"testing"

	"github.com/exitmesh/skills/internal/config"
)

func TestGitHubAuthenticatorSelectsConfiguredMode(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{name: "token", cfg: config.Config{GitHubAuthMode: "token", GitHubToken: "token"}, want: "token"},
		{name: "oauth app", cfg: config.Config{GitHubAuthMode: "oauth_app", GitHubClientID: "id", GitHubClientSecret: "secret"}, want: "oauth_app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator, err := githubAuthenticator(test.cfg, http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			if authenticator.Name() != test.want {
				t.Fatalf("authenticator name = %q, want %q", authenticator.Name(), test.want)
			}
		})
	}
}

func TestGitHubAuthenticatorRejectsUnknownMode(t *testing.T) {
	if _, err := githubAuthenticator(config.Config{GitHubAuthMode: "unknown"}, http.DefaultClient); err == nil {
		t.Fatal("githubAuthenticator() error = nil, want unknown mode error")
	}
}
