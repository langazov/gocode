package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// refreshMargin matches the window in packages/core/src/integration.ts:399 —
// a credential is refreshed once it is within five minutes of expiring, so a
// long turn does not die halfway through on a token that was valid when it
// started.
const refreshMargin = 5 * time.Minute

// TokenResponse is an RFC 6749 token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ExpiresAt converts expires_in into the absolute timestamp stored in
// auth.json.
//
// The unit is milliseconds since the epoch, because that is what the
// TypeScript side writes (`Date.now() + expires_in * 1000`) and both binaries
// read the same file. Storing seconds here would make every TS-written
// credential look like it expired in 1970.
func (t TokenResponse) ExpiresAt() int64 {
	seconds := t.ExpiresIn
	if seconds <= 0 {
		seconds = 3600 // the fallback openai.ts uses when the server omits it
	}
	return time.Now().Add(time.Duration(seconds) * time.Second).UnixMilli()
}

// NeedsRefresh reports whether a credential should be renewed before use.
//
// An Expires of 0 means "no known expiry" — GitHub's OAuth tokens do not
// expire and the Copilot flow stores 0 — so it is never treated as stale.
func NeedsRefresh(info Info) bool {
	if info.Type != "oauth" || info.Expires == 0 {
		return false
	}
	return time.UnixMilli(info.Expires).Before(time.Now().Add(refreshMargin))
}

// RefreshGrant performs the refresh_token grant (RFC 6749 §6).
func RefreshGrant(ctx context.Context, client *http.Client, endpoint, clientID, refreshToken, userAgent string) (*TokenResponse, error) {
	var out TokenResponse
	err := postToken(ctx, client, endpoint, userAgent, false, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("auth: refresh at %s returned no access token", endpoint)
	}
	// A server may rotate the refresh token or keep the existing one; carry the
	// old one forward when it does not send a replacement, or the next refresh
	// has nothing to present.
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return &out, nil
}

// doPost sends the request body and returns the raw status and bytes,
// leaving status interpretation to the caller.
func doPost(ctx context.Context, client *http.Client, endpoint, userAgent string, jsonBody bool, body map[string]string) (int, []byte, error) {
	var (
		payload     io.Reader
		contentType string
	)
	if jsonBody {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload, contentType = bytes.NewReader(encoded), "application/json"
	} else {
		form := url.Values{}
		for key, value := range body {
			form.Set(key, value)
		}
		payload, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, data, nil
}

// postToken posts to an OAuth endpoint, form-encoded by default and JSON when
// the provider expects it (GitHub's device endpoints do).
func postToken(ctx context.Context, client *http.Client, endpoint, userAgent string, jsonBody bool, body map[string]string, out any) error {
	status, data, err := doPost(ctx, client, endpoint, userAgent, jsonBody, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("auth: %s returned %d: %s", endpoint, status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

// postPoll posts to a device-token polling endpoint and decodes the body
// regardless of HTTP status. RFC 8628 servers signal "authorization_pending"
// and "slow_down" in the JSON body; the opencode console's device endpoint
// (and others) send those with a 400 status rather than 200, so a strict
// status check would abort the poll loop before ever reading the verdict —
// the TS reference (account.ts) parses the body the same way, unconditional
// on status.
func postPoll(ctx context.Context, client *http.Client, endpoint, userAgent string, jsonBody bool, body map[string]string, out any) error {
	status, data, err := doPost(ctx, client, endpoint, userAgent, jsonBody, body)
	if err != nil {
		return err
	}
	if unmarshalErr := json.Unmarshal(data, out); unmarshalErr != nil {
		if status < 200 || status >= 300 {
			return fmt.Errorf("auth: %s returned %d: %s", endpoint, status, strings.TrimSpace(string(data)))
		}
		return unmarshalErr
	}
	return nil
}

// JWTClaim reads a single string claim out of a JWT payload without verifying
// the signature. Ported from claim() in plugin/provider/openai.ts, which uses
// it only to label the credential with an account id — the token is not being
// trusted for authorization here, just read for display.
func JWTClaim(token string, path ...string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return ""
	}
	var current any = claims
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	if value, ok := current.(string); ok {
		return value
	}
	return ""
}

// PostTokenForm posts an arbitrary form-encoded grant to a token endpoint,
// for flows whose parameters are not the refresh grant's (the PKCE
// authorization-code exchange).
func PostTokenForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (*TokenResponse, error) {
	body := map[string]string{}
	for key := range form {
		body[key] = form.Get(key)
	}
	var out TokenResponse
	if err := postToken(ctx, client, endpoint, "", false, body, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("auth: %s returned no access token", endpoint)
	}
	return &out, nil
}
