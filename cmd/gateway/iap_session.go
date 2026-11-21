package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// mountIAPSession registers POST /auth/iap-session on app.Mux when IAP_AUDIENCES is set.
// Hosted Cloud Run refuses to boot without it; elsewhere unset leaves the route unmounted.
// No CORS wrapper: the browser call is same-origin through the console proxy.
func mountIAPSession(app *platform.App, getenv func(string) string, iap auth.IAPConfig, worker, authURL *url.URL, gatewayToken string, store gateway.Handoffs, log *slog.Logger, pool *pgxpool.Pool) error {
	auds, err := auth.ParseIAPAudiences(getenv("IAP_AUDIENCES"))
	if err != nil {
		return fmt.Errorf("IAP_AUDIENCES: %w", err)
	}
	if len(auds) == 0 {
		if platform.Posture(platform.DeployEnvFrom(getenv)) == platform.PostureHosted && getenv("K_SERVICE") != "" {
			return errors.New("IAP_AUDIENCES must be set on hosted Cloud Run")
		}
		return nil
	}
	iap.Audiences = auds
	v, err := auth.NewIAPVerifier(iap)
	if err != nil {
		return fmt.Errorf("IAP_AUDIENCES: %w", err)
	}
	// Same client shape as the sign-in handlers' GoTrue client; the worker call rides the same ID-token transport.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(platform.ResetRetryTransport(nil)),
	}
	app.Mux.Handle("POST /auth/iap-session", gateway.IAPSessionHandler(v, worker, client, gatewayToken, authURL, client, store, log, gateway.NewAccountEvents(pool, log)))
	return nil
}
