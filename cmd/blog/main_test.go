package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfig_RefusesAMissingOrRelativeGatewayURL(t *testing.T) {
	for _, bad := range []string{"", "api.example", "ftp://x", "http://"} {
		_, _, err := config(env(map[string]string{"VITE_GATEWAY_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "VITE_GATEWAY_URL") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	_, u, err := config(env(map[string]string{"VITE_GATEWAY_URL": "https://api.example"}))
	if err != nil || u.Host != "api.example" {
		t.Errorf("ok case: %v %v", u, err)
	}
}

func TestConfig_PortDefaultsTo8080(t *testing.T) {
	for in, want := range map[string]string{"": "8080", "9000": "9000"} {
		port, _, err := config(env(map[string]string{"PORT": in, "VITE_GATEWAY_URL": "https://api.example"}))
		if err != nil || port != want {
			t.Errorf("PORT %q: %q %v", in, port, err)
		}
	}
}

func TestServe_ExitsThroughFatalWithoutAValidGatewayURL(t *testing.T) {
	if os.Getenv("BLOG_SERVE_CHILD") == "1" {
		serve(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		return
	}
	for _, bad := range []string{"", "api.example", "ftp://x"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestServe_ExitsThroughFatalWithoutAValidGatewayURL$")
		cmd.Env = append(os.Environ(), "BLOG_SERVE_CHILD=1", "VITE_GATEWAY_URL="+bad, "PORT=0")
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() == 0 {
			t.Errorf("%q: exit %v, want a non-zero exit\n%s", bad, err, out)
		}
		if !strings.Contains(string(out), "VITE_GATEWAY_URL") {
			t.Errorf("%q: output lacks the reason\n%s", bad, out)
		}
	}
}

func TestRun_LetsAnInFlightRequestFinishWhenTheContextIsCancelled(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, h, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()
	<-started
	cancel()
	select {
	case err := <-runErr:
		t.Fatalf("run returned %v before the in-flight request finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if got := <-body; got != "done" {
		t.Errorf("in-flight response = %q, want done", got)
	}
	if err := <-runErr; err != nil {
		t.Errorf("run: %v", err)
	}
}

func TestRun_BlogServerCutsStalledAndIdleConnections(t *testing.T) {
	const bound = 10 * time.Second
	lower := func(t *testing.T, d *time.Duration) {
		r, w, i := platform.ServerReadTimeout, platform.ServerWriteTimeout, platform.ServerIdleTimeout
		t.Cleanup(func() { platform.ServerReadTimeout, platform.ServerWriteTimeout, platform.ServerIdleTimeout = r, w, i })
		*d = 200 * time.Millisecond
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("POST /body", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		_, _ = io.WriteString(w, "late")
	})
	start := func(t *testing.T) net.Conn {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- run(ctx, ln, mux, 5*time.Second) }()
		t.Cleanup(func() { cancel(); <-done })
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(bound))
		return c
	}
	timedOut := func(err error) bool {
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}

	t.Run("stalled_body", func(t *testing.T) {
		lower(t, &platform.ServerReadTimeout)
		c := start(t)
		began := time.Now()
		_, _ = io.WriteString(c, "POST /body HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err == nil && resp.StatusCode/100 == 2 {
			t.Fatalf("stalled body answered %d, want a cut", resp.StatusCode)
		}
		if timedOut(err) || time.Since(began) >= bound {
			t.Fatalf("server did not cut the stalled body within %v: %v", bound, err)
		}
	})

	t.Run("slow_handler", func(t *testing.T) {
		lower(t, &platform.ServerWriteTimeout)
		c := start(t)
		began := time.Now()
		_, _ = io.WriteString(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err == nil && resp.StatusCode == http.StatusOK {
			t.Fatal("slow handler answered 200, want the write cut")
		}
		if timedOut(err) || time.Since(began) >= bound {
			t.Fatalf("no cut within %v: %v", bound, err)
		}
	})

	t.Run("idle_keepalive", func(t *testing.T) {
		lower(t, &platform.ServerIdleTimeout)
		c := start(t)
		br := bufio.NewReader(c)
		_, _ = io.WriteString(c, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		time.Sleep(3 * time.Second)
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			t.Errorf("read after idle = %v, want EOF", err)
		}
	})
}
