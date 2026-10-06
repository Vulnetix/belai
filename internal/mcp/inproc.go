package mcp

import (
	"context"
	"io"

	"github.com/vulnetix/belai/internal/jsonrpc"
)

// BuiltinTransport is the transport name Status reports for an in-process
// server.
const BuiltinTransport = "builtin"

// inprocTransport talks to a server that runs inside this process. Both ends
// are real jsonrpc.Conns joined by pipes, so the server sees exactly the
// requests a stdio server would, and the Client above it is unchanged.
type inprocTransport struct {
	client *jsonrpc.Conn
	server *jsonrpc.Conn
	pipes  []io.Closer
}

// newInproc starts handler as an MCP server on one end of a pipe pair and
// returns the transport for the other end.
func newInproc(handler jsonrpc.Handler) *inprocTransport {
	toServerR, toServerW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	return &inprocTransport{
		server: jsonrpc.NewConn(toServerR, toClientW, handler),
		client: jsonrpc.NewConn(toClientR, toServerW, serverRequests),
		pipes:  []io.Closer{toServerR, toServerW, toClientR, toClientW},
	}
}

func (t *inprocTransport) call(ctx context.Context, method string, params, result any) error {
	return t.client.Call(ctx, method, params, result)
}

func (t *inprocTransport) notify(_ context.Context, method string, params any) error {
	return t.client.Notify(method, params)
}

func (t *inprocTransport) close() error {
	t.client.Close()
	t.server.Close()
	for _, p := range t.pipes {
		_ = p.Close()
	}
	return nil
}

func (t *inprocTransport) diag() string { return "" }
