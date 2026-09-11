package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// revokingGocoder serves DELETE /api/keys/{id}, recording each revocation
// as "id bearer".
func revokingGocoder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var revoked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, "/api/keys/") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		revoked = append(revoked, strings.TrimPrefix(r.URL.Path, "/api/keys/")+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), revoked...)
	}
}

func TestRetirePreviousRevokesReplacedKey(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srv, revoked := revokingGocoder(t)
	previous := &gocoder.Account{URL: srv.URL, Email: "old@example.com", Key: "gk_old", KeyID: "old"}

	// Signing in again as the same key is not a replacement.
	if err := gocoder.SaveAccount(previous); err != nil {
		t.Fatal(err)
	}
	retirePrevious(context.Background(), previous, &strings.Builder{})
	if got := revoked(); len(got) != 0 {
		t.Fatalf("revoked the still-current key: %v", got)
	}

	if err := gocoder.SaveAccount(&gocoder.Account{URL: srv.URL, Email: "new@example.com", Key: "gk_new", KeyID: "new"}); err != nil {
		t.Fatal(err)
	}
	retirePrevious(context.Background(), previous, &strings.Builder{})
	if got := revoked(); len(got) != 1 || got[0] != "old Bearer gk_old" {
		t.Fatalf("revoked = %v", got)
	}
}

func TestLogoutRevokesAndRemovesAccount(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srv, revoked := revokingGocoder(t)
	if err := gocoder.SaveAccount(&gocoder.Account{URL: srv.URL, Email: "a@example.com", Key: "gk_live", KeyID: "k1"}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runGocoderLogout(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if account, _ := gocoder.LoadAccount(); account != nil {
		t.Fatalf("account still stored: %+v", account)
	}
	if got := revoked(); len(got) != 1 || got[0] != "k1 Bearer gk_live" {
		t.Fatalf("revoked = %v", got)
	}
	// Logging out again is a no-op, not an error.
	out.Reset()
	if err := runGocoderLogout(context.Background(), &out); err != nil || !strings.Contains(out.String(), "Not signed in") {
		t.Fatalf("second logout: %v, %q", err, out.String())
	}
}
