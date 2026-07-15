package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthAppAuthenticatorUsesClientCredentials(t *testing.T) {
	authenticator := NewOAuthAppAuthenticator("client-id", "client-secret")
	request := httptest.NewRequest(http.MethodGet, "https://api.github.com/meta", nil)
	if err := authenticator.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	username, password, ok := request.BasicAuth()
	if !ok || username != "client-id" || password != "client-secret" {
		t.Fatalf("BasicAuth = %q, %q, %t", username, password, ok)
	}
}
