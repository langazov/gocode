// Device authorization grant (RFC 8628), shared by every provider whose
// login is a device-code flow. The TypeScript side re-implements the polling
// loop inside each plugin (see the authorize() callback in
// packages/opencode/src/plugin/github-copilot/copilot.ts); this port has one
// implementation and each provider supplies only its endpoints and client id.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// pollingSafetyMargin matches OAUTH_POLLING_SAFETY_MARGIN_MS in copilot.ts: a
// small cushion so clock skew and timer drift do not make us poll marginally
// early, which the server counts as a slow_down offense.
const pollingSafetyMargin = 3 * time.Second

// DeviceFlow describes one provider's device authorization endpoints.
type DeviceFlow struct {
	ClientID      string
	DeviceCodeURL string
	TokenURL      string
	Scope         string
	UserAgent     string
	// JSONRequest sends the request bodies as JSON rather than form encoding.
	// GitHub accepts both; the TS plugin uses JSON, so this port does too.
	JSONRequest bool
	HTTP        *http.Client
	// SafetyMargin overrides the polling cushion. Zero means the default;
	// tests set it small so the suite does not spend seconds waiting.
	SafetyMargin time.Duration
}

func (d DeviceFlow) margin() time.Duration {
	if d.SafetyMargin != 0 {
		return d.SafetyMargin
	}
	return pollingSafetyMargin
}

// DeviceCode is the authorization request a user must complete in a browser.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete embeds the user code in the URL so the user can
	// skip typing it. The opencode console returns only this form.
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

// DeviceToken is a completed device authorization.
type DeviceToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ErrDeviceFlowDenied reports that the user refused or the code expired,
// as distinct from a transport failure.
var ErrDeviceFlowDenied = errors.New("auth: device authorization was denied or expired")

func (d DeviceFlow) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Start requests a device code, the first leg of the flow.
func (d DeviceFlow) Start(ctx context.Context) (*DeviceCode, error) {
	body := map[string]string{"client_id": d.ClientID}
	if d.Scope != "" {
		body["scope"] = d.Scope
	}
	var out DeviceCode
	if err := d.post(ctx, d.DeviceCodeURL, body, &out); err != nil {
		return nil, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return nil, errors.New("auth: device authorization response was missing a code")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

// Poll waits for the user to complete authorization, honoring the interval
// and the backoff rules in RFC 8628 §3.5. It returns when the user approves,
// when they deny, or when ctx is cancelled.
func (d DeviceFlow) Poll(ctx context.Context, code *DeviceCode) (*DeviceToken, error) {
	interval := time.Duration(code.Interval)*time.Second + d.margin()
	deadline := time.Time{}
	if code.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, ErrDeviceFlowDenied
		}

		var response struct {
			DeviceToken
			Error    string `json:"error"`
			Interval int    `json:"interval"`
		}
		err := d.post(ctx, d.TokenURL, map[string]string{
			"client_id":   d.ClientID,
			"device_code": code.DeviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		}, &response)
		if err != nil {
			return nil, err
		}

		if response.AccessToken != "" {
			token := response.DeviceToken
			return &token, nil
		}

		switch response.Error {
		case "authorization_pending", "":
			// Keep waiting at the current interval.
		case "slow_down":
			// RFC 8628 requires adding 5 seconds; a server may also name the
			// new interval outright, which takes precedence.
			if response.Interval > 0 {
				interval = time.Duration(response.Interval)*time.Second + d.margin()
			} else {
				interval += 5 * time.Second
			}
		case "access_denied", "expired_token":
			return nil, ErrDeviceFlowDenied
		default:
			return nil, fmt.Errorf("auth: device authorization failed: %s", response.Error)
		}
	}
}

func (d DeviceFlow) post(ctx context.Context, endpoint string, body map[string]string, out any) error {
	return postToken(ctx, d.client(), endpoint, d.UserAgent, d.JSONRequest, body, out)
}

// Refresh renews an expired credential against the same token endpoint the
// device flow used.
func (d DeviceFlow) Refresh(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	return RefreshGrant(ctx, d.client(), d.TokenURL, d.ClientID, refreshToken, d.UserAgent)
}
