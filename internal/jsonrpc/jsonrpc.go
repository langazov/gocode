// Package jsonrpc implements a JSON-RPC 2.0 connection over the LSP base
// protocol: each message is a `Content-Length: N` header block, a blank line,
// then N bytes of JSON.
//
// The connection is symmetric: both sides can Call, Notify, Handle and
// OnNotify, so the same type serves the language-server client in
// internal/lsp and the markdown language server in internal/mdlsp. It is
// extracted from the client-only conn that used to live in
// internal/lsp/jsonrpc.go, which replaced vscode-jsonrpc (the library the
// TypeScript opencode client uses).
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Error codes from the JSON-RPC 2.0 spec that this package produces.
const (
	CodeMethodNotFound  = -32601
	CodeInternalError   = -32603
	CodeConnectionClose = -32000
)

// ErrClosed is returned by Call once the connection has been shut down.
var ErrClosed = errors.New("jsonrpc: connection closed")

// RPCError is a JSON-RPC error object, returned by Call when the remote side
// answers with an error instead of a result.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc: rpc error %d: %s", e.Code, e.Message)
}

// HandlerFunc answers an incoming request. A returned error becomes an error
// response carrying CodeInternalError.
type HandlerFunc func(params json.RawMessage) (any, error)

// NotifyFunc receives an incoming notification.
type NotifyFunc func(params json.RawMessage)

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  any              `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// Conn is one JSON-RPC connection. Create with NewConn and start its read
// loop with Listen; every other method is safe for concurrent use.
type Conn struct {
	writer io.WriteCloser
	reader *bufio.Reader

	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan rpcResponse
	handlers map[string]HandlerFunc
	notify   map[string]NotifyFunc
	closed   bool
	closeErr error

	// missingMethod, when set, answers requests that have no registered
	// handler. A client typically returns (nil, nil) here — a null result —
	// because servers probe for capabilities it does not implement and a
	// MethodNotFound can make them give up. When nil, such requests get a
	// CodeMethodNotFound error response, which is what a server should send.
	missingMethod func(method string) (any, error)

	// ordered, when set, runs inbound requests and notifications one at a
	// time in arrival order instead of one goroutine each. See
	// SetOrderedDispatch. inbox is the FIFO the read loop appends to and the
	// worker drains; inboxWake signals a waiting worker.
	ordered   bool
	inbox     []func()
	inboxWake *sync.Cond
}

// NewConn builds a connection that writes to w and reads from r.
func NewConn(w io.WriteCloser, r io.Reader) *Conn {
	c := &Conn{
		writer:   w,
		reader:   bufio.NewReaderSize(r, 64*1024),
		pending:  map[int64]chan rpcResponse{},
		handlers: map[string]HandlerFunc{},
		notify:   map[string]NotifyFunc{},
	}
	c.inboxWake = sync.NewCond(&c.mu)
	return c
}

// SetOrderedDispatch makes inbound requests and notifications run one at a
// time, in the order they arrived, rather than each on its own goroutine.
// Call it before Listen.
//
// The default is concurrent dispatch, which is what a client wants: it lets a
// slow handler for a server-initiated request run while other traffic
// continues. A server usually needs the opposite. LSP guarantees ordering,
// and editors rely on it — a client that sends textDocument/didOpen and then
// immediately textDocument/documentSymbol expects the open to have been
// applied. Under concurrent dispatch those two land in a race, and the
// request can be answered against state the notification had not reached yet.
//
// Responses to our own outbound Calls are still delivered inline on the read
// loop, never queued behind a handler, so a handler is free to Call back.
func (c *Conn) SetOrderedDispatch(ordered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ordered = ordered
}

// SetMissingMethod registers the fallback for unhandled requests. Call it
// before Listen.
func (c *Conn) SetMissingMethod(fn func(method string) (any, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.missingMethod = fn
}

// Handle registers a responder for an incoming request.
func (c *Conn) Handle(method string, fn HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[method] = fn
}

// OnNotify registers a listener for an incoming notification.
func (c *Conn) OnNotify(method string, fn NotifyFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify[method] = fn
}

// Listen reads messages until the stream ends, then fails every in-flight
// call. It runs in its own goroutine for the lifetime of the connection.
func (c *Conn) Listen() {
	c.mu.Lock()
	ordered := c.ordered
	c.mu.Unlock()
	if ordered {
		done := make(chan struct{})
		go func() { defer close(done); c.drainInbox() }()
		// Shutdown wakes the worker; wait for the queue to drain so a
		// handler is never abandoned mid-flight.
		defer func() { <-done }()
	}
	for {
		payload, err := c.readMessage()
		if err != nil {
			c.Shutdown(err)
			return
		}
		var message rpcResponse
		if err := json.Unmarshal(payload, &message); err != nil {
			// A malformed frame is not fatal: skip it rather than tearing down
			// a working server.
			continue
		}
		c.dispatch(message)
	}
}

func (c *Conn) dispatch(message rpcResponse) {
	// A message with a method is a request or notification from the remote
	// side; one with only an id is a response to something we sent.
	if message.Method != "" {
		if message.ID == nil {
			c.mu.Lock()
			fn := c.notify[message.Method]
			c.mu.Unlock()
			if fn != nil {
				c.run(func() { fn(message.Params) })
			}
			return
		}
		c.run(func() { c.respond(message) })
		return
	}

	if message.ID == nil {
		return
	}
	id, err := strconv.ParseInt(strings.Trim(string(*message.ID), `"`), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- message
	}
}

// run executes an inbound handler off the read loop, which must keep reading
// so that responses to our own Calls still arrive. Under the default
// concurrent dispatch that is one goroutine per message; under ordered
// dispatch it is an append to the inbox the single worker drains in order.
func (c *Conn) run(fn func()) {
	c.mu.Lock()
	if !c.ordered {
		c.mu.Unlock()
		go fn()
		return
	}
	// Appending is unbounded on purpose: blocking here would stall the read
	// loop, which is the one thing the queue exists to prevent.
	c.inbox = append(c.inbox, fn)
	c.mu.Unlock()
	c.inboxWake.Signal()
}

// drainInbox is the ordered-dispatch worker: it runs queued handlers one at a
// time until the connection closes and the queue is empty.
func (c *Conn) drainInbox() {
	c.mu.Lock()
	for {
		for len(c.inbox) == 0 {
			if c.closed {
				c.mu.Unlock()
				return
			}
			c.inboxWake.Wait()
		}
		fn := c.inbox[0]
		// Drop the reference as well as the slot so a completed handler's
		// captures are not pinned by the backing array.
		c.inbox[0] = nil
		c.inbox = c.inbox[1:]
		c.mu.Unlock()
		fn()
		c.mu.Lock()
	}
}

// respond answers an incoming request. A handler error becomes an error
// response; an unregistered method goes to missingMethod, or gets
// MethodNotFound when no fallback is installed.
func (c *Conn) respond(message rpcResponse) {
	c.mu.Lock()
	fn := c.handlers[message.Method]
	missing := c.missingMethod
	c.mu.Unlock()

	if fn == nil && missing != nil {
		result, err := missing(message.Method)
		c.reply(message.ID, result, nil, err)
		return
	}
	if fn == nil {
		c.reply(message.ID, nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + message.Method}, nil)
		return
	}

	result, herr := fn(message.Params)
	c.reply(message.ID, result, nil, herr)
}

func (c *Conn) reply(id *json.RawMessage, result any, rpcErr *RPCError, err error) {
	if err != nil {
		rpcErr = &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	payload, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
		Error   *RPCError       `json:"error,omitempty"`
	}{JSONRPC: "2.0", ID: *id, Result: result, Error: rpcErr})
	if err != nil {
		return
	}
	c.write(payload)
}

// Call sends a request and waits for its response.
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      rawID(id),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		c.forget(id)
		return err
	}
	if err := c.write(payload); err != nil {
		c.forget(id)
		return err
	}

	select {
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	case response := <-ch:
		if response.Error != nil {
			return response.Error
		}
		if out == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, out)
	}
}

// Notify delivers a notification, which has no reply.
func (c *Conn) Notify(method string, params any) error {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.write(payload)
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func rawID(id int64) *json.RawMessage {
	raw := json.RawMessage(strconv.FormatInt(id, 10))
	return &raw
}

func (c *Conn) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := c.writer.Write(payload)
	return err
}

// readMessage reads one base-protocol frame.
func (c *Conn) readMessage() ([]byte, error) {
	length := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "content-length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("jsonrpc: bad Content-Length: %w", err)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("jsonrpc: message had no Content-Length header")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// Shutdown fails every in-flight call so no caller waits on a dead remote,
// and closes the write end. It is safe to call more than once.
func (c *Conn) Shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.closeErr = true, err
	pending := c.pending
	c.pending = map[int64]chan rpcResponse{}
	c.mu.Unlock()
	// An ordered-dispatch worker parked on an empty inbox only learns the
	// connection is gone by being woken.
	c.inboxWake.Broadcast()

	for _, ch := range pending {
		ch <- rpcResponse{Error: &RPCError{Code: CodeConnectionClose, Message: "connection closed: " + err.Error()}}
	}
	c.writer.Close()
}
