package session

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/diff"
)

// TestPermissionMetadataDescribesTheAsk pins the metadata seam (§4.3): every
// action with something to show carries it, so the answering surface can
// render the diff/command/URL before approval instead of "No diff provided".
func TestPermissionMetadataDescribesTheAsk(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  map[string]any
	}{
		{
			tool: "edit",
			input: map[string]any{
				"path": "a.go", "oldString": "x", "newString": "y",
			},
			want: map[string]any{
				"filepath": "a.go",
				"diff":     diff.Unified("a.go", "a.go", "x", "y"),
			},
		},
		{
			tool:  "write",
			input: map[string]any{"path": "new.txt", "content": "hello"},
			want: map[string]any{
				"filepath": "new.txt",
				"diff":     diff.Unified("new.txt", "new.txt", "", "hello"),
			},
		},
		{
			tool:  "apply_patch",
			input: map[string]any{"patchText": "*** Begin Patch"},
			want:  map[string]any{"diff": "*** Begin Patch"},
		},
		{
			tool:  "bash",
			input: map[string]any{"command": "ls -la"},
			want:  map[string]any{"command": "ls -la"},
		},
		{
			tool:  "webfetch",
			input: map[string]any{"url": "https://x.example"},
			want:  map[string]any{"url": "https://x.example"},
		},
		{
			tool:  "websearch",
			input: map[string]any{"query": "go modules"},
			want:  map[string]any{"query": "go modules"},
		},
		{
			tool: "task",
			input: map[string]any{
				"subagent_type": "explore", "description": "find the seam",
			},
			want: map[string]any{"subagent_type": "explore", "description": "find the seam"},
		},
	}
	for _, c := range cases {
		got := permissionMetadata(c.tool, c.input)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d fields (%v), want %d", c.tool, len(got), got, len(c.want))
			continue
		}
		for key, wantValue := range c.want {
			gotValue, ok := got[key]
			if !ok {
				t.Errorf("%s: missing %q", c.tool, key)
				continue
			}
			if gotValue != wantValue {
				t.Errorf("%s: %s = %v, want %v", c.tool, key, gotValue, wantValue)
			}
		}
	}
	// Tools with nothing to show, or missing fields, return nil — the
	// resource-based fallback renders those.
	for _, c := range []struct {
		tool  string
		input map[string]any
	}{
		{"read", map[string]any{"path": "a.go"}},
		{"todowrite", map[string]any{}},
		{"bash", map[string]any{}},
		{"edit", map[string]any{"oldString": "x", "newString": "y"}},
	} {
		if got := permissionMetadata(c.tool, c.input); got != nil {
			t.Errorf("%s: expected nil metadata, got %v", c.tool, got)
		}
	}
}

// TestPermissionMetadataEditDiffIsReal pins that the edit diff renders the
// actual change: without the seam the banner showed "No diff provided", and a
// degenerate implementation could satisfy the table above with an empty diff.
func TestPermissionMetadataEditDiffIsReal(t *testing.T) {
	metadata := permissionMetadata("edit", map[string]any{
		"path": "a.go", "oldString": "old line", "newString": "new line",
	})
	text, _ := metadata["diff"].(string)
	if !strings.Contains(text, "-old line") || !strings.Contains(text, "+new line") {
		t.Fatalf("edit diff does not show the change:\n%s", text)
	}
}
