package llm

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The bug this whole file guards: http.Client.Timeout bounded the entire
// exchange, body included, so a live stream was killed for taking too long
// rather than for going quiet. The client must carry no absolute deadline.
func TestStreamHTTPClientBoundsSilenceNotDuration(t *testing.T) {
	client := NewStreamHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %s, want none — it caps the body read too", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != StreamHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, StreamHeaderTimeout)
	}
	// Cloned from the default transport, so proxy and connection-pool
	// settings survive.
	if transport.Proxy == nil {
		t.Fatal("expected the default transport's proxy configuration to be kept")
	}
}

// A stream that keeps delivering runs as long as it likes, however far past
// the idle window its total duration goes.
func TestIdleReaderAllowsSlowButLiveStream(t *testing.T) {
	pr, pw := io.Pipe()
	const idle = 200 * time.Millisecond
	go func() {
		for i := 0; i < 8; i++ {
			time.Sleep(idle / 4)
			if _, err := pw.Write([]byte("data: chunk\n")); err != nil {
				return
			}
		}
		pw.Close()
	}()

	reader := NewIdleReader(pr, idle)
	defer reader.Close()
	start := time.Now()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The whole stream outlived the idle window several times over.
	if elapsed := time.Since(start); elapsed <= idle {
		t.Fatalf("stream finished in %s, too fast to prove anything", elapsed)
	}
	if got := strings.Count(string(body), "chunk"); got != 8 {
		t.Fatalf("read %d chunks, want 8", got)
	}
}

// Silence, on the other hand, ends the read — with a message that says so,
// rather than the "closed body" the underlying reader would report.
func TestIdleReaderFailsOnSilence(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		pw.Write([]byte("data: one\n"))
		// ...and then the connection goes quiet without ever closing.
	}()

	reader := NewIdleReader(pr, 50*time.Millisecond)
	defer reader.Close()
	_, err := io.ReadAll(reader)
	if err == nil {
		t.Fatal("expected a stalled stream to fail")
	}
	if !strings.Contains(err.Error(), "stream stalled") {
		t.Fatalf("error = %v, want it to name the stall", err)
	}
	if errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("the stall must not surface as the close it was implemented with: %v", err)
	}
}

// Closing the reader retires the clock, so a body read to completion cannot
// have its (already returned) connection closed under a later firing.
func TestIdleReaderCloseStopsTheClock(t *testing.T) {
	tracked := &trackedCloser{Reader: strings.NewReader("data: done\n")}
	reader := NewIdleReader(tracked, 20*time.Millisecond)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if tracked.closed {
		t.Fatal("the idle timer fired after Close and closed the body")
	}
}

type trackedCloser struct {
	io.Reader
	closed bool
}

func (t *trackedCloser) Close() error {
	t.closed = true
	return nil
}
