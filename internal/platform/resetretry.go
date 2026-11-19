package platform

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"syscall"
)

// ResetRetryTransport retries a bodyless GET, HEAD or OPTIONS once when its connection is reset before
// any response arrives. Any other request is never retried: a reset may follow a request the upstream
// already acted on. A nil base is http.DefaultTransport, resolved now because tests swap it.
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
	resp, err := t.base.RoundTrip(req)
	if err == nil || !errors.Is(err, syscall.ECONNRESET) || req.Context().Err() != nil {
		return resp, err
	}
	retry := retryable(req)
	logReset(req, 1, retry, err)
	if !retry {
		return nil, err
	}
	resp, err = t.base.RoundTrip(req)
	if err != nil && errors.Is(err, syscall.ECONNRESET) {
		logReset(req, 2, false, err)
	}
	return resp, err
}

// retryable is true for safe methods with no body; a one-shot body stream cannot be re-sent.
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
