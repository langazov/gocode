package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	gocodesync "github.com/langazov/gocode-go/internal/sync"
)

// syncCommand and its subcommands manage the settings sync against a
// gocoder.org account: reconcile now, inspect status, or turn the loops off.
func syncCommand() *clix.Command {
	return &clix.Command{
		Name:     "sync",
		Describe: "sync your gocode settings with your gocoder.org account",
		Sub: []*clix.Command{
			{
				Name:     "status",
				Describe: "show sync state: account, revision, last sync",
				Run:      func(*clix.Args) error { return runSyncStatus(os.Stdout) },
			},
			{
				Name:     "enable",
				Describe: "turn settings sync on",
				Run:      func(*clix.Args) error { return runSyncToggle(os.Stdout, true) },
			},
			{
				Name:     "disable",
				Describe: "turn settings sync off (loops stop; nothing is deleted)",
				Run:      func(*clix.Args) error { return runSyncToggle(os.Stdout, false) },
			},
		},
		// A bare `gocode sync` reconciles: pull first (so a push wins the
		// optimistic lock against the freshest server copy), then push when
		// local still differs.
		Run: func(*clix.Args) error { return runSyncOnce(os.Stdout, os.Stderr) },
	}
}

func syncManager() (*gocodesync.Manager, *gocoder.Account, error) {
	account, err := gocoder.LoadAccount()
	if err != nil {
		return nil, nil, err
	}
	if account == nil {
		return nil, nil, errors.New("not signed in to gocoder.org — run gocode login")
	}
	state := globalState()
	manager := gocodesync.NewManager(gocoder.NewClient(account.URL), syncPaths(), state,
		func() string { return account.Key })
	return manager, account, nil
}

func globalState() string { return global.Resolve().State }

func runSyncOnce(out, errOut *os.File) error {
	manager, _, err := syncManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	applied, err := manager.Pull(ctx)
	if err != nil {
		if errors.Is(err, gocodesync.ErrDecrypt) {
			return fmt.Errorf("the synced settings could not be decrypted — run gocode login to re-derive the key")
		}
		if errors.Is(err, gocodesync.ErrNoKey) {
			return fmt.Errorf("run gocode login once to set up settings sync (it needs your password to derive the encryption key)")
		}
		return err
	}
	if applied {
		fmt.Fprintln(out, "Pulled settings from gocoder.org.")
	}
	if err := manager.Push(ctx); err != nil {
		var apiErr *gocoder.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			return fmt.Errorf("another machine saved newer settings while this one worked; run gocode sync again")
		}
		if errors.Is(err, gocodesync.ErrNoKey) {
			return fmt.Errorf("run gocode login once to set up settings sync (it needs your password to derive the encryption key)")
		}
		return err
	}
	if !applied {
		fmt.Fprintln(out, "Settings are in sync with gocoder.org.")
	}
	return nil
}

func runSyncStatus(out *os.File) error {
	account, err := gocoder.LoadAccount()
	if err != nil {
		return err
	}
	if account == nil {
		fmt.Fprintln(out, "Not signed in to gocoder.org; settings sync is inactive.")
		return nil
	}
	fmt.Fprintf(out, "Signed in as %s (%s).\n", account.Email, gocoder.NewClient(account.URL).BaseURL)

	state, err := gocodesync.LoadState(globalState())
	if err != nil {
		return err
	}
	switch {
	case !state.IsEnabled():
		fmt.Fprintln(out, "Sync is disabled (gocode sync enable turns it back on).")
	case state.NeedsRelogin:
		fmt.Fprintln(out, "Sync is paused: no usable sync key on this machine.")
		fmt.Fprintln(out, "Run gocode login to derive it (the password is needed; it is never stored or sent).")
	default:
		fmt.Fprintf(out, "Sync is on. Last synced revision %d at %s.\n", state.LastRevision, state.LastSyncAt.Format(time.RFC3339))
		fmt.Fprintln(out, "Synced: global config, theme, and project configs for known projects.")
	}
	return nil
}

func runSyncToggle(out *os.File, enabled bool) error {
	state, err := gocodesync.LoadState(globalState())
	if err != nil {
		return err
	}
	value := enabled
	state.Enabled = &value
	if err := gocodesync.SaveState(globalState(), state); err != nil {
		return err
	}
	if enabled {
		fmt.Fprintln(out, "Settings sync enabled.")
	} else {
		fmt.Fprintln(out, "Settings sync disabled. Nothing was deleted; gocode sync enable re-enables it.")
	}
	return nil
}
