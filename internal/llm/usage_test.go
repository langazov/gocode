package llm_test

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

func TestSubtractTokens(t *testing.T) {
	cases := []struct {
		name   string
		total  int
		subset int
		want   int
	}{
		{"ordinary split", 1000, 900, 100},
		{"no subset reported", 1000, 0, 1000},
		{"whole total cached", 1000, 1000, 0},
		// A provider reporting a subset larger than its own total is
		// nonsensical, but a negative bucket would credit the step's cost
		// rather than merely mis-report it.
		{"subset exceeds total", 1000, 1200, 0},
		{"negative subset ignored", 1000, -5, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.SubtractTokens(tc.total, tc.subset); got != tc.want {
				t.Errorf("SubtractTokens(%d, %d) = %d, want %d", tc.total, tc.subset, got, tc.want)
			}
		})
	}
}
