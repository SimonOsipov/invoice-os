package platform

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"syscall"
)

// ResetRetryTransport retries a bodyless GET, HEAD or OPTIONS once when its connection is reset, or an HTTP/2
// connection ends in unexpected EOF, before any response arrives, unless NoResetRetry marked it. Any other request is never retried: a reset may
// follow a request the upstream already acted on. A nil base is http.DefaultTransport, resolved now because tests swap it.
func ResetRetryTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &resetRetry{base: base}
}

type noRetryKey struct{}

// NoResetRetry marks a request that must never be re-sent after a reset, such as a GET whose
// handler writes upstream.
func NoResetRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRetryKey{}, true)
}

type resetRetry struct {
	base http.RoundTripper
}

func (t *resetRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	retry := retryable(req)
	resp, err, viaH2 := t.attempt(req, retry)
	if err == nil || !isReset(err, viaH2) || req.Context().Err() != nil {
		return resp, err
	}
	logReset(req, 1, retry, err)
	if !retry {
		return nil, err
	}
	resp, err, viaH2 = t.attempt(req, true)
	if err != nil && isReset(err, viaH2) {
		logReset(req, 2, false, err)
	}
	return resp, err
}

// attempt round-trips once; with probe it also reports whether the connection negotiated h2.
func (t *resetRetry) attempt(req *http.Request, probe bool) (*http.Response, error, bool) {
	if !probe {
		resp, err := t.base.RoundTrip(req)
		return resp, err, false
	}
	var h2 atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if c, ok := i.Conn.(*tls.Conn); ok && c.ConnectionState().NegotiatedProtocol == "h2" {
			h2.Store(true)
		}
	}}
	resp, err := t.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	return resp, err, h2.Load()
}

// isReset: on Linux a client write after the RST consumes the socket error, so the h2 read ends in
// unexpected EOF instead of ECONNRESET. HTTP/1.1 unexpected EOF is a truncated response, not a reset.
func isReset(err error, viaH2 bool) bool {
	return errors.Is(err, syscall.ECONNRESET) || (viaH2 && errors.Is(err, io.ErrUnexpectedEOF))
}

// retryable is true for safe methods with no body, unless NoResetRetry marked the request; a one-shot
// body stream cannot be re-sent.
func retryable(req *http.Request) bool {
	if marked, _ := req.Context().Value(noRetryKey{}).(bool); marked {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return noBody(req.Body)
	}
	return false
}

// logReset omits the path, which can carry ids; the context adds request_id.
func logReset(req *http.Request, attempt int, retrying bool, err error) {
	slog.WarnContext(req.Context(), "upstream connection reset",
		"attempt", attempt, "host", req.URL.Host, "method", req.Method, "retrying", retrying, "err", err)
}

func noBody(b io.ReadCloser) bool {
	return b == nil || b == http.NoBody
}
