package platform

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestNewRegistersHealthAndMiddleware(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	app, err := New("tenancy")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header from the middleware chain")
	}
}

func TestAppRoutesAndReadiness(t *testing.T) {
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	app.Ready("dep", func(context.Context) error { return nil })

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ping", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("/v1/ping status = %d, want 204", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/readyz", nil))
	if rec2.Code != http.StatusOK {
		t.Errorf("/readyz status = %d, want 200", rec2.Code)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	t.Setenv("PORT", "0") // ephemeral port
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	time.Sleep(100 * time.Millisecond) // give the listener time to bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not shut down within 5s of cancellation")
	}
}

// Cloud Run's default request timeout; Terraform sets no override.
const cloudRunRequestTimeout = 300 * time.Second

func TestNewServer_TimeoutsOutlastTheCloudRunRequestTimeout(t *testing.T) {
	h := http.NewServeMux()
	srv := NewServer(":0", h)
	idleLimit := http.DefaultTransport.(*http.Transport).IdleConnTimeout
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= cloudRunRequestTimeout {
		t.Errorf("ReadTimeout = %v, want > %v", srv.ReadTimeout, cloudRunRequestTimeout)
	}
	if srv.WriteTimeout <= cloudRunRequestTimeout {
		t.Errorf("WriteTimeout = %v, want > %v", srv.WriteTimeout, cloudRunRequestTimeout)
	}
	if srv.WriteTimeout <= srv.ReadTimeout {
		t.Errorf("WriteTimeout = %v, want > ReadTimeout %v", srv.WriteTimeout, srv.ReadTimeout)
	}
	if srv.IdleTimeout <= idleLimit {
		t.Errorf("IdleTimeout = %v, want > gateway proxy idle limit %v", srv.IdleTimeout, idleLimit)
	}
	if srv.Addr != ":0" || srv.Handler != http.Handler(h) {
		t.Errorf("Addr/Handler = %q/%v, want the arguments", srv.Addr, srv.Handler)
	}
}

func TestRun_ServerCutsStalledAndIdleConnections(t *testing.T) {
	const bound = 10 * time.Second
	lower := func(t *testing.T, read, write, idle *time.Duration) {
		r, w, i := ServerReadTimeout, ServerWriteTimeout, ServerIdleTimeout
		t.Cleanup(func() { ServerReadTimeout, ServerWriteTimeout, ServerIdleTimeout = r, w, i })
		for _, d := range []*time.Duration{read, write, idle} {
			if d != nil {
				*d = 200 * time.Millisecond
			}
		}
	}
	// The free port is probed then released, so Run can lose it to another
	// process; a failed Run is retried on a fresh port.
	start := func(t *testing.T) string {
		t.Setenv("SENTRY_DSN", "")
		for attempt := 0; attempt < 5; attempt++ {
			if addr, ok := tryStart(t, bound); ok {
				return addr
			}
		}
		t.Fatal("server did not start on any of 5 ports")
		return ""
	}
	dial := func(t *testing.T, addr string) net.Conn {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(bound))
		return c
	}

	t.Run("stalled_body", func(t *testing.T) {
		lower(t, &ServerReadTimeout, nil, nil)
		addr := start(t)
		c := dial(t, addr)
		began := time.Now()
		_, _ = io.WriteString(c, "POST /body HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err == nil && resp.StatusCode/100 == 2 {
			t.Fatalf("stalled body answered %d, want a cut", resp.StatusCode)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("server did not cut the stalled body within %v: %v", bound, err)
		}
		if time.Since(began) >= bound {
			t.Errorf("cut took %v, want < %v", time.Since(began), bound)
		}
	})

	t.Run("slow_handler", func(t *testing.T) {
		lower(t, nil, &ServerWriteTimeout, nil)
		addr := start(t)
		c := dial(t, addr)
		began := time.Now()
		_, _ = io.WriteString(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err == nil && resp.StatusCode == http.StatusOK {
			t.Fatal("slow handler answered 200, want the write cut")
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("no cut within %v: %v", bound, err)
		}
		if time.Since(began) >= bound {
			t.Errorf("cut took %v, want < %v", time.Since(began), bound)
		}
	})

	t.Run("idle_keepalive", func(t *testing.T) {
		lower(t, nil, nil, &ServerIdleTimeout)
		addr := start(t)
		c := dial(t, addr)
		br := bufio.NewReader(c)
		_, _ = io.WriteString(c, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		time.Sleep(3 * time.Second)
		began := time.Now()
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			t.Errorf("read after idle = %v, want EOF", err)
		}
		if time.Since(began) >= bound {
			t.Errorf("cut took %v, want < %v", time.Since(began), bound)
		}
	})
}

// tryStart runs an App on a probed free port; ok is false when Run fails (port lost).
func tryStart(t *testing.T, bound time.Duration) (addr string, ok bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	t.Setenv("PORT", port)
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	app.Mux.HandleFunc("POST /body", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		}
	})
	app.Mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		_, _ = io.WriteString(w, "late")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	addr = "127.0.0.1:" + port
	deadline := time.Now().Add(bound)
	for {
		select {
		case <-done:
			cancel()
			return "", false
		default:
		}
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			t.Cleanup(func() { cancel(); <-done })
			return addr, true
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not listen: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
