// Command submission is the 05 Submission context service. M2-04 skeleton: it serves the
// platform kit's /healthz + /readyz plus one stub endpoint; real endpoints
// arrive in a later milestone.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func main() {
	app, err := platform.New("submission")
	if err != nil {
		log.Fatalf("submission: startup: %v", err)
	}

	// Stub endpoint — proves the service builds, boots, and routes end to end;
	// replaced by real endpoints in a later milestone.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"submission","status":"ok"}`))
	})

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("submission: %v", err)
	}
}
