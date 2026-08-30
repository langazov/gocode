package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// This file ports packages/tui/src/prompt/history.tsx: a JSONL-persisted,
// global (not per-project) log of submitted prompts, walked with the same
// Array.prototype.at()-style negative indexing (0 = the live draft, more
// negative = further back) and the same boundary/dedup rules. The TS version
// also stores file/agent parts and shell-mode per entry; the Go prompt has
// neither concept yet, so entries here are plain text.

const maxHistoryEntries = 50

const promptHistoryFile = "prompt-history.jsonl"

type promptHistoryEntry struct {
	Input string `json:"input"`
}

// promptHistory is the Bubble Tea analogue of usePromptHistory's store: no
// reactive signal, just a plain index into entries that callers mutate via
// Move/Append.
type promptHistory struct {
	path    string
	entries []promptHistoryEntry
	index   int
}

// loadPromptHistory reads path (best-effort: a missing or corrupt file
// yields an empty history, matching history.tsx's `.catch(() => "")` and
// per-line JSON.parse skip-on-failure), keeping only the newest
// maxHistoryEntries lines.
func loadPromptHistory(path string) *promptHistory {
	h := &promptHistory{path: path}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry promptHistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		h.entries = append(h.entries, entry)
	}
	if len(h.entries) > maxHistoryEntries {
		h.entries = h.entries[len(h.entries)-maxHistoryEntries:]
	}
	return h
}

// at ports the Array.prototype.at() negative-indexing history.tsx relies on:
// -1 is the newest entry, -len is the oldest.
func (h *promptHistory) at(index int) (promptHistoryEntry, bool) {
	n := len(h.entries)
	if n == 0 {
		return promptHistoryEntry{}, false
	}
	i := index
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		return promptHistoryEntry{}, false
	}
	return h.entries[i], true
}

// Move ports history.move(direction, input): direction -1 is "previous"
// (older), +1 is "next" (newer). ok is false when there is nothing to move
// to and the caller should let the arrow key fall through to normal cursor
// movement instead. Moving next past the newest entry yields ("", true) —
// the empty live draft, matching TS returning {input: "", parts: []}.
func (h *promptHistory) Move(direction int, currentInput string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	current, ok := h.at(h.index)
	if !ok {
		return "", false
	}
	// Only continue browsing from an empty box or from text that still
	// matches the entry we last recalled — an edited draft blocks further
	// navigation until it matches again, exactly like the TS guard.
	if current.Input != currentInput && currentInput != "" {
		return "", false
	}
	next := h.index + direction
	if abs(next) > len(h.entries) {
		return "", false
	}
	if next <= 0 {
		h.index = next
	}
	if h.index == 0 {
		return "", true
	}
	entry, ok := h.at(h.index)
	if !ok {
		return "", false
	}
	return entry.Input, true
}

// Append ports history.append: pushes a submitted prompt, collapsing an
// exact repeat of the last entry into a no-op (besides resetting the browse
// index), trimming to maxHistoryEntries, and persisting to disk best-effort.
func (h *promptHistory) Append(input string) {
	if input == "" {
		return
	}
	if len(h.entries) > 0 && h.entries[len(h.entries)-1].Input == input {
		h.index = 0
		return
	}
	h.entries = append(h.entries, promptHistoryEntry{Input: input})
	h.index = 0
	if len(h.entries) > maxHistoryEntries {
		h.entries = h.entries[len(h.entries)-maxHistoryEntries:]
		h.rewrite()
		return
	}
	h.appendLine(input)
}

func (h *promptHistory) rewrite() {
	if h.path == "" {
		return
	}
	var buf bytes.Buffer
	for _, entry := range h.entries {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(h.path, buf.Bytes(), 0o644)
}

func (h *promptHistory) appendLine(input string) {
	if h.path == "" {
		return
	}
	data, err := json.Marshal(promptHistoryEntry{Input: input})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	data = append(data, '\n')
	_, _ = f.Write(data)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
