package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func pair(ha, hb Handler) (*Conn, *Conn) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return NewConn(ar, aw, ha), NewConn(br, bw, hb)
}

func TestCallRoundTrip(t *testing.T) {
	_, b := pair(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		var p struct{ N int }
		json.Unmarshal(params, &p)
		return map[string]int{"double": p.N * 2}, nil
	}, nil)
	var out struct{ Double int }
	if err := b.Call(context.Background(), "double", map[string]int{"n": 21}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Double != 42 {
		t.Fatalf("out = %+v", out)
	}
}

func TestErrorCodes(t *testing.T) {
	_, b := pair(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "bad" {
			return nil, Errorf(CodeInvalidParams, "nope")
		}
		return nil, errors.New("boom")
	}, nil)
	var rpcErr *Error
	if err := b.Call(context.Background(), "bad", nil, nil); !errors.As(err, &rpcErr) || rpcErr.Code != CodeInvalidParams {
		t.Fatalf("err = %v", err)
	}
	if err := b.Call(context.Background(), "other", nil, nil); !errors.As(err, &rpcErr) || rpcErr.Code != CodeInternalError {
		t.Fatalf("err = %v", err)
	}
	_, c := pair(nil, nil)
	if err := c.Call(context.Background(), "x", nil, nil); !errors.As(err, &rpcErr) || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("err = %v", err)
	}
}

// A slow handler does not block other requests on the same connection.
func TestConcurrentDispatch(t *testing.T) {
	release := make(chan struct{})
	_, b := pair(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "slow" {
			<-release
		}
		return method, nil
	}, nil)
	slowDone := make(chan error, 1)
	go func() { slowDone <- b.Call(context.Background(), "slow", nil, nil) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got string
	if err := b.Call(ctx, "fast", nil, &got); err != nil || got != "fast" {
		t.Fatalf("fast call blocked behind slow: %v %q", err, got)
	}
	close(release)
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
}

func TestNotifyAndClose(t *testing.T) {
	got := make(chan string, 1)
	a, b := pair(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		got <- method
		return nil, nil
	}, nil)
	if err := b.Notify("ping", nil); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m != "ping" {
		t.Fatalf("method = %q", m)
	}
	a.Close()
	b.Close()
	if err := b.Call(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("call on a closed conn succeeded")
	}
}

func TestCallCancelled(t *testing.T) {
	_, b := pair(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, nil
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Call(ctx, "hang", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
