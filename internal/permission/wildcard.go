package permission

import (
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Match implements packages/core/src/util/wildcard.ts: glob-style patterns
// where * matches any run and ? matches one character, full-string match,
// case-insensitive on Windows.
func Match(input, pattern string) bool {
	normalized := strings.ReplaceAll(input, "\\", "/")
	expr := compile(pattern)
	if expr == nil {
		return false
	}
	return expr.MatchString(normalized)
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*regexp.Regexp{}
)

func compile(pattern string) *regexp.Regexp {
	normalized := strings.ReplaceAll(pattern, "\\", "/")
	cacheMu.Lock()
	defer cacheMu.Unlock()
	key := "(?s)" + normalized
	if runtime.GOOS == "windows" {
		key = "(?is)" + normalized
	}
	if cached, ok := cache[key]; ok {
		return cached
	}
	escaped := escape(normalized)
	if runtime.GOOS == "windows" {
		escaped = "(?is)" + escaped
	} else {
		escaped = "(?s)" + escaped
	}
	expr, err := regexp.Compile(escaped)
	if err != nil {
		return nil
	}
	cache[key] = expr
	return expr
}

var specialChars = regexp.MustCompile(`[.+^${}()|[\]\\]`)

func escape(pattern string) string {
	escaped := specialChars.ReplaceAllString(pattern, `\$0`)
	escaped = strings.ReplaceAll(escaped, "*", ".*")
	escaped = strings.ReplaceAll(escaped, "?", ".")
	if strings.HasSuffix(escaped, " .*") {
		escaped = escaped[:len(escaped)-3] + "( .*)?"
	}
	return "^" + escaped + "$"
}
