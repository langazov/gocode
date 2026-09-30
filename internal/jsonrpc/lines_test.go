package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// lineConns is pipeConns with newline-delimited framing and ACP's
// cancellation notification on both sides.
func lineConns(t *testing.T) (client, server *Conn) {
	t.Helper()
	cRead, sWrite := io.Pipe()
	sRead, cWrite := io.Pipe()
	client = NewLineConn(cWrite, cRead)
	server = NewLineConn(sWrite, sRead)
	client.SetCancelRequest("$/cancel_request", "requestId")
	server.SetCancelRequest("$/cancel_request", "requestId")
	go client.Listen()
	go server.Listen()
	t.Cleanup(func() {
		client.Shutdown(io.ErrClosedPipe)
		server.Shutdown(io.ErrClosedPipe)
	})
	return client, server
}

// rawServer runs a line-framed server whose input the test writes raw and
// whose output it reads line by line.
func rawServer(t *testing.T) (server *Conn, in io.WriteCloser, out *bufio.Reader) {
	t.Helper()
	sRead, in := io.Pipe()
	outRead, sWrite := io.Pipe()
	server = NewLineConn(sWrite, sRead)
	server.SetCancelRequest("$/cancel_request", "requestId")
	t.Cleanup(func() { server.Shutdown(io.ErrClosedPipe) })
	return server, in, bufio.NewReader(outRead)
}

func readLine(t *testing.T, out *bufio.Reader) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, _ := out.ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("message not newline-terminated: %q", line)
		}
		return strings.TrimSuffix(line, "\n")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a line")
		return ""
	}
}

func TestLineConnRoundTripKeepsMessagesOnOneLine(t *testing.T) {
	client, server := lineConns(t)
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
	// Embedded newlines must survive as escapes, never as frame breaks.
	if err := client.Call(ctx, "echo", map[string]string{"msg": "a\nb"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "a\nb" {
		t.Fatalf("echo = %q", out.Echo)
	}
}

func TestLineConnBatch(t *testing.T) {
	server, in, out := rawServer(t)
	server.Handle("double", func(params json.RawMessage) (any, error) {
		var n int
		json.Unmarshal(params, &n)
		return n * 2, nil
	})
	notified := make(chan struct{}, 1)
	server.OnNotify("ping", func(json.RawMessage) { notified <- struct{}{} })
	go server.Listen()

	io.WriteString(in, `[{"jsonrpc":"2.0","id":1,"method":"double","params":2},`+
		`{"jsonrpc":"2.0","method":"ping"},`+
		`42,`+
		`{"jsonrpc":"2.0","id":"b","method":"double","params":5}]`+"\n")

	var replies []struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal([]byte(readLine(t, out)), &replies); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 3 {
		t.Fatalf("want 3 replies (two results, one invalid entry), got %d", len(replies))
	}
	results := map[string]string{}
	invalid := 0
	for _, reply := range replies {
		if reply.Error != nil {
			if reply.Error.Code != CodeInvalidRequest {
				t.Fatalf("invalid entry code = %d", reply.Error.Code)
			}
			invalid++
			continue
		}
		results[string(reply.ID)] = string(reply.Result)
	}
	if invalid != 1 || results["1"] != "4" || results[`"b"`] != "10" {
		t.Fatalf("unexpected batch replies: %+v", replies)
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("batched notification was not delivered")
	}
}

func TestLineConnEmptyBatchIsInvalid(t *testing.T) {
	server, in, out := rawServer(t)
	go server.Listen()
	io.WriteString(in, "[]\n")
	var reply struct {
		Error *RPCError `json:"error"`
	}
	json.Unmarshal([]byte(readLine(t, out)), &reply)
	if reply.Error == nil || reply.Error.Code != CodeInvalidRequest {
		t.Fatalf("empty batch reply = %+v", reply)
	}
}

func TestLineConnCancelRequest(t *testing.T) {
	client, server := lineConns(t)
	started := make(chan struct{})
	server.HandleContext("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	server2Reply := make(chan error, 1)
	go func() {
		// Issue the request through a raw id so the test can cancel it.
		server2Reply <- client.Call(context.Background(), "slow", nil, nil)
	}()
	<-started
	if err := client.Notify("$/cancel_request", map[string]any{"requestId": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-server2Reply:
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != CodeRequestCancelled {
			t.Fatalf("want CodeRequestCancelled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request never answered")
	}
}

func TestCallSendsCancelWhenContextEnds(t *testing.T) {
	client, server := lineConns(t)
	cancelled := make(chan struct{})
	server.HandleContext("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := client.Call(ctx, "slow", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server handler was not cancelled by the outbound $/cancel_request")
	}
}

func TestHandlerRPCErrorKeepsItsCode(t *testing.T) {
	client, server := lineConns(t)
	server.Handle("auth", func(json.RawMessage) (any, error) {
		return nil, &RPCError{Code: -32000, Message: "Authentication required"}
	})
	err := client.Call(context.Background(), "auth", nil, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 {
		t.Fatalf("want -32000, got %v", err)
	}
}
