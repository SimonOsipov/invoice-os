package main

import (
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
