// Command deploycheck is the production deploy check (GKE-09). It runs as the Cloud Run job
// deploy-check inside the production network and waits until every new revision, reached by its
// revision tag, serves the commit under test. Nothing it sends writes.
//
//	deploycheck <sha> <tag>
//
// RUN_HOST_SUFFIX is the part of a run.app host after the service name, such as
// 428987361227.africa-south1.run.app. platform.Fatal names each target that failed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// backends answer /health with their build (a run.app host reserves /healthz); auth (GoTrue) has no build and answers its JWKS.
var backends = []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission", "notifications", "worker", "docling"}

// sites serve /build.txt and their page at /.
var sites = []string{"app", "landing", "library", "blog", "ops-console", "support-console"}

const (
	deadline = 15 * time.Minute
	interval = 5 * time.Second
	// Four targets at a time: each wakes a cold revision with a CPU boost, and africa-south1 allows
	// 20 vCPU of allocation per minute.
	parallel = 4
)

func main() {
	defer platform.ReportBootPanic()
	deployCheck(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

func deployCheck(log *slog.Logger) {
	if len(os.Args) != 3 || os.Getenv("RUN_HOST_SUFFIX") == "" {
		platform.Fatal(log, "usage: RUN_HOST_SUFFIX=<number>.<region>.run.app deploycheck <sha> <tag>")
	}
	if failed := check(); len(failed) > 0 {
		platform.Fatal(log, "deploy check failed:\n%s", strings.Join(failed, "\n"))
	}
	log.Info("every revision serves the commit", "sha", os.Args[1])
}

func check() []string {
	c := checker{
		sha:    os.Args[1],
		client: &http.Client{Timeout: 30 * time.Second, Transport: platform.IDTokenTransport(nil)},
		url: func(svc string) string {
			return "https://" + os.Args[2] + "---" + svc + "-" + os.Getenv("RUN_HOST_SUFFIX")
		},
		interval: interval,
		parallel: parallel,
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return c.run(ctx)
}

type checker struct {
	sha      string
	client   *http.Client
	url      func(svc string) string
	interval time.Duration
	parallel int
}

// run checks every target in rounds until each passes or ctx ends: one attempt per pending target
// per round, at most c.parallel at once. It returns one line per target that never passed.
func (c checker) run(ctx context.Context) []string {
	pending := map[string]func() error{
		"auth": func() error {
			return c.status(ctx, http.MethodGet, c.url("auth")+"/.well-known/jwks.json", http.StatusOK)
		},
	}
	for _, svc := range backends {
		pending[svc] = func() error { return c.backend(ctx, svc) }
	}
	pending["gateway"] = func() error {
		if err := c.backend(ctx, "gateway"); err != nil {
			return err
		}
		return c.gatewayPosture(ctx)
	}
	for _, site := range sites {
		pending[site] = func() error { return c.site(ctx, site) }
	}

	last := map[string]error{}
	for {
		var mu sync.Mutex
		var wg sync.WaitGroup
		slots := make(chan struct{}, c.parallel)
		round := maps.Clone(pending)
		for name, check := range round {
			wg.Go(func() {
				slots <- struct{}{}
				defer func() { <-slots }()
				err := check()
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					last[name] = err
					return
				}
				delete(pending, name)
				fmt.Println("ok", name)
			})
		}
		wg.Wait()
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			var failed []string
			for name := range pending {
				failed = append(failed, name+": "+last[name].Error())
			}
			sort.Strings(failed)
			return failed
		case <-time.After(c.interval):
		}
	}
}

func (c checker) backend(ctx context.Context, svc string) error {
	body, err := c.get(ctx, c.url(svc)+"/health")
	if err != nil {
		return err
	}
	var h struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("/health is not JSON: %w", err)
	}
	if h.Build != c.sha {
		return fmt.Errorf("/health build %q, want %s", h.Build, c.sha)
	}
	return nil
}

// gatewayPosture runs once the gateway serves the commit: production trusts one issuer and serves
// no mint route.
func (c checker) gatewayPosture(ctx context.Context) error {
	body, err := c.get(ctx, c.url("gateway")+"/health")
	if err != nil {
		return err
	}
	var h struct {
		MockIssuer  string `json:"mock_issuer"`
		AuthIssuers int    `json:"auth_issuers"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("/health is not JSON: %w", err)
	}
	if h.MockIssuer != "absent" || h.AuthIssuers != 1 {
		return fmt.Errorf("mock_issuer %q and auth_issuers %d, want absent and 1", h.MockIssuer, h.AuthIssuers)
	}
	if err := c.status(ctx, http.MethodGet, c.url("gateway")+"/.well-known/jwks.json", http.StatusNotFound); err != nil {
		return err
	}
	return c.status(ctx, http.MethodPost, c.url("gateway")+"/auth/login", http.StatusNotFound)
}

func (c checker) site(ctx context.Context, site string) error {
	body, err := c.get(ctx, c.url(site)+"/build.txt")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(body)); got != c.sha {
		return fmt.Errorf("/build.txt %q, want %s", got, c.sha)
	}
	page, err := c.get(ctx, c.url(site)+"/")
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(string(page)), "<!doctype html") {
		return fmt.Errorf("/ is not an HTML page")
	}
	return nil
}

func (c checker) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", req.URL.Path, resp.StatusCode)
	}
	return body, nil
}

func (c checker) status(ctx context.Context, method, url string, want int) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: status %d, want %d", method, req.URL.Path, resp.StatusCode, want)
	}
	return nil
}
