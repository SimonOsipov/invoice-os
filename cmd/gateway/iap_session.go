package main

import (
	"log/slog"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// mountIAPSession is a compile stub for the red tests (CSSO-01-04); the executor replaces it.
// It reads IAP_AUDIENCES from getenv and mounts POST /auth/iap-session on app.Mux when set;
// unset on hosted Cloud Run, or any invalid value, is an error naming IAP_AUDIENCES.
func mountIAPSession(app *platform.App, getenv func(string) string, iap auth.IAPConfig, worker, authURL *url.URL, gatewayToken string, store gateway.Handoffs, log *slog.Logger, pool *pgxpool.Pool) error {
	return nil
}
