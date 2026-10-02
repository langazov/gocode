package acp

// Next edit suggestions and inline completion.
//
// Neither stable ACP version has editor predictions. The Next Edit
// Suggestions RFD (rfds/next-edit-suggestions) drafted them; it is not part
// of v1 or v2, so — like forking — this agent offers the draft under
// extension names (protocol/v1/extensibility): every method and
// notification is the RFD's, prefixed with `_gocode/`, and the capability is
// advertised in `_meta.gocode.nes`. Payloads follow the RFD.
//
//	_gocode/nes/start        {workspaceUri, workspaceFolders?}      -> {sessionId}
//	_gocode/nes/close        {sessionId}                            -> {}
//	_gocode/document/didOpen|didChange|didClose|didSave|didFocus    (notifications)
//	_gocode/nes/suggest      {sessionId, uri, version, position, selection?, triggerKind, context?}
//	                         -> {suggestions: [{id, kind: "edit", uri, edits, cursorPosition}]}
//	_gocode/nes/accept|reject {sessionId, id, reason?}               (notifications)
//
// Positions are UTF-16 (the RFD's mandatory default encoding).
//
// One model call serves both features: the model rewrites a small editable
// region around the cursor (the approach of Zed's edit prediction), and the
// rewrite is reduced to one minimal edit. An edit that only inserts text at
// the cursor is an inline completion; anything else — a change before or
// after the cursor, a replacement — is a next edit. The client renders the
// two differently; the protocol does not need to tell them apart.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/langazov/gocode-go/internal/diff"
	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/session"
)

const (
	nesPrefix = "_gocode/"
	// Lines of the editable region before and after the cursor line.
	nesRegionBefore = 8
	nesRegionAfter  = 8
	// Read-only context around the region, in lines.
	nesContextBefore = 120
	nesContextAfter  = 40
	nesHistoryMax    = 6
	// Lines around the cursor a reply that is not the exact region may be
	// placed in.
	nesAlignWindow = 40
	nesTimeout     = 20 * time.Second
	// Room for hidden reasoning on models that think even when asked not
	// to; the rewrite itself is a few hundred tokens.
	nesMaxTokens = 4096

	cursorMarker = "<|user_cursor|>"
	regionStart  = "<|editable_region_start|>"
	regionEnd    = "<|editable_region_end|>"
)

// nesCapability is advertised under _meta.gocode.nes.
func nesCapability() obj {
	return obj{
		"positionEncoding": "utf-16",
		"events": obj{"document": obj{
			"didOpen":   obj{},
			"didChange": obj{"syncKind": "incremental"},
			"didClose":  obj{},
			"didSave":   obj{},
			"didFocus":  obj{},
		}},
		"context": obj{
			"editHistory": obj{"maxCount": nesHistoryMax},
			"diagnostics": obj{},
		},
	}
}

// nesState is the agent's NES sessions.
type nesState struct {
	mu       sync.Mutex
	next     int
	sessions map[string]*nesSession
}

type nesSession struct {
	id      string
	runtime *Runtime

	mu   sync.Mutex
	docs map[string]*nesDoc
	// history is the recent edits as unified hunks, oldest first.
	history []string
	// suggested is the last suggestion per id, for accept bookkeeping.
	suggested map[string]string
	accepted  int
	rejected  int
}

type nesDoc struct {
	text     string
	version  int
	language string
	// base is the text the last history checkpoint saw.
	base string
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type textRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

func (a *Agent) registerNES() {
	a.handle(nesPrefix+"nes/start", anyVersion, a.nesStart)
	a.handle(nesPrefix+"nes/close", anyVersion, a.nesClose)
	a.handle(nesPrefix+"nes/suggest", anyVersion, a.nesSuggest)
	notify := func(method string, fn func(*nesSession, json.RawMessage)) {
		a.conn.OnNotify(nesPrefix+method, func(params json.RawMessage) {
			if !a.ready() {
				return
			}
			var in struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(params, &in) != nil {
				return
			}
			if s := a.nesSession(in.SessionID); s != nil {
				fn(s, params)
			}
		})
	}
	notify("document/didOpen", (*nesSession).didOpen)
	notify("document/didChange", (*nesSession).didChange)
	notify("document/didClose", (*nesSession).didClose)
	notify("document/didSave", func(*nesSession, json.RawMessage) {})
	notify("document/didFocus", func(*nesSession, json.RawMessage) {})
	notify("nes/accept", func(s *nesSession, params json.RawMessage) { s.feedback(params, true) })
	notify("nes/reject", func(s *nesSession, params json.RawMessage) { s.feedback(params, false) })
}

func (a *Agent) nesSession(id string) *nesSession {
	a.nes.mu.Lock()
	defer a.nes.mu.Unlock()
	return a.nes.sessions[id]
}

func (a *Agent) nesStart(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		WorkspaceURI     string `json:"workspaceUri"`
		WorkspaceFolders []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	uri := in.WorkspaceURI
	if uri == "" && len(in.WorkspaceFolders) > 0 {
		uri = in.WorkspaceFolders[0].URI
	}
	dir := ""
	if uri != "" {
		p, err := uriPath(uri)
		if err != nil {
			return nil, invalidParams("workspaceUri: %v", err)
		}
		dir = p
	}
	rt, err := a.runtime(ctx, dir)
	if err != nil {
		return nil, err
	}
	if rt.Runner == nil || rt.Runner.Provider == nil {
		return nil, internalError("no model provider for predictions")
	}
	a.nes.mu.Lock()
	defer a.nes.mu.Unlock()
	a.nes.next++
	id := fmt.Sprintf("nes_%d", a.nes.next)
	if a.nes.sessions == nil {
		a.nes.sessions = map[string]*nesSession{}
	}
	a.nes.sessions[id] = &nesSession{id: id, runtime: rt, docs: map[string]*nesDoc{}, suggested: map[string]string{}}
	return obj{"sessionId": id}, nil
}

func (a *Agent) nesClose(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	a.nes.mu.Lock()
	defer a.nes.mu.Unlock()
	if a.nes.sessions[in.SessionID] == nil {
		return nil, invalidParams("unknown NES session %q", in.SessionID)
	}
	delete(a.nes.sessions, in.SessionID)
	return obj{}, nil
}

// --- document sync ------------------------------------------------------------------

func (s *nesSession) didOpen(params json.RawMessage) {
	var in struct {
		URI        string `json:"uri"`
		LanguageID string `json:"languageId"`
		Version    int    `json:"version"`
		Text       string `json:"text"`
	}
	if json.Unmarshal(params, &in) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[in.URI] = &nesDoc{text: in.Text, base: in.Text, version: in.Version, language: in.LanguageID}
}

func (s *nesSession) didChange(params json.RawMessage) {
	var in struct {
		URI            string `json:"uri"`
		Version        int    `json:"version"`
		ContentChanges []struct {
			Range *textRange `json:"range"`
			Text  string     `json:"text"`
		} `json:"contentChanges"`
	}
	if json.Unmarshal(params, &in) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[in.URI]
	if doc == nil {
		return
	}
	for _, change := range in.ContentChanges {
		if change.Range == nil {
			doc.text = change.Text // full sync
			continue
		}
		start := offsetOf(doc.text, change.Range.Start)
		end := offsetOf(doc.text, change.Range.End)
		if end < start {
			start, end = end, start
		}
		doc.text = doc.text[:start] + change.Text + doc.text[end:]
	}
	doc.version = in.Version
}

func (s *nesSession) didClose(params json.RawMessage) {
	var in struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(params, &in) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc := s.docs[in.URI]; doc != nil {
		s.checkpoint(in.URI, doc)
	}
	delete(s.docs, in.URI)
}

// checkpoint moves the edits since the last checkpoint into the history.
// Callers hold s.mu.
func (s *nesSession) checkpoint(uri string, doc *nesDoc) {
	if doc.base == doc.text {
		return
	}
	name := uri
	if p, err := uriPath(uri); err == nil {
		name = filepath.Base(p)
	}
	if hunks := diff.Trim(diff.Unified(name, name, doc.base, doc.text)); strings.TrimSpace(hunks) != "" {
		s.history = append(s.history, hunks)
		if len(s.history) > nesHistoryMax {
			s.history = s.history[len(s.history)-nesHistoryMax:]
		}
	}
	doc.base = doc.text
}

func (s *nesSession) feedback(params json.RawMessage, accepted bool) {
	var in struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(params, &in) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.suggested[in.ID]; !ok {
		return
	}
	delete(s.suggested, in.ID)
	if accepted {
		s.accepted++
	} else {
		s.rejected++
	}
}

// --- suggestions ----------------------------------------------------------------------

type nesDiagnostic struct {
	Range    textRange `json:"range"`
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
	URI      string    `json:"uri"`
}

type nesSuggestParams struct {
	SessionID   string   `json:"sessionId"`
	URI         string   `json:"uri"`
	Version     int      `json:"version"`
	Position    position `json:"position"`
	TriggerKind string   `json:"triggerKind"`
	Context     struct {
		EditHistory []struct {
			URI  string `json:"uri"`
			Diff string `json:"diff"`
		} `json:"editHistory"`
		Diagnostics []nesDiagnostic `json:"diagnostics"`
	} `json:"context"`
}

func (a *Agent) nesSuggest(ctx context.Context, params json.RawMessage) (any, error) {
	var in nesSuggestParams
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	s := a.nesSession(in.SessionID)
	if s == nil {
		return nil, invalidParams("unknown NES session %q", in.SessionID)
	}
	s.mu.Lock()
	doc := s.docs[in.URI]
	if doc == nil {
		s.mu.Unlock()
		return nil, invalidParams("document %s is not open", in.URI)
	}
	s.checkpoint(in.URI, doc)
	text, language := doc.text, doc.language
	history := append([]string(nil), s.history...)
	s.mu.Unlock()
	for _, h := range in.Context.EditHistory {
		history = append(history, h.Diff)
	}
	if len(history) > nesHistoryMax {
		history = history[len(history)-nesHistoryMax:]
	}

	req := buildNESRequest(in.URI, language, text, in.Position, history, in.Context.Diagnostics)
	rt := s.runtime
	model := rt.CompletionModel
	if model.ID == "" {
		model = rt.Sessions.DefaultModel
	}
	// The model call is slow; free the ordered queue so document changes
	// keep flowing while it runs (and $/cancel_request can reach it).
	return jsonrpc.Deferred(func() (any, error) {
		cctx, cancel := context.WithTimeout(ctx, nesTimeout)
		defer cancel()
		none := obj{"suggestions": []obj{}}
		var edit nesEdit
		ok := false
		// Fill-in-the-middle models complete at the cursor directly — fast,
		// and nothing to reconstruct. With nothing to insert there, the
		// rewrite path below still looks for a next edit.
		if rt.FIM != nil {
			prefix := text[max(0, req.cursor-fimPrefixMax):req.cursor]
			suffix := text[req.cursor:min(len(text), req.cursor+fimSuffixMax)]
			out, supported, err := rt.FIM(cctx, model, prefix, suffix, fimMaxTokens)
			if supported {
				if err != nil {
					if ctx.Err() != nil {
						return none, nil // cancelled: superseded by newer typing
					}
					return nil, internalError("prediction failed: %v", err)
				}
				edit, ok = req.fimEdit(out)
			}
		}
		if !ok {
			output, err := complete(cctx, rt.Runner.Provider, model, lowReasoning(rt.Runner, model), nesSystemPrompt, req.prompt)
			if err != nil {
				if ctx.Err() != nil {
					return none, nil // cancelled: superseded by newer typing
				}
				return nil, internalError("prediction failed: %v", err)
			}
			if edit, ok = req.edit(output); !ok {
				return none, nil
			}
		}
		id, err := newMessageID()
		if err != nil {
			id = fmt.Sprintf("sugg_%d", time.Now().UnixNano())
		}
		s.mu.Lock()
		s.suggested[id] = in.URI
		s.mu.Unlock()
		return obj{"suggestions": []obj{{
			"id":             id,
			"kind":           "edit",
			"uri":            in.URI,
			"edits":          edit.Edits,
			"cursorPosition": edit.Cursor,
		}}}, nil
	}), nil
}

const nesSystemPrompt = `You are a code completion and next-edit prediction engine inside an editor.
You see a file with an editable region marked by ` + regionStart + ` and ` + regionEnd + `, and the cursor marked by ` + cursorMarker + `.
Predict what the user will type or change next, judging from the code and their recent edits:
complete the code at the cursor, and/or make the follow-up edit the recent changes imply (e.g. update the other uses of a renamed identifier, add the missing field, fix the obvious error).
Reply with ONLY the full rewritten editable region, from its first line to its last line: no markers, no cursor marker, no explanations, no code fences.
Keep everything outside your change byte-for-byte identical. If no change is needed, return the region unchanged.`

// nesRequest is one prepared prediction: the prompt and how to map the
// model's rewrite of the region back to an edit.
type nesRequest struct {
	prompt string
	text   string
	// region is the editable region [start, end) as byte offsets in text.
	start, end int
	cursor     int
}

// nesEdit is one prediction: edits in original-document coordinates
// (non-overlapping, in order) and the caret after applying them.
type nesEdit struct {
	Edits  []nesTextEdit
	Cursor position
}

type nesTextEdit struct {
	Range   textRange `json:"range"`
	NewText string    `json:"newText"`
}

func buildNESRequest(uri, language, text string, pos position, history []string, diags []nesDiagnostic) nesRequest {
	lines := lineStarts(text)
	cursor := offsetOf(text, pos)
	line := min(pos.Line, len(lines)-1)
	firstLine := max(0, line-nesRegionBefore)
	lastLine := min(len(lines)-1, line+nesRegionAfter)
	start := lines[firstLine]
	end := len(text)
	if lastLine+1 < len(lines) {
		end = lines[lastLine+1]
	}
	ctxStart := lines[max(0, firstLine-nesContextBefore)]
	ctxEnd := len(text)
	if lastLine+1+nesContextAfter < len(lines) {
		ctxEnd = lines[lastLine+1+nesContextAfter]
	}

	var b strings.Builder
	name := uri
	if p, err := uriPath(uri); err == nil {
		name = filepath.Base(p)
	}
	if len(history) > 0 {
		b.WriteString("Recent edits by the user (oldest first):\n")
		for _, h := range history {
			b.WriteString(strings.TrimRight(h, "\n"))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(diags) > 0 {
		b.WriteString("Diagnostics near the cursor:\n")
		for _, d := range diags {
			fmt.Fprintf(&b, "- line %d: %s: %s\n", d.Range.Start.Line+1, firstNonEmpty(d.Severity, "error"), d.Message)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "File %s", name)
	if language != "" {
		fmt.Fprintf(&b, " (%s)", language)
	}
	b.WriteString(":\n")
	b.WriteString(text[ctxStart:start])
	b.WriteString(regionStart + "\n")
	b.WriteString(text[start:cursor])
	b.WriteString(cursorMarker)
	b.WriteString(text[cursor:end])
	if !strings.HasSuffix(text[start:end], "\n") {
		b.WriteString("\n")
	}
	b.WriteString(regionEnd + "\n")
	b.WriteString(text[end:ctxEnd])
	return nesRequest{prompt: b.String(), text: text, start: start, end: end, cursor: cursor}
}

const (
	// Fill-in-the-middle context around the cursor, in bytes.
	fimPrefixMax = 8000
	fimSuffixMax = 3000
	fimMaxTokens = 256
)

// fimEdit turns a fill-in-the-middle completion into an insertion at the
// cursor: cut at the first blank line (one statement or block, not the
// rest of the file), without the part that repeats the text after the
// cursor, and subject to the same plausibility checks as rewrites.
func (r nesRequest) fimEdit(out string) (nesEdit, bool) {
	if i := strings.Index(out, "\n\n"); i >= 0 {
		out = out[:i]
	}
	out = strings.TrimRight(out, " \t\n")
	if lines := strings.SplitAfter(out, "\n"); len(lines) > 15 {
		out = strings.TrimRight(strings.Join(lines[:15], ""), "\n")
	}
	// Models often close what is already closed ("foo(" + "x)" before an
	// existing ")"): drop the longest end of the completion that the text
	// after the cursor starts with.
	suffix := r.text[r.cursor:]
	if nl := strings.IndexByte(suffix, '\n'); nl >= 0 {
		suffix = suffix[:nl]
	}
	for k := min(len(out), len(suffix)); k > 0; k-- {
		if strings.HasSuffix(out, suffix[:k]) {
			out = out[:len(out)-k]
			break
		}
	}
	if strings.TrimSpace(out) == "" {
		return nesEdit{}, false
	}
	// In the middle of a line, a completion stays on it: a multi-line
	// insertion before existing code (a new function body pushed in front
	// of a renamed function's parameters) breaks the line.
	if strings.TrimSpace(suffix) != "" && strings.Contains(out, "\n") {
		nesReject("fim: multi-line insertion in the middle of a line")
		return nesEdit{}, false
	}
	if reason := implausibleEdits(r.text, []span{{r.cursor, r.cursor, out}}); reason != "" {
		nesReject("fim: " + reason)
		return nesEdit{}, false
	}
	pos := positionOf(r.text, r.cursor)
	edited := r.text[:r.cursor] + out + r.text[r.cursor:]
	return nesEdit{
		Edits:  []nesTextEdit{{Range: textRange{Start: pos, End: pos}, NewText: out}},
		Cursor: positionOf(edited, r.cursor+len(out)),
	}, true
}

// lowReasoning is the model's cheapest reasoning variant (none, minimal or
// low, from the catalog), so predictions answer fast; nil when the model
// has none or the runner can't resolve variants.
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

// edit reduces the model's rewrite of the region to minimal edits: one
// per changed hunk of lines, each trimmed to the characters that differ
// (a rename at two call sites is two small edits, not one span covering
// both).
func (r nesRequest) edit(output string) (nesEdit, bool) {
	if strings.TrimSpace(output) == "" {
		// Nothing came back (a length stop spent on hidden reasoning, or an
		// empty reply): no prediction — never "delete the region".
		return nesEdit{}, false
	}
	region := r.text[r.start:r.end]
	base := r.start
	rewritten := cleanRewrite(output, region)
	if rewritten == region {
		return nesEdit{}, false
	}
	// Implausible rewrites (the model echoed the prompt, leaked a marker,
	// returned a fragment that can't be placed) are not suggestions. A
	// wrong prediction costs a keystroke; a destructive one costs the
	// user's code.
	if reason := r.implausibleRewrite(region, rewritten); reason != "" {
		// Models often return just the lines around the change. Place such a
		// fragment by its first and last lines and diff only that slice;
		// lines it doesn't cover are never touched.
		a, b, frag, ok := r.alignFragment(output)
		if !ok || strings.HasPrefix(reason, "marker") {
			nesReject(fmt.Sprintf("%s (reply %q)", reason, truncate(output, 600)))
			return nesEdit{}, false
		}
		region, rewritten, base = r.text[a:b], frag, a
		if rewritten == region {
			return nesEdit{}, false
		}
	}
	var spans []span
	add := func(at int, old, repl string) {
		prefix := commonPrefix(old, repl)
		suffix := commonSuffix(old[prefix:], repl[prefix:])
		if sp := (span{at + prefix, at + len(old) - suffix, repl[prefix : len(repl)-suffix]}); sp.start != sp.end || sp.text != "" {
			spans = append(spans, sp)
		}
	}
	for _, h := range udiff.Lines(region, rewritten) {
		old := region[h.Start:h.End]
		at := base + h.Start
		oldLines, newLines := strings.SplitAfter(old, "\n"), strings.SplitAfter(h.New, "\n")
		if len(oldLines) != len(newLines) {
			add(at, old, h.New) // lines added or removed: one edit
			continue
		}
		// Line-for-line changes: one small edit per line.
		for k := range oldLines {
			add(at, oldLines[k], newLines[k])
			at += len(oldLines[k])
		}
	}
	if len(spans) == 0 {
		return nesEdit{}, false
	}
	if reason := implausibleEdits(r.text, spans); reason != "" {
		nesReject(reason)
		return nesEdit{}, false
	}
	// Prefer anchoring a lone insertion at the cursor, so typing-ahead
	// reads as a completion even when the inserted text repeats what
	// follows ("foo(" + "x)" vs "foo(x" + ")").
	if len(spans) == 1 && spans[0].start == spans[0].end && spans[0].start != r.cursor {
		if shifted, ok := anchorAt(r.text, spans[0].start, spans[0].text, r.cursor); ok {
			spans[0] = span{r.cursor, r.cursor, shifted}
		}
	}
	// The caret lands after the first edit at or after the cursor (else the
	// last one), in the edited text.
	target := len(spans) - 1
	for k, sp := range spans {
		if sp.end >= r.cursor {
			target = k
			break
		}
	}
	var out nesEdit
	var edited strings.Builder
	prev, caret := 0, 0
	for k, sp := range spans {
		out.Edits = append(out.Edits, nesTextEdit{
			Range:   textRange{Start: positionOf(r.text, sp.start), End: positionOf(r.text, sp.end)},
			NewText: sp.text,
		})
		edited.WriteString(r.text[prev:sp.start])
		edited.WriteString(sp.text)
		if k == target {
			caret = edited.Len()
		}
		prev = sp.end
	}
	edited.WriteString(r.text[prev:])
	out.Cursor = positionOf(edited.String(), caret)
	return out, true
}

// span is one minimal replacement, as byte offsets in the document.
type span struct {
	start, end int
	text       string
}

// nesReject receives why a prediction was dropped (tests observe it).
var nesReject = func(string) {}

// implausibleRewrite checks the model returned the whole region, intact
// around the change. It returns why not, or "".
func (r nesRequest) implausibleRewrite(region, rewritten string) string {
	if len(rewritten) > 2*len(region)+400 {
		return "rewrite much longer than the region"
	}
	for _, m := range []string{"editable_region", "user_cursor", "<|", "|>"} {
		if strings.Contains(rewritten, m) && !strings.Contains(region, m) {
			return "marker leaked into the rewrite"
		}
	}
	// The region's first and last non-blank lines anchor it: unless the
	// cursor is on them they must come back unchanged, or the model
	// returned a fragment (whose missing lines would become deletions).
	oldLines := nonBlankLines(region)
	newLines := nonBlankLines(rewritten)
	if len(oldLines) == 0 || len(newLines) == 0 {
		return "empty region or rewrite"
	}
	cursorLine := strings.TrimRight(lineAt(r.text, r.cursor), " \t")
	if first := oldLines[0]; first != cursorLine && newLines[0] != first {
		return "rewrite lost the region's first line"
	}
	if last := oldLines[len(oldLines)-1]; last != cursorLine && newLines[len(newLines)-1] != last {
		return "rewrite lost the region's last line"
	}
	return ""
}

// alignFragment places a reply that is not the exact region — a fragment
// of it, or a chunk that starts or ends in the surrounding context. Its
// first and last non-blank lines must match document lines near the
// cursor (ignoring indentation; the occurrence nearest the cursor), or be
// the cursor line itself, edited around the cursor. It returns the
// document slice [a, b) the reply replaces and the reply shaped like that
// slice. Lines outside the slice are never touched.
func (r nesRequest) alignFragment(output string) (a, b int, frag string, ok bool) {
	// A long reply may start or end beyond what can be placed (it ran on
	// into the context, or stopped mid-line at the token limit): drop a few
	// lines from either end until it aligns. Dropped lines only mean the
	// document lines there stay untouched.
	all := strings.Split(stripReply(output), "\n")
	for head := 0; head <= 10 && head < len(all); head++ {
		for tail := 0; tail <= 20 && head+tail < len(all); tail++ {
			if a, b, frag, ok = r.alignLines(strings.Join(all[head:len(all)-tail], "\n")); ok {
				return a, b, frag, true
			}
		}
	}
	return 0, 0, "", false
}

// alignLines places one candidate reply (see alignFragment).
func (r nesRequest) alignLines(out string) (a, b int, frag string, ok bool) {
	outLines := strings.Split(out, "\n")
	nonBlank := nonBlankLines(out)
	if len(nonBlank) == 0 {
		return 0, 0, "", false
	}
	// Document lines around the cursor, with byte offsets.
	type dl struct {
		text       string // without the newline, right-trimmed
		start, end int    // end includes the newline
	}
	starts := lineStarts(r.text)
	cursorLine := positionOf(r.text, r.cursor).Line
	lo := max(0, cursorLine-nesAlignWindow)
	hi := min(len(starts)-1, cursorLine+nesAlignWindow)
	var lines []dl
	for i := lo; i <= hi; i++ {
		end := len(r.text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		lines = append(lines, dl{strings.TrimRight(strings.TrimSuffix(r.text[starts[i]:end], "\n"), " \t"), starts[i], end})
	}
	cursorIdx := cursorLine - lo
	before := r.text[lines[cursorIdx].start:r.cursor]
	after := strings.TrimRight(strings.TrimSuffix(r.text[r.cursor:lines[cursorIdx].end], "\n"), " \t")
	same := func(x, y string) bool { return strings.TrimSpace(x) == strings.TrimSpace(y) }
	nearest := func(want string, from int) int {
		best := -1
		for i := from; i < len(lines); i++ {
			if strings.TrimSpace(lines[i].text) != "" && same(lines[i].text, want) &&
				(best < 0 || abs(i-cursorIdx) < abs(best-cursorIdx)) {
				best = i
			}
		}
		return best
	}
	first, last := nonBlank[0], nonBlank[len(nonBlank)-1]
	sIdx := nearest(first, 0)
	if sIdx < 0 && strings.TrimSpace(before) != "" && strings.HasPrefix(strings.TrimSpace(first), strings.TrimSpace(before)) {
		sIdx = cursorIdx // starts on the edited cursor line
	}
	if sIdx < 0 {
		return 0, 0, "", false
	}
	eIdx := nearest(last, sIdx)
	if eIdx < 0 && cursorIdx >= sIdx && strings.HasSuffix(last, after) &&
		(len(nonBlank) > 1 || strings.HasPrefix(strings.TrimSpace(last), strings.TrimSpace(before))) {
		eIdx = cursorIdx // ends on the edited cursor line
	}
	if eIdx < 0 || eIdx < sIdx {
		return 0, 0, "", false
	}
	// The reply must be about as long as the slice it replaces.
	if span := eIdx - sIdx + 1; len(nonBlank) > span+15 || span > len(nonBlank)+3 {
		return 0, 0, "", false
	}
	// Models often get the first line's indentation wrong: keep the
	// document's when the line is otherwise the same.
	for k, l := range outLines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		docLine := r.text[lines[sIdx].start:lines[sIdx].end]
		editedCursorLine := sIdx == cursorIdx && strings.HasPrefix(strings.TrimSpace(l), strings.TrimSpace(before))
		if same(l, docLine) || editedCursorLine {
			indent := docLine[:len(docLine)-len(strings.TrimLeft(docLine, " \t"))]
			outLines[k] = indent + strings.TrimLeft(l, " \t")
		}
		break
	}
	a, b = lines[sIdx].start, lines[eIdx].end
	frag = strings.Trim(strings.Join(outLines, "\n"), "\n")
	if strings.HasSuffix(r.text[a:b], "\n") {
		frag += "\n"
	}
	return a, b, frag, true
}

// stripReply removes fences, markers and surrounding blank lines from a
// reply, keeping indentation.
func stripReply(output string) string {
	out := strings.TrimSpace(output)
	if strings.HasPrefix(out, "```") {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		out = strings.TrimSuffix(strings.TrimRight(out, " \t\n"), "```")
	}
	for _, m := range []string{regionStart, regionEnd, cursorMarker} {
		out = strings.ReplaceAll(out, m, "")
	}
	// TrimSpace above ate the first line's indentation; take it back from
	// the raw reply.
	raw := strings.TrimLeft(output, "\n")
	if lead := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]; lead != "" && !strings.HasPrefix(out, lead) && !strings.HasPrefix(out, "```") {
		out = lead + out
	}
	return strings.Trim(out, "\n")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// implausibleEdits bounds what one prediction may do: a few small,
// local edits, never a block deletion or a pasted copy of nearby code.
func implausibleEdits(text string, spans []span) string {
	if len(spans) > 3 {
		return "too many edits"
	}
	deleted, inserted := 0, 0
	for _, sp := range spans {
		oldNL := strings.Count(text[sp.start:sp.end], "\n")
		newNL := strings.Count(sp.text, "\n")
		if oldNL > 3 {
			return "replaces too many lines"
		}
		if oldNL > newNL {
			deleted += oldNL - newNL
		} else {
			inserted += newNL - oldNL
		}
		// A multi-line insertion that repeats code already around it is the
		// model duplicating the region, not a completion: reject when half
		// or more of its substantial lines already exist nearby.
		if newNL >= 2 {
			near := map[string]bool{}
			lo, hi := max(0, sp.start-3000), min(len(text), sp.end+3000)
			for _, l := range strings.Split(text[lo:sp.start]+text[sp.end:hi], "\n") {
				near[strings.TrimSpace(l)] = true
			}
			substantial, repeated := 0, 0
			for _, l := range nonBlankLines(sp.text) {
				if t := strings.TrimSpace(l); len(t) >= 6 {
					substantial++
					if near[t] {
						repeated++
					}
				}
			}
			if substantial >= 2 && repeated*2 >= substantial {
				return "insertion duplicates nearby code"
			}
		}
	}
	if deleted > 2 {
		return "deletes too many lines"
	}
	if inserted > 15 {
		return "inserts too many lines"
	}
	return ""
}

func nonBlankLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " \t"))
		}
	}
	return out
}

// lineAt returns the text of the line containing byte offset off.
func lineAt(text string, off int) string {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : off+end]
}

// anchorAt moves a pure insertion of ins at off to the cursor when the text
// between them lets it slide (the same insertion, rotated).
func anchorAt(text string, off int, ins string, cursor int) (string, bool) {
	if off < cursor {
		between := text[off:cursor]
		if strings.HasPrefix(ins, between) {
			return ins[len(between):] + between, true
		}
	} else {
		between := text[cursor:off]
		if strings.HasSuffix(ins, between) {
			return between + ins[:len(ins)-len(between)], true
		}
	}
	return "", false
}

// cleanRewrite strips what models add around the region despite the
// instructions: code fences, the markers, a dropped trailing newline.
func cleanRewrite(output, region string) string {
	out := strings.TrimSpace(output)
	if strings.HasPrefix(out, "```") {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		out = strings.TrimSuffix(strings.TrimRight(out, " \t\n"), "```")
	}
	for _, m := range []string{regionStart, regionEnd, cursorMarker} {
		out = strings.ReplaceAll(out, m, "")
	}
	out = strings.Trim(out, "\n")
	// Restore the region's own leading/trailing blank structure, which the
	// trimming above removed.
	lead := region[:len(region)-len(strings.TrimLeft(region, "\n"))]
	trail := region[len(strings.TrimRight(region, "\n")):]
	// Leading indentation of the first line matters; the model keeps it, and
	// TrimSpace removed it, so take it back from the region when the lines
	// start alike.
	firstRegion := strings.TrimLeft(region, "\n")
	indent := firstRegion[:len(firstRegion)-len(strings.TrimLeft(firstRegion, " \t"))]
	if indent != "" && !strings.HasPrefix(out, indent) {
		out = indent + out
	}
	return lead + out + trail
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	for n > 0 && n < len(a) && !utf8.RuneStart(a[n]) { // whole runes only
		n--
	}
	return n
}

func commonSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	for n > 0 && n < len(a) && !utf8.RuneStart(a[len(a)-n]) {
		n--
	}
	return n
}

// complete runs one non-streaming-for-the-caller model call and returns
// the text it produced.
func complete(ctx context.Context, provider llm.StreamClient, model session.ModelRef, reasoning map[string]any, system, prompt string) (string, error) {
	zero := 0.0
	var out strings.Builder
	var streamErr error
	err := provider.Stream(ctx, llm.Request{
		ProviderID:  model.ProviderID,
		ModelID:     model.ID,
		System:      []string{system},
		Messages:    []llm.Message{llm.UserText("nes", prompt)},
		MaxTokens:   nesMaxTokens,
		Temperature: &zero,
		Reasoning:   reasoning,
	}, func(ev llm.StreamEvent) {
		switch ev.Type {
		case llm.EventTextDelta:
			out.WriteString(ev.Text)
		case llm.EventProviderError:
			streamErr = ev.Error
		}
	})
	if err == nil {
		err = streamErr
	}
	return out.String(), err
}

// --- positions (UTF-16) ------------------------------------------------------------------

func lineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// offsetOf converts a UTF-16 position to a byte offset, clamping to the
// line and the text.
func offsetOf(text string, p position) int {
	starts := lineStarts(text)
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(starts) {
		return len(text)
	}
	off := starts[p.Line]
	units := 0
	for off < len(text) && text[off] != '\n' && units < p.Character {
		r, size := utf8.DecodeRuneInString(text[off:])
		units += len(utf16.Encode([]rune{r}))
		off += size
	}
	return off
}

// positionOf converts a byte offset to a UTF-16 position.
func positionOf(text string, off int) position {
	off = min(max(off, 0), len(text))
	line, lineStart := 0, 0
	for i := 0; i < off; i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	units := 0
	for _, r := range text[lineStart:off] {
		units += len(utf16.Encode([]rune{r}))
	}
	return position{Line: line, Character: units}
}

// uriPath converts a file:// URI to a path.
func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %s", uri)
	}
	return filepath.FromSlash(u.Path), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
