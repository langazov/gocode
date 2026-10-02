// AI commit messages for the desktop client's Source Control view: describe
// the pending changes with one model call and stream the message back as it
// is written. Ported from goide (host/internal/rpc/ai_commit.go), which
// prompted a separate ACP agent process; here the server already holds a
// provider, so it calls the model directly — no tools, no session, nothing
// recorded in the event log.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/vcs/gitops"
)

const (
	// commitDiffLimit caps the patch sent to the model; the file list
	// always covers every file.
	commitDiffLimit = 60 << 10
	// commitMaxTokens leaves room for models that think before answering.
	commitMaxTokens = 4096
	commitTimeout   = 3 * time.Minute
	// commitFlushEvery throttles progress lines, so a fast stream of tiny
	// deltas is not a flood of writes.
	commitFlushEvery = 80 * time.Millisecond
)

// commitMessageEvent is one NDJSON line of the reply: the message so far,
// then a final line with done set and either the cleaned message or error.
type commitMessageEvent struct {
	Text  string `json:"text,omitempty"`
	Model string `json:"model,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

// gitCommitMessage answers POST /api/vcs/git/commit-message {stagedOnly,
// model}: the staged changes (stagedOnly) or every change a "Commit All"
// would take, described by the model — `model` ("provider/id") when given,
// else small_model, else the default model. Problems found before the model
// is called (nothing to describe, no provider) are ordinary JSON errors;
// once streaming starts (application/x-ndjson), a failure is the final
// line's `error`.
func (s *Server) gitCommitMessage(w http.ResponseWriter, r *http.Request) {
	root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		StagedOnly bool   `json:"stagedOnly"`
		Model      string `json:"model"`
	}
	if !decodeGitBody(w, r, &body) {
		return
	}
	if s.Runner == nil || s.Runner.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "no model provider configured")
		return
	}
	model, ok := s.commitModel(body.Model)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no model configured — pick one or set small_model")
		return
	}

	ctx := r.Context()
	changes, err := gitops.ChangesToCommit(ctx, root, body.StagedOnly, commitDiffLimit)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "reading changes: "+err.Error())
		return
	}
	if strings.TrimSpace(changes.Patch) == "" && strings.TrimSpace(changes.Stat) == "" {
		msg := "No changes to describe"
		if body.StagedOnly {
			msg = "No staged changes to describe"
		}
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	var recent []string
	if log, err := gitops.Log(ctx, root, gitops.LogOptions{Limit: 8}); err == nil {
		for _, c := range log {
			recent = append(recent, c.Subject)
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	modelName := model.ProviderID + "/" + model.ID
	var writeMu sync.Mutex
	send := func(ev commitMessageEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		ev.Model = modelName
		line, _ := json.Marshal(ev)
		_, _ = w.Write(append(line, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}

	cctx, cancel := context.WithTimeout(ctx, commitTimeout)
	defer cancel()
	var (
		out       strings.Builder
		sent      string
		lastFlush time.Time
		finish    string
		streamErr error
	)
	err = s.Runner.Provider.Stream(cctx, llm.Request{
		ProviderID: model.ProviderID,
		ModelID:    model.ID,
		System:     []string{commitSystemPrompt},
		Messages:   []llm.Message{llm.UserText("commit-message", commitPrompt(changes, recent))},
		MaxTokens:  commitMaxTokens,
		Reasoning:  lowReasoning(s.Runner, model),
	}, func(ev llm.StreamEvent) {
		switch ev.Type {
		case llm.EventTextDelta:
			out.WriteString(ev.Text)
			text := strings.TrimSpace(out.String())
			if text != sent && time.Since(lastFlush) >= commitFlushEvery {
				sent, lastFlush = text, time.Now()
				send(commitMessageEvent{Text: text})
			}
		case llm.EventFinish:
			finish = ev.Finish
		case llm.EventProviderError:
			streamErr = ev.Error
		}
	})
	if err == nil {
		err = streamErr
	}
	switch {
	case ctx.Err() != nil:
		return // the client went away (or stopped the draft)
	case err != nil:
		send(commitMessageEvent{Done: true, Error: err.Error()})
		return
	}
	final := cleanCommitMessage(out.String())
	switch {
	case final == "" && isLengthStop(finish):
		send(commitMessageEvent{Done: true, Error: modelName + " ran out of output before finishing the message"})
	case final == "":
		send(commitMessageEvent{Done: true, Error: modelName + " returned an empty message"})
	default:
		send(commitMessageEvent{Done: true, Text: final})
	}
}

// commitModel resolves the model a commit message is written with: the
// request's choice, else small_model, else the session default.
func (s *Server) commitModel(requested string) (session.ModelRef, bool) {
	if providerID, modelID, ok := config.ParseModelRef(requested); ok {
		return session.ModelRef{ProviderID: providerID, ID: modelID}, true
	}
	if s.Config != nil {
		if providerID, modelID, ok := config.ParseModelRef(s.Config.SmallModel); ok {
			return session.ModelRef{ProviderID: providerID, ID: modelID}, true
		}
	}
	if s.Session != nil && s.Session.DefaultModel.ID != "" {
		return s.Session.DefaultModel, true
	}
	return session.ModelRef{}, false
}

// lowReasoning picks the cheapest reasoning variant the model offers:
// describing a diff needs no deliberation. Mirrors internal/acp/nes.go.
func lowReasoning(runner *session.Runner, model session.ModelRef) map[string]any {
	if runner == nil || runner.ReasoningVariants == nil {
		return nil
	}
	for _, variant := range []string{"none", "minimal", "low"} {
		if opts := runner.ReasoningVariants(model.ProviderID, model.ID, variant); len(opts) > 0 {
			return opts
		}
	}
	return nil
}

func isLengthStop(finish string) bool {
	switch strings.ToLower(finish) {
	case "length", "max_tokens", "max-tokens":
		return true
	}
	return false
}

const commitSystemPrompt = "You write git commit messages. Reply with the commit message only."

func commitPrompt(c *gitops.PendingChanges, recent []string) string {
	var b strings.Builder
	b.WriteString(`Write a git commit message for the changes below.

Rules:
- First line: a concise summary in the imperative mood, at most 72 characters, no trailing period.
- If the change needs explaining, add a blank line, then a short body wrapped at 72 columns saying what changed and why. Omit the body for small, obvious changes.
- Describe only what the diff shows; don't invent motivation or mention files you can't see.
- Match the style of the recent commits (for example a "feat:"/"fix:" prefix) if they use one.
- Reply with the commit message only: no preamble, no quotes, no code fences.
`)
	if len(recent) > 0 {
		b.WriteString("\nRecent commits:\n")
		for _, s := range recent {
			b.WriteString("- " + s + "\n")
		}
	}
	fmt.Fprintf(&b, "\nFiles changed (%d):\n%s\n", c.Files, c.Stat)
	b.WriteString("\nDiff:\n")
	b.WriteString(c.Patch)
	if c.Truncated {
		b.WriteString("\n[diff truncated; the file list above is complete]\n")
	}
	return b.String()
}

var (
	fenceRe    = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n(.*?)\\n?```$")
	preambleRe = regexp.MustCompile(`(?i)^(here(?:'s| is) (?:a |the |your )?(?:suggested |proposed )?commit message[^:\n]*:|commit message:)\s*`)
)

// cleanCommitMessage strips what models add around the message anyway: a
// preamble line, code fences, wrapping quotes, trailing whitespace.
func cleanCommitMessage(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	s = strings.TrimSpace(preambleRe.ReplaceAllString(s, ""))
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '`' && s[len(s)-1] == '`') {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}
