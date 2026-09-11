package gocoder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

func TestLoginAndCreateKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "correct-horse" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid email or password"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"token":"jwt-1","expiresAt":"2026-09-13T00:00:00Z","user":{"id":"u1","email":"a@b.c","displayName":"A"}}`))
		case "/api/keys":
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"k1","name":"cli","prefix":"gk_ab12","key":"gk_ab12secret"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL + "/")
	ctx := context.Background()

	_, err := client.Login(ctx, "a@b.c", "wrong")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Error() != "invalid email or password" {
		t.Fatalf("bad password error = %#v", err)
	}

	session, err := client.Login(ctx, "a@b.c", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if session.Token != "jwt-1" || session.User.ID != "u1" {
		t.Fatalf("session = %+v", session)
	}
	key, err := client.CreateKey(ctx, session.Token, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if key.Secret != "gk_ab12secret" || key.Prefix != "gk_ab12" {
		t.Fatalf("key = %+v", key)
	}
}

func TestAccountRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if account, err := LoadAccount(); err != nil || account != nil {
		t.Fatalf("empty store: account=%v err=%v", account, err)
	}
	want := &Account{URL: DefaultURL, UserID: "u1", Email: "a@b.c", Key: "gk_x"}
	if err := SaveAccount(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAccount()
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "gk_x" || got.Email != "a@b.c" {
		t.Fatalf("round trip = %+v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(AccountPath())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("account file mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestBaseURLOverride(t *testing.T) {
	t.Setenv("GOCODE_GOCODER_URL", "http://localhost:8080/")
	if got := BaseURL(); got != "http://localhost:8080" {
		t.Fatalf("BaseURL = %q", got)
	}
}
