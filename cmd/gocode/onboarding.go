package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/configedit"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/sync"
	"github.com/langazov/gocode-go/internal/tui"
	"github.com/langazov/gocode-go/internal/tui/signin"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// maybeOnboard offers to register or log in to gocoder.org the first time
// gocode starts, which is defined as "no global config file exists yet".
//
// It runs before the TUI takes over the terminal, and only when a person is
// at one: a piped or scripted start must never block on a prompt. Once the
// user has answered — signed in or skipped — an empty global config is
// written so the question is not asked again. A skip after gocoder.org could
// not be reached leaves the config absent, so the offer returns next start.
//
// It returns false when the user pressed ctrl+c, which ends startup the way
// an interrupt always has.
func maybeOnboard(ctx context.Context) bool {
	if os.Getenv("GOCODE_CONFIG_CONTENT") != "" || !interactiveTerminal() {
		return true
	}
	if _, exists, err := configedit.Global(); err != nil || exists {
		return true
	}
	if account, err := gocoder.LoadAccount(); err != nil || account != nil {
		return true
	}

	client := gocoder.NewClient("")
	outcome, err := runSignIn(ctx, client, signin.ScreenMenu, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gocoder.org sign-in: %v\n", err)
		return true
	}
	switch outcome.Status {
	case signin.StatusCancelled:
		return false
	case signin.StatusSkipped:
		if unreachable(outcome.LastErr) {
			fmt.Fprintf(os.Stderr, "Couldn't reach %s; you'll be asked again next time.\n", displayHost(client.BaseURL))
			return true
		}
	}
	if path, err := configedit.CreateGlobal(); err != nil {
		fmt.Fprintf(os.Stderr, "could not create %s: %v\n", path, err)
	}
	return true
}

func interactiveTerminal() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// runSignIn shows the sign-in screen in the user's TUI theme.
func runSignIn(ctx context.Context, client *gocoder.Client, start signin.Screen, notice string) (signin.Outcome, error) {
	var configured string
	if cfg, err := config.Load(); err == nil && cfg != nil {
		configured = cfg.Theme
	}
	name := tui.ResolveStartupTheme(configured, tui.ThemeStatePath())
	var restore sync.RestoreOutcome
	outcome, err := signin.Run(ctx, signin.Options{
		Theme: theme.Resolve(name),
		// Nothing chosen anywhere: follow the terminal's background.
		AutoTheme: configured == "" && name == "gocode-dark",
		Site:      displayHost(client.BaseURL),
		Start:     start,
		AllowSkip: start == signin.ScreenMenu,
		Notice:    notice,
		Submit:    submitter(client, func(o sync.RestoreOutcome) { restore = o }),
	})
	if err == nil && outcome.Status == signin.StatusSignedIn {
		reportRestore(restore)
	}
	return outcome, err
}

// submitter is the sign-in screen's one side effect: register or log in,
// trade the short-lived session for an API key, store it, and — because the
// password is in hand only here — derive the settings-sync key and restore
// the account's synced settings onto this machine.
func submitter(client *gocoder.Client, onRestore func(sync.RestoreOutcome)) func(context.Context, bool, signin.Credentials) (signin.Result, error) {
	return func(ctx context.Context, register bool, c signin.Credentials) (signin.Result, error) {
		var session *gocoder.Session
		var err error
		if register {
			session, err = client.Register(ctx, c.Email, c.Password, c.DisplayName)
		} else {
			session, err = client.Login(ctx, c.Email, c.Password)
		}
		if err != nil {
			return signin.Result{}, describeGocoderErr(client, err)
		}
		account, err := storeSession(ctx, client, session)
		if err != nil {
			return signin.Result{}, err
		}
		if onRestore != nil {
			onRestore(sync.RestoreOnLogin(ctx, client, account, c.Password, syncPaths(), syncStateDir(), os.Stderr))
		}
		return signin.Result{
			Name:      account.DisplayName,
			Email:     account.Email,
			KeyPrefix: account.KeyPrefix,
			Path:      gocoder.AccountPath(),
		}, nil
	}
}

// syncPaths and syncStateDir locate the files sync reads and writes. They
// are tiny wrappers so tests can spot the seam.
func syncPaths() sync.Paths {
	resolved := global.Resolve()
	return sync.Paths{ConfigDir: resolved.Config, StateDir: resolved.State, DataDir: resolved.Data}
}

func syncStateDir() string { return global.Resolve().State }

// reportRestore prints what the sign-in restore did, one line, after the
// sign-in screen has finished (never during — it renders inline).
func reportRestore(outcome sync.RestoreOutcome) {
	switch {
	case outcome.Restored:
		fmt.Fprintln(os.Stderr, "Settings restored from gocoder.org.")
	case outcome.LocalWon:
		fmt.Fprintln(os.Stderr, "Keeping this machine's settings; they will be uploaded to gocoder.org.")
	}
	if outcome.Staged > 0 {
		fmt.Fprintf(os.Stderr, "%d project config(s) from other machines are staged and apply when those projects are opened.\n", outcome.Staged)
	}
}

// storeSession trades the session's short-lived token for an API key and
// stores the account.
func storeSession(ctx context.Context, client *gocoder.Client, session *gocoder.Session) (*gocoder.Account, error) {
	keyName := "gocode"
	if host, err := os.Hostname(); err == nil && host != "" {
		keyName += " on " + host
	}
	key, err := client.CreateKey(ctx, session.Token, keyName)
	if err != nil {
		return nil, fmt.Errorf("signed in, but creating an API key failed: %w", describeGocoderErr(client, err))
	}
	account := &gocoder.Account{
		URL:         client.BaseURL,
		UserID:      session.User.ID,
		Email:       session.User.Email,
		DisplayName: session.User.DisplayName,
		KeyID:       key.ID,
		KeyPrefix:   key.Prefix,
		Key:         key.Secret,
		CreatedAt:   time.Now(),
	}
	if err := gocoder.SaveAccount(account); err != nil {
		return nil, fmt.Errorf("saving account: %w", err)
	}
	return account, nil
}

// describeGocoderErr keeps the website's own messages ("invalid email or
// password") and puts a readable face on transport failures.
func describeGocoderErr(client *gocoder.Client, err error) error {
	var apiErr *gocoder.APIError
	if errors.As(err, &apiErr) {
		return err
	}
	return fmt.Errorf("couldn't reach %s: %w", displayHost(client.BaseURL), err)
}

// unreachable reports whether err means the website could not be talked to,
// as opposed to having answered with a refusal.
func unreachable(err error) bool {
	var apiErr *gocoder.APIError
	return err != nil && !errors.As(err, &apiErr)
}

func displayHost(baseURL string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	return strings.TrimRight(host, "/")
}
