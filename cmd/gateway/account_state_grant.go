package main

import (
	"context"
	"log/slog"
	"time"
)

// grantAccountStateRead retries grant until it reports true. Stub: LOGFIX-02-02 implements it.
func grantAccountStateRead(ctx context.Context, dsn string, grant func(context.Context, string) (bool, error), every time.Duration, attempts int, log *slog.Logger) {
}

// startAccountStateGrant gates the boot grant on the Railway posture. Stub: LOGFIX-02-02 implements it.
func startAccountStateGrant(railwayEnvironmentName, migrationDSN, password string, log *slog.Logger, start func(dsn string)) bool {
	return false
}
