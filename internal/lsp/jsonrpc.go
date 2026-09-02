// Package lsp implements a Language Server Protocol client, the Go port of
// packages/opencode/src/lsp. It spawns language servers, tracks diagnostics,
// and answers the questions the edit/write/read tools ask about a file.
package lsp

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

// conn is a JSON-RPC 2.0 connection over the LSP base protocol: each message
// is a `Content-Length: N` header block, a blank line, then N bytes of JSON.
// It replaces vscode-jsonrpc, which the TypeScript client uses.
type conn struct {
	writer io.WriteCloser
	reader *bufio.Reader

	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan rpcResponse
	handlers map[string]handlerFunc
	notify   map[string]notifyFunc
	closed   bool
	closeErr error
}

// handlerFunc answers a server-to-client request.
type handlerFunc func(params json.RawMessage) (any, error)

// notifyFunc receives a server-to-client notification.
type notifyFunc func(params json.RawMessage)

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
	Error   *rpcError        `json:"error,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("lsp: rpc error %d: %s", e.Code, e.Message)
}

var errConnClosed = errors.New("lsp: connection closed")

func newConn(w io.WriteCloser, r io.Reader) *conn {
	return &conn{
		writer:   w,
		reader:   bufio.NewReaderSize(r, 64*1024),
		pending:  map[int64]chan rpcResponse{},
		handlers: map[string]handlerFunc{},
		notify:   map[string]notifyFunc{},
	}
}

// handle registers a responder for a server-to-client request.
func (c *conn) handle(method string, fn handlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[method] = fn
}

// onNotify registers a listener for a server-to-client notification.
func (c *conn) onNotify(method string, fn notifyFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify[method] = fn
}

// listen reads messages until the stream ends. It runs in its own goroutine
// for the lifetime of the connection.
func (c *conn) listen() {
	for {
		payload, err := c.readMessage()
		if err != nil {
			c.shutdown(err)
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

func (c *conn) dispatch(message rpcResponse) {
	// A message with a method is a request or notification from the server; one
	// with only an id is a response to something we sent.
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

// respond answers a server-to-client request. An unregistered method gets a
// null result rather than an error: servers commonly probe for capabilities
// this client does not implement, and a MethodNotFound can make them give up.
func (c *conn) respond(message rpcResponse) {
	c.mu.Lock()
	fn := c.handlers[message.Method]
	c.mu.Unlock()

	var result any
	if fn != nil {
		value, err := fn(message.Params)
		if err == nil {
			result = value
		}
	}
	payload, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: *message.ID, Result: result})
	if err != nil {
		return
	}
	c.write(payload)
}

// call sends a request and waits for its response.
func (c *conn) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = errConnClosed
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

// send delivers a notification, which has no reply.
func (c *conn) send(method string, params any) error {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.write(payload)
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func rawID(id int64) *json.RawMessage {
	raw := json.RawMessage(strconv.FormatInt(id, 10))
	return &raw
}

func (c *conn) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := c.writer.Write(payload)
	return err
}

// readMessage reads one base-protocol frame.
func (c *conn) readMessage() ([]byte, error) {
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
				return nil, fmt.Errorf("lsp: bad Content-Length: %w", err)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("lsp: message had no Content-Length header")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// shutdown fails every in-flight call so no caller waits on a dead server.
func (c *conn) shutdown(err error) {
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
		ch <- rpcResponse{Error: &rpcError{Code: -32000, Message: "connection closed: " + err.Error()}}
	}
	c.writer.Close()
}
