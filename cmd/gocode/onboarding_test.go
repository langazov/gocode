package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/tui/signin"
)

// fakeGocoder serves the three endpoints sign-in uses. Registered and
// known accounts share one password.
func fakeGocoder(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		user := `{"id":"u1","email":"` + body["email"] + `","displayName":"` + body["displayName"] + `"}`
		switch r.URL.Path {
		case "/api/auth/register":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"jwt","expiresAt":"2026-09-13T00:00:00Z","user":` + user + `}`))
		case "/api/auth/login":
			if body["password"] != "correct-horse" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid email or password"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"token":"jwt","expiresAt":"2026-09-13T00:00:00Z","user":` + user + `}`))
		case "/api/keys":
			if r.Header.Get("Authorization") != "Bearer jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"k1","name":"` + body["name"] + `","prefix":"gk_ab12","key":"gk_ab12secret"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSubmitterRegisterStoresKey(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	submit := submitter(gocoder.NewClient(fakeGocoder(t).URL))

	result, err := submit(context.Background(), true, signin.Credentials{
		Email: "alice@example.com", DisplayName: "alice", Password: "correct-horse",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Email != "alice@example.com" || result.Name != "alice" || result.KeyPrefix != "gk_ab12" || result.Path != gocoder.AccountPath() {
		t.Fatalf("result = %+v", result)
	}
	account, err := gocoder.LoadAccount()
	if err != nil || account == nil || account.Key != "gk_ab12secret" || account.KeyID != "k1" {
		t.Fatalf("account = %+v, err = %v", account, err)
	}
}

func TestSubmitterRejectedLogin(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	submit := submitter(gocoder.NewClient(fakeGocoder(t).URL))

	_, err := submit(context.Background(), false, signin.Credentials{Email: "alice@example.com", Password: "wrong-password"})
	if err == nil || err.Error() != "invalid email or password" || unreachable(err) {
		t.Fatalf("err = %v (unreachable=%v)", err, unreachable(err))
	}
	if account, _ := gocoder.LoadAccount(); account != nil {
		t.Fatalf("rejected login stored an account: %+v", account)
	}
}

func TestSubmitterUnreachable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	submit := submitter(gocoder.NewClient("http://127.0.0.1:1"))

	_, err := submit(context.Background(), false, signin.Credentials{Email: "alice@example.com", Password: "correct-horse"})
	if err == nil || !strings.HasPrefix(err.Error(), "couldn't reach 127.0.0.1:1") || !unreachable(err) {
		t.Fatalf("err = %v (unreachable=%v)", err, unreachable(err))
	}
}
