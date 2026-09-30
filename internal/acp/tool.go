package acp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/diff"
	"github.com/langazov/gocode-go/internal/patch"
)

// toolTrack is what the agent remembers about one tool call while it runs:
// enough to title it, locate it, and turn its settlement into a diff.
type toolTrack struct {
	callID string
	name   string
	input  map[string]any
	// before holds each touched file's content from before the call ran; a
	// nil entry means the file did not exist. Captured when the call is
	// reported, which is ahead of execution: the runner publishes the call,
	// then authorizes it, then runs it.
	before map[string]*string
	// terminalID is the client terminal (v1) or display terminal (v2) the
	// call's output is shown in.
	terminalID string
	status     string
}

// toolKind maps a gocode tool to the ACP kind clients pick icons by. Ports
// toToolKind in packages/opencode/src/acp/tool.ts, extended to this port's
// tool names.
func toolKind(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell":
		return "execute"
	case "webfetch", "websearch":
		return "fetch"
	case "edit", "write", "apply_patch", "patch":
		return "edit"
	case "grep", "glob", "context", "context7_resolve_library_id", "context7_get_library_docs", "lsp":
		return "search"
	case "read":
		return "read"
	case "task", "todowrite", "skill":
		return "think"
	case "plan_enter", "plan_exit":
		return "switch_mode"
	default:
		return "other"
	}
}

// toolTitle is the human label for a call. Shell calls show the command so it
// stays visible before output lands (tool.ts toolTitle); the rest show what
// they act on when that is one obvious thing.
func toolTitle(name string, input map[string]any, fallback string) string {
	str := func(key string) string {
		value, _ := input[key].(string)
		return value
	}
	switch strings.ToLower(name) {
	case "bash", "shell":
		if command := str("command"); command != "" {
			return command
		}
	case "read":
		if path := str("path"); path != "" {
			return "Read " + path
		}
	case "write":
		if path := str("path"); path != "" {
			return "Write " + path
		}
	case "edit":
		if path := str("path"); path != "" {
			return "Edit " + path
		}
	case "apply_patch":
		return "Apply patch"
	case "grep":
		if pattern := str("pattern"); pattern != "" {
			return "Search for " + pattern
		}
	case "glob":
		if pattern := str("pattern"); pattern != "" {
			return "Find " + pattern
		}
	case "webfetch":
		if url := str("url"); url != "" {
			return "Fetch " + url
		}
	case "websearch":
		if query := str("query"); query != "" {
			return "Search the web for " + query
		}
	case "task":
		if description := str("description"); description != "" {
			return description
		}
	case "todowrite":
		return "Update plan"
	case "skill":
		if skill := str("name"); skill != "" {
			return "Load skill " + skill
		}
	}
	if fallback != "" {
		return fallback
	}
	return name
}

// absolute resolves a tool input path against the session directory; ACP
// paths MUST be absolute (protocol/v1/overview "Argument requirements").
func absolute(path, cwd string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// toolLocations reports the files a call works on, for the client's
// follow-along. Ports toLocations in tool.ts.
func toolLocations(name string, input map[string]any, cwd string) []obj {
	str := func(key string) string {
		value, _ := input[key].(string)
		return value
	}
	var paths []string
	switch strings.ToLower(name) {
	case "bash", "shell":
		if dir := str("workdir"); dir != "" {
			paths = append(paths, absolute(dir, cwd))
		} else {
			paths = append(paths, cwd)
		}
	case "read", "edit", "write":
		if path := str("path"); path != "" {
			paths = append(paths, absolute(path, cwd))
		}
	case "grep", "glob":
		if path := str("path"); path != "" {
			paths = append(paths, absolute(path, cwd))
		}
	case "apply_patch":
		paths = patchPaths(str("patchText"), cwd)
	}
	locations := make([]obj, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		location := obj{"path": path}
		if strings.ToLower(name) == "read" {
			if offset, ok := input["offset"].(float64); ok && offset >= 1 {
				location["line"] = int(offset)
			}
		}
		locations = append(locations, location)
	}
	return locations
}

// toolRawInput is the input as reported. Shell input gains the directory it
// runs in, unless the model gave one (tool.ts rawInput).
func toolRawInput(name string, input map[string]any, cwd string) map[string]any {
	if strings.ToLower(name) != "bash" {
		return input
	}
	if _, ok := input["workdir"]; ok {
		return input
	}
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	out["cwd"] = cwd
	return out
}

// patchPaths lists the absolute paths an apply_patch touches, including a
// move's destination.
func patchPaths(patchText, cwd string) []string {
	hunks, err := patch.Parse(patchText)
	if err != nil {
		return nil
	}
	var paths []string
	for _, hunk := range hunks {
		paths = append(paths, absolute(hunk.Path, cwd))
		if hunk.MovePath != "" {
			paths = append(paths, absolute(hunk.MovePath, cwd))
		}
	}
	return paths
}

// editedPaths lists the files an edit-kind call may change.
func editedPaths(name string, input map[string]any, cwd string) []string {
	switch strings.ToLower(name) {
	case "edit", "write":
		if path, _ := input["path"].(string); path != "" {
			return []string{absolute(path, cwd)}
		}
	case "apply_patch":
		text, _ := input["patchText"].(string)
		return patchPaths(text, cwd)
	}
	return nil
}

// snapshot reads the current content of each path; nil marks a missing file.
func snapshot(paths []string) map[string]*string {
	if len(paths) == 0 {
		return nil
	}
	out := make(map[string]*string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			out[path] = nil
			continue
		}
		text := string(data)
		out[path] = &text
	}
	return out
}

// fileChange is one file's before/after pair, the input both diff shapes are
// built from.
type fileChange struct {
	path          string
	before, after *string
}

// changes compares the snapshot taken before a call with the files now.
func (t *toolTrack) changes() []fileChange {
	if len(t.before) == 0 {
		return nil
	}
	now := snapshot(keys(t.before))
	var out []fileChange
	for _, path := range sortedKeys(t.before) {
		before, after := t.before[path], now[path]
		if before == nil && after == nil {
			continue
		}
		if before != nil && after != nil && *before == *after {
			continue
		}
		out = append(out, fileChange{path: path, before: before, after: after})
	}
	return out
}

// diffV1 renders changes in the v1 shape: one {path, oldText, newText} per
// file, oldText null for a new file (protocol/v1/tool-calls "Diffs"). v1
// cannot express a deletion; an emptied file is the closest it has.
func diffV1(changes []fileChange) []obj {
	out := make([]obj, 0, len(changes))
	for _, change := range changes {
		entry := obj{"type": "diff", "path": change.path}
		if change.before == nil {
			entry["oldText"] = nil
		} else {
			entry["oldText"] = *change.before
		}
		if change.after == nil {
			entry["newText"] = ""
		} else {
			entry["newText"] = *change.after
		}
		out = append(out, entry)
	}
	return out
}

// diffV2 renders changes as one structured v2 diff: the authoritative
// per-file operations, plus renderable git_patch text (protocol/v2/migration
// "Diff content").
func diffV2(changes []fileChange) []obj {
	if len(changes) == 0 {
		return nil
	}
	fileChanges := make([]obj, 0, len(changes))
	var patchText strings.Builder
	for _, change := range changes {
		operation := "modify"
		oldText, newText := "", ""
		switch {
		case change.before == nil:
			operation = "add"
			newText = *change.after
		case change.after == nil:
			operation = "delete"
			oldText = *change.before
		default:
			oldText, newText = *change.before, *change.after
		}
		fileChanges = append(fileChanges, obj{
			"operation": operation,
			"path":      change.path,
			"fileType":  "text",
		})
		patchText.WriteString(gitPatch(change.path, operation, oldText, newText))
	}
	entry := obj{"type": "diff", "changes": fileChanges}
	if patchText.Len() > 0 {
		entry["patch"] = obj{"format": "git_patch", "text": patchText.String()}
	}
	return []obj{entry}
}

// gitPatch renders one file as a `diff --git` section of Git's --patch
// format, with absolute paths as git_patch requires.
func gitPatch(path, operation, oldText, newText string) string {
	body := diff.Unified(path, path, oldText, newText)
	// diff.Unified leads with its own ---/+++ header; git_patch wants the
	// `diff --git` line first and, for adds and deletes, /dev/null on the
	// missing side, so only the hunks are kept. Only the leading header is
	// dropped: a removed line whose text starts "-- " renders as "--- ..."
	// inside a hunk and must survive.
	var hunks strings.Builder
	if at := strings.Index(body, "\n@@"); at >= 0 {
		hunks.WriteString(body[at+1:])
	}
	if strings.TrimSpace(hunks.String()) == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString("diff --git " + path + " " + path + "\n")
	switch operation {
	case "add":
		out.WriteString("new file mode 100644\n--- /dev/null\n+++ " + path + "\n")
	case "delete":
		out.WriteString("deleted file mode 100644\n--- " + path + "\n+++ /dev/null\n")
	default:
		out.WriteString("--- " + path + "\n+++ " + path + "\n")
	}
	out.WriteString(hunks.String())
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\n")
	}
	return out.String()
}

func keys(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

func sortedKeys(m map[string]*string) []string {
	out := keys(m)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// textContent wraps text as tool call content.
func textContent(text string) obj {
	return obj{"type": "content", "content": obj{"type": "text", "text": text}}
}
