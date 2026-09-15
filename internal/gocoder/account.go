package gocoder

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// account.go holds what a signed-in gocode shows about its account: profile,
// usage and invite link, plus the sign-in bookkeeping the server's
// /api/account routes share with onboarding. The calls take the stored API
// key as bearer; the website accepts keys on these routes.

// KeyName names the API key a sign-in on this machine creates.
func KeyName() string {
	name := "gocode"
	if host, err := os.Hostname(); err == nil && host != "" {
		name += " on " + host
	}
	return name
}

// StoreSession trades a session's short-lived token for a named API key and
// saves the account (gocoder.json) the rest of gocode reads.
func StoreSession(ctx context.Context, client *Client, session *Session, keyName string) (*Account, error) {
	key, err := client.CreateKey(ctx, session.Token, keyName)
	if err != nil {
		return nil, err
	}
	account := &Account{
		URL:         client.BaseURL,
		UserID:      session.User.ID,
		Email:       session.User.Email,
		DisplayName: session.User.DisplayName,
		KeyID:       key.ID,
		KeyPrefix:   key.Prefix,
		Key:         key.Secret,
		CreatedAt:   time.Now(),
	}
	if err := SaveAccount(account); err != nil {
		return nil, fmt.Errorf("saving account: %w", err)
	}
	return account, nil
}

// Me returns the account's current profile.
func (c *Client) Me(ctx context.Context, bearer string) (*User, error) {
	var user User
	if err := c.do(ctx, http.MethodGet, "/api/auth/me", bearer, nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// CheckKey reports whether the site still accepts bearer as an API key: nil
// when it does, an *APIError with status 401 when the key is revoked. It
// asks the settings-sync revision route, which takes keys wherever the site
// is deployed — unlike /api/auth/me, whose 401 can't tell a revoked key from
// a deployment that wants a session token there.
func (c *Client) CheckKey(ctx context.Context, bearer string) error {
	_, err := c.SettingsRevision(ctx, bearer, "")
	return err
}

// UpdateDisplayName renames the account. Password changes are not offered:
// the website refuses them with an API key and wants a signed-in session.
func (c *Client) UpdateDisplayName(ctx context.Context, bearer, displayName string) (*User, error) {
	var user User
	body := map[string]string{"displayName": displayName}
	if err := c.do(ctx, http.MethodPatch, "/api/auth/me", bearer, body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// UsageTotals aggregates one usage window (the website's /api/stats shape).
type UsageTotals struct {
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	PromptTokens int     `json:"promptTokens"`
	CachedRead   int     `json:"cachedRead"`
	CachedWrite  int     `json:"cachedWrite"`
	Errors       int     `json:"errors"`
	AvgLatencyMs float64 `json:"avgLatencyMs"`
	ActiveKeys   int     `json:"activeKeys"`
}

// UsageProvider is one provider's share of a window.
type UsageProvider struct {
	Provider string `json:"provider"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

// UsageModel is one provider+model's share of a window.
type UsageModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

// UsageDay is one zero-filled UTC day.
type UsageDay struct {
	Date     string `json:"date"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

// UsageSummary is the account's usage over a window of days.
type UsageSummary struct {
	Totals     UsageTotals     `json:"totals"`
	ByProvider []UsageProvider `json:"byProvider"`
	ByModel    []UsageModel    `json:"byModel"`
	Daily      []UsageDay      `json:"daily"`
}

// UsageSummary aggregates the account's last days days (the site allows 1-90).
func (c *Client) UsageSummary(ctx context.Context, bearer string, days int) (*UsageSummary, error) {
	var summary UsageSummary
	path := "/api/stats/summary?days=" + strconv.Itoa(days)
	if err := c.do(ctx, http.MethodGet, path, bearer, nil, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

// Invite is the account's shareable invite: its code, the sign-up link that
// carries it, and how many people have joined through it.
type Invite struct {
	Code    string `json:"code"`
	URL     string `json:"url"`
	Invited int64  `json:"invited"`
}

// Invite returns the account's invite link and referral count.
func (c *Client) Invite(ctx context.Context, bearer string) (*Invite, error) {
	var invite Invite
	if err := c.do(ctx, http.MethodGet, "/api/invites", bearer, nil, &invite); err != nil {
		return nil, err
	}
	return &invite, nil
}
