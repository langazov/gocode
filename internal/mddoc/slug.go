package mddoc

import (
	"strings"
	"unicode"
)

// slugger converts heading text to GitHub-style anchor slugs and dedupes
// repeats the way GitHub does: the second "Setup" heading becomes "setup-1".
type slugger struct {
	seen map[string]int
}

func newSlugger() *slugger { return &slugger{seen: map[string]int{}} }

// Slug lowercases, strips everything that is not a letter, digit, space or
// hyphen, turns spaces into hyphens, and suffixes duplicates with -1, -2, ….
// This ports the anchor generation GitHub uses for #heading links.
func (s *slugger) Slug(text string) string {
	base := slugBase(text)
	n := s.seen[base]
	s.seen[base] = n + 1
	if n == 0 {
		return base
	}
	return base + "-" + itoa(n)
}

// slugBase is the pure text-to-slug step without dedupe.
func slugBase(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.TrimSpace(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			// GitHub keeps underscores; it strips other punctuation.
			b.WriteRune(unicode.ToLower(r))
		case r == ' ' || r == '\t':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
