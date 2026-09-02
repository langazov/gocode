package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestFlow points a DeviceFlow at a test server and shrinks the polling
// interval, since the real one starts at 5s plus the safety margin.
func newTestFlow(t *testing.T, handler http.HandlerFunc) (DeviceFlow, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return DeviceFlow{
		ClientID:      "test-client",
		DeviceCodeURL: server.URL + "/device/code",
		TokenURL:      server.URL + "/token",
		Scope:         "read:user",
		JSONRequest:   true,
		HTTP:          server.Client(),
		SafetyMargin:  time.Millisecond,
	}, server
}

func TestDeviceFlowStart(t *testing.T) {
	var body map[string]string
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://example.com/device",
			"interval":         5,
		})
	})

	code, err := flow.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if code.UserCode != "ABCD-1234" || code.DeviceCode != "dev-code" {
		t.Errorf("unexpected code: %+v", code)
	}
	if body["client_id"] != "test-client" || body["scope"] != "read:user" {
		t.Errorf("request body = %v, want the client id and scope", body)
	}
}

// TestDeviceFlowStartRejectsIncompleteResponse: a 200 with no code must not
// be mistaken for a usable authorization.
func TestDeviceFlowStartRejectsIncompleteResponse(t *testing.T) {
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	})
	if _, err := flow.Start(context.Background()); err == nil {
		t.Fatal("expected an error when the response carries no device code")
	}
}

// TestDeviceFlowPollSucceedsAfterPending is the core of RFC 8628: keep polling
// while the user has not yet approved.
func TestDeviceFlowPollSucceedsAfterPending(t *testing.T) {
	var calls atomic.Int32
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_token"})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := flow.Poll(ctx, &DeviceCode{DeviceCode: "dev", Interval: 0})
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "gho_token" {
		t.Errorf("access token = %q, want gho_token", token.AccessToken)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("polled %d times, want 3", got)
	}
}

// TestDeviceFlowPollBacksOffOnSlowDown: a slow_down must widen the interval,
// per RFC 8628 §3.5, or the server starts rejecting outright.
func TestDeviceFlowPollBacksOffOnSlowDown(t *testing.T) {
	var timestamps []time.Time
	var calls atomic.Int32
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		timestamps = append(timestamps, time.Now())
		if calls.Add(1) == 1 {
			// Name an explicit interval, which takes precedence over +5s.
			json.NewEncoder(w).Encode(map[string]any{"error": "slow_down", "interval": 1})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t"})
	})

	// A zero starting interval means the first poll is immediate (safety
	// margin aside), so any measurable gap is the backoff taking effect.
	flowCode := &DeviceCode{DeviceCode: "dev", Interval: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := flow.Poll(ctx, flowCode); err != nil {
		t.Fatal(err)
	}
	if len(timestamps) < 2 {
		t.Fatalf("expected at least 2 polls, got %d", len(timestamps))
	}
	if gap := timestamps[1].Sub(timestamps[0]); gap < time.Second {
		t.Errorf("second poll came %v after the first, want at least the 1s the server asked for", gap)
	}
}

func TestDeviceFlowPollReportsDenial(t *testing.T) {
	for _, reason := range []string{"access_denied", "expired_token"} {
		t.Run(reason, func(t *testing.T) {
			flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"error": reason})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := flow.Poll(ctx, &DeviceCode{DeviceCode: "dev", Interval: 0})
			if err != ErrDeviceFlowDenied {
				t.Fatalf("err = %v, want ErrDeviceFlowDenied", err)
			}
		})
	}
}

// TestDeviceFlowPollStopsOnContextCancel: the loop is unbounded, so
// cancellation is the only thing that ends a wait the user walked away from.
func TestDeviceFlowPollStopsOnContextCancel(t *testing.T) {
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { _, err := flow.Poll(ctx, &DeviceCode{DeviceCode: "dev", Interval: 0}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the context is cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll ignored context cancellation")
	}
}

func TestDeviceFlowSurfacesHTTPFailure(t *testing.T) {
	flow, _ := newTestFlow(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream is down"))
	})
	_, err := flow.Start(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status, got: %v", err)
	}
}
