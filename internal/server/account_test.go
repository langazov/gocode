package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	settingssync "github.com/langazov/gocode-go/internal/sync"
)

// fakeSite is a stand-in gocoder.org with one account (a@b.c, password
// correct-horse) whose logins mint numbered API keys.
type fakeSite struct {
	*httptest.Server
	mu          sync.Mutex
	displayName string
	keys        map[string]string // live key secret → key ID
	revoked     []string
	nextKey     int
	summaryDays string
}

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	site := &fakeSite{displayName: "Alice", keys: map[string]string{}}
	site.Server = httptest.NewServer(http.HandlerFunc(site.serve))
	t.Cleanup(site.Close)
	return site
}

func (f *fakeSite) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "error", "message": message},
		})
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, liveKey := f.keys[bearer]
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "a@b.c" || body["password"] != "correct-horse" {
			fail(http.StatusUnauthorized, "invalid email or password")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "jwt-1", "expiresAt": "2030-01-01T00:00:00Z",
			"user": map[string]string{"id": "u1", "email": "a@b.c", "displayName": f.displayName},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/keys":
		if bearer != "jwt-1" {
			fail(http.StatusUnauthorized, "missing Bearer token")
			return
		}
		f.nextKey++
		id := fmt.Sprintf("k%d", f.nextKey)
		secret := "gk_secret_" + id
		f.keys[secret] = id
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "name": "gocode", "prefix": "gk_" + id, "key": secret})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/keys/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
		for secret, keyID := range f.keys {
			if keyID == id {
				delete(f.keys, secret)
			}
		}
		f.revoked = append(f.revoked, id)
		w.WriteHeader(http.StatusNoContent)
	case !liveKey:
		fail(http.StatusUnauthorized, "invalid or expired token")
	case r.URL.Path == "/api/auth/me":
		if r.Method == http.MethodPatch {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.displayName = body["displayName"]
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "u1", "email": "a@b.c", "displayName": f.displayName, "createdAt": "2026-01-02T03:04:05Z",
		})
	case r.URL.Path == "/api/stats/summary":
		f.summaryDays = r.URL.Query().Get("days")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totals": map[string]int{"requests": 12, "tokens": 3400}, "byModel": []any{}, "daily": []any{},
		})
	case r.URL.Path == "/api/invites":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "abcd2345", "url": f.URL + "/#/register?invite=abcd2345", "invited": 3,
		})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSite) revokedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func (f *fakeSite) lastSummaryDays() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.summaryDays
}

// accountEnv points gocoder.json and the sync state at temp dirs, and the
// gocoder.org client at site.
func accountEnv(t *testing.T, siteURL string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GOCODE_GOCODER_URL", siteURL)
}

func TestAccountSignedOut(t *testing.T) {
	site := newFakeSite(t)
	accountEnv(t, site.URL)
	server := &Server{}

	rec := doJSON(t, server, http.MethodGet, "/api/account", nil)
	view := decodeBody[accountView](t, rec)
	if rec.Code != http.StatusOK || view.SignedIn || view.Site != site.URL {
		t.Fatalf("signed-out account = %d %+v", rec.Code, view)
	}
	for _, path := range []string{"/api/account/usage", "/api/account/invite"} {
		if rec := doJSON(t, server, http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s signed out = %d, want 401", path, rec.Code)
		}
	}
	if rec := doJSON(t, server, http.MethodPatch, "/api/account", map[string]string{"displayName": "X"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PATCH /api/account signed out = %d, want 401", rec.Code)
	}
	if rec := doJSON(t, server, http.MethodPost, "/api/account/logout", nil); rec.Code != http.StatusOK {
		t.Fatalf("logout while signed out = %d, want 200", rec.Code)
	}
}

func TestAccountLifecycle(t *testing.T) {
	site := newFakeSite(t)
	accountEnv(t, site.URL)
	server := &Server{}

	// The site's own message survives a failed sign-in.
	rec := doJSON(t, server, http.MethodPost, "/api/account/login", map[string]string{"email": "a@b.c", "password": "wrong"})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid email or password") {
		t.Fatalf("bad password = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, server, http.MethodPost, "/api/account/login", map[string]string{"email": "a@b.c"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing password = %d, want 400", rec.Code)
	}

	rec = doJSON(t, server, http.MethodPost, "/api/account/login", map[string]string{"email": "a@b.c", "password": "correct-horse"})
	view := decodeBody[accountView](t, rec)
	if rec.Code != http.StatusOK || !view.SignedIn || view.Email != "a@b.c" || view.KeyPrefix != "gk_k1" {
		t.Fatalf("sign-in = %d %+v", rec.Code, view)
	}
	if stored, _ := gocoder.LoadAccount(); stored == nil || stored.Key != "gk_secret_k1" {
		t.Fatalf("stored account = %+v", stored)
	}

	// A rename goes to the site, and the stored copy follows.
	rec = doJSON(t, server, http.MethodPatch, "/api/account", map[string]string{"displayName": "Alice Liddell"})
	view = decodeBody[accountView](t, rec)
	if rec.Code != http.StatusOK || view.DisplayName != "Alice Liddell" || view.MemberSince == nil {
		t.Fatalf("rename = %d %+v", rec.Code, view)
	}
	if stored, _ := gocoder.LoadAccount(); stored.DisplayName != "Alice Liddell" {
		t.Fatalf("stored display name = %q", stored.DisplayName)
	}
	if rec := doJSON(t, server, http.MethodPatch, "/api/account", map[string]string{"displayName": "  "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rename = %d, want 400", rec.Code)
	}

	rec = doJSON(t, server, http.MethodGet, "/api/account/usage?days=7", nil)
	summary := decodeBody[gocoder.UsageSummary](t, rec)
	if rec.Code != http.StatusOK || summary.Totals.Requests != 12 || site.lastSummaryDays() != "7" {
		t.Fatalf("usage = %d %+v (days sent %q)", rec.Code, summary, site.lastSummaryDays())
	}
	if rec := doJSON(t, server, http.MethodGet, "/api/account/usage?days=0", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("usage days=0 = %d, want 400", rec.Code)
	}

	rec = doJSON(t, server, http.MethodGet, "/api/account/invite", nil)
	invite := decodeBody[gocoder.Invite](t, rec)
	if rec.Code != http.StatusOK || invite.Code != "abcd2345" || invite.Invited != 3 || !strings.HasSuffix(invite.URL, "invite=abcd2345") {
		t.Fatalf("invite = %d %+v", rec.Code, invite)
	}

	// Signing out revokes the key, removes the sign-in and drops the sync key.
	stateDir := global.Resolve().State
	if err := settingssync.SaveState(stateDir, &settingssync.State{Key: "derived-sync-key"}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, server, http.MethodPost, "/api/account/logout", nil)
	if view := decodeBody[accountView](t, rec); rec.Code != http.StatusOK || view.SignedIn {
		t.Fatalf("sign-out = %d %+v", rec.Code, view)
	}
	if revoked := site.revokedKeys(); len(revoked) != 1 || revoked[0] != "k1" {
		t.Fatalf("revoked keys = %v, want [k1]", revoked)
	}
	if stored, _ := gocoder.LoadAccount(); stored != nil {
		t.Fatalf("account still stored after sign-out: %+v", stored)
	}
	state, err := settingssync.LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Key != "" || !state.NeedsRelogin {
		t.Fatalf("sync state after sign-out = key %q, needsRelogin %v", state.Key, state.NeedsRelogin)
	}
}

func TestAccountSignInAgainRevokesThePreviousKey(t *testing.T) {
	site := newFakeSite(t)
	accountEnv(t, site.URL)
	server := &Server{}
	login := map[string]string{"email": "a@b.c", "password": "correct-horse"}

	doJSON(t, server, http.MethodPost, "/api/account/login", login)
	rec := doJSON(t, server, http.MethodPost, "/api/account/login", login)
	if view := decodeBody[accountView](t, rec); view.KeyPrefix != "gk_k2" {
		t.Fatalf("second sign-in key = %q, want gk_k2", view.KeyPrefix)
	}
	if revoked := site.revokedKeys(); len(revoked) != 1 || revoked[0] != "k1" {
		t.Fatalf("revoked keys = %v, want the replaced k1", revoked)
	}
}

func TestAccountReportsExpiredAndOffline(t *testing.T) {
	site := newFakeSite(t)
	accountEnv(t, site.URL)
	server := &Server{}
	stored := &gocoder.Account{
		URL: site.URL, UserID: "u1", Email: "a@b.c", DisplayName: "Stored",
		KeyID: "k9", KeyPrefix: "gk_k9", Key: "gk_revoked",
	}

	// A key the site no longer accepts.
	if err := gocoder.SaveAccount(stored); err != nil {
		t.Fatal(err)
	}
	view := decodeBody[accountView](t, doJSON(t, server, http.MethodGet, "/api/account", nil))
	if !view.SignedIn || !view.Expired || view.Offline || view.DisplayName != "Stored" {
		t.Fatalf("revoked key view = %+v", view)
	}

	// A site that can't be reached.
	gone := httptest.NewServer(http.NotFoundHandler())
	stored.URL = gone.URL
	gone.Close()
	if err := gocoder.SaveAccount(stored); err != nil {
		t.Fatal(err)
	}
	view = decodeBody[accountView](t, doJSON(t, server, http.MethodGet, "/api/account", nil))
	if !view.SignedIn || !view.Offline || view.Expired || view.Email != "a@b.c" {
		t.Fatalf("unreachable site view = %+v", view)
	}
}
