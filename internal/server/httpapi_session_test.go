package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/session"
)

// Ported from packages/opencode/test/server/httpapi-session.test.ts, for the
// routes this port serves. Upstream's suite additionally covers instance
// context, workspace routing, compression, CORS, authorization and the
// generated SDK surface; none of those exist here.

func newSession(t *testing.T, server *Server) session.Info {
	t.Helper()
	rec := doJSON(t, server, "POST", "/api/session", map[string]string{"directory": t.TempDir()})
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var info session.Info
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	return info
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// A session that is not there is 404, not 500. The distinction is the whole
// contract for a client: a 500 says the server broke and the call is worth
// retrying, a 404 says the thing is gone and it is not. These all used to
// answer 500 — /stats with a raw "sql: no rows in result set" — so a deleted
// session was indistinguishable from an outage.
func TestMissingSessionIsNotFound(t *testing.T) {
	server, _, _ := newTestServer(t)
	const missing = "ses_does_not_exist"

	for _, c := range []struct {
		method string
		path   string
		body   any
	}{
		{"GET", "/api/session/" + missing, nil},
		{"GET", "/api/session/" + missing + "/stats", nil},
		{"POST", "/api/session/" + missing + "/prompt", map[string]any{"text": "hi"}},
		{"POST", "/api/session/" + missing + "/rename", map[string]any{"title": "x"}},
		{"POST", "/api/session/" + missing + "/agent", map[string]any{"agent": "build"}},
		{"POST", "/api/session/" + missing + "/model", map[string]any{"providerID": "anthropic", "id": "claude"}},
		{"POST", "/api/session/" + missing + "/fork", map[string]any{"messageID": "msg_1"}},
		{"DELETE", "/api/session/" + missing, nil},
	} {
		rec := doJSON(t, server, c.method, c.path, c.body)
		if rec.Code != 404 {
			t.Errorf("%s %s = %d, want 404 (%s)", c.method, c.path, rec.Code, rec.Body.String())
		}
		// The body must not leak the storage layer at a client.
		if body := rec.Body.String(); contains(body, "sql:") {
			t.Errorf("%s %s leaked a database error: %s", c.method, c.path, body)
		}
	}
}

// Malformed input is the caller's fault and says so, rather than being
// accepted and failing later somewhere less obvious.
func TestSessionRoutesValidateInput(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	for _, c := range []struct {
		name string
		path string
		body any
	}{
		{"prompt with neither text nor files", "/api/session/" + info.ID + "/prompt", map[string]any{}},
		{"rename with no title", "/api/session/" + info.ID + "/rename", map[string]any{}},
		{"agent with no id", "/api/session/" + info.ID + "/agent", map[string]any{}},
		{"model with no id", "/api/session/" + info.ID + "/model", map[string]any{"providerID": "anthropic"}},
	} {
		if rec := doJSON(t, server, "POST", c.path, c.body); rec.Code != 400 {
			t.Errorf("%s = %d, want 400 (%s)", c.name, rec.Code, rec.Body.String())
		}
	}

	// A body that is not JSON at all.
	req := httptest.NewRequest("POST", "/api/session/"+info.ID+"/prompt", stringReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Mux().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("an unparseable body = %d, want 400", rec.Code)
	}
}

// The mutating routes are only worth anything if the change is readable back.
func TestSessionMutationsArePersisted(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	if rec := doJSON(t, server, "POST", "/api/session/"+info.ID+"/rename",
		map[string]string{"title": "renamed"}); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	after := decodeBody[session.Info](t, doJSON(t, server, "GET", "/api/session/"+info.ID, nil))
	if after.Title != "renamed" {
		t.Errorf("title = %q, want renamed", after.Title)
	}

	if rec := doJSON(t, server, "POST", "/api/session/"+info.ID+"/model",
		map[string]string{"providerID": "anthropic", "id": "claude-x"}); rec.Code != 200 {
		t.Fatalf("set model: %d %s", rec.Code, rec.Body.String())
	}
	after = decodeBody[session.Info](t, doJSON(t, server, "GET", "/api/session/"+info.ID, nil))
	if after.Model == nil || after.Model.ID != "claude-x" {
		t.Errorf("model = %+v, want claude-x", after.Model)
	}

	if rec := doJSON(t, server, "POST", "/api/session/"+info.ID+"/agent",
		map[string]string{"agent": "plan"}); rec.Code != 200 {
		t.Fatalf("set agent: %d %s", rec.Code, rec.Body.String())
	}
}

// Delete removes the session, and the routes that read it agree afterwards.
// A delete that reported success while leaving the row behind would look
// identical from the call that performed it.
func TestDeleteSessionRemovesIt(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	if rec := doJSON(t, server, "DELETE", "/api/session/"+info.ID, nil); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, server, "GET", "/api/session/"+info.ID, nil); rec.Code != 404 {
		t.Errorf("a deleted session should be gone, got %d", rec.Code)
	}
	for _, listed := range decodeBody[[]session.Info](t, doJSON(t, server, "GET", "/api/session", nil)) {
		if listed.ID == info.ID {
			t.Error("a deleted session is still listed")
		}
	}
	// Deleting twice is a 404, not a success.
	if rec := doJSON(t, server, "DELETE", "/api/session/"+info.ID, nil); rec.Code != 404 {
		t.Errorf("deleting twice = %d, want 404", rec.Code)
	}
}

// A collection route answers with an empty array, never null: a client
// decoding JSON into a list gets a list, and `for x of null` is the bug this
// prevents.
func TestSessionCollectionsAreNeverNull(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	for _, path := range []string{
		"/api/session",
		"/api/session/" + info.ID + "/message",
		"/api/session/" + info.ID + "/todo",
		"/api/session/" + info.ID + "/children",
		"/api/session/" + info.ID + "/queue",
	} {
		rec := doJSON(t, server, "GET", path, nil)
		if rec.Code != 200 {
			t.Errorf("GET %s = %d (%s)", path, rec.Code, rec.Body.String())
			continue
		}
		if body := trimSpace(rec.Body.String()); body == "null" {
			t.Errorf("GET %s returned null, want []", path)
		}
	}
}

// Stats start at zero rather than absent, so the interface has numbers to show
// before the first turn settles.
func TestSessionStatsStartAtZero(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	stats := decodeBody[map[string]any](t, doJSON(t, server, "GET", "/api/session/"+info.ID+"/stats", nil))
	if len(stats) == 0 {
		t.Fatal("stats should report a zeroed record, not an empty object")
	}
	if cost, ok := stats["cost"].(float64); !ok || cost != 0 {
		t.Errorf("cost = %v, want 0", stats["cost"])
	}
}

// Status reports whether a turn is running. An idle session is idle, and the
// route is the authoritative answer a client reconciles its event stream
// against — see session.Service.Busy.
func TestSessionStatusReportsIdle(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	status := decodeBody[map[string]bool](t, doJSON(t, server, "GET", "/api/session/"+info.ID+"/status", nil))
	if status["busy"] {
		t.Error("a fresh session is not busy")
	}
}

// Interrupting a session with nothing running is a no-op rather than an error:
// the interface sends it on a keypress and cannot know whether the turn ended
// microseconds earlier.
func TestInterruptIsIdempotent(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	for range 2 {
		if rec := doJSON(t, server, "POST", "/api/session/"+info.ID+"/interrupt", map[string]any{}); rec.Code != 200 {
			t.Fatalf("interrupt: %d %s", rec.Code, rec.Body.String())
		}
	}
}

// A prompt is admitted durably and reported back by id, which is the id the
// message will carry once the runner promotes it.
func TestPromptReturnsTheMessageID(t *testing.T) {
	server, _, _ := newTestServer(t)
	info := newSession(t, server)

	rec := doJSON(t, server, "POST", "/api/session/"+info.ID+"/prompt", map[string]any{"text": "hello"})
	if rec.Code != 200 {
		t.Fatalf("prompt: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody[map[string]string](t, rec)
	if body["messageID"] == "" {
		t.Fatal("a prompt must report the message id it was admitted under")
	}

	// A prompt carrying only an attachment is legitimate — text is required
	// only when there is nothing else to send.
	rec = doJSON(t, server, "POST", "/api/session/"+info.ID+"/prompt", map[string]any{
		"files": []map[string]string{{"name": "a.png", "mime": "image/png", "uri": "data:image/png;base64,AAAA"}},
	})
	if rec.Code != 200 {
		t.Fatalf("attachment-only prompt: %d %s", rec.Code, rec.Body.String())
	}

	// A prompt wakes execution on its own goroutine. Join it before the test
	// returns, or its writes race t.TempDir's cleanup.
	settle(t, server, info.ID)
}

// settle joins any drain the session has in flight, so a test that prompts
// does not leave a goroutine running past its own cleanup.
func settle(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	if server.Session.Execution == nil {
		return
	}
	_ = server.Session.Execution.Resume(context.Background(), sessionID)
}

// An unknown route is a 404 from the mux rather than a panic or a hang, and an
// unroutable method on a known path is not silently treated as a GET.
func TestUnknownRoutes(t *testing.T) {
	server, _, _ := newTestServer(t)
	if rec := doJSON(t, server, "GET", "/api/nope", nil); rec.Code != 404 {
		t.Errorf("unknown path = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, server, "PATCH", "/api/session", nil); rec.Code == 200 {
		t.Error("an unrouted method must not succeed")
	}
}

// Small helpers, kept local so the assertions above read as assertions.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
func trimSpace(s string) string             { return strings.TrimSpace(s) }
func stringReader(s string) io.Reader       { return strings.NewReader(s) }
