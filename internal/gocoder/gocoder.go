// Package gocoder talks to gocoder.org, the gocode website, and persists the
// account the CLI signed in with.
//
// Registration and login return a short-lived (24h) JWT, which is the wrong
// credential for a CLI that may not run again for weeks. So a sign-in is
// immediately traded for a long-lived `gk_` API key (POST /api/keys), and only
// that key is stored; the JWT is discarded.
//
// The account lives in its own file rather than in auth.json: auth.json is
// keyed by provider ID and enumerated by the provider catalog, where a
// "gocoder" entry would surface as a provider that does not exist.
package gocoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/installation"
)

// DefaultURL is the production website; GOCODE_GOCODER_URL overrides it
// (e.g. http://localhost:8080 against a local gocode_api).
const DefaultURL = "https://gocoder.org"

// BaseURL returns the website base URL, without a trailing slash.
func BaseURL() string {
	if value := os.Getenv("GOCODE_GOCODER_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return DefaultURL
}

// User is the public account shape returned by the website.
type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// Session is a successful register/login: a short-lived bearer token.
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	User      User      `json:"user"`
}

// Key is a freshly minted API key. Secret is only ever returned once.
type Key struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Secret string `json:"key"`
}

// APIError is the website's {"error":{"code","message"}} envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("gocoder.org returned HTTP %d", e.Status)
}

// Client is a minimal gocoder.org API client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for baseURL (BaseURL() when empty).
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = BaseURL()
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Register creates an account and returns its session.
func (c *Client) Register(ctx context.Context, email, password, displayName string) (*Session, error) {
	var session Session
	body := map[string]string{"email": email, "password": password, "displayName": displayName}
	if err := c.do(ctx, http.MethodPost, "/api/auth/register", "", body, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Login signs in to an existing account.
func (c *Client) Login(ctx context.Context, email, password string) (*Session, error) {
	var session Session
	body := map[string]string{"email": email, "password": password}
	if err := c.do(ctx, http.MethodPost, "/api/auth/login", "", body, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// CreateKey mints a named API key for the session's user.
func (c *Client) CreateKey(ctx context.Context, token, name string) (*Key, error) {
	var key Key
	if err := c.do(ctx, http.MethodPost, "/api/keys", token, map[string]string{"name": name}, &key); err != nil {
		return nil, err
	}
	if key.Secret == "" {
		return nil, errors.New("gocoder.org returned an API key without a secret")
	}
	return &key, nil
}

// EmbeddingProvider is one upstream the site can embed with.
type EmbeddingProvider struct {
	Name         string `json:"name"`
	DefaultModel string `json:"defaultModel"`
}

// EmbeddingProviders lists the embedding upstreams active on the site. The
// endpoint is public.
func (c *Client) EmbeddingProviders(ctx context.Context) ([]EmbeddingProvider, error) {
	var out struct {
		Providers []EmbeddingProvider `json:"providers"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/embeddings/providers", "", nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// RevokeKey revokes key id. The website accepts an API key as the bearer for
// key management, so a key can revoke itself.
func (c *Client) RevokeKey(ctx context.Context, bearer, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/keys/"+url.PathEscape(id), bearer, nil, nil)
}

// SettingsDoc is the settings-sync document: an opaque client-encrypted
// envelope plus the server-assigned revision for last-writer-wins.
type SettingsDoc struct {
	Envelope  string    `json:"envelope"`
	Revision  int64     `json:"revision"`
	Device    string    `json:"device,omitempty"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ErrNoSettings reports that the account has no synced settings yet — the
// normal first-machine case, not a failure.
var ErrNoSettings = errors.New("no settings synced for this account")

// GetSettings fetches the account's settings doc. The 404 every fresh
// account produces becomes ErrNoSettings, so callers branch on "nothing to
// restore" without string matching.
func (c *Client) GetSettings(ctx context.Context, bearer string) (*SettingsDoc, error) {
	var doc SettingsDoc
	if err := c.do(ctx, http.MethodGet, "/api/settings", bearer, nil, &doc); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrNoSettings
		}
		return nil, err
	}
	return &doc, nil
}

// PutSettings uploads an envelope. baseRevision is the revision the caller
// last saw; a mismatch is HTTP 409 and the caller must Get, reconcile, and
// retry.
func (c *Client) PutSettings(ctx context.Context, bearer, envelope string, baseRevision int64, device string) (*SettingsDoc, error) {
	body := map[string]any{
		"envelope":     envelope,
		"baseRevision": baseRevision,
	}
	if device != "" {
		body["device"] = device
	}
	var doc SettingsDoc
	if err := c.do(ctx, http.MethodPut, "/api/settings", bearer, body, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// SettingsRevision returns the current server revision without moving the
// envelope — the cheap check the background poller runs every 30 seconds.
func (c *Client) SettingsRevision(ctx context.Context, bearer string) (int64, error) {
	var out struct {
		Revision int64 `json:"revision"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/settings/pending", bearer, nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return 0, nil
		}
		return 0, err
	}
	return out.Revision, nil
}

// DeleteSettings clears the account's synced settings.
func (c *Client) DeleteSettings(ctx context.Context, bearer string) error {
	return c.do(ctx, http.MethodDelete, "/api/settings", bearer, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gocode/"+installation.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("gocoder.org: unexpected response: %w", err)
	}
	return nil
}

// Account is the persisted sign-in: who, against which site, and the key.
type Account struct {
	URL         string    `json:"url"`
	UserID      string    `json:"userId"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	KeyID       string    `json:"keyId"`
	KeyPrefix   string    `json:"keyPrefix"`
	Key         string    `json:"key"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AccountPath is where the account is stored.
func AccountPath() string {
	return filepath.Join(global.Resolve().Data, "gocoder.json")
}

// LoadAccount returns the stored account, or nil when there is none.
func LoadAccount() (*Account, error) {
	data, err := os.ReadFile(AccountPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, fmt.Errorf("%s: %w", AccountPath(), err)
	}
	return &account, nil
}

// RemoveAccount deletes the stored account; a missing one is not an error.
func RemoveAccount() error {
	if err := os.Remove(AccountPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SaveAccount writes the account atomically, readable by the owner only
// since it carries a live API key.
func SaveAccount(account *Account) error {
	path := AccountPath()
	data, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gocoder-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// FreeModel is one $0.0-price model from the site's OpenRouter catalog view.
type FreeModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	ContextLength int    `json:"contextLength,omitempty"`
	Modality      string `json:"modality,omitempty"`
	Free          bool   `json:"free"`
}

// FreeModels lists gocoder.org's free ($0.0) inference models. Requires the
// account's key (the endpoint is authenticated); a 403 means the account has
// no OpenRouter token issued yet.
func (c *Client) FreeModels(ctx context.Context, bearer string) ([]FreeModel, error) {
	var out struct {
		Models    []FreeModel `json:"models"`
		Count     int         `json:"count"`
		FetchedAt string      `json:"fetchedAt"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/inference/models/free", bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// InferenceBaseURL is the OpenAI-compatible root the site's inference
// proxy serves — including the /v1 segment, because the OpenAI client
// appends "/chat/completions" to it directly (a base without /v1 produced
// proxy 404s). GOCODE_INFERENCE_URL overrides it (tests, local stacks).
func InferenceBaseURL() string {
	if value := os.Getenv("GOCODE_INFERENCE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://proxy.gocoder.org/v1"
}
