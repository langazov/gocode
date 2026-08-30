package id

import (
	"strings"
	"testing"
)

func TestAscendingPrefix(t *testing.T) {
	got, err := Ascending(KindSession)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "ses_") {
		t.Fatalf("expected ses_ prefix, got %s", got)
	}
}

func TestGivenPassthrough(t *testing.T) {
	got, err := Ascending(KindMessage, "msg_abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "msg_abc123" {
		t.Fatalf("expected passthrough, got %s", got)
	}
}

func TestGivenWrongPrefix(t *testing.T) {
	if _, err := Ascending(KindSession, "msg_abc"); err == nil {
		t.Fatalf("expected error for mismatched prefix")
	}
}

func TestUnknownKind(t *testing.T) {
	if _, err := Ascending(Kind("nope")); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestPrefixTable(t *testing.T) {
	cases := map[Kind]string{
		KindJob:        "job",
		KindEvent:      "evt",
		KindSession:    "ses",
		KindMessage:    "msg",
		KindPermission: "per",
		KindQuestion:   "que",
		KindPart:       "prt",
		KindPty:        "pty",
		KindTool:       "tool",
		KindWorkspace:  "wrk",
	}
	for kind, prefix := range cases {
		got, err := Ascending(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, prefix+"_") {
			t.Fatalf("kind %s: expected prefix %s_, got %s", kind, prefix, got)
		}
	}
}

// TestTimestampRoundtrip uses a timestamp small enough that ts*0x1000 fits in
// the 48-bit window, so extraction round-trips exactly (counter < 0x1000).
func TestTimestampRoundtrip(t *testing.T) {
	ts := int64(1000000)
	got := Create("ses", false, ts)
	extracted, err := Timestamp(got)
	if err != nil {
		t.Fatal(err)
	}
	if extracted != ts {
		t.Fatalf("expected %d, got %d", ts, extracted)
	}
}

func TestTimestampMalformed(t *testing.T) {
	if _, err := Timestamp("nounderscore"); err == nil {
		t.Fatalf("expected error for malformed id")
	}
	if _, err := Timestamp("ses_short"); err == nil {
		t.Fatalf("expected error for short id")
	}
}
