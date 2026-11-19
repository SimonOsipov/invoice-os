package platform

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// eofH2Srv is an HTTP/2 server that RSTs the first resetFirstN counted requests (all when negative).
// /warm is answered without being counted.
func eofH2Srv(t *testing.T, resetFirstN int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var reqs atomic.Int32
	type connKey struct{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Proto != "HTTP/2.0" {
			t.Errorf("server saw %s, want HTTP/2.0", r.Proto)
		}
		if r.URL.Path == "/warm" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		n := reqs.Add(1)
		if resetFirstN < 0 || n <= resetFirstN {
			// Drain first: a client write after the RST would otherwise consume the socket error.
			_, _ = io.Copy(io.Discard, r.Body)
			if tc, ok := r.Context().Value(connKey{}).(*tls.Conn); ok {
				rst(tc.NetConn().(*net.TCPConn))
			}
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connKey{}, c)
	}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &reqs
}

type faultMode int

const (
	modeEOF   faultMode = iota // a read error becomes io.EOF: the Linux outcome after a client write
	modeReset                  // a read error becomes ECONNRESET
)

type faultConn struct {
	net.Conn
	mode faultMode
}

func (c *faultConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err == nil {
		return n, nil
	}
	if c.mode == modeEOF {
		return n, io.EOF
	}
	return n, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
}

// faultClient returns an h2 transport (no keep-alives) whose nth dial uses modes[min(n, len-1)], and the dial count.
func faultClient(srv *httptest.Server, modes ...faultMode) (*http.Transport, *atomic.Int32) {
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	tr.ForceAttemptHTTP2 = true
	var dials atomic.Int32
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		n := int(dials.Add(1)) - 1
		raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		cfg := tr.TLSClientConfig.Clone()
		cfg.NextProtos = []string{"h2", "http/1.1"}
		cfg.ServerName, _, _ = net.SplitHostPort(addr)
		tc := tls.Client(&faultConn{Conn: raw, mode: modes[min(n, len(modes)-1)]}, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return tc, nil
	}
	return tr, &dials
}

func pinOneProc(t *testing.T) {
	t.Helper()
	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
}

func wantUnexpectedEOF(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
}

func TestResetRetry_H2UnexpectedEOF_FixtureYieldsUnexpectedEOF(t *testing.T) {
	srv, reqs := eofH2Srv(t, 1)
	tr, _ := faultClient(srv, modeEOF)
	_, err := call(t, context.Background(), tr, http.MethodGet, srv.URL, nil)
	wantUnexpectedEOF(t, err)
	if errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("err = %v, want no ECONNRESET", err)
	}
	if reqs.Load() != 1 { // the handler asserts HTTP/2.0, so one request proves h2
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
}

func TestResetRetry_H2UnexpectedEOF_SafeMethodsAreRetried(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(m, func(t *testing.T) {
			srv, reqs := eofH2Srv(t, 1)
			tr, _ := faultClient(srv, modeEOF)
			resp, err := call(t, context.Background(), ResetRetryTransport(tr), m, srv.URL, nil)
			if err != nil {
				t.Fatalf("%s: %v", m, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 || resp.ProtoMajor != 2 || reqs.Load() != 2 {
				t.Errorf("status %d proto %d requests %d; want 200, 2, 2", resp.StatusCode, resp.ProtoMajor, reqs.Load())
			}
		})
	}
}

// The real race, no injection and no warm-up; it passes whichever error attempt 1 gets.
func TestResetRetry_H2UnexpectedEOF_LinuxColdConnRaceIsRetried(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the unexpected EOF outcome of the RST race is Linux-only")
	}
	pinOneProc(t)
	srv, reqs := eofH2Srv(t, 1)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	resp, err := call(t, context.Background(), ResetRetryTransport(tr), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || reqs.Load() != 2 {
		t.Errorf("status %d requests %d; want 200 and 2", resp.StatusCode, reqs.Load())
	}
}

func TestResetRetry_H2UnexpectedEOF_ReusedConnIsRetried(t *testing.T) {
	logs := captureLogs(t)
	srv, reqs := eofH2Srv(t, 1)
	tr, dials := faultClient(srv, modeEOF)
	tr.DisableKeepAlives = false
	t.Cleanup(tr.CloseIdleConnections)
	rt := ResetRetryTransport(tr)

	warm, err := call(t, context.Background(), rt, http.MethodGet, srv.URL+"/warm", nil)
	if err != nil {
		t.Fatalf("warm GET: %v", err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	warm.Body.Close()
	if warm.StatusCode != 200 || warm.ProtoMajor != 2 {
		t.Fatalf("warm: status %d proto %d; want 200, 2", warm.StatusCode, warm.ProtoMajor)
	}

	var mu sync.Mutex
	var got []httptrace.GotConnInfo
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		mu.Lock()
		got = append(got, i)
		mu.Unlock()
	}})
	resp, err := call(t, ctx, rt, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if resp.StatusCode != 200 || reqs.Load() != 2 {
		t.Errorf("status %d requests %d; want 200 and 2", resp.StatusCode, reqs.Load())
	}
	if len(got) == 0 || !got[0].Reused {
		t.Errorf("first attempt GotConn = %+v, want a reused connection", got)
	}
	if dials.Load() != 2 {
		t.Errorf("dials = %d, want 2", dials.Load())
	}
	if len(got) != 2 { // the caller's own trace still fires on both attempts
		t.Errorf("GotConn calls = %d, want 2", len(got))
	}
	recs := logs()
	if len(recs) != 1 || recs[0]["attempt"] != float64(1) || recs[0]["retrying"] != true {
		t.Errorf("records = %v, want one record, attempt 1, retrying true", recs)
	}
}

func TestResetRetry_H2UnexpectedEOF_WritesAreNeverRetried(t *testing.T) {
	cases := []struct {
		method string
		body   bool
	}{
		{http.MethodPost, true}, {http.MethodPost, false}, {http.MethodPut, true},
		{http.MethodPatch, true}, {http.MethodDelete, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/body=%v", c.method, c.body), func(t *testing.T) {
			logs := captureLogs(t)
			srv, reqs := eofH2Srv(t, -1)
			tr, _ := faultClient(srv, modeEOF)
			var body io.Reader
			if c.body {
				body = bytes.NewReader([]byte(`{"a":1}`))
			}
			_, err := call(t, context.Background(), ResetRetryTransport(tr), c.method, srv.URL, body)
			wantUnexpectedEOF(t, err)
			if reqs.Load() != 1 {
				t.Errorf("requests = %d, want 1", reqs.Load())
			}
			if recs := logs(); len(recs) != 0 {
				t.Errorf("records = %v, want none", recs)
			}
		})
	}
}

func TestResetRetry_H2UnexpectedEOF_SafeMethodWithBodyIsNotRetried(t *testing.T) {
	srv, reqs := eofH2Srv(t, -1)
	tr, _ := faultClient(srv, modeEOF)
	_, err := call(t, context.Background(), ResetRetryTransport(tr), http.MethodGet, srv.URL, bytes.NewReader([]byte("x")))
	wantUnexpectedEOF(t, err)
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
}

func TestResetRetry_H2UnexpectedEOF_MarkedGETIsNotRetried(t *testing.T) {
	srv, reqs := eofH2Srv(t, -1)
	tr, _ := faultClient(srv, modeEOF)
	_, err := call(t, NoResetRetry(context.Background()), ResetRetryTransport(tr), http.MethodGet, srv.URL, nil)
	wantUnexpectedEOF(t, err)
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
}

func TestResetRetry_H2SecondFailureReturnsTheError(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(m, func(t *testing.T) { h2SecondFailure(t, m) })
	}
}

func h2SecondFailure(t *testing.T, method string) {
	cases := []struct {
		name  string
		modes []faultMode
	}{
		{"eof,eof", []faultMode{modeEOF, modeEOF}},
		{"reset,eof", []faultMode{modeReset, modeEOF}},
		{"eof,reset", []faultMode{modeEOF, modeReset}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLogs(t)
			srv, reqs := eofH2Srv(t, -1)
			tr, _ := faultClient(srv, c.modes...)
			ctx := WithRequestID(context.Background(), "req-160")
			_, err := call(t, ctx, ResetRetryTransport(tr), method, srv.URL+"/secret/path", nil)
			if c.modes[1] == modeEOF {
				wantUnexpectedEOF(t, err)
			} else if !errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("err = %v, want ECONNRESET", err)
			}
			if reqs.Load() != 2 {
				t.Errorf("requests = %d, want 2", reqs.Load())
			}
			recs := logs()
			if len(recs) != 2 {
				t.Fatalf("records = %v, want 2", recs)
			}
			for i, rec := range recs {
				if rec["request_id"] != "req-160" || rec["attempt"] != float64(i+1) || rec["retrying"] != (i == 0) {
					t.Errorf("record %d = %v", i, rec)
				}
				if strings.Contains(fmt.Sprint(rec), "/secret/path") {
					t.Errorf("record %d carries the path: %v", i, rec)
				}
			}
		})
	}
}

// cancelAfterErr cancels the caller's context once the base fails, as a client hang-up between attempts.
type cancelAfterErr struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (c cancelAfterErr) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if err != nil {
		c.cancel()
	}
	return resp, err
}

func TestResetRetry_H2UnexpectedEOF_CancelledContextIsNotRetried(t *testing.T) {
	logs := captureLogs(t)
	srv, reqs := eofH2Srv(t, 1)
	tr, _ := faultClient(srv, modeEOF)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := call(t, ctx, ResetRetryTransport(cancelAfterErr{tr, cancel}), http.MethodGet, srv.URL, nil)
	wantUnexpectedEOF(t, err)
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
	if recs := logs(); len(recs) != 0 {
		t.Errorf("records = %v, want none", recs)
	}
}

func TestResetRetry_H2UnexpectedEOF_ThenSuccessLogsOnce(t *testing.T) {
	logs := captureLogs(t)
	srv, reqs := eofH2Srv(t, 1)
	tr, _ := faultClient(srv, modeEOF)
	resp, err := call(t, context.Background(), ResetRetryTransport(tr), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || reqs.Load() != 2 {
		t.Errorf("status %d requests %d; want 200 and 2", resp.StatusCode, reqs.Load())
	}
	recs := logs()
	if len(recs) != 1 || recs[0]["attempt"] != float64(1) || recs[0]["retrying"] != true {
		t.Errorf("records = %v, want one record, attempt 1, retrying true", recs)
	}
}

// A response cut off mid-header is a truncated response, not a reset: HTTP/1.1 is sent once.
func TestResetRetry_HTTP1UnexpectedEOFIsNotRetried(t *testing.T) {
	const cut = "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n"
	cases := []struct {
		name  string
		start func(t *testing.T) (url string, reqs *atomic.Int32, tr *http.Transport)
	}{
		{"tcp", func(t *testing.T) (string, *atomic.Int32, *http.Transport) {
			srv := newRawSrv(t, false, func(_ int, c *net.TCPConn, _ *http.Request) {
				_, _ = io.WriteString(c, cut)
				_ = c.Close()
			})
			return srv.url, &srv.reqs, noKeepAlives()
		}},
		{"tls", func(t *testing.T) (string, *atomic.Int32, *http.Transport) {
			var reqs atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reqs.Add(1)
				c, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.WriteString(c, cut)
				_ = c.Close()
			}))
			t.Cleanup(srv.Close)
			tr := srv.Client().Transport.(*http.Transport).Clone()
			tr.DisableKeepAlives = true
			return srv.URL, &reqs, tr
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLogs(t)
			url, reqs, tr := c.start(t)
			_, err := call(t, context.Background(), ResetRetryTransport(tr), http.MethodGet, url, nil)
			wantUnexpectedEOF(t, err)
			if reqs.Load() != 1 {
				t.Errorf("requests = %d, want 1", reqs.Load())
			}
			if recs := logs(); len(recs) != 0 {
				t.Errorf("records = %v, want none", recs)
			}
		})
	}
}

type gotConnTwice struct {
	conns []net.Conn
	calls atomic.Int32
}

func (s *gotConnTwice) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if tr := httptrace.ContextClientTrace(r.Context()); tr != nil {
		for _, c := range s.conns {
			tr.GotConn(httptrace.GotConnInfo{Conn: c})
		}
	}
	return nil, io.ErrUnexpectedEOF
}

// net/http can re-dial inside one RoundTrip; the last conn used decides, so h2 then HTTP/1.1 is not retried.
func TestResetRetry_H2ThenHTTP1ConnUnexpectedEOFIsNotRetried(t *testing.T) {
	srv, _ := eofH2Srv(t, 0)
	h2conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer h2conn.Close()
	if h2conn.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("fixture conn did not negotiate h2")
	}
	plain, other := net.Pipe()
	defer plain.Close()
	defer other.Close()

	base := &gotConnTwice{conns: []net.Conn{h2conn, plain}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err = ResetRetryTransport(base).RoundTrip(req)
	wantUnexpectedEOF(t, err)
	if base.calls.Load() != 1 {
		t.Errorf("base round trips = %d, want 1", base.calls.Load())
	}
}
