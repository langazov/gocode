package server

import (
	"encoding/json"
	"testing"

	"github.com/langazov/gocode-go/internal/permission"
)

func newPermissionServer(t *testing.T) (*Server, *permission.Engine) {
	t.Helper()
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Ask}}},
		nil, permission.Hooks{}, nil)
	return &Server{Permissions: engine}, engine
}

func TestPermissionEndpoints(t *testing.T) {
	server, engine := newPermissionServer(t)

	rec := doJSON(t, server, "GET", "/api/permission/request", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	_, effect, err := engine.Ask(permission.AssertInput{
		SessionID: "ses_1",
		Action:    "bash",
		Resources: []string{"ls"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if effect != permission.Ask {
		t.Fatalf("expected ask effect, got %v", effect)
	}

	rec = doJSON(t, server, "GET", "/api/session/ses_1/permission", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var requests []permission.Request
	if err := json.NewDecoder(rec.Body).Decode(&requests); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(requests))
	}
	requestID := requests[0].ID
	if requests[0].Action != "bash" {
		t.Fatalf("unexpected request: %+v", requests[0])
	}

	rec = doJSON(t, server, "POST", "/api/session/ses_1/permission/"+requestID+"/reply", map[string]string{"reply": "once"})
	if rec.Code != 200 {
		t.Fatalf("reply: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, server, "GET", "/api/session/ses_1/permission", nil)
	requests = nil
	if err := json.NewDecoder(rec.Body).Decode(&requests); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("expected no pending requests after reply, got %d", len(requests))
	}
}

func TestPermissionReplyValidation(t *testing.T) {
	server, engine := newPermissionServer(t)
	_, _, err := engine.Ask(permission.AssertInput{SessionID: "ses_1", Action: "bash", Resources: []string{"ls"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, server, "POST", "/api/permission/does-not-exist/reply", map[string]string{"reply": "once"})
	if rec.Code != 404 {
		t.Fatalf("expected 404 for unknown request, got %d", rec.Code)
	}
	pending := engine.List()
	rec = doJSON(t, server, "POST", "/api/permission/"+pending[0].ID+"/reply", map[string]string{"reply": "bogus"})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for invalid reply, got %d", rec.Code)
	}
}

func TestPermissionListEmpty(t *testing.T) {
	server, _ := newPermissionServer(t)
	rec := doJSON(t, server, "GET", "/api/permission/request", nil)
	var requests []permission.Request
	if err := json.NewDecoder(rec.Body).Decode(&requests); err != nil {
		t.Fatal(err)
	}
	if requests == nil || len(requests) != 0 {
		t.Fatalf("expected empty array, got %+v", requests)
	}
}
