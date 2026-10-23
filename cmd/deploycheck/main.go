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
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// backends answer /healthz with their build; auth (GoTrue) has no build and answers its JWKS.
var backends = []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission", "notifications", "worker", "docling"}

// sites serve /build.txt and their page at /.
var sites = []string{"app", "landing", "library", "blog", "ops-console", "support-console"}

const (
	deadline = 15 * time.Minute
	interval = 5 * time.Second
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
}

// run checks every target in parallel and returns one line per failed target.
func (c checker) run(ctx context.Context) []string {
	type target struct {
		name  string
		check func() error
	}
	var targets []target
	for _, svc := range backends {
		targets = append(targets, target{svc, func() error { return c.backend(ctx, svc) }})
	}
	targets = append(targets, target{"auth", func() error {
		return c.status(ctx, http.MethodGet, c.url("auth")+"/.well-known/jwks.json", http.StatusOK)
	}})
	for _, site := range sites {
		targets = append(targets, target{site, func() error { return c.site(ctx, site) }})
	}

	var mu sync.Mutex
	var failed []string
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Go(func() {
			err := c.poll(ctx, t.check)
			if err == nil && t.name == "gateway" {
				err = c.gatewayPosture(ctx)
			}
			if err != nil {
				mu.Lock()
				failed = append(failed, t.name+": "+err.Error())
				mu.Unlock()
			} else {
				fmt.Println("ok", t.name)
			}
		})
	}
	wg.Wait()
	return failed
}

// poll repeats check until it passes or ctx ends, and returns the last error.
func (c checker) poll(ctx context.Context, check func() error) error {
	for {
		err := check()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(c.interval):
		}
	}
}

func (c checker) backend(ctx context.Context, svc string) error {
	body, err := c.get(ctx, c.url(svc)+"/healthz")
	if err != nil {
		return err
	}
	var h struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("/healthz is not JSON: %w", err)
	}
	if h.Build != c.sha {
		return fmt.Errorf("/healthz build %q, want %s", h.Build, c.sha)
	}
	return nil
}

// gatewayPosture runs once the gateway serves the commit: production trusts one issuer and serves
// no mint route.
func (c checker) gatewayPosture(ctx context.Context) error {
	body, err := c.get(ctx, c.url("gateway")+"/healthz")
	if err != nil {
		return err
	}
	var h struct {
		MockIssuer  string `json:"mock_issuer"`
		AuthIssuers int    `json:"auth_issuers"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("/healthz is not JSON: %w", err)
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
