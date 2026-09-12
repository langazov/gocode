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

// TestReplyRouteRejectsForeignSession pins the ownership check the
// session-scoped reply route implies: a request raised by ses_1 must not be
// settled through ses_2's path. Without the check the route accepted any
// request ID under any session, so consent addressed to one session could
// answer another's ask.
func TestReplyRouteRejectsForeignSession(t *testing.T) {
	server, engine := newPermissionServer(t)
	if _, _, err := engine.Ask(permission.AssertInput{
		SessionID: "ses_owner",
		Action:    "bash",
		Resources: []string{"ls"},
	}); err != nil {
		t.Fatal(err)
	}
	pending := engine.List()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(pending))
	}
	requestID := pending[0].ID

	// The foreign session's path must 404, not settle the request.
	rec := doJSON(t, server, "POST", "/api/session/ses_other/permission/"+requestID+"/reply",
		map[string]string{"reply": "once"})
	if rec.Code != 404 {
		t.Fatalf("foreign session reply: expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
	if still := engine.List(); len(still) != 1 {
		t.Fatalf("foreign reply settled the request: %d pending", len(still))
	}
	// The owning session's path still works.
	rec = doJSON(t, server, "POST", "/api/session/ses_owner/permission/"+requestID+"/reply",
		map[string]string{"reply": "once"})
	if rec.Code != 200 {
		t.Fatalf("owner reply: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if still := engine.List(); len(still) != 0 {
		t.Fatalf("owner reply left %d pending", len(still))
	}
	// The unscoped route is unchanged: request ID alone identifies it.
	if _, _, err := engine.Ask(permission.AssertInput{
		SessionID: "ses_owner",
		Action:    "bash",
		Resources: []string{"ls"},
	}); err != nil {
		t.Fatal(err)
	}
	pending = engine.List()
	rec = doJSON(t, server, "POST", "/api/permission/"+pending[0].ID+"/reply",
		map[string]string{"reply": "once"})
	if rec.Code != 200 {
		t.Fatalf("unscoped reply: expected 200, got %d", rec.Code)
	}
}
