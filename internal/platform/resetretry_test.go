package platform

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// rawSrv is a TCP server that parses one request per connection and lets the test decide the answer.
type rawSrv struct {
	ln    net.Listener
	conns atomic.Int32
	reqs  atomic.Int32
	url   string
}

// newRawSrv calls handle(n, conn, req) for the nth connection (1-based). With preSend the server reads
// one chunk without parsing it (a TLS ClientHello) and passes a nil request.
func newRawSrv(t *testing.T, preSend bool, handle func(n int, c *net.TCPConn, req *http.Request)) *rawSrv {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawSrv{ln: ln, url: "http://" + ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := int(s.conns.Add(1))
			go func() {
				tc := c.(*net.TCPConn)
				if preSend {
					_, _ = tc.Read(make([]byte, 1024))
					handle(n, tc, nil)
					return
				}
				req, err := http.ReadRequest(bufio.NewReader(tc))
				if err != nil {
					_ = tc.Close()
					return
				}
				s.reqs.Add(1)
				handle(n, tc, req)
			}()
		}
	}()
	return s
}

func rst(c *net.TCPConn) {
	_ = c.SetLinger(0)
	_ = c.Close()
}

func reply(c *net.TCPConn, req *http.Request, status, body string) {
	fmt.Fprintf(c, "HTTP/1.1 %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", status, len(body))
	if req.Method != http.MethodHead {
		_, _ = io.WriteString(c, body)
	}
	_ = c.Close()
}

// resetFirst resets the first n connections after the request is read, then answers 200.
func resetFirst(n int) func(int, *net.TCPConn, *http.Request) {
	return func(i int, c *net.TCPConn, req *http.Request) {
		if i <= n {
			rst(c)
			return
		}
		reply(c, req, "200 OK", "ok")
	}
}

func resetAlways(_ int, c *net.TCPConn, _ *http.Request) { rst(c) }

func noKeepAlives() *http.Transport {
	return &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

func call(t *testing.T, ctx context.Context, rt http.RoundTripper, method, url string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return rt.RoundTrip(req)
}

func wantReset(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("err = %v, want a connection reset", err)
	}
}

func TestResetRetry_GETResetAfterRequestSentIsRetried(t *testing.T) {
	srv := newRawSrv(t, false, resetFirst(1))
	rt := ResetRetryTransport(noKeepAlives())
	resp, err := call(t, context.Background(), rt, http.MethodGet, srv.url, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || srv.reqs.Load() != 2 {
		t.Errorf("status %d, requests %d; want 200 and 2", resp.StatusCode, srv.reqs.Load())
	}
}

func TestResetRetry_HEADAndOPTIONSAreRetried(t *testing.T) {
	for _, m := range []string{http.MethodHead, http.MethodOptions} {
		srv := newRawSrv(t, false, resetFirst(1))
		resp, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), m, srv.url, nil)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || srv.reqs.Load() != 2 {
			t.Errorf("%s: status %d, requests %d; want 200 and 2", m, resp.StatusCode, srv.reqs.Load())
		}
	}
}

// h2Srv resets the raw connection on the first request of each test and records the protocol.
func h2Srv(t *testing.T, resetFirstN int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var reqs atomic.Int32
	type connKey struct{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Proto != "HTTP/2.0" {
			t.Errorf("server saw %s, want HTTP/2.0", r.Proto)
		}
		if reqs.Add(1) <= resetFirstN {
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

func h2Client(srv *httptest.Server) *http.Transport {
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return tr
}

func TestResetRetry_GETResetOverHTTP2IsRetried(t *testing.T) {
	srv, reqs := h2Srv(t, 1)
	resp, err := call(t, context.Background(), ResetRetryTransport(h2Client(srv)), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 || reqs.Load() != 2 {
		t.Errorf("status %d proto %d requests %d; want 200, 2, 2", resp.StatusCode, resp.ProtoMajor, reqs.Load())
	}
}

func TestResetRetry_UnsafeMethodsAreNeverRetried(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, preSend := range []bool{false, true} {
			for _, withBody := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/preSend=%v/body=%v", m, preSend, withBody), func(t *testing.T) {
					srv := newRawSrv(t, preSend, resetAlways)
					url := srv.url
					if preSend {
						url = "https://" + srv.ln.Addr().String()
					}
					var body io.Reader
					if withBody {
						body = bytes.NewReader([]byte(`{"a":1}`))
					}
					_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), m, url, body)
					wantReset(t, err)
					if c, r := srv.conns.Load(), srv.reqs.Load(); c != 1 || r > 1 {
						t.Errorf("connections %d, requests %d; want 1 and at most 1", c, r)
					}
				})
			}
		}
	}
}

func TestResetRetry_UnsafeMethodWithIdempotencyKeyIsNotRetried(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut} {
		srv := newRawSrv(t, false, resetAlways)
		req, _ := http.NewRequest(m, srv.url, strings.NewReader("{}"))
		req.Header.Set("Idempotency-Key", "k1")
		_, err := ResetRetryTransport(noKeepAlives()).RoundTrip(req)
		wantReset(t, err)
		if srv.reqs.Load() != 1 {
			t.Errorf("%s: requests = %d, want 1", m, srv.reqs.Load())
		}
	}
}

func TestResetRetry_POSTResetOverHTTP2IsNotRetried(t *testing.T) {
	srv, reqs := h2Srv(t, 1)
	_, err := call(t, context.Background(), ResetRetryTransport(h2Client(srv)), http.MethodPost, srv.URL, strings.NewReader("{}"))
	wantReset(t, err)
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
}

func TestResetRetry_GoTrueShapedPOSTsAreNeverRetried(t *testing.T) {
	paths := []string{"/token?grant_type=password", "/token?grant_type=refresh_token", "/verify", "/signup"}
	for _, preSend := range []bool{false, true} {
		for _, p := range paths {
			srv := newRawSrv(t, preSend, resetAlways)
			base := srv.url
			if preSend {
				base = "https://" + srv.ln.Addr().String()
			}
			_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodPost, base+p, strings.NewReader(`{"email":"a@b.c"}`))
			wantReset(t, err)
			if c := srv.conns.Load(); c != 1 {
				t.Errorf("POST %s preSend=%v: connections = %d, want 1", p, preSend, c)
			}
		}
	}
}

func TestResetRetry_SafeMethodWithBodyIsNotRetried(t *testing.T) {
	srv := newRawSrv(t, false, resetAlways)
	_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, bytes.NewReader([]byte("x")))
	wantReset(t, err)
	if srv.reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", srv.reqs.Load())
	}
}

func TestResetRetry_SecondResetReturnsTheError(t *testing.T) {
	srv := newRawSrv(t, false, resetAlways)
	_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
	wantReset(t, err)
	if srv.reqs.Load() != 2 {
		t.Errorf("requests = %d, want 2", srv.reqs.Load())
	}
}

// captureLogs sends the default slog logger to a buffer of JSON records.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevOut := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()
	prev := slog.Default()
	slog.SetDefault(newLogger(Config{Service: "test", Environment: "test", LogLevel: "debug"}))
	t.Cleanup(func() { os.Stdout = prevOut; slog.SetDefault(prev) })
	return func() []map[string]any {
		os.Stdout = prevOut
		_ = w.Close()
		<-done
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "upstream connection reset" {
				out = append(out, rec)
			}
		}
		return out
	}
}

func TestResetRetry_LogsEveryAttemptWithRequestID(t *testing.T) {
	logs := captureLogs(t)
	srv := newRawSrv(t, false, resetAlways)
	ctx := WithRequestID(context.Background(), "req-150")
	_, err := call(t, ctx, ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url+"/secret/path", nil)
	wantReset(t, err)
	recs := logs()
	if len(recs) != 2 {
		t.Fatalf("records = %v, want 2", recs)
	}
	for i, rec := range recs {
		if rec["level"] != "WARN" || rec["request_id"] != "req-150" || rec["attempt"] != float64(i+1) || rec["retrying"] != (i == 0) {
			t.Errorf("record %d = %v", i, rec)
		}
		if _, ok := rec["request_sent"]; ok || strings.Contains(fmt.Sprint(rec), "/secret/path") {
			t.Errorf("record %d carries request_sent or the path: %v", i, rec)
		}
	}
}

func TestResetRetry_NonRetriedResetLogsOnce(t *testing.T) {
	logs := captureLogs(t)
	srv := newRawSrv(t, false, resetAlways)
	_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodPost, srv.url, strings.NewReader("{}"))
	wantReset(t, err)
	recs := logs()
	if len(recs) != 1 || recs[0]["attempt"] != float64(1) || recs[0]["retrying"] != false || recs[0]["method"] != "POST" {
		t.Errorf("records = %v, want one POST record, attempt 1, retrying false", recs)
	}
}

func TestResetRetry_SecondAttemptNonResetErrorIsReturned(t *testing.T) {
	logs := captureLogs(t)
	var srv *rawSrv
	srv = newRawSrv(t, false, func(_ int, c *net.TCPConn, _ *http.Request) {
		_ = srv.ln.Close()
		rst(c)
	})
	_, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want connection refused", err)
	}
	recs := logs()
	if len(recs) != 1 || recs[0]["attempt"] != float64(1) || recs[0]["retrying"] != true {
		t.Errorf("records = %v, want one record, attempt 1, retrying true", recs)
	}
}

func TestResetRetry_OtherFailuresAreNotRetried(t *testing.T) {
	t.Run("503", func(t *testing.T) {
		srv := newRawSrv(t, false, func(_ int, c *net.TCPConn, req *http.Request) { reply(c, req, "503 Service Unavailable", "no") })
		resp, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 503 || srv.reqs.Load() != 1 {
			t.Errorf("status %d requests %d; want 503 and 1", resp.StatusCode, srv.reqs.Load())
		}
	})
	t.Run("refused", func(t *testing.T) {
		var dials atomic.Int32
		tr := noKeepAlives()
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		_ = ln.Close()
		_, err := call(t, context.Background(), ResetRetryTransport(tr), http.MethodGet, "http://"+addr, nil)
		if err == nil || !strings.Contains(err.Error(), "connection refused") || dials.Load() != 1 {
			t.Errorf("err = %v, dials = %d; want connection refused after 1 dial", err, dials.Load())
		}
	})
	t.Run("cancelled before the call", func(t *testing.T) {
		srv := newRawSrv(t, false, resetAlways)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := call(t, ctx, ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
		if !errors.Is(err, context.Canceled) || srv.reqs.Load() > 1 {
			t.Errorf("err = %v, requests = %d; want canceled and at most 1", err, srv.reqs.Load())
		}
	})
}

func TestResetRetry_CancelDuringFirstAttemptIsNotRetried(t *testing.T) {
	release := make(chan struct{})
	srv := newRawSrv(t, false, func(_ int, c *net.TCPConn, _ *http.Request) {
		<-release
		rst(c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := call(t, ctx, ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
		done <- err
	}()
	for srv.reqs.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("want an error")
	}
	time.Sleep(50 * time.Millisecond)
	if srv.reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", srv.reqs.Load())
	}
}

func TestResetRetry_ResetAfterResponseHeadersIsNotRetried(t *testing.T) {
	srv := newRawSrv(t, false, func(_ int, c *net.TCPConn, _ *http.Request) {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		time.Sleep(20 * time.Millisecond)
		rst(c)
	})
	resp, err := call(t, context.Background(), ResetRetryTransport(noKeepAlives()), http.MethodGet, srv.url, nil)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("body read succeeded, want a reset")
	}
	if srv.reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", srv.reqs.Load())
	}
}

type countingRT struct{ calls atomic.Int32 }

func (c *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
}

func TestResetRetry_NilBaseResolvesDefaultTransportAtConstruction(t *testing.T) {
	prev := http.DefaultTransport
	stub := &countingRT{}
	http.DefaultTransport = stub
	rt := ResetRetryTransport(nil)
	http.DefaultTransport = prev
	if _, err := call(t, context.Background(), rt, http.MethodGet, "http://example.invalid/", nil); err != nil {
		t.Fatal(err)
	}
	if stub.calls.Load() != 1 {
		t.Errorf("stub calls = %d, want 1", stub.calls.Load())
	}
}

func TestResetRetry_IDTokenFetchIsRetried(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	md := newRawSrv(t, false, func(n int, c *net.TCPConn, req *http.Request) {
		if n == 1 {
			rst(c)
			return
		}
		reply(c, req, "200 OK", fakeJWT(now.Add(time.Hour)))
	})
	retry := ResetRetryTransport(noKeepAlives())
	stub := &countingRT{}
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), md.url) {
			return retry.RoundTrip(req)
		}
		return stub.RoundTrip(req)
	})
	tr := newIDTokenTransport(base, md.url, func() time.Time { return now })
	resp, err := call(t, context.Background(), tr, http.MethodGet, "https://svc-abc-uc.a.run.app/v1/x", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	resp.Body.Close()
	if md.reqs.Load() != 2 || stub.calls.Load() != 1 {
		t.Errorf("metadata requests %d, upstream calls %d; want 2 and 1", md.reqs.Load(), stub.calls.Load())
	}
}

type resetRT struct{ calls atomic.Int32 }

func (r *resetRT) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, &net.OpError{Op: "read", Err: syscall.ECONNRESET}
}

// A cancelled caller must not get a second attempt even when the base reports a reset (the real
// transport usually reports the context error instead, which hides the guard).
func TestResetRetry_CancelledContextWithResetIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := &resetRT{}
	_, err := call(t, ctx, ResetRetryTransport(base), http.MethodGet, "http://example.invalid/", nil)
	if !errors.Is(err, syscall.ECONNRESET) || base.calls.Load() != 1 {
		t.Errorf("err = %v, calls = %d; want the reset and 1 call", err, base.calls.Load())
	}
}
