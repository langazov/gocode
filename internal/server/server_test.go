package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(Mux())
	defer srv.Close()

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	Mux().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["healthy"] != true {
		t.Fatalf("expected healthy true, got %v", body)
	}
}
