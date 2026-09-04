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
}

// NewConn builds a connection that writes to w and reads from r.
func NewConn(w io.WriteCloser, r io.Reader) *Conn {
	return &Conn{
		writer:   w,
		reader:   bufio.NewReaderSize(r, 64*1024),
		pending:  map[int64]chan rpcResponse{},
		handlers: map[string]HandlerFunc{},
		notify:   map[string]NotifyFunc{},
	}
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
				// Serve notifications on their own goroutine: a handler that
				// blocks must not stall the read loop.
				go fn(message.Params)
			}
			return
		}
		go c.respond(message)
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

	for _, ch := range pending {
		ch <- rpcResponse{Error: &RPCError{Code: CodeConnectionClose, Message: "connection closed: " + err.Error()}}
	}
	c.writer.Close()
}
