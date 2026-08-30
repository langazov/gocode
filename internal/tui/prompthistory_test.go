package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// --- promptHistory: ports prompt/history.tsx ------------------------------

func TestPromptHistoryMoveEmptyIsNoop(t *testing.T) {
	h := &promptHistory{}
	if _, ok := h.Move(-1, ""); ok {
		t.Fatalf("Move on empty history returned ok=true, want false")
	}
}

// mirrors: pressing "up" from an empty box recalls the most recent entry.
func TestPromptHistoryUpFromEmptyRecallsNewest(t *testing.T) {
	h := &promptHistory{entries: []promptHistoryEntry{{Input: "one"}, {Input: "two"}, {Input: "three"}}}
	got, ok := h.Move(-1, "")
	if !ok || got != "three" {
		t.Fatalf("Move(-1, \"\") = (%q, %v), want (\"three\", true)", got, ok)
	}
}

func TestPromptHistoryWalksBackThenForward(t *testing.T) {
	h := &promptHistory{entries: []promptHistoryEntry{{Input: "one"}, {Input: "two"}, {Input: "three"}}}

	got, ok := h.Move(-1, "")
	assertMove(t, got, ok, "three", true)
	got, ok = h.Move(-1, got)
	assertMove(t, got, ok, "two", true)
	got, ok = h.Move(-1, got)
	assertMove(t, got, ok, "one", true)

	// At the oldest entry, one more "up" has nowhere to go.
	if _, ok := h.Move(-1, got); ok {
		t.Fatalf("Move(-1, ..) past the oldest entry returned ok=true, want false")
	}

	// Walking back down returns to "two", then "three", then the empty draft.
	got, ok = h.Move(1, got)
	assertMove(t, got, ok, "two", true)
	got, ok = h.Move(1, got)
	assertMove(t, got, ok, "three", true)
	got, ok = h.Move(1, got)
	assertMove(t, got, ok, "", true)
}

// mirrors the TS guard: `current.input !== input && input.length` blocks
// further navigation once the box no longer matches the recalled entry.
func TestPromptHistoryEditedDraftBlocksFurtherNavigation(t *testing.T) {
	h := &promptHistory{entries: []promptHistoryEntry{{Input: "one"}, {Input: "two"}}}
	got, ok := h.Move(-1, "")
	assertMove(t, got, ok, "two", true)

	if _, ok := h.Move(-1, got+" edited"); ok {
		t.Fatalf("Move after editing the recalled text returned ok=true, want false")
	}
}

// mirrors: a non-empty draft that doesn't match the current position (i.e.
// the user typed something fresh) blocks navigation from starting at all.
func TestPromptHistoryUnrelatedDraftBlocksNavigation(t *testing.T) {
	h := &promptHistory{entries: []promptHistoryEntry{{Input: "one"}}}
	if _, ok := h.Move(-1, "something the user typed"); ok {
		t.Fatalf("Move with an unrelated non-empty draft returned ok=true, want false")
	}
}

func TestPromptHistoryAppendDedupesConsecutiveRepeat(t *testing.T) {
	h := &promptHistory{}
	h.Append("hello")
	h.Append("hello")
	if len(h.entries) != 1 {
		t.Fatalf("len(entries) = %d after a consecutive repeat, want 1", len(h.entries))
	}
}

func TestPromptHistoryAppendTrimsToMax(t *testing.T) {
	h := &promptHistory{}
	for i := 0; i < maxHistoryEntries+10; i++ {
		h.Append(string(rune('a' + i%26)))
	}
	if len(h.entries) != maxHistoryEntries {
		t.Fatalf("len(entries) = %d, want %d", len(h.entries), maxHistoryEntries)
	}
}

func TestPromptHistoryPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", promptHistoryFile)

	h := loadPromptHistory(path)
	h.Append("first")
	h.Append("second")

	reloaded := loadPromptHistory(path)
	if len(reloaded.entries) != 2 || reloaded.entries[0].Input != "first" || reloaded.entries[1].Input != "second" {
		t.Fatalf("reloaded entries = %+v, want [first second]", reloaded.entries)
	}
}

func TestLoadPromptHistoryMissingFileIsEmpty(t *testing.T) {
	h := loadPromptHistory(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if len(h.entries) != 0 {
		t.Fatalf("len(entries) = %d for a missing file, want 0", len(h.entries))
	}
}

func TestLoadPromptHistorySkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), promptHistoryFile)
	content := "{\"input\":\"ok1\"}\nnot json\n{\"input\":\"ok2\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	h := loadPromptHistory(path)
	if len(h.entries) != 2 || h.entries[0].Input != "ok1" || h.entries[1].Input != "ok2" {
		t.Fatalf("entries = %+v, want [ok1 ok2]", h.entries)
	}
}

func assertMove(t *testing.T, got string, ok bool, wantVal string, wantOK bool) {
	t.Helper()
	if got != wantVal || ok != wantOK {
		t.Fatalf("Move(..) = (%q, %v), want (%q, %v)", got, ok, wantVal, wantOK)
	}
}

// --- App wiring: up/down boundary gating ----------------------------------

func TestHistoryArrowsRecallAcrossHomeAndChat(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.history.Append("earlier prompt")

	if !app.inputAtStart() || !app.inputAtEnd() {
		t.Fatalf("a fresh empty input should be at both boundaries")
	}

	cmd, handled := app.historyKey("up")
	if !handled {
		t.Fatalf("historyKey(up) on an empty box with history = not handled, want handled")
	}
	if cmd != nil {
		t.Fatalf("historyKey(up) returned a non-nil Cmd, want nil")
	}
	if got := app.input.Value(); got != "earlier prompt" {
		t.Fatalf("input.Value() = %q, want %q", got, "earlier prompt")
	}

	// Cursor lands at the document start, so "down" recalls the empty draft.
	if !app.inputAtStart() {
		t.Fatalf("cursor should be at the start after recalling with up")
	}
	if _, handled := app.historyKey("down"); handled {
		t.Fatalf("historyKey(down) right after up (cursor not at end) = handled, want not handled (normal cursor move)")
	}
}

func TestHistoryArrowsIgnoredMidLine(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.history.Append("earlier prompt")
	app.input.SetValue("draft text")
	app.input.SetCursorColumn(3) // mid-line: neither boundary

	if app.inputAtStart() || app.inputAtEnd() {
		t.Fatalf("cursor set mid-line should be at neither boundary")
	}
	if _, handled := app.historyKey("up"); handled {
		t.Fatalf("historyKey(up) mid-line = handled, want not handled")
	}
	if _, handled := app.historyKey("down"); handled {
		t.Fatalf("historyKey(down) mid-line = handled, want not handled")
	}
	if got := app.input.Value(); got != "draft text" {
		t.Fatalf("input.Value() = %q, want unchanged %q", got, "draft text")
	}
}
