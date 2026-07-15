package proxy

import (
	"kiro-proxy/config"
	"net/http"
	"strings"
	"testing"
)

func TestApplyKiroBaseHeadersUsesAPIKeyCredential(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/generateAssistantResponse", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	account := &config.Account{
		AccessToken: "stale-access-token",
		KiroApiKey:  "kiro-api-key",
		AuthMethod:  "api_key",
	}

	applyKiroBaseHeaders(req, account, kiroHeaderValues{})

	if got := req.Header.Get("Authorization"); got != "Bearer kiro-api-key" {
		t.Fatalf("expected API key bearer authorization, got %q", got)
	}
	if got := req.Header.Get("TokenType"); got != "API_KEY" {
		t.Fatalf("expected API_KEY token type, got %q", got)
	}
}

func TestApplyKiroBaseHeadersSupportsLegacyAPIKeyAccessToken(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/generateAssistantResponse", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	account := &config.Account{
		AccessToken: "legacy-api-key",
		AuthMethod:  "APIKEY",
	}

	applyKiroBaseHeaders(req, account, kiroHeaderValues{})

	if got := req.Header.Get("Authorization"); got != "Bearer legacy-api-key" {
		t.Fatalf("expected legacy API key bearer authorization, got %q", got)
	}
	if got := req.Header.Get("tokentype"); got != "API_KEY" {
		t.Fatalf("expected API_KEY token type, got %q", got)
	}
}

func TestApplyKiroBaseHeadersKeepsOAuthBehavior(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/generateAssistantResponse", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	applyKiroBaseHeaders(req, &config.Account{AccessToken: "oauth-token", AuthMethod: "social"}, kiroHeaderValues{})

	if got := req.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("expected OAuth bearer authorization, got %q", got)
	}
	if got := req.Header.Get("TokenType"); got != "" {
		t.Fatalf("expected no token type for OAuth account, got %q", got)
	}
}

func TestBuildStreamingHeaderValuesAlignsWithKiroIDEFormat(t *testing.T) {
	account := &config.Account{MachineId: "machine-123"}
	values := buildStreamingHeaderValues(account, "q.us-east-1.amazonaws.com")

	if values.Host != "q.us-east-1.amazonaws.com" {
		t.Fatalf("expected host to be preserved, got %q", values.Host)
	}
	if !strings.Contains(values.UserAgent, "aws-sdk-js/1.0.34") {
		t.Fatalf("expected streaming sdk version in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "api/codewhispererstreaming#1.0.34") {
		t.Fatalf("expected streaming API marker in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "KiroIDE-0.11.107-machine-123") {
		t.Fatalf("expected kiro version and machine id in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.AmzUserAgent, "aws-sdk-js/1.0.34 KiroIDE-0.11.107-machine-123") {
		t.Fatalf("expected x-amz-user-agent to include version and machine id, got %q", values.AmzUserAgent)
	}
}

func TestBuildRuntimeHeaderValuesUsesRuntimeAPIFormat(t *testing.T) {
	account := &config.Account{MachineId: "machine-456"}
	values := buildRuntimeHeaderValues(account, "codewhisperer.us-east-1.amazonaws.com")

	if !strings.Contains(values.UserAgent, "aws-sdk-js/1.0.0") {
		t.Fatalf("expected runtime sdk version in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "api/codewhispererruntime#1.0.0") {
		t.Fatalf("expected runtime API marker in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "m/N,E") {
		t.Fatalf("expected runtime mode marker in user agent, got %q", values.UserAgent)
	}
}
