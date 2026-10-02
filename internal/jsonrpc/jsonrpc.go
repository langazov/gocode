// Package jsonrpc implements a JSON-RPC 2.0 connection. NewConn frames
// messages with the LSP base protocol: each message is a `Content-Length: N`
// header block, a blank line, then N bytes of JSON. NewLineConn frames them as
// newline-delimited JSON instead, one message (or batch array) per line, which
// is the Agent Client Protocol's stdio transport.
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
	"bytes"
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
	CodeParseError      = -32700
	CodeInvalidRequest  = -32600
	CodeMethodNotFound  = -32601
	CodeInvalidParams   = -32602
	CodeInternalError   = -32603
	CodeConnectionClose = -32000
	// CodeRequestCancelled answers a request whose handler was cancelled,
	// whether by the remote side (see SetCancelRequest) or by shutdown.
	CodeRequestCancelled = -32800
)

// ErrClosed is returned by Call once the connection has been shut down.
var ErrClosed = errors.New("jsonrpc: connection closed")

// RPCError is a JSON-RPC error object, returned by Call when the remote side
// answers with an error instead of a result.
//
// A handler may also return one: its code and data then reach the remote side
// as given, instead of being flattened into CodeInternalError.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc: rpc error %d: %s", e.Code, e.Message)
}

// HandlerFunc answers an incoming request. A returned error becomes an error
// response carrying CodeInternalError.
type HandlerFunc func(params json.RawMessage) (any, error)

// ContextHandlerFunc answers an incoming request with a context that is
// cancelled when the remote side cancels the request (see SetCancelRequest)
// or the connection shuts down. A handler that returns an error after its
// context was cancelled is answered with CodeRequestCancelled.
type ContextHandlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

// Deferred is a handler result whose value is not ready yet. A handler that
// returns one is finished as far as dispatch is concerned — under ordered
// dispatch the next message is processed immediately — and the reply is sent
// when Wait returns, from a goroutine of its own. It exists for requests that
// legitimately stay open for a long time (an ACP v1 session/prompt lasts a
// whole turn) on a connection that otherwise needs arrival-order processing.
type Deferred func() (any, error)

// Then is a handler result with a follow-up: Result is sent as the reply, and
// After runs once the reply has been written. It exists for protocols where a
// notification must follow a response — ACP's available_commands_update may
// only arrive once the client knows the session id the response carries.
type Then struct {
	Result any
	After  func()
}

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
	// lines selects newline-delimited framing (NewLineConn) over the
	// Content-Length base protocol. Batches are only accepted in this mode:
	// the LSP base protocol has none.
	lines bool

	// ctx is the parent of every ContextHandlerFunc's context; stop cancels
	// it on Shutdown so no handler outlives the connection.
	ctx  context.Context
	stop context.CancelFunc

	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan rpcResponse
	handlers map[string]HandlerFunc
	ctxFuncs map[string]ContextHandlerFunc
	notify   map[string]NotifyFunc
	// inflight maps the raw id of each running context handler to its
	// cancel, so a cancellation notification can reach it.
	inflight map[string]context.CancelFunc

	// cancelMethod and cancelParam name the notification that cancels a
	// request, in both directions: inbound it cancels a running context
	// handler, outbound Call sends it when its own context ends. Empty
	// disables both. See SetCancelRequest.
	cancelMethod string
	cancelParam  string
	closed       bool
	closeErr     error

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

	// drainOnEOF, when set, makes a read loop that reaches the end of its
	// input finish the handlers already running before the connection shuts
	// down, so a peer that writes its requests and closes its end still gets
	// every reply. onEOF runs first, so the owner can cut long-running work
	// short. See SetDrainOnEOF.
	drainOnEOF bool
	onEOF      func()
	// readDone is set once the input has ended: an outbound call made after
	// that could never be answered, so Call refuses it.
	readDone bool
	running  sync.WaitGroup
}

// NewConn builds a connection that writes to w and reads from r.
func NewConn(w io.WriteCloser, r io.Reader) *Conn {
	ctx, stop := context.WithCancel(context.Background())
	c := &Conn{
		writer:   w,
		reader:   bufio.NewReaderSize(r, 64*1024),
		ctx:      ctx,
		stop:     stop,
		pending:  map[int64]chan rpcResponse{},
		handlers: map[string]HandlerFunc{},
		ctxFuncs: map[string]ContextHandlerFunc{},
		notify:   map[string]NotifyFunc{},
		inflight: map[string]context.CancelFunc{},
	}
	c.inboxWake = sync.NewCond(&c.mu)
	return c
}

// NewLineConn builds a connection framed as newline-delimited JSON: every
// message is one line, and a line may also carry a JSON-RPC batch array. This
// is the ACP stdio transport (agentclientprotocol.com/protocol/v1/transports).
// encoding/json never emits a raw newline inside a value, so a marshalled
// message is always a single line.
func NewLineConn(w io.WriteCloser, r io.Reader) *Conn {
	c := NewConn(w, r)
	c.lines = true
	return c
}

// SetCancelRequest names the notification that cancels an in-flight request
// and the params field that carries the cancelled request's id: ACP uses
// ("$/cancel_request", "requestId"), LSP ("$/cancelRequest", "id"). Inbound,
// it cancels the matching context handler; outbound, Call sends it when its
// own context ends before the response arrives. Call it before Listen.
func (c *Conn) SetCancelRequest(method, param string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelMethod, c.cancelParam = method, param
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

// SetDrainOnEOF makes the end of the input wait for in-flight handlers
// before shutting the connection down, calling onEOF (when non-nil) first.
// Call it before Listen. Only concurrent dispatch is affected; ordered
// dispatch already drains its queue.
func (c *Conn) SetDrainOnEOF(onEOF func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainOnEOF, c.onEOF = true, onEOF
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

// HandleContext registers a cancellable responder for an incoming request.
func (c *Conn) HandleContext(method string, fn ContextHandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ctxFuncs[method] = fn
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
			c.mu.Lock()
			drain, onEOF := c.drainOnEOF, c.onEOF
			c.readDone = true
			c.mu.Unlock()
			if drain {
				// Nothing can answer our own outbound calls any more; fail
				// them first, or a handler waiting on one would never finish
				// and the drain would never end.
				c.failPending(err)
				if onEOF != nil {
					onEOF()
				}
				c.running.Wait()
			}
			c.Shutdown(err)
			return
		}
		if c.lines {
			trimmed := bytes.TrimSpace(payload)
			if len(trimmed) == 0 {
				continue
			}
			if trimmed[0] == '[' {
				c.dispatchBatch(trimmed)
				continue
			}
		}
		var message rpcResponse
		if err := json.Unmarshal(payload, &message); err != nil {
			// A malformed frame is not fatal: skip it rather than tearing down
			// a working server.
			continue
		}
		c.dispatch(message, c.write)
	}
}

// dispatchBatch handles a JSON-RPC batch: every entry is dispatched as if it
// had arrived alone, and the replies to its requests go back together as one
// array once the last of them is ready. Notifications and responses in the
// batch get no reply; an entry that is not a request object gets its own
// CodeInvalidRequest error, and so does an empty batch, as the JSON-RPC 2.0
// batch rules require.
func (c *Conn) dispatchBatch(payload []byte) {
	var entries []json.RawMessage
	if err := json.Unmarshal(payload, &entries); err != nil {
		c.write(errorPayload(nil, &RPCError{Code: CodeParseError, Message: "parse error: " + err.Error()}))
		return
	}
	if len(entries) == 0 {
		c.write(errorPayload(nil, &RPCError{Code: CodeInvalidRequest, Message: "invalid request: empty batch"}))
		return
	}

	batch := &batchReplies{conn: c}
	var messages []rpcResponse
	for _, entry := range entries {
		var message rpcResponse
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(entry, &message) != nil ||
			(message.Method == "" && message.ID == nil) {
			batch.fixed = append(batch.fixed, errorPayload(nil, &RPCError{Code: CodeInvalidRequest, Message: "invalid request"}))
			continue
		}
		if message.Method != "" && message.ID != nil {
			batch.expected++
		}
		messages = append(messages, message)
	}
	batch.remaining = batch.expected
	if batch.expected == 0 {
		batch.flush()
	}
	for _, message := range messages {
		c.dispatch(message, batch.add)
	}
}

// batchReplies collects the replies to one batch's requests.
type batchReplies struct {
	conn      *Conn
	mu        sync.Mutex
	fixed     [][]byte
	replies   [][]byte
	expected  int
	remaining int
}

func (b *batchReplies) add(payload []byte) error {
	b.mu.Lock()
	b.replies = append(b.replies, payload)
	b.remaining--
	done := b.remaining == 0
	b.mu.Unlock()
	if done {
		b.flush()
	}
	return nil
}

func (b *batchReplies) flush() {
	all := append(append([][]byte{}, b.fixed...), b.replies...)
	if len(all) == 0 {
		return
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, reply := range all {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(reply)
	}
	buf.WriteByte(']')
	b.conn.write(buf.Bytes())
}

func errorPayload(id *json.RawMessage, rpcErr *RPCError) []byte {
	raw := json.RawMessage("null")
	if id != nil {
		raw = *id
	}
	payload, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *RPCError       `json:"error"`
	}{JSONRPC: "2.0", ID: raw, Error: rpcErr})
	return payload
}

// dispatch routes one message. send is where a reply to a request goes: the
// connection itself, or a batch collecting its entries' replies.
func (c *Conn) dispatch(message rpcResponse, send func([]byte) error) {
	// A message with a method is a request or notification from the remote
	// side; one with only an id is a response to something we sent.
	if message.Method != "" {
		if message.ID == nil {
			c.mu.Lock()
			fn := c.notify[message.Method]
			cancelMethod := c.cancelMethod
			c.mu.Unlock()
			// Cancellation is handled on the read loop, not queued: under
			// ordered dispatch it would otherwise sit behind the very handler
			// it is meant to interrupt.
			if cancelMethod != "" && message.Method == cancelMethod {
				c.cancelInflight(message.Params)
				if fn == nil {
					return
				}
			}
			if fn != nil {
				c.run(func() { fn(message.Params) })
			}
			return
		}
		c.run(func() { c.respond(message, send) })
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
	c.running.Add(1)
	tracked := func() {
		defer c.running.Done()
		fn()
	}
	if !c.ordered {
		c.mu.Unlock()
		go tracked()
		return
	}
	// Appending is unbounded on purpose: blocking here would stall the read
	// loop, which is the one thing the queue exists to prevent.
	c.inbox = append(c.inbox, tracked)
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
func (c *Conn) respond(message rpcResponse, send func([]byte) error) {
	c.mu.Lock()
	fn := c.handlers[message.Method]
	ctxFn := c.ctxFuncs[message.Method]
	missing := c.missingMethod
	c.mu.Unlock()

	if ctxFn != nil {
		key := string(*message.ID)
		ctx, cancel := context.WithCancel(c.ctx)
		c.mu.Lock()
		c.inflight[key] = cancel
		c.mu.Unlock()
		settle := func(result any, herr error) {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			cancelled := ctx.Err() != nil
			cancel()
			if herr != nil && cancelled {
				var rpcErr *RPCError
				if !errors.As(herr, &rpcErr) {
					herr = &RPCError{Code: CodeRequestCancelled, Message: "request cancelled"}
				}
			}
			var after func()
			if then, ok := result.(Then); ok {
				result, after = then.Result, then.After
			}
			c.reply(send, message.ID, result, nil, herr)
			if after != nil && herr == nil {
				after()
			}
		}
		result, herr := ctxFn(ctx, message.Params)
		if later, ok := result.(Deferred); ok && herr == nil {
			c.running.Add(1)
			go func() {
				defer c.running.Done()
				settle(later())
			}()
			return
		}
		settle(result, herr)
		return
	}
	if fn == nil && missing != nil {
		result, err := missing(message.Method)
		c.reply(send, message.ID, result, nil, err)
		return
	}
	if fn == nil {
		c.reply(send, message.ID, nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + message.Method}, nil)
		return
	}

	result, herr := fn(message.Params)
	c.reply(send, message.ID, result, nil, herr)
}

// cancelInflight cancels the context handler named by a cancellation
// notification's params. An unknown or finished id is ignored: the response
// may already be on its way, which the protocol allows.
func (c *Conn) cancelInflight(params json.RawMessage) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return
	}
	c.mu.Lock()
	id, ok := fields[c.cancelParam]
	var cancel context.CancelFunc
	if ok {
		cancel = c.inflight[string(bytes.TrimSpace(id))]
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Conn) reply(send func([]byte) error, id *json.RawMessage, result any, rpcErr *RPCError, err error) {
	if err != nil {
		if !errors.As(err, &rpcErr) {
			rpcErr = &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
	}
	var payload []byte
	if rpcErr != nil {
		payload = errorPayload(id, rpcErr)
	} else {
		payload, err = json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{JSONRPC: "2.0", ID: *id, Result: result})
		if err != nil {
			payload = errorPayload(id, &RPCError{Code: CodeInternalError, Message: err.Error()})
		}
	}
	send(payload)
}

// Call sends a request and waits for its response.
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.readDone && !c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
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
		c.mu.Lock()
		method, param := c.cancelMethod, c.cancelParam
		c.mu.Unlock()
		if method != "" {
			_ = c.Notify(method, map[string]any{param: id})
		}
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
	if c.lines {
		line := make([]byte, 0, len(payload)+1)
		line = append(append(line, payload...), '\n')
		_, err := c.writer.Write(line)
		return err
	}
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := c.writer.Write(payload)
	return err
}

// readMessage reads one base-protocol frame.
func (c *Conn) readMessage() ([]byte, error) {
	if c.lines {
		// ReadBytes grows past the buffer size, so a large message (an image
		// prompt, say) is read whole rather than truncated.
		line, err := c.reader.ReadBytes('\n')
		if err != nil && len(bytes.TrimSpace(line)) > 0 && errors.Is(err, io.EOF) {
			// A final message without a trailing newline still counts.
			return line, nil
		}
		return line, err
	}
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
	c.stop()
	c.mu.Unlock()
	// An ordered-dispatch worker parked on an empty inbox only learns the
	// connection is gone by being woken.
	c.inboxWake.Broadcast()
	c.failPending(err)
	c.writer.Close()
}

// failPending fails every outbound call still waiting for a response.
func (c *Conn) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = map[int64]chan rpcResponse{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResponse{Error: &RPCError{Code: CodeConnectionClose, Message: "connection closed: " + err.Error()}}
	}
}
