// Package provider bridges the models.dev catalog with auth resolution,
// the Go analogue of the general case in provider.ts.
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

type Service struct {
	catalog *modelsdev.Service
}

func New(catalog *modelsdev.Service) *Service {
	return &Service{catalog: catalog}
}

func (s *Service) Catalog(ctx context.Context) (modelsdev.Catalog, error) {
	return s.catalog.Get(ctx)
}

// ResolveAPIKey returns the API key for a provider. Environment variables
// listed in the catalog entry take precedence, then the conventional
// {PROVIDER}_API_KEY variable, then the auth.json store (api key or oauth
// access token).
func (s *Service) ResolveAPIKey(ctx context.Context, providerID string) (string, error) {
	catalog, err := s.catalog.Get(ctx)
	if err != nil {
		return "", err
	}
	var envNames []string
	if provider, ok := catalog[providerID]; ok {
		envNames = provider.Env
	}
	if len(envNames) == 0 {
		envNames = []string{strings.ToUpper(providerID) + "_API_KEY"}
	}
	for _, name := range envNames {
		if value := os.Getenv(name); value != "" {
			return value, nil
		}
	}
	// ResolveCredential, not auth.Get: an OAuth token near expiry is renewed here
	// rather than being sent stale and coming back as a 401.
	info, err := ResolveCredential(ctx, providerID, catalog[providerID])
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", fmt.Errorf("provider: no credentials for %q (set %v or run auth login)", providerID, envNames)
	}
	switch info.Type {
	case "api", "wellknown":
		return info.Key, nil
	case "oauth":
		return info.Access, nil
	}
	return "", fmt.Errorf("provider: unsupported auth type %q for %q", info.Type, providerID)
}
