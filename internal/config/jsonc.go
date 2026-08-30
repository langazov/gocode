package config

import (
	"fmt"
	"strings"
)

// StripJSONC removes // and /* */ comments and trailing commas from JSONC
// input while preserving string contents, matching ConfigParse.jsonc.
func StripJSONC(input string) (string, error) {
	var out strings.Builder
	inString := false
	escaped := false
	i := 0
	for i < len(input) {
		c := input[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteByte(c)
			i++
		case c == '/' && i+1 < len(input) && input[i+1] == '/':
			for i < len(input) && input[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(input) && input[i+1] == '*':
			i += 2
			for i+1 < len(input) && !(input[i] == '*' && input[i+1] == '/') {
				i++
			}
			if i+1 >= len(input) {
				return "", fmt.Errorf("config: unterminated block comment")
			}
			i += 2
		case c == ',':
			// lookahead: drop trailing commas before } or ]
			j := i + 1
			for j < len(input) && (input[j] == ' ' || input[j] == '\t' || input[j] == '\n' || input[j] == '\r') {
				j++
			}
			if j < len(input) && (input[j] == '}' || input[j] == ']') {
				i++
				continue
			}
			out.WriteByte(c)
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), nil
}

// deepMerge merges src into dst recursively; maps merge, everything else
// replaces. Returns the merged map, mirroring mergeConfig.
func deepMerge(dst, src map[string]any) map[string]any {
	for key, value := range src {
		if srcMap, ok := value.(map[string]any); ok {
			if dstMap, ok := dst[key].(map[string]any); ok {
				dst[key] = deepMerge(dstMap, srcMap)
				continue
			}
			dst[key] = deepMerge(map[string]any{}, srcMap)
			continue
		}
		dst[key] = value
	}
	return dst
}
