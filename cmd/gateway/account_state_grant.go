package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// grantAccountStateRead retries grant until it reports true. Forks need the retry: the gateway
// boots before GoTrue creates auth.users. It logs the error class only, never the error text.
func grantAccountStateRead(ctx context.Context, dsn string, grant func(context.Context, string) (bool, error), every time.Duration, attempts int, log *slog.Logger) {
	for i := 1; i <= attempts; i++ {
		ok, err := grant(ctx, dsn)
		if err == nil && ok {
			log.Info("gateway: account state read granted")
			return
		}
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
	if ctx.Err() != nil {
		return
	}
	log.Warn("gateway: account state read not granted", "attempts", attempts)
}

// startAccountStateGrant starts the boot grant on local and PR forks. A hosted environment
// holds the hand grant, so it returns before it reads the password.
func startAccountStateGrant(railwayEnvironmentName, migrationDSN, password string, log *slog.Logger, start func(dsn string)) bool {
	if platform.Posture(railwayEnvironmentName) == platform.PostureHosted {
		log.Info("gateway: account state grant skipped in a hosted environment")
		return false
	}
	dsn, err := db.AuthAdminDSN(migrationDSN, password)
	if err != nil {
		log.Warn("gateway: account state read not started", "reason", err.Error())
		return false
	}
	start(dsn)
	return true
}
