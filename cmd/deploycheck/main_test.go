package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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
		case svc == "gateway" && path == "/healthz":
			fmt.Fprintf(w, `{"build":%q,"mock_issuer":"absent","auth_issuers":1}`, sha)
		case svc == "gateway":
			http.NotFound(w, r)
		case svc == "auth" && path == "/.well-known/jwks.json":
			fmt.Fprint(w, `{"keys":[]}`)
		case path == "/healthz":
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
	return checker{sha: sha, client: srv.Client(), url: func(svc string) string { return srv.URL + "/" + svc }, interval: time.Millisecond, parallel: 4}
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
		case svc == "docling" && path == "/healthz":
			fmt.Fprint(w, `{"build":"old"}`)
		case svc == "blog" && path == "/build.txt":
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
	if want := []string{"app", "auth", "blog", "docling"}; !slices.Equal(names, want) {
		t.Fatalf("failed targets = %v (%v), want %v", names, failed, want)
	}
}

func TestRun_GatewayMustServeNoMintRouteAndOneIssuer(t *testing.T) {
	t.Parallel()
	for name, override := range map[string]func(svc, path string, w http.ResponseWriter) bool{
		"mock issuer on": func(svc, path string, w http.ResponseWriter) bool {
			if svc == "gateway" && path == "/healthz" {
				fmt.Fprint(w, `{"build":"abc","mock_issuer":"on","auth_issuers":2}`)
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
