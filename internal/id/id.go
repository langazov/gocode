package id

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/identifier"
)

type Kind string

const (
	KindJob        Kind = "job"
	KindEvent      Kind = "event"
	KindSession    Kind = "session"
	KindMessage    Kind = "message"
	KindPermission Kind = "permission"
	KindQuestion   Kind = "question"
	KindPart       Kind = "part"
	KindPty        Kind = "pty"
	KindTool       Kind = "tool"
	KindWorkspace  Kind = "workspace"
	KindMemory     Kind = "memory"
)

var prefixes = map[Kind]string{
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
	KindMemory:     "mem",
}

func Ascending(kind Kind, given ...string) (string, error) {
	return generate(kind, false, given)
}

func Descending(kind Kind, given ...string) (string, error) {
	return generate(kind, true, given)
}

func generate(kind Kind, descending bool, given []string) (string, error) {
	prefix, ok := prefixes[kind]
	if !ok {
		return "", fmt.Errorf("unknown id kind %q", kind)
	}
	if len(given) > 0 && given[0] != "" {
		if !strings.HasPrefix(given[0], prefix) {
			return "", fmt.Errorf("ID %s does not start with %s", given[0], prefix)
		}
		return given[0], nil
	}
	return Create(prefix, descending, time.Now().UnixMilli()), nil
}

func Create(prefix string, descending bool, timestamp int64) string {
	return prefix + "_" + identifier.Create(descending, timestamp)
}

// Timestamp extracts the timestamp from an ascending ID. Does not work with descending IDs.
func Timestamp(value string) (int64, error) {
	prefix, _, ok := strings.Cut(value, "_")
	if !ok {
		return 0, errors.New("malformed id")
	}
	hexPart := value[len(prefix)+1:]
	if len(hexPart) < 12 {
		return 0, errors.New("malformed id")
	}
	encoded, err := strconv.ParseUint(hexPart[:12], 16, 64)
	if err != nil {
		return 0, err
	}
	return int64(encoded / 0x1000), nil
}
