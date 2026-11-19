package gateway

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// resetUpstream is a raw TCP upstream that RSTs connections, so the proxy sees a Go-visible
// "connection reset by peer" like the Google-side resets it cannot reproduce.
type resetUpstream struct {
	url       *url.URL
	mu        sync.Mutex
	conns     int
	requests  int
	headers   []http.Header // read through requestHeaders
	resetUpTo int           // connections 1..resetUpTo are reset; later ones answer 200 "ok"
	readFirst bool          // read the request (and body) before resetting
}

func newResetUpstream(t *testing.T, resetUpTo int, readFirst bool) *resetUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	u := &resetUpstream{resetUpTo: resetUpTo, readFirst: readFirst}
	u.url = &url.URL{Scheme: "http", Host: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c.(*net.TCPConn))
		}
	}()
	return u
}

func (u *resetUpstream) serve(c *net.TCPConn) {
	defer c.Close()
	u.mu.Lock()
	u.conns++
	reset := u.conns <= u.resetUpTo
	u.mu.Unlock()

	if reset && !u.readFirst {
		// Wait for the first bytes: an RST before any arrive can surface as EOF, not ECONNRESET.
		_, _ = c.Read(make([]byte, 1))
		_ = c.SetLinger(0)
		return
	}
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, req.Body)
	u.mu.Lock()
	u.requests++
	u.headers = append(u.headers, req.Header.Clone())
	u.mu.Unlock()
	if reset {
		_ = c.SetLinger(0)
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
}

func (u *resetUpstream) counts() (conns, requests int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.conns, u.requests
}

func (u *resetUpstream) requestHeaders(i int) http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.headers[i].Clone()
}

// resetGateway wires a gateway whose tenancy upstream is up and returns a valid token.
func resetGateway(t *testing.T, up *resetUpstream, log *slog.Logger) (http.Handler, string) {
	t.Helper()
	issuer, err := auth.NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: testIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	h := Handler(Options{Verifier: verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": up.url, "invoice": up.url, "submission": up.url}, GatewayToken: testGatewayToken, Logger: log})
	tok, err := issuer.Mint(auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return h, tok
}

func TestProxyRetriesAnUpstreamConnectionReset(t *testing.T) {
	up := newResetUpstream(t, 1, true)
	h, tok := resetGateway(t, up, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tok))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if _, reqs := up.counts(); reqs != 2 {
		t.Fatalf("upstream saw %d requests, want 2", reqs)
	}
	retried := up.requestHeaders(1)
	if got := retried.Get(headerTenantID); got != testTenant {
		t.Errorf("retry %s = %q, want %q", headerTenantID, got, testTenant)
	}
	if got := retried.Get(headerUserID); got != testSubject {
		t.Errorf("retry %s = %q, want %q", headerUserID, got, testSubject)
	}
}

// A GET whose handler writes upstream (an audit row, the export lock) must not be re-sent.
func TestProxyDoesNotRetryAResetGETThatWrites(t *testing.T) {
	for _, p := range []string{
		"/api/invoice/v1/invoice-register",
		"/api/invoice/v1/workspace/export",
		"/api/invoice/v1/evidence-bundle",
		"/api/invoice/v1/documents/abc",
		"/api/invoice/v1/documents/abc/sheet",
		"/api/invoice/v1/imports/saved-mapping",
		"/api/submission/v1/extractions/abc",
		"/api/invoice/v1/x/../invoice-register",
	} {
		t.Run(p, func(t *testing.T) {
			up := newResetUpstream(t, 1, true)
			h, tok := resetGateway(t, up, slog.New(slog.DiscardHandler))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, request("GET", p, tok))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", rec.Code)
			}
			if _, reqs := up.counts(); reqs != 1 {
				t.Fatalf("upstream saw %d requests, want 1", reqs)
			}
		})
	}
}

func postRequest(tok string) *http.Request {
	r := httptest.NewRequest("POST", "/api/tenancy/v1/things", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestProxyDoesNotRetryAResetPOST(t *testing.T) {
	up := newResetUpstream(t, 100, true)
	h, tok := resetGateway(t, up, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postRequest(tok))

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "bad gateway") {
		t.Fatalf("got %d %q, want 502 bad gateway", rec.Code, rec.Body.String())
	}
	if conns, reqs := up.counts(); conns != 1 || reqs != 1 {
		t.Fatalf("upstream saw %d connections, %d requests, want 1 and 1", conns, reqs)
	}
}

func TestProxyDoesNotRetryAPOSTResetBeforeRead(t *testing.T) {
	up := newResetUpstream(t, 100, false)
	h, tok := resetGateway(t, up, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postRequest(tok))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if conns, reqs := up.counts(); conns != 1 || reqs != 0 {
		t.Fatalf("upstream saw %d connections, %d requests, want 1 and 0", conns, reqs)
	}
}

// Not parallel: it swaps slog.Default, which the retry transport logs through.
func TestProxySecondResetIs502AndBothAttemptsLogged(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	up := newResetUpstream(t, 100, true)
	h, tok := resetGateway(t, up, log)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tok))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	out := buf.String()
	if n := strings.Count(out, `"msg":"upstream connection reset"`); n != 2 {
		t.Errorf("%d reset records, want 2:\n%s", n, out)
	}
	for _, attempt := range []string{`"attempt":1`, `"attempt":2`} {
		if !strings.Contains(out, attempt) {
			t.Errorf("log lacks %s:\n%s", attempt, out)
		}
	}
	if n := strings.Count(out, `"msg":"gateway upstream unreachable"`); n != 1 {
		t.Errorf("%d unreachable records, want 1:\n%s", n, out)
	}
}
