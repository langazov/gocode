// The JSON-RPC transport used to live here. It is now the shared
// internal/jsonrpc package, which serves both the language-server client in
// this package and the markdown language server in internal/mdlsp. These
// aliases keep the existing client call sites untouched.
package lsp

import (
	"io"

	"github.com/langazov/gocode-go/internal/jsonrpc"
)

// conn is a JSON-RPC 2.0 connection over the LSP base protocol.
type conn = jsonrpc.Conn

// handlerFunc answers a server-to-client request.
type handlerFunc = jsonrpc.HandlerFunc

// notifyFunc receives a server-to-client notification.
type notifyFunc = jsonrpc.NotifyFunc

// rpcError is a JSON-RPC error object.
type rpcError = jsonrpc.RPCError

// errConnClosed reports a connection that is shut down.
var errConnClosed = jsonrpc.ErrClosed

func newConn(w io.WriteCloser, r io.Reader) *conn {
	return jsonrpc.NewConn(w, r)
}
