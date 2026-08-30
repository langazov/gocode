package identifier

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestLength(t *testing.T) {
	if got := Ascending(); len(got) != Length {
		t.Fatalf("expected length %d, got %d (%s)", Length, len(got), got)
	}
}

func TestCharset(t *testing.T) {
	id := Ascending()
	for _, r := range id {
		if !strings.ContainsRune(chars, r) {
			t.Fatalf("unexpected rune %q in %s", r, id)
		}
	}
}

// TestTimePrefixParity pins the encoding to the TypeScript output. For
// timestamp 1735689600000 and counter 1, the TS implementation emits the time
// prefix "41f297c00001".
func TestTimePrefixParity(t *testing.T) {
	current := int64(1735689600000)*0x1000 + 1
	if got := timePrefix(current); got != "41f297c00001" {
		t.Fatalf("expected 41f297c00001, got %s", got)
	}
}

// TestTruncationParity documents that the encoder keeps only the low 48 bits,
// so the extracted timestamp matches the (truncated) TypeScript behavior.
func TestTruncationParity(t *testing.T) {
	decoded, err := hex.DecodeString("41f297c00001")
	if err != nil {
		t.Fatal(err)
	}
	var value uint64
	for _, b := range decoded {
		value = value<<8 | uint64(b)
	}
	if got := value / 0x1000; got != 17702681600 {
		t.Fatalf("expected truncated timestamp 17702681600, got %d", got)
	}
}

func TestDescendingInverts(t *testing.T) {
	current := int64(1735689600000)*0x1000 + 1
	if timePrefix(current) == timePrefix(^current) {
		t.Fatalf("ascending and descending prefixes should differ")
	}
}

func TestCounterAdvances(t *testing.T) {
	a := Create(false, 1000)
	b := Create(false, 1000)
	if a == b {
		t.Fatalf("same-timestamp ids should differ via counter")
	}
}
