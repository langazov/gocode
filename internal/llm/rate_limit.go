package llm

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A rate limit is a provider scheduling a retry, not refusing the request.
//
// Every client in this package lowers an HTTP 429 to RateLimitError, carrying
// the provider's own answer to "how long?" wherever it stated one. The session
// runner waits out exactly that delay and re-runs the step
// (internal/session/netretry.go) instead of failing the turn: the provider
// said the window reopens at T, and retrying before T only collects another
// 429 while idling past T wastes a window the provider already reopened.
type RateLimitError struct {
	// Provider prefixes the message the way each client's own APIError does
	// ("anthropic", "openai", ...), so the string a user reads keeps the
	// shape it always had.
	Provider string
	Message  string
	// RetryAfter is meaningful only when RetryAfterKnown is true. A bare
	// zero will not do: a genuine "retry now" (Retry-After: 0) is a real,
	// if unusual, answer that must not read as "no hint given".
	RetryAfter      time.Duration
	RetryAfterKnown bool
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: 429 %s", e.Provider, e.Message)
}

// retryAfterCap bounds any single provider-supplied wait. Real hints run
// seconds to minutes; anything past a day is a misparsed or hostile value,
// and clamping keeps the duration arithmetic sane while still saying "a very
// long time" to whoever acts on it.
const retryAfterCap = 24 * time.Hour

// RetryAfter finds the provider's answer to "how long should I wait?", in the
// order providers state it: typed protobuf duration strings first (Gemini
// carries the wait as retryDelay inside its error details — the most specific
// statement a backend can make), then the standard Retry-After header, then
// the prose some providers write into the message body ("Please try again in
// 1.728s"). The first candidate that parses wins; none of them is guaranteed
// present, which is what the bool says.
func RetryAfter(header, message string, delays ...string) (time.Duration, bool) {
	for _, candidate := range delays {
		if d, ok := ParseRetryAfterDelay(candidate); ok {
			return d, true
		}
	}
	if d, ok := ParseRetryAfterHeader(header); ok {
		return d, true
	}
	return RetryAfterFromMessage(message)
}

// ParseRetryAfterHeader reads a standard Retry-After header: either a
// non-negative number of seconds, or an HTTP-date. A date already in the past
// means "retry now" and returns a known zero.
func ParseRetryAfterHeader(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		if secs > float64(retryAfterCap/time.Second) {
			return retryAfterCap, true
		}
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampRetryAfter(time.Until(t)), true
	}
	return 0, false
}

// ParseRetryAfterDelay reads a protobuf-encoded Duration ("32s", "3.500s") —
// the shape Gemini's google.rpc.RetryInfo details carry. The seconds unit is
// required: these strings come from a typed field, not free text, which is
// also why "ms" is rejected rather than guessed at.
func ParseRetryAfterDelay(v string) (time.Duration, bool) {
	if strings.HasSuffix(v, "ms") || !strings.HasSuffix(v, "s") {
		return 0, false
	}
	secs, err := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	if secs > float64(retryAfterCap/time.Second) {
		return retryAfterCap, true
	}
	return time.Duration(secs * float64(time.Second)), true
}

// retryAfterPattern matches the wait some providers spell out in prose when
// no Retry-After header carries it (OpenAI: "Please try again in 1.728s").
var retryAfterPattern = regexp.MustCompile(`(?i)try again in\s+([0-9]*\.?[0-9]+)\s*(ms|s)\b`)

// RetryAfterFromMessage finds a "try again in <n><unit>" in an error message.
func RetryAfterFromMessage(msg string) (time.Duration, bool) {
	m := retryAfterPattern.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := time.Second
	if strings.EqualFold(m[2], "ms") {
		unit = time.Millisecond
	}
	return clampRetryAfter(time.Duration(val * float64(unit))), true
}

// clampRetryAfter pins a parsed hint inside [0, retryAfterCap].
func clampRetryAfter(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d > retryAfterCap:
		return retryAfterCap
	default:
		return d
	}
}
