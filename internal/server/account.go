package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	settingssync "github.com/langazov/gocode-go/internal/sync"
)

// account.go serves the gocoder.org account behind this machine's sign-in
// (gocoder.json): who is signed in, signing in and out, renaming, usage and
// the invite link. The server makes every call with the stored API key, so
// interfaces never hold a gocoder.org credential themselves — and a remote
// interface sees the account of the machine the server runs on.

// accountView answers GET /api/account and the routes that change it.
type accountView struct {
	SignedIn bool `json:"signedIn"`
	// Site is the gocoder.org instance: the signed-in account's, or where a
	// sign-in would go.
	Site        string     `json:"site"`
	UserID      string     `json:"userId,omitempty"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"displayName,omitempty"`
	KeyPrefix   string     `json:"keyPrefix,omitempty"`
	MemberSince *time.Time `json:"memberSince,omitempty"`
	// Expired: the site rejects the stored key (revoked, or the account was
	// deleted). Signing in again fixes it.
	Expired bool `json:"expired,omitempty"`
	// Offline: the site couldn't be reached, so the fields above come from
	// the stored sign-in and may be stale.
	Offline bool `json:"offline,omitempty"`
}

func signedOutView() accountView {
	return accountView{Site: gocoder.BaseURL()}
}

func accountViewOf(account *gocoder.Account) accountView {
	site := account.URL
	if site == "" {
		site = gocoder.BaseURL()
	}
	return accountView{
		SignedIn:    true,
		Site:        site,
		UserID:      account.UserID,
		Email:       account.Email,
		DisplayName: account.DisplayName,
		KeyPrefix:   account.KeyPrefix,
	}
}

// getAccount answers who is signed in, refreshed from the site when it can
// be reached.
func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	account, err := gocoder.LoadAccount()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account == nil {
		writeJSON(w, http.StatusOK, signedOutView())
		return
	}
	view := accountViewOf(account)
	client := gocoder.NewClient(account.URL)
	user, err := client.Me(r.Context(), account.Key)
	if unauthorized(err) {
		// Some deployments answer /api/auth/me for session tokens only, so
		// its 401 may just mean "not with an API key". The key has only
		// really expired when a route that does take keys refuses it too.
		user, err = nil, client.CheckKey(r.Context(), account.Key)
	}
	switch {
	case err == nil && user != nil:
		refreshStoredProfile(account, user)
		view.Email, view.DisplayName = user.Email, user.DisplayName
		if !user.CreatedAt.IsZero() {
			view.MemberSince = &user.CreatedAt
		}
	case err == nil:
		// The key is live but the profile isn't reachable with it: show the
		// stored one.
	case unauthorized(err):
		view.Expired = true
	default:
		view.Offline = true
	}
	writeJSON(w, http.StatusOK, view)
}

// unauthorized reports whether err is the site refusing the credential.
func unauthorized(err error) bool {
	var apiErr *gocoder.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

// refreshStoredProfile keeps gocoder.json's copy of the name and email
// current after they change on the website. Best effort: the live values are
// what gets shown either way.
func refreshStoredProfile(account *gocoder.Account, user *gocoder.User) {
	if user.DisplayName == account.DisplayName && user.Email == account.Email {
		return
	}
	account.DisplayName, account.Email = user.DisplayName, user.Email
	_ = gocoder.SaveAccount(account)
}

type accountLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginAccount signs this machine in: the same exchange as `gocode login`
// (session token → named API key → gocoder.json). A previous account is
// replaced and its key revoked, so repeated sign-ins don't pile up live keys.
func (s *Server) loginAccount(w http.ResponseWriter, r *http.Request) {
	var body accountLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		strings.TrimSpace(body.Email) == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	previous, err := gocoder.LoadAccount()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	client := gocoder.NewClient("")
	session, err := client.Login(r.Context(), strings.TrimSpace(body.Email), body.Password)
	if err != nil {
		writeGocoderError(w, err)
		return
	}
	account, err := gocoder.StoreSession(r.Context(), client, session, gocoder.KeyName())
	if err != nil {
		writeGocoderError(w, err)
		return
	}
	if previous != nil && previous.KeyID != account.KeyID {
		revokeAccountKey(r.Context(), previous)
	}
	writeJSON(w, http.StatusOK, accountViewOf(account))
}

// logoutAccount signs this machine out, like `gocode logout`: revoke the key,
// remove gocoder.json, and drop the settings-sync key so a signed-out machine
// can't push or pull settings.
func (s *Server) logoutAccount(w http.ResponseWriter, r *http.Request) {
	account, err := gocoder.LoadAccount()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account != nil {
		revokeAccountKey(r.Context(), account)
		if err := gocoder.RemoveAccount(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		dropSyncKey()
	}
	writeJSON(w, http.StatusOK, signedOutView())
}

// revokeAccountKey revokes a replaced or signed-out account's key, best
// effort. A key the site already rejects is as dead as revoking makes it,
// and a failure otherwise must not block signing out; the key can still be
// revoked from the website's settings.
func revokeAccountKey(ctx context.Context, account *gocoder.Account) {
	if account.KeyID == "" {
		return
	}
	_ = gocoder.NewClient(account.URL).RevokeKey(ctx, account.Key, account.KeyID)
}

// dropSyncKey clears the derived settings-sync key, as `gocode logout` does.
func dropSyncKey() {
	dir := global.Resolve().State
	state, err := settingssync.LoadState(dir)
	if err != nil {
		return
	}
	state.Key = ""
	state.NeedsRelogin = true
	_ = settingssync.SaveState(dir, state)
}

// updateAccount renames the account.
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	account := signedInAccount(w)
	if account == nil {
		return
	}
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "displayName is required")
		return
	}
	user, err := gocoder.NewClient(account.URL).UpdateDisplayName(r.Context(), account.Key, strings.TrimSpace(body.DisplayName))
	if err != nil {
		writeGocoderError(w, err)
		return
	}
	refreshStoredProfile(account, user)
	view := accountViewOf(account)
	if !user.CreatedAt.IsZero() {
		view.MemberSince = &user.CreatedAt
	}
	writeJSON(w, http.StatusOK, view)
}

// accountUsage answers the account's usage summary over ?days= (1-90,
// default 30).
func (s *Server) accountUsage(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 90 {
			writeError(w, http.StatusBadRequest, "days must be 1-90")
			return
		}
		days = n
	}
	account := signedInAccount(w)
	if account == nil {
		return
	}
	summary, err := gocoder.NewClient(account.URL).UsageSummary(r.Context(), account.Key, days)
	if err != nil {
		writeGocoderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// accountInvite answers the account's invite link and referral count.
func (s *Server) accountInvite(w http.ResponseWriter, r *http.Request) {
	account := signedInAccount(w)
	if account == nil {
		return
	}
	invite, err := gocoder.NewClient(account.URL).Invite(r.Context(), account.Key)
	if err != nil {
		writeGocoderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, invite)
}

// signedInAccount loads the stored account, answering 401 itself when there
// is none (500 when it can't be read).
func signedInAccount(w http.ResponseWriter) *gocoder.Account {
	account, err := gocoder.LoadAccount()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if account == nil {
		writeError(w, http.StatusUnauthorized, "not signed in to gocoder.org")
		return nil
	}
	return account
}

// writeGocoderError answers with the site's own status and message when it
// replied (a bad password stays "invalid email or password"), 502 when it
// couldn't be reached, 500 otherwise.
func writeGocoderError(w http.ResponseWriter, err error) {
	var apiErr *gocoder.APIError
	var urlErr *url.Error
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr.Status, apiErr.Error())
	case errors.As(err, &urlErr):
		writeError(w, http.StatusBadGateway, "couldn't reach gocoder.org: "+urlErr.Err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
