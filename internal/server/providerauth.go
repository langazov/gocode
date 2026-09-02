package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/identifier"
	"github.com/anomalyco/opencode-go/internal/provider"
)

// This file backs the interface's "Connect a provider" dialog, the port of
// packages/tui/src/component/dialog-provider.tsx. It exposes what
// provider.Methods knows — the env and API-key methods every models.dev
// catalog entry gets, plus any OAuth flow a transform registers — and drives
// a device flow to completion on the client's behalf.

// authMethod is one login method as the interface sees it.
type authMethod struct {
	Type  string   `json:"type"`
	Label string   `json:"label"`
	Env   []string `json:"env,omitempty"`
	// Satisfied reports that an env method's variables are already set, so the
	// dialog can say so instead of offering a no-op.
	Satisfied bool         `json:"satisfied,omitempty"`
	Prompts   []authPrompt `json:"prompts,omitempty"`
}

type authPrompt struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Options []string `json:"options,omitempty"`
}

// listProviderAuth serves GET /api/provider/{providerID}/auth.
func (s *Server) listProviderAuth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerID")
	methods, err := provider.Methods(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]authMethod, 0, len(methods))
	for _, method := range methods {
		entry := authMethod{
			Type:      method.Type,
			Label:     method.Label,
			Env:       method.Env,
			Satisfied: method.EnvSatisfied(),
		}
		for _, prompt := range method.Prompts {
			entry.Prompts = append(entry.Prompts, authPrompt{
				Key:     prompt.Key,
				Label:   prompt.Label,
				Options: prompt.Options,
			})
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// setProviderKey serves POST /api/provider/{providerID}/auth, storing a
// pasted API key.
func (s *Server) setProviderKey(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerID")
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if err := auth.Set(providerID, auth.Info{Type: "api", Key: body.Key}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider.InvalidateModelCaches()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// logoutProvider serves DELETE /api/provider/{providerID}/auth.
func (s *Server) logoutProvider(w http.ResponseWriter, r *http.Request) {
	if err := auth.Remove(r.PathValue("providerID")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider.InvalidateModelCaches()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// oauthAttempt is one in-flight OAuth login.
//
// The flow has to outlive the request that starts it: a device flow can take
// a minute of the user typing a code into a browser. Authorize returns
// immediately with the code to display and runs the wait in a goroutine; the
// interface polls status until it settles.
type oauthAttempt struct {
	URL     string
	Code    string
	Status  string // "pending" | "done" | "failed"
	Error   string
	Created time.Time
}

type oauthAttempts struct {
	mu   sync.Mutex
	byID map[string]*oauthAttempt
}

func (a *oauthAttempts) put(id string, attempt *oauthAttempt) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byID == nil {
		a.byID = map[string]*oauthAttempt{}
	}
	// Drop attempts nobody came back for, so an abandoned login does not
	// pin its entry for the life of the process.
	for key, existing := range a.byID {
		if time.Since(existing.Created) > time.Hour {
			delete(a.byID, key)
		}
	}
	a.byID[id] = attempt
}

func (a *oauthAttempts) get(id string) (oauthAttempt, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt, ok := a.byID[id]
	if !ok {
		return oauthAttempt{}, false
	}
	return *attempt, true
}

func (a *oauthAttempts) update(id string, apply func(*oauthAttempt)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if attempt, ok := a.byID[id]; ok {
		apply(attempt)
	}
}

// startProviderOAuth serves POST /api/provider/{providerID}/auth/oauth.
func (s *Server) startProviderOAuth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerID")
	var body struct {
		Method  string            `json:"method"`
		Answers map[string]string `json:"answers"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	methods, err := provider.Methods(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var chosen *provider.Method
	for i, method := range methods {
		if method.Type != provider.MethodOAuth {
			continue
		}
		if body.Method == "" || method.Label == body.Method {
			chosen = &methods[i]
			break
		}
	}
	if chosen == nil || chosen.Login == nil {
		writeError(w, http.StatusBadRequest, "provider has no OAuth login method")
		return
	}

	id := identifier.Ascending()
	attempt := &oauthAttempt{Status: "pending", Created: time.Now()}
	s.oauth.put(id, attempt)

	// Report the verification URL and user code as soon as the flow produces
	// them, without waiting for the user to finish. provider.Method.Login
	// prints them for the CLI; capture them here instead.
	display := make(chan provider.LoginPrompt, 1)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
	go func() {
		defer cancel()
		credential, err := chosen.Login(provider.WithLoginPrompt(ctx, func(p provider.LoginPrompt) {
			select {
			case display <- p:
			default:
			}
		}), body.Answers)
		if err != nil {
			s.oauth.update(id, func(a *oauthAttempt) { a.Status, a.Error = "failed", err.Error() })
			return
		}
		info := auth.Info{
			Type:    credential.Type,
			Key:     credential.Key,
			Access:  credential.Access,
			Refresh: credential.Refresh,
			Expires: credential.Expires,
		}
		if domain := body.Answers["enterpriseUrl"]; domain != "" && body.Answers["deploymentType"] == "enterprise" {
			info.EnterpriseURL = domain
		}
		if err := auth.Set(providerID, info); err != nil {
			s.oauth.update(id, func(a *oauthAttempt) { a.Status, a.Error = "failed", err.Error() })
			return
		}
		provider.InvalidateModelCaches()
		s.oauth.update(id, func(a *oauthAttempt) { a.Status = "done" })
	}()

	select {
	case prompt := <-display:
		s.oauth.update(id, func(a *oauthAttempt) { a.URL, a.Code = prompt.URL, prompt.Code })
	case <-time.After(30 * time.Second):
		// The flow did not produce a code in time; the poller will surface
		// whatever it settles on.
	}

	current, _ := s.oauth.get(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"url":    current.URL,
		"code":   current.Code,
		"status": current.Status,
	})
}

// providerOAuthStatus serves GET /api/provider/auth/oauth/{attemptID}.
func (s *Server) providerOAuthStatus(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.oauth.get(r.PathValue("attemptID"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown authorization attempt")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": attempt.Status,
		"error":  attempt.Error,
		"url":    attempt.URL,
		"code":   attempt.Code,
	})
}

// connectedProviders returns the ids that have a stored credential, so the
// dialog can tick them.
func connectedProviders() map[string]bool {
	entries, err := auth.All()
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for id := range entries {
		out[id] = true
	}
	return out
}

// sortedIDs is a small helper for deterministic listings.
func sortedIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

var errNoOAuth = errors.New("provider has no OAuth login method")
