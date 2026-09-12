package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// RestoreOnLogin is the fresh-machine path: run right after a successful
// sign-in, while the password is still in hand to derive the sync key.
//
// It never returns an error that should abort a login — a sync failure is
// reported as a notice on stderr, not a failed sign-in. The outcome says
// what happened so the caller can print an accurate line.
type RestoreOutcome struct {
	// Restored is true when a server bundle was decrypted and applied.
	Restored bool
	// LocalWon is true when a pre-existing local config differed and was
	// kept, with a push scheduled in its place.
	LocalWon bool
	// Staged counts project configs parked for projects not cloned here.
	Staged int
	// KeyStored is true when the derived key was saved for the loops.
	KeyStored bool
}

// RestoreOnLogin derives the key from password, fetches the account's
// settings, and applies them. On a fresh account it only primes the state
// (new salt) so the first push creates the doc.
func RestoreOnLogin(ctx context.Context, client *gocoder.Client, account *gocoder.Account, password string, paths Paths, stateDir string, warn io.Writer) RestoreOutcome {
	var outcome RestoreOutcome

	state, err := LoadState(stateDir)
	if err != nil {
		fmt.Fprintf(warn, "gocode sync: could not read state: %v\n", err)
		return outcome
	}

	doc, err := client.GetSettings(ctx, account.Key)
	if err == gocoder.ErrNoSettings {
		// Fresh account (or sync never used): prime a salt so the first
		// push seals under a stable key, and store the key for the loops.
		salt := NewSalt()
		key, kerr := DeriveKey(password, salt, DefaultIterations)
		if kerr != nil {
			fmt.Fprintf(warn, "gocode sync: %v\n", kerr)
			return outcome
		}
		state.Salt = salt
		state.Iter = DefaultIterations
		state.SetKey(key)
		state.NeedsRelogin = false
		if err := SaveState(stateDir, state); err != nil {
			fmt.Fprintf(warn, "gocode sync: could not save state: %v\n", err)
			return outcome
		}
		outcome.KeyStored = true
		return outcome
	}
	if err != nil {
		fmt.Fprintf(warn, "gocode sync: could not reach %s: %v\n", client.BaseURL, err)
		return outcome
	}

	var envelope Envelope
	if err := json.Unmarshal([]byte(doc.Envelope), &envelope); err != nil {
		fmt.Fprintf(warn, "gocode sync: stored settings use an unreadable format; skipping\n")
		return outcome
	}
	key, err := DeriveKey(password, envelope.Salt, envelope.Iter)
	if err != nil {
		fmt.Fprintf(warn, "gocode sync: %v\n", err)
		return outcome
	}
	plaintext, err := Open(&envelope, key)
	if err != nil {
		// Most often: the password changed on the website after these
		// settings were saved, or the account predates sync. Either way
		// this sign-in's password is the truth now — keep local files,
		// store the key, and let the next push overwrite the doc.
		fmt.Fprintf(warn, "gocode sync: stored settings could not be decrypted with this password; keeping local settings\n")
		state.Salt = envelope.Salt
		state.Iter = envelope.Iter
		state.SetKey(key)
		state.NeedsRelogin = false
		_ = SaveState(stateDir, state)
		outcome.KeyStored = true
		outcome.LocalWon = true
		return outcome
	}
	bundle, err := ParseBundle(plaintext)
	if err != nil {
		fmt.Fprintf(warn, "gocode sync: %v\n", err)
		return outcome
	}

	// Local-wins on first contact: a machine that already has settings the
	// user curated keeps them and pushes; a fresh machine takes the server's.
	manager := NewManager(client, paths, stateDir, func() string { return account.Key })
	if localGlobal, found := readGlobal(paths); found && localGlobal != bundle.Files[KeyGlobal] {
		state.Salt = envelope.Salt
		state.Iter = envelope.Iter
		state.SetKey(key)
		state.NeedsRelogin = false
		// Stamp the server revision so the immediate follow-up push wins
		// the optimistic lock, but leave the file hashes stale: that is
		// what makes Push see a difference and overwrite the server copy.
		state.LastRevision = doc.Revision
		_ = SaveState(stateDir, state)
		outcome.LocalWon = true
		outcome.KeyStored = true
		// Still apply theme + projects; only the differing global stays local.
		trimmed := NewBundle()
		for k, v := range bundle.Files {
			if k != KeyGlobal {
				trimmed.Files[k] = v
			}
		}
		if len(trimmed.Files) > 0 {
			_, staged, err := manager.Apply(trimmed)
			if err != nil {
				fmt.Fprintf(warn, "gocode sync: %v\n", err)
			}
			outcome.Staged = len(staged)
		}
		return outcome
	}

	written, staged, err := manager.Apply(bundle)
	if err != nil {
		fmt.Fprintf(warn, "gocode sync: could not apply settings: %v\n", err)
		return outcome
	}
	state.Salt = envelope.Salt
	state.Iter = envelope.Iter
	state.SetKey(key)
	state.LastRevision = doc.Revision
	state.Hashes = bundleHashes(bundle)
	state.NeedsRelogin = false
	if err := SaveState(stateDir, state); err != nil {
		fmt.Fprintf(warn, "gocode sync: could not save state: %v\n", err)
		return outcome
	}
	outcome.Restored = len(written) > 0
	outcome.Staged = len(staged)
	outcome.KeyStored = true
	return outcome
}

// readGlobal reads the existing global config, if any.
func readGlobal(paths Paths) (string, bool) {
	path, found := paths.GlobalConfigPath()
	if !found {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}
