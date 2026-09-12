package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/sync"
	"github.com/langazov/gocode-go/internal/tui/signin"
)

// loginCommand, registerCommand and logoutCommand manage the gocoder.org
// account — the same flow first start offers, reachable any time after.
func loginCommand() *clix.Command {
	return &clix.Command{
		Name:     "login",
		Describe: "log in to your gocoder.org account",
		Run:      func(*clix.Args) error { return runGocoderSignIn(false) },
	}
}

func registerCommand() *clix.Command {
	return &clix.Command{
		Name:     "register",
		Describe: "create a gocoder.org account",
		Run:      func(*clix.Args) error { return runGocoderSignIn(true) },
	}
}

func logoutCommand() *clix.Command {
	return &clix.Command{
		Name:     "logout",
		Describe: "log out of gocoder.org and revoke this machine's API key",
		Run:      func(*clix.Args) error { return runGocoderLogout(context.Background(), os.Stdout) },
	}
}

func runGocoderSignIn(register bool) error {
	if !interactiveTerminal() {
		return errors.New("signing in to gocoder.org needs an interactive terminal")
	}
	ctx := context.Background()
	previous, err := gocoder.LoadAccount()
	if err != nil {
		return err
	}
	var notice string
	if previous != nil {
		notice = fmt.Sprintf("Signed in as %s — continuing replaces that account.", previous.Email)
	}
	start := signin.ScreenLogin
	if register {
		start = signin.ScreenRegister
	}
	outcome, err := runSignIn(ctx, gocoder.NewClient(""), start, notice)
	if err != nil {
		return err
	}
	if outcome.Status == signin.StatusSignedIn {
		retirePrevious(ctx, previous, os.Stderr)
	}
	return nil
}

// retirePrevious revokes the key of an account a new sign-in replaced, so
// repeated logins do not pile up live keys.
func retirePrevious(ctx context.Context, previous *gocoder.Account, warn io.Writer) {
	if previous == nil || previous.KeyID == "" {
		return
	}
	if current, _ := gocoder.LoadAccount(); current != nil && current.KeyID == previous.KeyID {
		return
	}
	revokeQuietly(ctx, previous, warn)
}

func runGocoderLogout(ctx context.Context, out io.Writer) error {
	account, err := gocoder.LoadAccount()
	if err != nil {
		return err
	}
	if account == nil {
		fmt.Fprintln(out, "Not signed in to gocoder.org.")
		return nil
	}
	if account.KeyID != "" {
		revokeQuietly(ctx, account, os.Stderr)
	}
	if err := gocoder.RemoveAccount(); err != nil {
		return err
	}
	// The sync state carries a derived settings key; drop it with the
	// account so a signed-out machine cannot push or pull settings.
	if state, err := sync.LoadState(global.Resolve().State); err == nil {
		state.Key = ""
		state.NeedsRelogin = true
		if err := sync.SaveState(global.Resolve().State, state); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not clear the sync key: %v\n", err)
		}
	}
	fmt.Fprintf(out, "Logged out of %s (%s).\n", displayHost(gocoder.NewClient(account.URL).BaseURL), account.Email)
	return nil
}

// revokeQuietly revokes an account's key, best effort. A key the server no
// longer accepts (401) or knows (404) is already as dead as revoking makes
// it; anything else is worth a warning, since the key may still be live.
func revokeQuietly(ctx context.Context, account *gocoder.Account, warn io.Writer) {
	err := gocoder.NewClient(account.URL).RevokeKey(ctx, account.Key, account.KeyID)
	var apiErr *gocoder.APIError
	if err == nil || errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusNotFound) {
		return
	}
	fmt.Fprintf(warn, "warning: could not revoke API key %s: %v (revoke it in your gocoder.org settings)\n", account.KeyPrefix, err)
}
