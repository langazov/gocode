package identifier

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const Length = 26

const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	mu            sync.Mutex
	lastTimestamp int64
	counter       int64
)

func Ascending() string {
	return Create(false, time.Now().UnixMilli())
}

func Descending() string {
	return Create(true, time.Now().UnixMilli())
}

func Create(descending bool, timestamp int64) string {
	mu.Lock()
	if timestamp != lastTimestamp {
		lastTimestamp = timestamp
		counter = 0
	}
	counter++
	current := timestamp*0x1000 + counter
	mu.Unlock()

	value := current
	if descending {
		value = ^current
	}
	return timePrefix(value) + randomSuffix()
}

// timePrefix encodes the low 48 bits of value as 12 hex characters, matching
// the TypeScript implementation (which truncates to 6 bytes).
func timePrefix(value int64) string {
	out := make([]byte, 6)
	for i := range out {
		out[i] = byte(uint64(value) >> (40 - 8*uint(i)))
	}
	return hex.EncodeToString(out)
}

func randomSuffix() string {
	random := make([]byte, Length-12)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	var sb strings.Builder
	sb.Grow(Length - 12)
	for _, b := range random {
		sb.WriteByte(chars[int(b)%62])
	}
	return sb.String()
}
