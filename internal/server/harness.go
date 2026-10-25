package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/harness"
)

// The /api/harness routes expose Harness² (internal/harness) to clients: the
// TUI is an HTTP client even locally, so a run it starts, watches or installs
// goes through here. Progress streams on /api/event as live `harness.*`
// events; see harnessEvent in cmd/gocode.

// HarnessPublisher publishes Harness² progress on bus as live (non-durable)
// `harness.<type>` events carrying the run's state and the event. Live events
// have no projector and are not stored: they exist for connected clients.
func HarnessPublisher(bus *event.Bus) func(harness.RunState, harness.Event) {
	return func(state harness.RunState, ev harness.Event) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = bus.Publish(ctx, event.Definition{Type: "harness." + ev.Type}, map[string]any{
			"run": state, "event": ev,
		}, event.PublishOptions{})
	}
}

func (s *Server) registerHarness(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/harness/run", s.listHarnessRuns)
	mux.HandleFunc("POST /api/harness/run", s.startHarnessRun)
	mux.HandleFunc("GET /api/harness/run/{runID}", s.getHarnessRun)
	mux.HandleFunc("POST /api/harness/run/{runID}/cancel", s.cancelHarnessRun)
	mux.HandleFunc("DELETE /api/harness/run/{runID}", s.deleteHarnessRun)
	mux.HandleFunc("GET /api/harness/run/{runID}/stage/{label}", s.getHarnessStage)
	mux.HandleFunc("GET /api/harness/run/{runID}/log", s.getHarnessLog)
	mux.HandleFunc("POST /api/harness/run/{runID}/install", s.installHarnessRun)
	mux.HandleFunc("GET /api/harness/library", s.listHarnessLibrary)
	mux.HandleFunc("GET /api/harness/library/{domain}/{task}", s.getHarnessEntry)
	mux.HandleFunc("DELETE /api/harness/library/{domain}/{task}", s.forgetHarnessEntry)
	mux.HandleFunc("POST /api/harness/library/sync", s.syncHarnessLibrary)
}

// startHarnessRequest is the body of POST /api/harness/run. Prompt is the
// task; the rest default the way `gocode harness run` defaults them.
type startHarnessRequest struct {
	Prompt        string           `json:"prompt"`
	ID            string           `json:"id,omitempty"`
	Domain        string           `json:"domain,omitempty"`
	Workspace     string           `json:"workspace,omitempty"`
	K             int              `json:"k,omitempty"`
	Mode          string           `json:"mode,omitempty"`
	Model         string           `json:"model,omitempty"`
	ImproverModel string           `json:"improverModel,omitempty"`
	Library       string           `json:"library,omitempty"`
	CrossDomain   bool             `json:"crossDomain,omitempty"`
	Prompts       *harness.Prompts `json:"prompts,omitempty"`
	// DryRun returns the plan (budget, backend, rows) without starting.
	DryRun bool `json:"dryRun,omitempty"`
}

func (s *Server) startHarnessRun(w http.ResponseWriter, r *http.Request) {
	var body startHarnessRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	workspace := body.Workspace
	if workspace == "" {
		workspace = s.VCSWorkdir
	}
	if workspace == "" {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return
	}
	task := harness.Task{ID: body.ID, Domain: body.Domain, Prompt: prompt, Workspace: workspace}
	if task.ID == "" {
		task.ID = harness.Slug(prompt)
	}
	if task.Domain == "" {
		task.Domain = harness.Slug(filepath.Base(workspace))
	}
	if body.Prompts != nil {
		task.Prompts = *body.Prompts
	}
	mode, err := harness.ParseMode(body.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	k := body.K
	if k < 1 {
		k = 1
	}
	launch := harness.Launch{
		Task: task, K: k, Mode: mode, Model: body.Model, ImproverModel: body.ImproverModel,
		Library: body.Library, CrossDomain: body.CrossDomain,
	}
	if body.DryRun {
		task.Prompts = task.Prompts.WithDefaults()
		if err := task.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		detection := harness.Detection{Backend: "unknown"}
		if s.Harness.Store != nil {
			if _, d, err := s.Harness.Store(r.Context()); err == nil {
				detection = d
			} else {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		library := detection.Backend != "unknown"
		writeJSON(w, http.StatusOK, harness.RunState{
			ID: harness.RunID(task.ID, k, mode), Task: task.ID, Domain: task.Domain, K: k, Mode: mode,
			Status: "planned", Backend: detection.Backend, Reason: detection.Reason,
			Rows: harness.PlannedRows(mode, k, library), Budget: harness.CostLine(mode, k, library),
		})
		return
	}
	state, err := s.Harness.Start(r.Context(), launch)
	switch {
	case errors.Is(err, harness.ErrRunning):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, state)
	}
}

func (s *Server) listHarnessRuns(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Harness.List())
}

func (s *Server) getHarnessRun(w http.ResponseWriter, r *http.Request) {
	state, err := s.Harness.Get(r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) cancelHarnessRun(w http.ResponseWriter, r *http.Request) {
	if !s.Harness.Cancel(r.PathValue("runID")) {
		writeError(w, http.StatusNotFound, "no running run with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// getHarnessLog serves a run's event log; ?after=N skips the first N, so a
// follower asks only for what is new.
func (s *Server) getHarnessLog(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	events, err := s.Harness.Log(r.PathValue("runID"), after)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) deleteHarnessRun(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.Harness.Delete(r.Context(), r.PathValue("runID"))
	switch {
	case errors.Is(err, harness.ErrStillRunning):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil && len(deleted.Removed) == 0:
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, deleted)
	}
}

func (s *Server) getHarnessStage(w http.ResponseWriter, r *http.Request) {
	detail, err := s.Harness.Stage(r.PathValue("runID"), r.PathValue("label"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) installHarnessRun(w http.ResponseWriter, r *http.Request) {
	var req harness.InstallRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if s.Harness.Install == nil {
		writeError(w, http.StatusServiceUnavailable, "install is not available on this server")
		return
	}
	summary, err := s.Harness.Summary(r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no finished run with that id")
		return
	}
	result, err := s.Harness.Install(r.Context(), summary, req)
	if err != nil {
		// The result still says what conflicted, so a client can show it.
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type harnessLibraryDomain struct {
	Domain  string              `json:"domain"`
	Entries []harness.EntryInfo `json:"entries"`
}

func (s *Server) harnessStore(w http.ResponseWriter, r *http.Request) (harness.ExperienceStore, harness.Detection, bool) {
	if s.Harness.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "the experience library is not available on this server")
		return nil, harness.Detection{}, false
	}
	store, detection, err := s.Harness.Store(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return nil, detection, false
	}
	return store, detection, true
}

func (s *Server) listHarnessLibrary(w http.ResponseWriter, r *http.Request) {
	store, detection, ok := s.harnessStore(w, r)
	if !ok {
		return
	}
	domains, err := store.Domains(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []harnessLibraryDomain{}
	for _, domain := range domains {
		entries, err := store.List(r.Context(), domain)
		if err != nil {
			continue
		}
		out = append(out, harnessLibraryDomain{Domain: domain, Entries: entries})
	}
	writeJSON(w, http.StatusOK, map[string]any{"backend": detection.Backend, "reason": detection.Reason, "domains": out})
}

func (s *Server) getHarnessEntry(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.harnessStore(w, r)
	if !ok {
		return
	}
	entry, err := store.Get(r.Context(), r.PathValue("domain"), r.PathValue("task"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": entry, "tree": entry.Tree})
}

func (s *Server) forgetHarnessEntry(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.harnessStore(w, r)
	if !ok {
		return
	}
	if err := store.Forget(r.Context(), r.PathValue("domain"), r.PathValue("task")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncHarnessLibrary(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.harnessStore(w, r)
	if !ok {
		return
	}
	cloud, isCloud := store.(*harness.CloudStore)
	if !isCloud {
		writeError(w, http.StatusConflict, "sync needs the cloud backend (harness.library: cloud)")
		return
	}
	pushed, err := cloud.Sync(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"uploaded": pushed})
}
