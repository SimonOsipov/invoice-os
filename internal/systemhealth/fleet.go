package systemhealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

const maxFleetBody = 64 << 10

// FleetReader reads the gateway's fleet roll-up.
// ceiling: cmd/submission links internal/gateway for FleetHealth; move it to a leaf package if binary size grows
type FleetReader func(context.Context) (gateway.FleetHealth, error)

// NewFleetReader reads base/healthz/fleet with the FleetTimeout deadline.
func NewFleetReader(base *url.URL, client *http.Client) FleetReader {
	return NewFleetReaderTimeout(base, client, FleetTimeout)
}

// NewFleetReaderTimeout reads base/healthz/fleet; the gateway answers 503 with a body when degraded.
// The reader owns the deadline so a gateway that never answers cannot hang the sampler.
func NewFleetReaderTimeout(base *url.URL, client *http.Client, timeout time.Duration) FleetReader {
	target := base.JoinPath("healthz/fleet").String()
	return func(ctx context.Context) (gateway.FleetHealth, error) {
		var f gateway.FleetHealth
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return f, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return f, fmt.Errorf("systemhealth: fleet read: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
			return f, fmt.Errorf("systemhealth: fleet read: status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFleetBody+1))
		if err != nil {
			return f, fmt.Errorf("systemhealth: fleet read: %w", err)
		}
		if len(body) > maxFleetBody {
			return f, fmt.Errorf("systemhealth: fleet read: body over %d bytes", maxFleetBody)
		}
		if err := json.Unmarshal(body, &f); err != nil {
			return gateway.FleetHealth{}, fmt.Errorf("systemhealth: fleet read: %w", err)
		}
		if f.Status != "ok" && f.Status != "degraded" {
			return gateway.FleetHealth{}, fmt.Errorf("systemhealth: fleet read: status %q", f.Status)
		}
		return f, nil
	}
}

// ServicesDown counts the services that are not up.
func ServicesDown(f gateway.FleetHealth) int {
	n := 0
	for _, s := range f.Services {
		if s.Status != "up" {
			n++
		}
	}
	return n
}

// GatewayURL parses GATEWAY_URL, the in-network gateway base the fleet reader calls.
func GatewayURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("GATEWAY_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("GATEWAY_URL must be an absolute http(s) URL, got %q", raw)
	}
	return u, nil
}
