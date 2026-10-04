// Command notifications is the 07 Notifications context service. M2-04 skeleton: it serves the
// platform kit's /healthz + /readyz plus one stub endpoint; real endpoints
// arrive in a later milestone.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("notifications")
	if err != nil {
		platform.Fatal(slog.Default(), "notifications: startup: %v", err)
	}

	// Stub endpoint — proves the service builds, boots, and routes end to end;
	// replaced by real endpoints in a later milestone.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"notifications","status":"ok"}`))
	})

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"))

	if err := app.Run(context.Background()); err != nil {
		platform.Fatal(app.Logger, "notifications: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "notifications: %s is required", key)
	}
	return v
}

// deliveryClients is a compile-only stub (AUTH-17-04 red phase).
func deliveryClients(func(string) string, string, http.RoundTripper) (notifications.Mode, notifications.HubSpotClient, notifications.ResendClient, error) {
	return "", nil, nil, errors.New("notifications: deliveryClients not implemented")
}
