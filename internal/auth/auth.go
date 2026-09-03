// Package auth ports packages/opencode/src/auth/index.ts, the file-based
// provider credential store persisted at <data>/auth.json.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/global"
)

const OauthDummyKey = "gocode-oauth-dummy-key"

// Info is the OAuth | Api | WellKnown union, discriminated by Type.
type Info struct {
	Type          string            `json:"type"`
	Refresh       string            `json:"refresh,omitempty"`
	Access        string            `json:"access,omitempty"`
	Expires       int64             `json:"expires,omitempty"`
	AccountID     string            `json:"accountId,omitempty"`
	EnterpriseURL string            `json:"enterpriseUrl,omitempty"`
	Key           string            `json:"key,omitempty"`
	Token         string            `json:"token,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func valid(info Info) bool {
	switch info.Type {
	case "oauth":
		return info.Refresh != "" && info.Access != ""
	case "api", "wellknown":
		return info.Key != ""
	}
	return false
}

func file() string {
	return filepath.Join(global.Resolve().Data, "auth.json")
}

// All returns every stored provider auth entry.
func All() (map[string]Info, error) {
	if content := os.Getenv("GOCODE_AUTH_CONTENT"); content != "" {
		var parsed map[string]Info
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			return filterValid(parsed), nil
		}
	}
	data, err := os.ReadFile(file())
	if err != nil {
		return map[string]Info{}, nil
	}
	var parsed map[string]Info
	if err := json.Unmarshal(data, &parsed); err != nil {
		return map[string]Info{}, nil
	}
	return filterValid(parsed), nil
}

func filterValid(input map[string]Info) map[string]Info {
	out := make(map[string]Info, len(input))
	for key, info := range input {
		if valid(info) {
			out[key] = info
		}
	}
	return out
}

// Get returns the stored auth for a provider, or nil.
func Get(providerID string) (*Info, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	if info, ok := all[providerID]; ok {
		return &info, nil
	}
	return nil, nil
}

// Set stores auth for a provider, normalizing trailing slashes and writing
// with 0600 permissions.
func Set(key string, info Info) error {
	if !valid(info) {
		return errors.New("auth: invalid info for provider " + key)
	}
	norm := strings.TrimRight(key, "/")
	data, err := All()
	if err != nil {
		return err
	}
	if norm != key {
		delete(data, key)
	}
	delete(data, norm+"/")
	data[norm] = info
	return write(data)
}

// Remove deletes a provider's stored auth.
func Remove(key string) error {
	norm := strings.TrimRight(key, "/")
	data, err := All()
	if err != nil {
		return err
	}
	delete(data, key)
	delete(data, norm)
	return write(data)
}

func write(data map[string]Info) error {
	out, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file(), out, 0o600)
}
