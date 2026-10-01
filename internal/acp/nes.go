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
	nesTimeout       = 20 * time.Second
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
		output, err := complete(cctx, rt.Runner.Provider, model, lowReasoning(rt.Runner, model), nesSystemPrompt, req.prompt)
		if err != nil {
			if ctx.Err() != nil {
				return obj{"suggestions": []obj{}}, nil // cancelled: superseded by newer typing
			}
			return nil, internalError("prediction failed: %v", err)
		}
		edit, ok := req.edit(output)
		if !ok {
			return obj{"suggestions": []obj{}}, nil
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
Reply with ONLY the full rewritten editable region: no markers, no cursor marker, no explanations, no code fences.
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
	rewritten := cleanRewrite(output, region)
	if rewritten == region {
		return nesEdit{}, false
	}
	// Implausible rewrites (the model echoed the prompt, or dropped most of
	// the region) are not suggestions.
	if len(rewritten) > 2*len(region)+400 || (len(region) > 80 && len(rewritten) < len(region)/3) {
		return nesEdit{}, false
	}
	type span struct {
		start, end int // byte offsets in r.text
		text       string
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
		at := r.start + h.Start
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
