package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/background"
	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/modelstate"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/skill"
)

// Server bundles the HTTP routes with their backing services.
type Server struct {
	Session     *session.Service
	Bus         *event.Bus
	Permissions *permission.Engine
	Models      *modelsdev.Service
	Agents      *agent.Registry
	Config      *config.Config
	MCP         *mcp.Service
	// Jobs tracks detached background subagents. nil unless background
	// subagents are enabled.
	Jobs *background.Registry
	// Questions owns the pending ask/reply rounds raised by the question tool
	// and plan mode.
	Questions *question.Service
	// Skills holds the discovered skills.
	Skills *skill.Registry
	// LSP owns the running language servers, for the status surfaces.
	LSP *lsp.Service
	// Commands holds the slash commands the interface completes and runs.
	Commands *command.Registry

	// oauth tracks in-flight provider logins started from the interface. A
	// device flow outlives the request that begins it, so the attempt is
	// parked here and polled.
	oauth oauthAttempts
}

// Mux builds the HTTP route tree. The TypeScript server exposes GET /api/health
// returning { healthy: true }; session routes follow the /api/session paths.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"healthy": true})
	})
	if s.Bus != nil {
		mux.HandleFunc("GET /api/event", s.eventStream)
	}
	if s.Permissions != nil {
		mux.HandleFunc("GET /api/permission/request", s.listPermissionRequests)
		mux.HandleFunc("POST /api/permission/{requestID}/reply", s.replyPermission)
		mux.HandleFunc("GET /api/session/{sessionID}/permission", s.listSessionPermissionRequests)
		mux.HandleFunc("POST /api/session/{sessionID}/permission/{requestID}/reply", s.replyPermission)
	}
	if s.Models != nil {
		mux.HandleFunc("GET /api/model", s.listModels)
		mux.HandleFunc("GET /api/provider", s.listProviders)
		mux.HandleFunc("GET /api/provider/{providerID}/auth", s.listProviderAuth)
		mux.HandleFunc("POST /api/provider/{providerID}/auth", s.setProviderKey)
		mux.HandleFunc("DELETE /api/provider/{providerID}/auth", s.logoutProvider)
		mux.HandleFunc("POST /api/provider/{providerID}/auth/oauth", s.startProviderOAuth)
		mux.HandleFunc("GET /api/provider/auth/oauth/{attemptID}", s.providerOAuthStatus)
	}
	if s.Agents != nil {
		mux.HandleFunc("GET /api/agent", s.listAgents)
	}
	if s.MCP != nil {
		mux.HandleFunc("GET /api/mcp", s.listMCP)
	}
	if s.Jobs != nil {
		mux.HandleFunc("GET /api/job", s.listJobs)
		mux.HandleFunc("POST /api/session/{sessionID}/background", s.backgroundSession)
	}
	if s.Questions != nil {
		mux.HandleFunc("GET /api/question", s.listQuestions)
		mux.HandleFunc("GET /api/session/{sessionID}/question", s.listSessionQuestions)
		mux.HandleFunc("POST /api/question/{requestID}/reply", s.replyQuestion)
		mux.HandleFunc("POST /api/question/{requestID}/reject", s.rejectQuestion)
	}
	if s.Skills != nil {
		mux.HandleFunc("GET /api/skill", s.listSkills)
	}
	mux.HandleFunc("GET /api/lsp", s.listLSP)
	mux.HandleFunc("GET /api/command", s.listCommands)
	if s.Session != nil {
		mux.HandleFunc("POST /api/session", s.createSession)
		mux.HandleFunc("GET /api/session", s.listSessions)
		mux.HandleFunc("GET /api/session/{sessionID}", s.getSession)
		mux.HandleFunc("POST /api/session/{sessionID}/prompt", s.promptSession)
		mux.HandleFunc("POST /api/session/{sessionID}/interrupt", s.interruptSession)
		mux.HandleFunc("GET /api/session/{sessionID}/message", s.listMessages)
		mux.HandleFunc("GET /api/session/{sessionID}/todo", s.listTodos)
		mux.HandleFunc("GET /api/session/{sessionID}/stats", s.sessionStats)
		mux.HandleFunc("POST /api/session/{sessionID}/model", s.setModel)
		mux.HandleFunc("POST /api/session/{sessionID}/agent", s.setAgent)
		mux.HandleFunc("POST /api/session/{sessionID}/rename", s.renameSession)
		mux.HandleFunc("DELETE /api/session/{sessionID}", s.deleteSession)
		mux.HandleFunc("GET /api/session/{sessionID}/children", s.listChildren)
		mux.HandleFunc("POST /api/session/{sessionID}/fork", s.forkSession)
		mux.HandleFunc("POST /api/session/{sessionID}/compact", s.compactSession)
	}
	return mux
}

// Mux returns the health-only route tree (no session service), kept for
// callers that only need liveness.
func Mux() *http.ServeMux {
	return (&Server{}).Mux()
}

// Run starts the HTTP server on addr and blocks until it fails or is closed.
func Run(addr string) error {
	return RunWith(addr, nil, nil, nil)
}

// RunWith starts the HTTP server with explicit session, event, and permission
// services.
func RunWith(addr string, sessionService *session.Service, bus *event.Bus, permissions *permission.Engine) error {
	server := &Server{Session: sessionService, Bus: bus, Permissions: permissions}
	srv := &http.Server{Addr: addr, Handler: server.Mux()}
	return srv.ListenAndServe()
}

// Serve accepts connections on a prepared listener using the same route
// tree, for embedded deployments like the TUI's in-process server.
func Serve(listener net.Listener, sessionService *session.Service, bus *event.Bus, permissions *permission.Engine) error {
	server := &Server{Session: sessionService, Bus: bus, Permissions: permissions}
	return ServeOn(listener, server.Mux())
}

// ServeOn serves a prepared mux on the listener.
func ServeOn(listener net.Listener, handler http.Handler) error {
	srv := &http.Server{Handler: handler}
	return srv.Serve(listener)
}

type permissionReplyRequest struct {
	Reply   string `json:"reply"`
	Message string `json:"message"`
}

func (s *Server) listPermissionRequests(w http.ResponseWriter, r *http.Request) {
	requests := s.Permissions.List()
	if requests == nil {
		requests = []permission.Request{}
	}
	writeJSON(w, http.StatusOK, requests)
}

func (s *Server) listSessionPermissionRequests(w http.ResponseWriter, r *http.Request) {
	requests := s.Permissions.ForSession(r.PathValue("sessionID"))
	if requests == nil {
		requests = []permission.Request{}
	}
	writeJSON(w, http.StatusOK, requests)
}

func (s *Server) replyPermission(w http.ResponseWriter, r *http.Request) {
	var body permissionReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reply := permission.Reply(body.Reply)
	switch reply {
	case permission.ReplyOnce, permission.ReplyAlways, permission.ReplyReject:
	default:
		writeError(w, http.StatusBadRequest, "reply must be once, always, or reject")
		return
	}
	if err := s.Permissions.Reply(r.PathValue("requestID"), reply, body.Message); err != nil {
		var notFound *permission.NotFoundError
		if errors.As(err, &notFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type eventWire struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Data    map[string]any `json:"data,omitempty"`
	Seq     *int           `json:"seq,omitempty"`
	Session string         `json:"sessionID,omitempty"`
}

// eventStreamBuffer bounds one SSE subscriber's backlog.
const eventStreamBuffer = 1024

// eventStream serves a server-sent event stream of committed events,
// optionally filtered by ?sessionID=.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Subscribe before writing headers so the client cannot observe the
	// response (and start publishing) before the subscription is live.
	//
	// The buffer is sized for multi-agent load: with several subagent
	// sessions settling tools at once the event rate is far higher than the
	// single-session case this originally assumed. Bus.Subscribe drops on a
	// full buffer by design, and a drop is recoverable — clients treat
	// streamed deltas as a liveness hint and refetch the durable timeline —
	// but a drop still costs a redraw, so the buffer is generous.
	events, unsubscribe := s.Bus.Subscribe(eventStreamBuffer)
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	filter := r.URL.Query().Get("sessionID")
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-events:
			wire := toEventWire(payload)
			if filter != "" && wire.Session != filter {
				continue
			}
			encoded, err := json.Marshal(wire)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: " + string(encoded) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func toEventWire(payload event.Payload) eventWire {
	wire := eventWire{ID: payload.ID, Type: payload.Type, Data: payload.Data}
	if payload.Durable != nil {
		seq := payload.Durable.Seq
		wire.Seq = &seq
	}
	if sessionID, ok := payload.Data["sessionID"].(string); ok {
		wire.Session = sessionID
	}
	return wire
}

type createSessionRequest struct {
	Directory string `json:"directory"`
	Title     string `json:"title"`
}

type promptRequest struct {
	Text     string `json:"text"`
	Delivery string `json:"delivery"`
	// Files carries attachments (a pasted image, say) as data: URIs.
	Files []session.FileAttachment `json:"files,omitempty"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Directory == "" {
		writeError(w, http.StatusBadRequest, "directory is required")
		return
	}
	info, err := s.Session.Create(r.Context(), session.CreateInput{
		Directory: body.Directory,
		Title:     body.Title,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Session.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []session.Info{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	info, err := s.Session.Get(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) promptSession(w http.ResponseWriter, r *http.Request) {
	var body promptRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// A message carrying only an attachment is legitimate; text is required
	// only when there is nothing else to send.
	if body.Text == "" && len(body.Files) == 0 {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	messageID, err := s.Session.PromptWith(r.Context(), r.PathValue("sessionID"),
		session.Prompt{Text: body.Text, Files: body.Files},
		session.Delivery(body.Delivery))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"messageID": messageID})
}

func (s *Server) interruptSession(w http.ResponseWriter, r *http.Request) {
	s.Session.Interrupt(r.PathValue("sessionID"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := s.Session.Messages.List(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type messageResponse struct {
		ID          string          `json:"id"`
		SessionID   string          `json:"sessionID"`
		Type        string          `json:"type"`
		Seq         int             `json:"seq"`
		TimeCreated int64           `json:"timeCreated"`
		Data        json.RawMessage `json:"data"`
	}
	out := make([]messageResponse, 0, len(messages))
	for _, message := range messages {
		out = append(out, messageResponse{
			ID:          message.ID,
			SessionID:   message.SessionID,
			Type:        message.Type,
			Seq:         message.Seq,
			TimeCreated: message.TimeCreated,
			Data:        message.Data,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

type modelEntry struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	// ContextLimit is models.dev's `limit.context`, the denominator the TUI
	// footer's usage segment needs to turn a token total into a percentage
	// (prompt/index.tsx reads `model.limit.context` off the same catalog).
	ContextLimit int `json:"contextLimit,omitempty"`
	// CostInput is models.dev's `cost.input` (dollars per million input
	// tokens). The sidebar footer's getting-started card keys off exactly
	// this: it greets users whose only usable models are the free ones.
	CostInput float64 `json:"costInput,omitempty"`
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.Models.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	catalog = resolveCatalog(r.Context(), catalog)
	out := []modelEntry{}
	seen := map[string]bool{}
	// Only providers the user can actually reach — see available.go. The
	// catalog is the database, not the list.
	availability := newProviderAvailability(s.Config)
	appendModel := func(providerID string, provider config.Provider, modelID string, model modelsdev.Model) {
		key := providerID + "/" + modelID
		if seen[key] {
			return
		}
		if !availability.available(providerID, catalog[providerID]) {
			return
		}
		seen[key] = true
		name := model.Name
		if name == "" {
			name = modelID
		}
		entry := modelEntry{
			ProviderID:   providerID,
			ID:           modelID,
			Name:         name,
			ContextLimit: int(model.Limit.Context),
		}
		if model.Cost != nil {
			entry.CostInput = model.Cost.Input
		}
		out = append(out, entry)
	}
	for providerID, entry := range catalog {
		for modelID, model := range providerModels(r.Context(), providerID, entry, s.Config) {
			appendModel(providerID, config.Provider{}, modelID, model)
		}
	}
	// Config-defined providers and models extend the catalog.
	if s.Config != nil {
		for providerID, provider := range s.Config.Provider {
			for modelID, model := range provider.Models {
				appendModel(providerID, provider, modelID, modelsdev.Model{Name: model.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.Models.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type providerEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Connected reports a stored credential, which the connect dialog
		// ticks. Env-var credentials count too: they are equally usable.
		Connected bool `json:"connected,omitempty"`
		// Available reports that the provider can be used right now. The
		// connect dialog lists every catalog provider, including ones with no
		// credential yet, and uses this to tell them apart.
		Available bool `json:"available,omitempty"`
	}
	out := []providerEntry{}
	availability := newProviderAvailability(s.Config)
	connected := connectedProviders()
	all := r.URL.Query().Get("all") == "true"
	for id, provider := range catalog {
		usable := availability.available(id, provider)
		if !usable && !all {
			continue
		}
		if !availability.allowed(id) {
			continue
		}
		out = append(out, providerEntry{
			ID:        id,
			Name:      provider.Name,
			Connected: connected[id],
			Available: usable,
		})
	}
	// A config-declared provider need not exist in the catalog at all.
	if s.Config != nil {
		for id, provider := range s.Config.Provider {
			if _, inCatalog := catalog[id]; inCatalog || !availability.allowed(id) {
				continue
			}
			name := provider.Name
			if name == "" {
				name = id
			}
			out = append(out, providerEntry{ID: id, Name: name, Connected: connected[id], Available: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, out)
}

type agentEntry struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"`
	Description string `json:"description,omitempty"`
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.Agents.All()
	out := make([]agentEntry, 0, len(agents))
	for _, info := range agents {
		out = append(out, agentEntry{ID: info.ID, Mode: info.Mode, Description: info.Description})
	}
	writeJSON(w, http.StatusOK, out)
}

// listMCP mirrors GET /mcp in server/routes/.../mcp.ts: the live per-server
// connection status map, keyed by server name.
func (s *Server) listMCP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.MCP.Statuses())
}

type setModelRequest struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
}

func (s *Server) setModel(w http.ResponseWriter, r *http.Request) {
	var body setModelRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "providerID and id are required")
		return
	}
	sessionID := r.PathValue("sessionID")
	if err := s.Session.SetModel(r.Context(), sessionID, session.ModelRef{
		ProviderID: body.ProviderID, ID: body.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Remember the last-used model per directory so restarts resume with it.
	if info, err := s.Session.Get(r.Context(), sessionID); err == nil && info != nil {
		_ = modelstate.Save(info.Directory, modelstate.Ref{
			ProviderID: body.ProviderID, ModelID: body.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Agent == "" {
		writeError(w, http.StatusBadRequest, "agent is required")
		return
	}
	if err := s.Session.SetAgent(r.Context(), r.PathValue("sessionID"), body.Agent); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.Session.Rename(r.Context(), r.PathValue("sessionID"), body.Title); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.Session.Delete(r.Context(), r.PathValue("sessionID")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) {
	todos, err := s.Session.Todos(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if todos == nil {
		todos = []session.Todo{}
	}
	writeJSON(w, http.StatusOK, todos)
}

func (s *Server) sessionStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Session.Stats(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) listChildren(w http.ResponseWriter, r *http.Request) {
	children, err := s.Session.Children(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if children == nil {
		children = []session.Info{}
	}
	writeJSON(w, http.StatusOK, children)
}

func (s *Server) forkSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"messageID"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	child, err := s.Session.Fork(r.Context(), r.PathValue("sessionID"), body.MessageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, child)
}

func (s *Server) compactSession(w http.ResponseWriter, r *http.Request) {
	compacted, err := s.Session.CompactNow(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"compacted": compacted})
}

// listJobs reports every background job this process is tracking.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Jobs.List())
}

// backgroundSession promotes every running foreground subagent of a session,
// so the user can push in-flight work to the background and get their prompt
// back. Ports the TypeScript sessionBackground handler.
func (s *Server) backgroundSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	promoted := s.Jobs.PromoteSession(sessionID)
	writeJSON(w, http.StatusOK, map[string]any{"promoted": promoted})
}

type questionReplyRequest struct {
	Answers [][]string `json:"answers"`
}

// listQuestions reports every question waiting on a user.
func (s *Server) listQuestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Questions.List())
}

func (s *Server) listSessionQuestions(w http.ResponseWriter, r *http.Request) {
	pending := s.Questions.ForSession(r.PathValue("sessionID"))
	if pending == nil {
		pending = []question.Request{}
	}
	writeJSON(w, http.StatusOK, pending)
}

// replyQuestion answers a pending question, unblocking the tool call that
// asked it.
func (s *Server) replyQuestion(w http.ResponseWriter, r *http.Request) {
	var body questionReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	answers := make([]question.Answer, 0, len(body.Answers))
	for _, answer := range body.Answers {
		answers = append(answers, question.Answer(answer))
	}
	if err := s.Questions.Reply(r.PathValue("requestID"), answers); err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// rejectQuestion declines a pending question, failing the tool call.
func (s *Server) rejectQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.Questions.Reject(r.PathValue("requestID")); err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// listSkills reports the discovered skills, without their bodies — the tool
// is what injects a skill's content.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	infos := s.Skills.List()
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, map[string]any{
			"name":        info.Name,
			"description": info.Description,
			"slash":       info.Slash,
			"location":    info.Location,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
