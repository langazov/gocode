package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterHeader(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"30", 30 * time.Second, true},
		{"1.5", 1500 * time.Millisecond, true},
		{"0", 0, true}, // a real, if unusual, answer: retry now
		{"-5", 0, false},
		{"garbage", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfterHeader(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRetryAfterHeader(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// An HTTP-date already in the past means "retry now", not "no hint".
func TestParseRetryAfterHeaderPastDate(t *testing.T) {
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	got, ok := ParseRetryAfterHeader(past)
	if !ok || got != 0 {
		t.Fatalf("past date = (%v, %v), want a known zero", got, ok)
	}
}

func TestParseRetryAfterHeaderFutureDate(t *testing.T) {
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	got, ok := ParseRetryAfterHeader(future)
	if !ok {
		t.Fatal("expected a parsed date")
	}
	// The exact wait depends on formatting/truncation; the shape is what
	// matters: close to 90s, never negative, never enormous.
	if got <= 0 || got > 2*time.Minute {
		t.Fatalf("future date wait = %v, want ~90s", got)
	}
}

func TestParseRetryAfterDelay(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"32s", 32 * time.Second, true},
		{"3.500s", 3500 * time.Millisecond, true},
		{"0s", 0, true},
		{"500ms", 0, false}, // protobuf Durations carry seconds only
		{"32", 0, false},    // the unit is required
		{"abc", 0, false},
		{"-3s", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfterDelay(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRetryAfterDelay(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRetryAfterFromMessage(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"Rate limit reached. Please try again in 1.728s.", 1728 * time.Millisecond, true},
		{"Try again in 750ms", 750 * time.Millisecond, true},
		{"try AGAIN IN 5s", 5 * time.Second, true},
		{"no timing here", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := RetryAfterFromMessage(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("RetryAfterFromMessage(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The whole point of the detection order: a typed field beats the header
// beats the prose, and everything is clamped at the cap.
func TestRetryAfterPrecedenceAndCap(t *testing.T) {
	if d, ok := RetryAfter("10", "", "32s"); !ok || d != 32*time.Second {
		t.Fatalf("typed delay should beat the header, got (%v, %v)", d, ok)
	}
	if d, ok := RetryAfter("10", "try again in 5s"); !ok || d != 10*time.Second {
		t.Fatalf("header should beat prose, got (%v, %v)", d, ok)
	}
	if d, ok := RetryAfter("", "try again in 5s"); !ok || d != 5*time.Second {
		t.Fatalf("prose is the last resort, got (%v, %v)", d, ok)
	}
	if d, ok := RetryAfter("", "", "junk"); ok || d != 0 {
		t.Fatalf("nothing stated means nothing known, got (%v, %v)", d, ok)
	}
	// A hostile or misparsed value lands at the cap, not at overflow.
	if d, ok := RetryAfter("999999", ""); !ok || d != retryAfterCap {
		t.Fatalf("enormous header should clamp to the cap, got (%v, %v)", d, ok)
	}
	if d, ok := RetryAfter("", "", "86400s"); !ok || d != retryAfterCap {
		t.Fatalf("enormous typed delay should clamp to the cap, got (%v, %v)", d, ok)
	}
}
