// Package mdlsp implements a Language Server Protocol server for markdown
// documents. It speaks the LSP base protocol over a reader/writer pair through
// the shared internal/jsonrpc transport and serves headings, links,
// completions, renames, formatting and diagnostics from the document model in
// internal/mddoc.
//
// All mutable state — open documents and the workspace index — belongs to one
// actor goroutine. Handlers marshal parameters, ship a closure over the
// actor's mailbox and wait on a reply channel, so no state access needs a
// mutex.
package mdlsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/lspprotocol"
)

// Server is the markdown language server. Construct with New, then Serve.
type Server struct {
	in  io.Reader
	out io.WriteCloser

	// request is the actor's mailbox. It is never closed; shutdown is
	// signaled through stop, which the actor also selects on.
	request chan func(*state) any
	stop    chan struct{}
	// reply is the buffered channel a handler waits on. Kept as a type so
	// call can allocate one channel carrying both value and stop signal.
	stopped chan struct{}
}

// New builds a server reading the protocol from in and writing responses and
// notifications to out.
func New(in io.Reader, out io.WriteCloser) *Server {
	return &Server{
		in:      in,
		out:     out,
		request: make(chan func(*state) any),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Serve runs the server until the client disconnects, the context is
// cancelled, or the exit notification arrives.
func (s *Server) Serve(ctx context.Context) error {
	conn := jsonrpc.NewConn(s.out, s.in)
	notify := func(method string, params any) {
		// Notification writes are best effort; a wedged client pipe must not
		// take the actor down.
		_ = conn.Notify(method, params)
	}
	actorDone := make(chan struct{})
	go func() {
		defer close(actorDone)
		st := &state{notify: notify, docs: map[string]*document{}}
		for {
			select {
			case run := <-s.request:
				run(st)
			case <-s.stop:
				return
			}
		}
	}()

	s.register(conn)

	// exit is the LSP lifecycle terminator: the client sends it after
	// shutdown and expects the process to end promptly, even though its
	// stdin pipe stays open a moment longer.
	var once sync.Once
	exit := make(chan struct{})
	conn.OnNotify("exit", func(json.RawMessage) { once.Do(func() { close(exit) }) })

	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		conn.Listen()
	}()

	select {
	case <-exit:
	case <-listenDone:
	case <-ctx.Done():
	}
	close(s.stop)
	<-actorDone
	close(s.stopped)
	return nil
}

// dispatch ships run to the actor. The returned channel yields run's result.
type replyChan = chan any

// dispatch ships run to the actor. A nil result channel means fire-and-forget.
func (s *Server) dispatch(ctx context.Context, run func(*state) any) (replyChan, error) {
	ch := make(replyChan, 1)
	full := func(st *state) any {
		result := run(st)
		ch <- result
		return nil
	}
	select {
	case s.request <- full:
		return ch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stopped:
		return nil, fmt.Errorf("mdlsp: server stopped")
	}
}

// callSync runs work on the actor and returns its result.
func (s *Server) callSync(ctx context.Context, run func(*state) any) (any, error) {
	ch, err := s.dispatch(ctx, run)
	if err != nil {
		return nil, err
	}
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// post queues work without waiting for it.
func (s *Server) post(run func(*state) any) {
	select {
	case s.request <- run:
	case <-s.stopped:
	}
}

// ---- wire parameter shapes ----

type initializeParams struct {
	RootURI string `json:"rootUri"`
}

type didOpenParams struct {
	TextDocument struct {
		URI        string `json:"uri"`
		LanguageID string `json:"languageId"`
		Version    int    `json:"version"`
		Text       string `json:"text"`
	} `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version int    `json:"version"`
	} `json:"textDocument"`
	ContentChanges []struct {
		Text string `json:"text"`
	} `json:"contentChanges"`
}

// ---- method registration ----

func (s *Server) register(conn *jsonrpc.Conn) {
	// Editors probe servers for capabilities they may or may not implement;
	// a MethodNotFound answer can make them give up, so unknown methods get
	// a null result.
	conn.SetMissingMethod(func(string) (any, error) { return nil, nil })

	conn.Handle("initialize", func(params json.RawMessage) (any, error) {
		var p initializeParams
		json.Unmarshal(params, &p)
		root := ""
		if p.RootURI != "" {
			if path, ok := lspprotocol.PathFromURI(p.RootURI); ok {
				root = path
			}
		}
		if _, err := s.callSync(context.Background(), func(st *state) any {
			st.initialize(root)
			return nil
		}); err != nil {
			return nil, err
		}
		return initializeResult{Capabilities: capabilities()}, nil
	})

	conn.Handle("shutdown", func(json.RawMessage) (any, error) { return nil, nil })

	textDocURI := func(params json.RawMessage) string {
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		json.Unmarshal(params, &p)
		return p.TextDocument.URI
	}

	conn.OnNotify("textDocument/didOpen", func(params json.RawMessage) {
		var p didOpenParams
		json.Unmarshal(params, &p)
		s.post(func(st *state) any { st.didOpen(p); return nil })
	})
	conn.OnNotify("textDocument/didChange", func(params json.RawMessage) {
		var p didChangeParams
		json.Unmarshal(params, &p)
		s.post(func(st *state) any { st.didChange(p); return nil })
	})
	conn.OnNotify("textDocument/didSave", func(params json.RawMessage) {
		uri := textDocURI(params)
		s.post(func(st *state) any { st.didSave(uri); return nil })
	})
	conn.OnNotify("textDocument/didClose", func(params json.RawMessage) {
		uri := textDocURI(params)
		s.post(func(st *state) any { st.didClose(uri); return nil })
	})

	conn.Handle("textDocument/documentSymbol", s.handle(s.documentSymbol))
	conn.Handle("textDocument/foldingRange", s.handle(s.foldingRange))
	conn.Handle("textDocument/definition", s.handle(s.definition))
	conn.Handle("textDocument/references", s.handle(s.references))
	conn.Handle("textDocument/documentLink", s.handle(s.documentLink))
	conn.Handle("textDocument/completion", s.handle(s.completion))
	conn.Handle("textDocument/prepareRename", s.handle(s.prepareRename))
	conn.Handle("textDocument/rename", s.handle(s.rename))
	conn.Handle("textDocument/formatting", s.handle(s.formatting))
	conn.Handle("workspace/symbol", s.handle(s.workspaceSymbol))
}

// handle adapts method handlers to jsonrpc.HandlerFunc, threading the
// request context through.
func (s *Server) handle(fn func(context.Context, json.RawMessage) (any, error)) jsonrpc.HandlerFunc {
	return func(raw json.RawMessage) (any, error) {
		return fn(context.Background(), raw)
	}
}

// capabilities is the static capability set, declared once.
func capabilities() map[string]any {
	return map[string]any{
		"textDocumentSync": map[string]any{
			"openClose": true,
			"change":    lspprotocol.SyncKindFull,
		},
		"documentSymbolProvider":     true,
		"foldingRangeProvider":       true,
		"definitionProvider":         true,
		"referencesProvider":         true,
		"documentLinkProvider":       true,
		"renameProvider":             map[string]any{"prepareProvider": true},
		"workspaceSymbolProvider":    true,
		"documentFormattingProvider": true,
		"completionProvider": map[string]any{
			"triggerCharacters": []string{"[", "(", "#", "/"},
			"resolveProvider":   false,
		},
	}
}

type initializeResult struct {
	Capabilities map[string]any `json:"capabilities"`
}
