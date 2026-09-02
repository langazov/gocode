package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/anomalyco/opencode-go/internal/auth"
)

// wellKnownDocument is the descriptor served at {url}/.well-known/opencode,
// ported from the WellKnown handling in cli/cmd/providers.ts.
type wellKnownDocument struct {
	Auth struct {
		// Command is run locally; its stdout becomes the credential.
		Command []string `json:"command"`
		// Env names the variable the resulting token stands in for.
		Env string `json:"env"`
	} `json:"auth"`
}

// wellKnownLogin implements `opencode providers login <url>`: fetch a
// descriptor from the given host and run the local command it names to mint a
// token.
//
// Divergence from TypeScript, deliberate: the command is shown and confirmed
// before it runs. The descriptor is fetched from a URL the user typed, and it
// names an arbitrary command to execute — the TS implementation runs it
// unannounced, which turns a typo'd or hostile host into local code execution.
// Printing it costs one prompt and makes what is about to happen visible.
// --yes skips the confirmation for scripted use.
func wellKnownLogin(ctx context.Context, rawURL string) error {
	endpoint := strings.TrimRight(rawURL, "/")
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	endpoint += "/.well-known/opencode"

	document, err := fetchWellKnown(ctx, endpoint)
	if err != nil {
		return err
	}
	if len(document.Auth.Command) == 0 {
		return fmt.Errorf("%s: descriptor has no auth command", endpoint)
	}
	if document.Auth.Env == "" {
		return fmt.Errorf("%s: descriptor has no auth env name", endpoint)
	}

	command := strings.Join(document.Auth.Command, " ")
	fmt.Printf("\n%s asks to run this command locally to obtain a token:\n\n    %s\n\n", rawURL, command)
	answer, err := askLine("Run it? [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return fmt.Errorf("login cancelled")
	}

	output, err := exec.CommandContext(ctx, document.Auth.Command[0], document.Auth.Command[1:]...).Output()
	if err != nil {
		return fmt.Errorf("running %q: %w", command, err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return fmt.Errorf("running %q produced no token", command)
	}

	// The key is stored under the URL, matching how the TS side addresses
	// well-known credentials.
	if err := auth.Set(strings.TrimRight(rawURL, "/"), auth.Info{
		Type:  "wellknown",
		Key:   document.Auth.Env,
		Token: token,
	}); err != nil {
		return err
	}
	fmt.Println("Login successful")
	return nil
}

func fetchWellKnown(ctx context.Context, endpoint string) (*wellKnownDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %d", endpoint, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var document wellKnownDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return &document, nil
}
