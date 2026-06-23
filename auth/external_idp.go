package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// External Identity Provider (Microsoft Entra ID / Okta) support.
//
// This mirrors exactly what the Kiro IDE stores in
// ~/.aws/sso/cache/kiro-auth-token.json for an enterprise account that signs in
// through an external IdP:
//
//	{
//	  "accessToken":   "<JWT used directly as the API bearer>",
//	  "refreshToken":  "<IdP refresh token>",
//	  "expiresAt":     "2026-06-23T03:45:09.991Z",
//	  "authMethod":    "external_idp",
//	  "provider":      "ExternalIdp",
//	  "tokenEndpoint": "https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token",
//	  "issuerUrl":     "https://login.microsoftonline.com/<tenant>/v2.0",
//	  "clientId":      "<application (client) id>",
//	  "scopes":        "api://<clientId>/codewhisperer:conversations api://<clientId>/codewhisperer:completions offline_access"
//	}
//
// The IdP-issued access token is accepted directly by the Kiro/CodeWhisperer
// API, so refresh happens against the IdP's own token endpoint (NOT AWS OIDC and
// NOT the Kiro desktop social endpoint).

// refreshExternalIdpToken refreshes an external IdP access token using the
// stored token endpoint, client ID and scopes captured from the IDE.
func refreshExternalIdpToken(refreshToken, tokenEndpoint, clientID, scopes string, client *http.Client) (string, string, int64, string, error) {
	if tokenEndpoint == "" || clientID == "" {
		return "", "", 0, "", fmt.Errorf("external_idp refresh requires tokenEndpoint and clientId")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	if scopes != "" {
		form.Set("scope", scopes)
	}

	req, _ := http.NewRequest("POST", tokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", 0, "", fmt.Errorf("refresh failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", 0, "", err
	}

	// The IdP may not return a new refresh token on every call; keep the old one.
	newRefresh := result.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	expiresAt := time.Now().Unix() + int64(result.ExpiresIn)
	return result.AccessToken, newRefresh, expiresAt, "", nil
}

// EmailFromJWT best-effort extracts an email/UPN claim from a JWT access or id
// token. Used when the Kiro usage API does not return an email.
func EmailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"email", "preferred_username", "upn", "unique_name"} {
		if v, ok := claims[key].(string); ok && strings.Contains(v, "@") {
			return v
		}
	}
	return ""
}

// ParseExpiresAt converts an RFC3339 timestamp (as stored by the IDE) to a Unix
// timestamp. Returns 0 if it cannot be parsed.
func ParseExpiresAt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}
