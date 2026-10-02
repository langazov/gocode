package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/vcs/gitops"
)

func decodeGit[T any](t *testing.T, server *Server, method, path string, body any) T {
	t.Helper()
	rec := doJSON(t, server, method, path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	var out T
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGitStatusListsChanges(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil)
	if !st.IsRepo || st.Branch != "main" || !st.HasCommits || st.Root == "" {
		t.Fatalf("status = %+v", st)
	}
	kinds := map[string]string{}
	for _, f := range st.Files {
		kinds[f.Path] = f.Status
	}
	if kinds["hello.txt"] != "M" || kinds["new.txt"] != "?" {
		t.Fatalf("files = %+v", st.Files)
	}
}

func TestGitDirectoryParameterOverridesWorkdir(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = t.TempDir() // not a repository
	repo := vcsTestRepo(t)

	st := decodeGit[gitops.Status](t, server, http.MethodGet,
		"/api/vcs/git/status?directory="+url.QueryEscape(repo), nil)
	if !st.IsRepo {
		t.Fatalf("directory parameter ignored: %+v", st)
	}

	rec := doJSON(t, server, http.MethodGet, "/api/vcs/git/status?directory=relative/dir", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("relative directory: %d, want 400", rec.Code)
	}
}

func TestGitStatusWithoutWorkdirIsNotARepo(t *testing.T) {
	server, _, _ := newTestServer(t)

	st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil)
	if st.IsRepo {
		t.Fatalf("workdir-less status = %+v, want not a repo", st)
	}
}

func TestGitInitOutsideRepository(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = t.TempDir()
	if st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil); st.IsRepo {
		t.Fatalf("temp dir already a repo: %+v", st)
	}
	decodeGit[struct{}](t, server, http.MethodPost, "/api/vcs/git/init", struct{}{})
	if st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil); !st.IsRepo {
		t.Fatalf("after init: %+v", st)
	}
}

func TestGitStageCommitLogShow(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	diff := decodeGit[map[string]string](t, server, http.MethodGet, "/api/vcs/git/diff?path=hello.txt", nil)
	if !strings.Contains(diff["diff"], "+line two") {
		t.Fatalf("working diff = %q", diff["diff"])
	}

	decodeGit[struct{}](t, server, http.MethodPost, "/api/vcs/git/stage", map[string]any{"paths": []string{"hello.txt"}})
	staged := decodeGit[map[string]string](t, server, http.MethodGet, "/api/vcs/git/diff?path=hello.txt&staged=true", nil)
	if !strings.Contains(staged["diff"], "+line two") {
		t.Fatalf("staged diff = %q", staged["diff"])
	}

	commit := decodeGit[map[string]string](t, server, http.MethodPost, "/api/vcs/git/commit", map[string]any{"message": "second"})
	if commit["commit"] == "" {
		t.Fatalf("commit reply = %+v", commit)
	}

	logReply := decodeGit[struct {
		Commits []gitops.CommitInfo `json:"commits"`
	}](t, server, http.MethodGet, "/api/vcs/git/log", nil)
	if len(logReply.Commits) != 2 || logReply.Commits[0].Subject != "second" {
		t.Fatalf("log = %+v", logReply.Commits)
	}

	show := decodeGit[gitops.CommitDetails](t, server, http.MethodGet, "/api/vcs/git/show/"+logReply.Commits[0].Hash, nil)
	if len(show.Files) != 1 || show.Files[0].Path != "hello.txt" || show.Additions != 1 {
		t.Fatalf("show = %+v", show)
	}

	// new.txt is still untracked: committing nothing staged with an empty
	// message is git's refusal, reported as 422 with its message.
	rec := doJSON(t, server, http.MethodPost, "/api/vcs/git/commit", map[string]any{"message": ""})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "empty commit message") {
		t.Fatalf("empty commit: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGitApplyHunkPatch(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	diff := decodeGit[map[string]string](t, server, http.MethodGet, "/api/vcs/git/diff?path=hello.txt", nil)
	decodeGit[struct{}](t, server, http.MethodPost, "/api/vcs/git/apply", map[string]any{"patch": diff["diff"], "cached": true})

	st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil)
	var stagedHello bool
	for _, f := range st.Files {
		if f.Path == "hello.txt" && f.Staged {
			stagedHello = true
		}
	}
	if !stagedHello {
		t.Fatalf("hunk not staged: %+v", st.Files)
	}

	rec := doJSON(t, server, http.MethodPost, "/api/vcs/git/apply", map[string]any{"patch": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d, want 400", rec.Code)
	}
}

func TestGitBranchesAndCheckout(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	decodeGit[struct{}](t, server, http.MethodPost, "/api/vcs/git/checkout", map[string]any{"branch": "feature", "create": true})
	branches := decodeGit[struct {
		Current  string              `json:"current"`
		Branches []gitops.BranchInfo `json:"branches"`
		Remote   []string            `json:"remote"`
	}](t, server, http.MethodGet, "/api/vcs/git/branches", nil)
	if branches.Current != "feature" || len(branches.Branches) != 2 || branches.Remote == nil {
		t.Fatalf("branches = %+v", branches)
	}
}

func TestGitStashRoundTrip(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)

	pushed := decodeGit[struct {
		Entries []gitops.StashEntry `json:"entries"`
	}](t, server, http.MethodPost, "/api/vcs/git/stash", map[string]any{"op": "push", "message": "wip", "includeUntracked": true})
	if len(pushed.Entries) != 1 || !strings.Contains(pushed.Entries[0].Message, "wip") {
		t.Fatalf("stash push = %+v", pushed.Entries)
	}
	if st := decodeGit[gitops.Status](t, server, http.MethodGet, "/api/vcs/git/status", nil); len(st.Files) != 0 || st.StashCount != 1 {
		t.Fatalf("after stash: %+v", st)
	}
	popped := decodeGit[struct {
		Entries []gitops.StashEntry `json:"entries"`
	}](t, server, http.MethodPost, "/api/vcs/git/stash", map[string]any{"op": "pop"})
	if len(popped.Entries) != 0 {
		t.Fatalf("stash pop = %+v", popped.Entries)
	}
}

// recordingProvider streams a scripted reply and keeps the request.
type recordingProvider struct {
	events  []llm.StreamEvent
	request llm.Request
}

func (p *recordingProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.request = request
	for _, ev := range p.events {
		emit(ev)
	}
	return nil
}

func commitMessageLines(t *testing.T, body string) []commitMessageEvent {
	t.Helper()
	var events []commitMessageEvent
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		var ev commitMessageEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestGitCommitMessageStreamsCleanedMessage(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)
	provider := &recordingProvider{events: []llm.StreamEvent{
		{Type: llm.EventTextDelta, Text: "Here is the commit message:\n```\nAdd second line"},
		{Type: llm.EventTextDelta, Text: " to hello\n```"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}
	server.Runner = &session.Runner{Provider: provider}
	server.Config = &config.Config{SmallModel: "openai/gpt-mini"}

	rec := doJSON(t, server, http.MethodPost, "/api/vcs/git/commit-message", map[string]any{"stagedOnly": false})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("commit-message: %d %s", rec.Code, rec.Body.String())
	}
	events := commitMessageLines(t, rec.Body.String())
	last := events[len(events)-1]
	if !last.Done || last.Error != "" || last.Text != "Add second line to hello" || last.Model != "openai/gpt-mini" {
		t.Fatalf("final event = %+v", last)
	}
	// small_model is used, and the prompt carries both changes: the
	// tracked edit and the untracked file.
	if provider.request.ProviderID != "openai" || provider.request.ModelID != "gpt-mini" {
		t.Fatalf("model = %s/%s", provider.request.ProviderID, provider.request.ModelID)
	}
	prompt := provider.request.Messages[0].Content[0].Text
	if !strings.Contains(prompt, "+line two") || !strings.Contains(prompt, "new.txt") || !strings.Contains(prompt, "- initial") {
		t.Fatalf("prompt missing diff, untracked file or recent commits:\n%s", prompt)
	}
}

func TestGitCommitMessageRequestedModelWins(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)
	provider := &recordingProvider{events: []llm.StreamEvent{{Type: llm.EventTextDelta, Text: "Update hello"}}}
	server.Runner = &session.Runner{Provider: provider}
	server.Config = &config.Config{SmallModel: "openai/gpt-mini"}

	doJSON(t, server, http.MethodPost, "/api/vcs/git/commit-message", map[string]any{"model": "anthropic/claude-haiku"})
	if provider.request.ProviderID != "anthropic" || provider.request.ModelID != "claude-haiku" {
		t.Fatalf("model = %s/%s", provider.request.ProviderID, provider.request.ModelID)
	}
}

func TestGitCommitMessageNothingStaged(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)
	server.Runner = &session.Runner{Provider: &recordingProvider{}}
	server.Config = &config.Config{SmallModel: "openai/gpt-mini"}

	rec := doJSON(t, server, http.MethodPost, "/api/vcs/git/commit-message", map[string]any{"stagedOnly": true})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "No staged changes") {
		t.Fatalf("nothing staged: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGitCommitMessageEmptyReplyIsAnError(t *testing.T) {
	server, _, _ := newTestServer(t)
	server.VCSWorkdir = vcsTestRepo(t)
	server.Runner = &session.Runner{Provider: &recordingProvider{events: []llm.StreamEvent{
		{Type: llm.EventFinish, Finish: "max_tokens"},
	}}}
	server.Config = &config.Config{SmallModel: "openai/gpt-mini"}

	rec := doJSON(t, server, http.MethodPost, "/api/vcs/git/commit-message", map[string]any{})
	last := commitMessageLines(t, rec.Body.String())[0]
	if !last.Done || !strings.Contains(last.Error, "ran out of output") {
		t.Fatalf("empty reply = %+v", last)
	}
}

func TestCleanCommitMessage(t *testing.T) {
	for in, want := range map[string]string{
		"Fix bug":                                 "Fix bug",
		"\"Fix bug\"":                             "Fix bug",
		"Commit message: Fix bug  \n\nBody  ":     "Fix bug\n\nBody",
		"```\nfeat: add x\n\nwhy\n```":            "feat: add x\n\nwhy",
		"Here's a suggested commit message:\nFix": "Fix",
	} {
		if got := cleanCommitMessage(in); got != want {
			t.Errorf("cleanCommitMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
