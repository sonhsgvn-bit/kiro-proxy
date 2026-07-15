package auth

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestValidateExternalIdpEndpoint(t *testing.T) {
	valid := []string{
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"https://login.microsoftonline.us/tenant/v2.0",
		"https://login.partner.microsoftonline.cn/tenant/oauth2/v2.0/token",
	}
	for _, rawURL := range valid {
		if err := ValidateExternalIdpEndpoint(rawURL); err != nil {
			t.Errorf("expected %q accepted, got %v", rawURL, err)
		}
	}

	invalid := []string{
		"",
		"http://login.microsoftonline.com/tenant/token",
		"https://127.0.0.1/oauth/token",
		"https://evil-microsoftonline.com/oauth/token",
		"https://login.microsoftonline.com.evil.example/oauth/token",
		"https://evil.example.com/oauth/token",
		"https:///oauth/token",
	}
	for _, rawURL := range invalid {
		if err := ValidateExternalIdpEndpoint(rawURL); err == nil {
			t.Errorf("expected %q rejected, got nil", rawURL)
		}
	}
}

func TestExternalIdpTokenEndpointRejectedBeforeOutboundPost(t *testing.T) {
	tests := []struct {
		name string
		call func(*http.Client) error
	}{
		{
			name: "refresh token",
			call: func(client *http.Client) error {
				_, _, _, _, err := refreshExternalIdpToken(
					"secret-refresh-token",
					"https://evil.example.com/oauth/token",
					"client-id",
					"offline_access",
					client,
				)
				return err
			},
		},
		{
			name: "authorization code",
			call: func(client *http.Client) error {
				_, _, _, err := exchangeExternalIdpCode(
					client,
					"https://evil.example.com/oauth/token",
					"client-id",
					"secret-authorization-code",
					"verifier",
					kiroRedirectURI+kiroOAuthCallbackPath,
					"offline_access",
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &countingRoundTripper{}
			err := test.call(&http.Client{Transport: transport})
			if err == nil || !strings.Contains(err.Error(), "external IdP token endpoint rejected") {
				t.Fatalf("expected rejected token endpoint error, got %v", err)
			}
			if transport.calls != 0 {
				t.Fatalf("unsafe endpoint reached HTTP transport %d time(s)", transport.calls)
			}
		})
	}
}

func TestExternalIdpValidatorTestSeamAllowsAndRestores(t *testing.T) {
	transport := &recordingRoundTripper{}
	client := &http.Client{Transport: transport}
	restore := SetExternalIdpValidatorForTest(func(string) error { return nil })
	t.Cleanup(func() { SetExternalIdpValidatorForTest(restore) })

	_, _, _, _, err := refreshExternalIdpToken(
		"refresh-token",
		"http://127.0.0.1/token",
		"client-id",
		"offline_access",
		client,
	)
	if err != nil {
		t.Fatalf("refreshExternalIdpToken with test seam: %v", err)
	}
	_, _, _, err = exchangeExternalIdpCode(
		client,
		"http://127.0.0.1/token",
		"client-id",
		"authorization-code",
		"verifier",
		kiroRedirectURI+kiroOAuthCallbackPath,
		"offline_access",
	)
	if err != nil {
		t.Fatalf("exchangeExternalIdpCode with test seam: %v", err)
	}

	if len(transport.forms) != 2 {
		t.Fatalf("outbound POST count = %d, want 2", len(transport.forms))
	}
	if got := transport.forms[0].Get("refresh_token"); got != "refresh-token" {
		t.Fatalf("refresh_token = %q, want %q", got, "refresh-token")
	}
	if got := transport.forms[1].Get("code"); got != "authorization-code" {
		t.Fatalf("code = %q, want %q", got, "authorization-code")
	}

	SetExternalIdpValidatorForTest(restore)
	if err := ValidateExternalIdpEndpoint("http://127.0.0.1/token"); err == nil {
		t.Fatal("expected real validator to be restored")
	}
}

type countingRoundTripper struct {
	calls int
}

func (transport *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls++
	return nil, errors.New("unexpected outbound request")
}

type recordingRoundTripper struct {
	forms []url.Values
}

func (transport *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	transport.forms = append(transport.forms, form)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"access-token","refresh_token":"rotated-refresh-token","expires_in":3600}`)),
		Request:    req,
	}, nil
}
