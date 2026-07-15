package github

import (
	"context"
	"net/http"
)

type RequestAuthenticator interface {
	Authenticate(context.Context, *http.Request) error
	Name() string
}

type tokenAuthenticator struct{ token string }

func NewTokenAuthenticator(token string) RequestAuthenticator {
	return tokenAuthenticator{token: token}
}

func (a tokenAuthenticator) Authenticate(_ context.Context, request *http.Request) error {
	request.Header.Set("Authorization", "Bearer "+a.token)
	return nil
}

func (tokenAuthenticator) Name() string { return "token" }

type oauthAppAuthenticator struct {
	clientID, clientSecret string
}

func NewOAuthAppAuthenticator(clientID, clientSecret string) RequestAuthenticator {
	return oauthAppAuthenticator{clientID: clientID, clientSecret: clientSecret}
}

func (a oauthAppAuthenticator) Authenticate(_ context.Context, request *http.Request) error {
	request.SetBasicAuth(a.clientID, a.clientSecret)
	return nil
}

func (oauthAppAuthenticator) Name() string { return "oauth_app" }
