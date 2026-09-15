package sync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// deviceTokenFileName stores this machine's random per-install token,
// reused by every account that signs in here. It never leaves the machine
// and is never itself sent to the server — only DeviceID's hash of it
// (folded with the account's email) is.
const deviceTokenFileName = "device-id"

// deviceTokenPath is DataDir/sync/device-id, alongside the project staging
// area (see Paths.ProjectStagingDir).
func deviceTokenPath(paths Paths) string {
	return filepath.Join(paths.DataDir, "sync", deviceTokenFileName)
}

// deviceToken returns this machine's stable random token, generating and
// persisting one on first use.
func deviceToken(paths Paths) (string, error) {
	path := deviceTokenPath(paths)
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// DeviceID derives this machine's settings-sync deviceID for email: stable
// across runs on one computer, and different on every other one — matching
// the deviceID doc comment in
// gocode-infra/website/backend/internal/settings/settings.go, which the
// server keys per-(userID, deviceID) so a laptop and a desktop with
// different MCP servers or plugins no longer overwrite each other's
// settings. It folds in email rather than sending deviceToken bare so the
// ID leaks nothing about the machine and two accounts sharing one machine
// (a CI runner, say) still land on different device docs.
func DeviceID(paths Paths, email string) (string, error) {
	token, err := deviceToken(paths)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + "|" + token))
	return hex.EncodeToString(sum[:])[:32], nil
}
