// Command notifications is the 07 Notifications context service: it takes contact intake
// from the gateway and delivers each contact to HubSpot and Resend through River.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	hubspotBaseURL = "https://api.hubapi.com"
	resendBaseURL  = "https://api.resend.com"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("notifications")
	if err != nil {
		platform.Fatal(slog.Default(), "notifications: startup: %v", err)
	}

	ctx := context.Background()
	mode, hubspot, resend, err := deliveryClients(os.Getenv, os.Getenv("RAILWAY_ENVIRONMENT_NAME"), realTransport())
	if err != nil {
		platform.Fatal(app.Logger, "notifications: %v", err)
	}
	platform.Contacts = string(mode)

	pool, err := db.NewPool(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "notifications: db pool: %v", err)
	}
	defer pool.Close()
	app.Ready("database", pool.Ping)

	qcfg := queue.Config{Logger: app.Logger}
	if mode != notifications.ModeOff {
		workers := river.NewWorkers()
		river.AddWorker(workers, &notifications.DeliverWorker{
			Pool: pool, HubSpot: hubspot, Resend: resend, Mode: mode, Logger: app.Logger,
		})
		qcfg.Workers = workers
		qcfg.Queues = map[string]river.QueueConfig{notifications.QueueContacts: {MaxWorkers: 2}}
	}
	q, err := queue.New(pool, qcfg)
	if err != nil {
		platform.Fatal(app.Logger, "notifications: queue: %v", err)
	}
	if mode != notifications.ModeOff {
		app.AddBackgroundWorker(q)
	}

	store := notifications.NewStore(pool, q.River())
	app.Mux.HandleFunc("POST /internal/contacts/registrants", notifications.RegistrantsHandler(store, app.Logger))
	app.Mux.HandleFunc("POST /internal/contacts/demo-requests", notifications.DemoRequestsHandler(store, app.Logger))
	app.Mux.HandleFunc("GET /v1/contacts/me", notifications.MeHandler(store, app.Logger))

	// Stub endpoint — proves the service builds, boots, and routes end to end.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"notifications","status":"ok"}`))
	})

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"))

	if err := app.Run(ctx); err != nil {
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

// realTransport clones the default transport so HTTPS_PROXY and NO_PROXY still apply.
func realTransport() http.RoundTripper { return http.DefaultTransport.(*http.Transport).Clone() }

// deliveryClients returns fakes in fake mode, vendor clients on transport in real mode, and
// nil clients when off.
func deliveryClients(getenv func(string) string, railwayEnvName string, transport http.RoundTripper) (notifications.Mode, notifications.HubSpotClient, notifications.ResendClient, error) {
	mode, keys, err := notifications.ModeFromEnv(getenv, platform.Posture(railwayEnvName))
	if err != nil {
		return "", nil, nil, err
	}
	switch mode {
	case notifications.ModeFake:
		return mode, notifications.FakeHubSpot{}, notifications.FakeResend{}, nil
	case notifications.ModeReal:
		hc := notifications.NewHTTPClient(transport)
		return mode, notifications.NewHubSpot(hubspotBaseURL, keys, hc), notifications.NewResend(resendBaseURL, keys, hc), nil
	}
	return mode, nil, nil, nil
}
