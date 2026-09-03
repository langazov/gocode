// Package markdown parses YAML-frontmatter markdown documents, the format
// gocode uses for skills (SKILL.md) and markdown-defined agents.
//
// Frontmatter is decoded with gopkg.in/yaml.v3. When strict YAML rejects the
// header, it is retried through Sanitize — the same fallback the TypeScript
// loader applies in packages/core/src/config/markdown.ts, which wraps
// gray-matter in a try/catch around a sanitizing retry. Other coding agents
// accept unquoted colons in frontmatter values, so config files in the wild
// contain them.
package markdown

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a parsed frontmatter document.
type Document struct {
	// Frontmatter holds the decoded YAML header, empty when there is none.
	Frontmatter map[string]any
	// Content is the markdown body with the frontmatter removed.
	Content string
}

// String returns a frontmatter value as a string, or "" when absent or of
// another type.
func (d Document) String(key string) string {
	value, _ := d.Frontmatter[key].(string)
	return value
}

// Bool returns a frontmatter value as a bool, or false when absent.
func (d Document) Bool(key string) bool {
	value, _ := d.Frontmatter[key].(bool)
	return value
}

// Has reports whether a key is present.
func (d Document) Has(key string) bool {
	_, ok := d.Frontmatter[key]
	return ok
}

// Int returns a frontmatter value as an int. It accepts every numeric type the
// YAML decoder may produce, so callers do not depend on that choice.
func (d Document) Int(key string) (int, bool) {
	switch value := d.Frontmatter[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	}
	return 0, false
}

// Float returns a frontmatter value as a float64, accepting integers too.
func (d Document) Float(key string) (float64, bool) {
	switch value := d.Frontmatter[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	}
	return 0, false
}

// Parse splits frontmatter from body. A document without frontmatter is
// returned whole with an empty map — that is not an error. A header that
// cannot be decoded even after sanitizing yields the parse error.
func Parse(content string) (Document, error) {
	header, body, ok := split(content)
	if !ok {
		return Document{Frontmatter: map[string]any{}, Content: content}, nil
	}

	values, err := decode(header)
	if err != nil {
		// Retry with unquoted colons folded into block scalars.
		if sanitized, changed := sanitizeHeader(header); changed {
			if retried, retryErr := decode(sanitized); retryErr == nil {
				return Document{Frontmatter: retried, Content: body}, nil
			}
		}
		return Document{}, err
	}
	return Document{Frontmatter: values, Content: body}, nil
}

// split separates the `---` delimited header from the body. It reports false
// when the document has no frontmatter, in which case the content is the whole
// input verbatim.
func split(content string) (header, body string, ok bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	// A bare "---" with nothing after it is a horizontal rule, not an opening
	// delimiter, so the prefix must include the newline.
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", false
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// An unterminated header is a body, not an error.
		return "", "", false
	}
	body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
	return rest[:end], body, true
}

// decode unmarshals a YAML mapping. An empty header is a valid empty mapping;
// a non-mapping header (a bare list or scalar) is not frontmatter.
func decode(header string) (map[string]any, error) {
	if strings.TrimSpace(header) == "" {
		return map[string]any{}, nil
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(header), &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

var (
	// entryPattern matches a top-level `key: value` frontmatter line.
	entryPattern = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\s*:\s*(.*)$`)
	// indentedPattern matches a continuation line, which is left alone.
	indentedPattern = regexp.MustCompile(`^\s+`)
)

// Sanitize rewrites frontmatter values containing unquoted colons as YAML
// block scalars, so `description: Use when: editing` parses as one string
// instead of failing. Ports sanitize() from
// packages/core/src/config/markdown.ts.
func Sanitize(content string) string {
	header, body, ok := split(content)
	if !ok {
		return content
	}
	sanitized, changed := sanitizeHeader(header)
	if !changed {
		return content
	}
	return "---\n" + sanitized + "\n---\n" + body
}

// sanitizeHeader returns the rewritten header and whether anything changed.
func sanitizeHeader(header string) (string, bool) {
	lines := strings.Split(header, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Comments, blanks, and continuation lines are structural.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || indentedPattern.MatchString(line) {
			out = append(out, line)
			continue
		}
		entry := entryPattern.FindStringSubmatch(line)
		if entry == nil {
			out = append(out, line)
			continue
		}
		value := strings.TrimSpace(entry[2])
		// Already a block scalar, already quoted, or empty: nothing to fix.
		if value == "" || value == ">" || value == "|" ||
			strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'") {
			out = append(out, line)
			continue
		}
		if !strings.Contains(value, ":") {
			out = append(out, line)
			continue
		}
		out = append(out, entry[1]+": |-", "  "+value)
		changed = true
	}
	return strings.Join(out, "\n"), changed
}
