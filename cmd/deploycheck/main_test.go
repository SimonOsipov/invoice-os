package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// fleet serves every target from one server; the first path segment names the service.
func fleet(t *testing.T, sha string, override func(svc, path string, w http.ResponseWriter) bool) checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		path = "/" + path
		if override != nil && override(svc, path, w) {
			return
		}
		switch {
		case svc == "gateway" && path == "/health" && r.Header.Get(platform.HeaderGatewayToken) != "tok":
			fmt.Fprint(w, `{"status":"ok"}`)
		case svc == "gateway" && path == "/health":
			fmt.Fprintf(w, `{"build":%q,"mock_issuer":"absent","auth_issuers":"1"}`, sha)
		case svc == "gateway":
			http.NotFound(w, r)
		case svc == "auth" && path == "/.well-known/jwks.json":
			fmt.Fprint(w, `{"keys":[]}`)
		case path == "/health":
			fmt.Fprintf(w, `{"build":%q}`, sha)
		case path == "/build.txt":
			fmt.Fprintln(w, sha)
		case path == "/":
			fmt.Fprint(w, "<!DOCTYPE html><div id=root></div>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return checker{sha: sha, client: srv.Client(), url: func(svc string) string { return srv.URL + "/" + svc }, interval: time.Millisecond, parallel: 4, gatewayToken: "tok"}
}

func runFor(c checker, d time.Duration) []string {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return c.run(ctx)
}

func TestRun_PassesWhenEveryRevisionServesTheCommit(t *testing.T) {
	t.Parallel()
	if failed := runFor(fleet(t, "abc", nil), time.Second); len(failed) > 0 {
		t.Fatalf("failed = %v, want none", failed)
	}
}

func TestRun_NamesEachFailedTarget(t *testing.T) {
	t.Parallel()
	c := fleet(t, "abc", func(svc, path string, w http.ResponseWriter) bool {
		switch {
		case svc == "docling" && path == "/health":
			fmt.Fprint(w, `{"build":"old"}`)
		case (svc == "blog" || svc == "check") && path == "/build.txt":
			fmt.Fprintln(w, "old")
		case svc == "auth":
			w.WriteHeader(http.StatusServiceUnavailable)
		case svc == "app" && path == "/":
			fmt.Fprint(w, "not a page")
		default:
			return false
		}
		return true
	})
	failed := runFor(c, 200*time.Millisecond)
	var names []string
	for _, f := range failed {
		names = append(names, strings.SplitN(f, ":", 2)[0])
	}
	slices.Sort(names)
	if want := []string{"app", "auth", "blog", "check", "docling"}; !slices.Equal(names, want) {
		t.Fatalf("failed targets = %v (%v), want %v", names, failed, want)
	}
}

func TestRun_GatewayMustServeNoMintRouteAndOneIssuer(t *testing.T) {
	t.Parallel()
	for name, override := range map[string]func(svc, path string, w http.ResponseWriter) bool{
		"mock issuer on": func(svc, path string, w http.ResponseWriter) bool {
			if svc == "gateway" && path == "/health" {
				fmt.Fprint(w, `{"build":"abc","mock_issuer":"on","auth_issuers":"2"}`)
				return true
			}
			return false
		},
		"login route": func(svc, path string, w http.ResponseWriter) bool {
			if svc == "gateway" && path == "/auth/login" {
				w.WriteHeader(http.StatusForbidden)
				return true
			}
			return false
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			failed := runFor(fleet(t, "abc", override), 200*time.Millisecond)
			if len(failed) != 1 || !strings.HasPrefix(failed[0], "gateway:") {
				t.Fatalf("failed = %v, want only the gateway", failed)
			}
		})
	}
}

func TestRun_ChecksAtMostParallelTargetsAtOnce(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var inFlight, peak int
	c := fleet(t, "abc", func(svc, path string, w http.ResponseWriter) bool {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return false
	})
	c.parallel = 2
	if failed := runFor(c, 5*time.Second); len(failed) > 0 {
		t.Fatalf("failed = %v, want none", failed)
	}
	if peak != 2 {
		t.Fatalf("peak concurrent requests = %d, want 2", peak)
	}
}

func TestRun_GatewayWithoutTheTokenFails(t *testing.T) {
	t.Parallel()
	c := fleet(t, "abc", nil)
	c.gatewayToken = "wrong"
	// A long interval keeps the deadline out of an in-flight request, whose error would replace the build message.
	c.interval = 100 * time.Millisecond
	failed := runFor(c, 250*time.Millisecond)
	if len(failed) != 1 || !strings.HasPrefix(failed[0], "gateway:") || !strings.Contains(failed[0], `build ""`) {
		t.Fatalf("failed = %v, want only the gateway, naming build \"\"", failed)
	}
}

func TestRun_SendsTheGatewayTokenOnlyToTheGateway(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gatewayHealth int
	var leaked []string
	c := fleet(t, "abc", func(svc, path string, w http.ResponseWriter) bool { return false })
	// Wrap the fleet server's client transport to record the header per request.
	c.client = &http.Client{Transport: recordTokens(c.client.Transport, func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		svc, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		got := r.Header.Get(platform.HeaderGatewayToken)
		switch {
		case svc == "gateway" && path == "health" && got == "tok":
			gatewayHealth++
		case got != "":
			leaked = append(leaked, r.Method+" "+r.URL.Path)
		}
	})}
	if failed := runFor(c, time.Second); len(failed) > 0 {
		t.Fatalf("failed = %v, want none", failed)
	}
	if gatewayHealth == 0 {
		t.Error("gateway /health never carried the token")
	}
	if len(leaked) > 0 {
		t.Errorf("token sent to %v, want the gateway /health reads only", leaked)
	}
}

type tokenRecorder struct {
	next http.RoundTripper
	seen func(*http.Request)
}

func (t tokenRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	t.seen(r)
	return t.next.RoundTrip(r)
}

func recordTokens(next http.RoundTripper, seen func(*http.Request)) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return tokenRecorder{next, seen}
}

const fatalChildEnv = "DEPLOYCHECK_FATAL_CHILD"

func TestDeployCheck_FatalsWithoutGatewayToken(t *testing.T) {
	if os.Getenv(fatalChildEnv) == "1" {
		os.Args = []string{"deploycheck", "sha", "tag"}
		deployCheck(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeployCheck_FatalsWithoutGatewayToken$", "-test.count=1")
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "GATEWAY_TOKEN=") }),
		fatalChildEnv+"=1", "RUN_HOST_SUFFIX=1.africa-south1.run.app")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("err = %v, want a non-zero exit (output %q)", err, out)
	}
	if !strings.Contains(string(out), "GATEWAY_TOKEN") {
		t.Errorf("output %q does not name GATEWAY_TOKEN", out)
	}
}
