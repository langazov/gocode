package llm

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// The bounds every provider client streams under. They replace the flat
// `http.Client{Timeout: 10 * time.Minute}` all four clients used to carry.
//
// http.Client.Timeout is an *absolute* cap on the whole exchange, body read
// included, so it cannot tell a stalled connection from a slow one: a live
// stream that simply takes a long time — a reasoning-heavy model emitting
// tens of thousands of thinking tokens in one step — was killed at the ten
// minute mark with tokens still arriving, and the turn settled as
// "interrupted" (see the runner's stepError). What actually needs bounding is
// silence, not duration, so the cap moves to two places that measure it:
//
//   - ResponseHeaderTimeout: the provider accepted the connection but never
//     answered. Generous, since a provider is free to sit on the request
//     while it processes a large prompt before writing its first byte.
//   - StreamIdleTimeout: headers arrived but the body then went quiet, the
//     shape a dropped connection takes when no FIN ever reaches us. Each read
//     resets it, so an active stream runs as long as it needs to.
//
// A user-ordered interrupt is unaffected: that cancels the request context,
// which aborts the read no matter what these say.
const (
	StreamHeaderTimeout = 5 * time.Minute
	StreamIdleTimeout   = 5 * time.Minute
)

// NewStreamHTTPClient builds the HTTP client a provider client streams with:
// no absolute Timeout, a response-header bound on the transport, and
// otherwise http.DefaultTransport's settings (connection pooling, proxy and
// TLS configuration included).
func NewStreamHTTPClient() *http.Client {
	transport, _ := http.DefaultTransport.(*http.Transport)
	if transport == nil {
		return &http.Client{}
	}
	cloned := transport.Clone()
	cloned.ResponseHeaderTimeout = StreamHeaderTimeout
	return &http.Client{Transport: cloned}
}

// NewIdleReader wraps a streaming response body so a connection that goes
// silent for idle fails with a description of what happened, rather than
// hanging until something else times out. Every Read restarts the clock, so
// the bound is on silence alone.
//
// Closing the returned reader stops the clock; it does not close the body,
// which each client already defers.
func NewIdleReader(body io.Reader, idle time.Duration) io.ReadCloser {
	r := &idleReader{body: body, idle: idle}
	closer, _ := body.(io.Closer)
	r.closer = closer
	r.timer = time.AfterFunc(idle, r.expire)
	return r
}

type idleReader struct {
	body   io.Reader
	closer io.Closer
	idle   time.Duration
	timer  *time.Timer

	mu      sync.Mutex
	expired bool
	stopped bool
}

// expire fires when nothing has been read for the idle window. Closing the
// underlying body is what unblocks the read parked in Read below — there is
// no other way to interrupt an io.Reader mid-call — and the flag it sets
// turns the resulting "closed body" error into an honest one.
func (r *idleReader) expire() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.expired = true
	closer := r.closer
	r.mu.Unlock()
	if closer != nil {
		closer.Close()
	}
}

func (r *idleReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	expired := r.expired
	if !expired {
		r.timer.Reset(r.idle)
	}
	r.mu.Unlock()
	if expired {
		return 0, r.timeoutError()
	}
	n, err := r.body.Read(p)
	if err != nil {
		r.mu.Lock()
		expired := r.expired
		r.mu.Unlock()
		if expired {
			return n, r.timeoutError()
		}
	}
	return n, err
}

func (r *idleReader) timeoutError() error {
	return fmt.Errorf("stream stalled: no data received for %s", r.idle)
}

func (r *idleReader) Close() error {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.timer.Stop()
	return nil
}
