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
)

var (
	sharedTransport = buildTransport()
	sharedClient    = &http.Client{Transport: sharedTransport}
)

// buildTransport returns the tuned transport. MaxIdleConnsPerHost is raised
// from Go's default of 2 to 16: HTTP/1.1 endpoints (the local-inference path
// being added) are exactly the ones hurt by a per-host pool of 2, while h2
// providers multiplex over one connection and are unaffected.
func buildTransport() *http.Transport {
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
		ResponseHeaderTimeout: ResponseHeaderTimeout,
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
