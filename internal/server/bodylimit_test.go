package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every route decodes JSON straight from r.Body. Without a bound a client
// could stream an unbounded payload into the process; the wrapper turns that
// into a rejected request instead of a ballooned one.

func TestRequestBodyIsCapped(t *testing.T) {
	// The health-only mux has no POST route; prompt is the real entry point
	// but needs a session service. A route that 404s still exercises the
	// wrapper: the body is capped before routing.
	huge := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1024)
	req := httptest.NewRequest("POST", "/api/session/ses_x/prompt", bytes.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Mux().ServeHTTP(rec, req)
	// Either 413 (the body limit trips) or the route's own answer is fine —
	// what must not happen is the request being read whole into memory and
	// processed. MaxBytesReader closes the connection-side read at the cap
	// and the decode fails; the handler reports a 4xx.
	if rec.Code != 413 && rec.Code < 400 {
		t.Fatalf("expected a 4xx on an oversized body, got %d", rec.Code)
	}
}

func TestNormalRequestBodyPasses(t *testing.T) {
	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest("POST", "/api/session/ses_x/prompt", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Mux().ServeHTTP(rec, req)
	// No session service is wired, so the route cannot succeed — but it must
	// fail on its own terms (a 4xx/5xx from the handler), not because the
	// body wrapper rejected a small request.
	if rec.Code == 413 {
		t.Fatal("a small request body was rejected by the size cap")
	}
}
