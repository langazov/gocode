package jsonrpc

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// pipeConns wires two connections over a pair of pipes, the way a client and
// a server face each other over stdio. Both directions exercise the framing.
func pipeConns(t *testing.T) (client, server *Conn) {
	t.Helper()
	cRead, sWrite := io.Pipe()
	sRead, cWrite := io.Pipe()
	client = NewConn(cWrite, cRead)
	server = NewConn(sWrite, sRead)
	go client.Listen()
	go server.Listen()
	t.Cleanup(func() {
		client.Shutdown(io.ErrClosedPipe)
		server.Shutdown(io.ErrClosedPipe)
	})
	return client, server
}

func TestCallRoundTrip(t *testing.T) {
	client, server := pipeConns(t)
	server.Handle("echo", func(params json.RawMessage) (any, error) {
		var in struct {
			Msg string `json:"msg"`
		}
		json.Unmarshal(params, &in)
		return map[string]string{"echo": in.Msg}, nil
	})

	var out struct {
		Echo string `json:"echo"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, "echo", map[string]string{"msg": "hello"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != "hello" {
		t.Fatalf("echo = %q", out.Echo)
	}
}

func TestServerErrorResponse(t *testing.T) {
	client, server := pipeConns(t)
	server.Handle("boom", func(json.RawMessage) (any, error) {
		return nil, errTest
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, "boom", nil, nil)
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	if rpcErr.Code != CodeInternalError || rpcErr.Message != errTest.Error() {
		t.Fatalf("rpcErr = %+v", rpcErr)
	}
}

func TestMethodNotFoundWithoutFallback(t *testing.T) {
	client, _ := pipeConns(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Call(ctx, "no/such/method", nil, nil)
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeMethodNotFound)
	}
}

func TestMissingMethodFallback(t *testing.T) {
	// The client behavior this package was extracted from: answer unknown
	// probes with a null result rather than an error, because servers that
	// get MethodNotFound for a capability probe may give up entirely.
	client, server := pipeConns(t)
	server.SetMissingMethod(func(string) (any, error) { return nil, nil })
	if err := client.Call(context.Background(), "probe", nil, nil); err != nil {
		t.Fatalf("probe with fallback: %v", err)
	}
}

func TestNotify(t *testing.T) {
	client, server := pipeConns(t)
	got := make(chan json.RawMessage, 1)
	server.OnNotify("ping", func(params json.RawMessage) { got <- params })
	if err := client.Notify("ping", map[string]int{"n": 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case params := <-got:
		var m map[string]int
		if err := json.Unmarshal(params, &m); err != nil || m["n"] != 7 {
			t.Fatalf("params = %s", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never arrived")
	}
}

func TestFramingSurvivesLargePayload(t *testing.T) {
	// Content-Length framing is where hand-rolled transports go wrong; push a
	// payload larger than the 64KiB read buffer through both directions.
	client, server := pipeConns(t)
	server.Handle("big", func(params json.RawMessage) (any, error) {
		var in struct {
			Blob string `json:"blob"`
		}
		json.Unmarshal(params, &in)
		return in.Blob, nil
	})
	blob := make([]byte, 256*1024)
	for i := range blob {
		blob[i] = byte('a' + i%26)
	}
	var out string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Call(ctx, "big", map[string]string{"blob": string(blob)}, &out); err != nil {
		t.Fatal(err)
	}
	if out != string(blob) {
		t.Fatalf("payload corrupted: got %d bytes, want %d", len(out), len(blob))
	}
}

func TestOrderedDispatchRunsInArrivalOrder(t *testing.T) {
	// The bug this guards: a notification and the request sent right after it
	// each got their own goroutine, so a server could answer the request
	// before applying the notification. mdlsp hit this as an empty document
	// outline when an editor sent didOpen then documentSymbol.
	cRead, sWrite := io.Pipe()
	sRead, cWrite := io.Pipe()
	client := NewConn(cWrite, cRead)
	server := NewConn(sWrite, sRead)
	server.SetOrderedDispatch(true)
	go client.Listen()
	go server.Listen()
	t.Cleanup(func() {
		client.Shutdown(io.ErrClosedPipe)
		server.Shutdown(io.ErrClosedPipe)
	})

	// The notification handler is deliberately the slow one: under concurrent
	// dispatch the request overtakes it and reads seen=false.
	opened := false
	server.OnNotify("open", func(json.RawMessage) {
		time.Sleep(20 * time.Millisecond)
		opened = true
	})
	server.Handle("read", func(json.RawMessage) (any, error) { return opened, nil })

	for i := range 20 {
		opened = false
		if err := client.Notify("open", nil); err != nil {
			t.Fatal(err)
		}
		var got bool
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := client.Call(ctx, "read", nil, &got)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatalf("iteration %d: request answered before the notification it followed", i)
		}
	}
}

func TestConcurrentDispatchIsTheDefault(t *testing.T) {
	// A client must not serialize inbound traffic: a slow server-initiated
	// request has to overlap with the rest, or a handler that calls back into
	// the same connection would deadlock.
	client, server := pipeConns(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	server.Handle("wait", func(json.RawMessage) (any, error) {
		entered <- struct{}{}
		<-release
		return nil, nil
	})

	go func() { _ = client.Call(context.Background(), "wait", nil, nil) }()
	go func() { _ = client.Call(context.Background(), "wait", nil, nil) }()

	for i := range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d handler(s) ran; dispatch is not concurrent", i)
		}
	}
	close(release)
}

var errTest = &testError{}

type testError struct{}

func (*testError) Error() string { return "boom" }
