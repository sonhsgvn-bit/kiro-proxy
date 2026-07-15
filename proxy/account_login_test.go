package proxy

import (
	"encoding/json"
	"kiro-proxy/auth"
	"kiro-proxy/config"
	accountpool "kiro-proxy/pool"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestApiAddAccountNormalizesKiroApiKey(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "kiro.db")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	h := &Handler{pool: accountpool.GetPool()}
	req := httptest.NewRequest("POST", "/accounts", strings.NewReader(`{
		"authMethod":"api_key",
		"kiroApiKey":"key-123",
		"nickname":"work",
		"enabled":false
	}`))
	rec := httptest.NewRecorder()
	h.apiAddAccount(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	accounts := config.GetAccounts()
	if len(accounts) != 1 {
		t.Fatalf("expected one account, got %d", len(accounts))
	}
	account := accounts[0]
	if !account.IsApiKeyCredential() || account.AuthMethod != "api_key" {
		t.Fatalf("expected normalized API-key account, got %+v", account)
	}
	if account.AccessToken != "key-123" || account.KiroApiKey != "key-123" {
		t.Fatalf("expected key to backfill AccessToken and KiroApiKey, got %+v", account)
	}
	if account.ExpiresAt != 0 || account.RefreshToken != "" {
		t.Fatalf("expected non-expiring key account without refresh token, got %+v", account)
	}
}

func TestApiAddAccountRejectsEmptyKiroApiKey(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "kiro.db")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	h := &Handler{pool: accountpool.GetPool()}
	req := httptest.NewRequest("POST", "/accounts", strings.NewReader(`{"authMethod":"api_key","enabled":false}`))
	rec := httptest.NewRecorder()
	h.apiAddAccount(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBuildKiroSsoAccountPersistsExternalIdpMetadata(t *testing.T) {
	account := buildKiroSsoAccount(&auth.KiroSsoResult{
		AccessToken:   "access",
		RefreshToken:  "refresh",
		AuthMethod:    "external_idp",
		Provider:      "AzureAD",
		ClientID:      "client",
		TokenEndpoint: "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		IssuerURL:     "https://login.microsoftonline.com/tenant/v2.0",
		Scopes:        "offline_access",
		Region:        "us-east-1",
		Email:         "user@example.com",
	}, "machine", 123)

	if account.Provider != "Microsoft365" || account.IssuerURL == "" || account.TokenEndpoint == "" {
		t.Fatalf("external IdP metadata was not preserved: %+v", account)
	}
	if account.ExpiresAt != 123 || account.AccessToken != "access" {
		t.Fatalf("unexpected SSO account: %+v", account)
	}
	if _, err := json.Marshal(account); err != nil {
		t.Fatalf("account should remain JSON serializable: %v", err)
	}
}
