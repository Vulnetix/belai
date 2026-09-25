// Package httpclient provides the single shared HTTP client for provider,
// tool, credential and model-catalogue I/O. One shared transport pools
// connections across all of those; a fresh tuned transport is available to
// callers that need a custom DialContext (WebFetch's SSRF address pinning).
//
// There is deliberately no blanket Client.Timeout: that would kill long SSE
// streams. Instead the transport carries a ResponseHeaderTimeout (pre-first-byte
// bound) and the stream path layers its own idle-gap watchdog on top.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

const (
	// DialTimeout bounds TCP+TLS connection establishment.
	DialTimeout = 10 * time.Second
	// ResponseHeaderTimeout bounds the wait for the first response byte. A
	// provider that accepts a connection then stalls can otherwise hang a turn
	// forever without producing an error (and so without a retry).
	ResponseHeaderTimeout = 30 * time.Second
	// StreamIdleTimeout is the SSE idle-gap watchdog: if no bytes arrive for
	// this long mid-stream, the stream is torn down.
	StreamIdleTimeout = 2 * time.Minute
	// KeepAlive keeps pooled connections warm between requests.
	KeepAlive = 30 * time.Second
	// BlockingResponseTimeout bounds a non-streaming model reply. Such a reply
	// sends its headers only once the whole completion is generated, so the
	// 30s ResponseHeaderTimeout measures generation time, not liveness: a
	// reasoning model writing a two-minute answer was cut off at 30s and
	// retried from scratch until the retry budget ran out, stalling sessions
	// for minutes with nothing to show. The blocking client waits this long
	// instead; a stream's liveness is the idle watchdog's job.
	BlockingResponseTimeout = 10 * time.Minute
)

var (
	sharedTransport = buildTransport()
	sharedClient    = &http.Client{Transport: withTracing(sharedTransport)}
	blockingClient  = &http.Client{Transport: withTracing(buildTransportWith(BlockingResponseTimeout))}
)

// buildTransport returns the tuned transport. MaxIdleConnsPerHost is raised
// from Go's default of 2 to 16: HTTP/1.1 endpoints (the local-inference path
// being added) are exactly the ones hurt by a per-host pool of 2, while h2
// providers multiplex over one connection and are unaffected.
func buildTransport() *http.Transport {
	return buildTransportWith(ResponseHeaderTimeout)
}

// buildTransportWith is buildTransport with a chosen pre-first-byte bound.
func buildTransportWith(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   DialTimeout,
			KeepAlive: KeepAlive,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   DialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: headerTimeout,
	}
}

// Default returns the shared client. It is safe for concurrent use and must
// not be mutated by callers.
func Default() *http.Client { return sharedClient }

// Transport returns a fresh copy of the tuned transport for callers that need
// a dedicated one (for example to install a validating DialContext). The copy
// shares no mutable state with the shared client.
func Transport() *http.Transport {
	return buildTransport()
}

// ForBlocking returns the client a non-streaming model request should use.
// The shared client is swapped for its blocking twin, whose header bound is
// BlockingResponseTimeout rather than ResponseHeaderTimeout; any other client
// (a test server's, a caller's own) is returned unchanged.
func ForBlocking(c *http.Client) *http.Client {
	if c == nil || c == sharedClient {
		return blockingClient
	}
	return c
}
