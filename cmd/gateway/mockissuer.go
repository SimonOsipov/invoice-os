//go:build mockissuer

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const mockIssuerCompiled = true

// mockIssuerRoutes builds the JWKS and login handlers, or returns nil, nil where
// gateway.MockIssuerEnabled refuses. Under Hosted posture the mint serves only seeded personas.
func mockIssuerRoutes(environment, flag string, withCORS func(http.Handler) http.Handler, logger *slog.Logger) (jwks, login http.Handler) {
	if !gateway.MockIssuerEnabled(environment, flag) {
		return nil, nil
	}
	issuer, err := auth.NewMockIssuer(mustEnv("AUTH_ISSUER"))
	if err != nil {
		platform.Fatal(logger, "gateway: mock issuer: %v", err)
	}
	// Read raw rather than off app.Config, which substitutes a literal default for
	// an unset value — that would classify a local or CI run as a real deployment.
	posture := platform.Posture(os.Getenv("RAILWAY_ENVIRONMENT_NAME"))

	jwks = issuer.JWKSHandler()
	// The browser calls /auth/login cross-origin, so it gets the same CORS layer as /api/.
	login = withCORS(gateway.MockLoginHandler(issuer, posture))
	logger.Warn("mock issuer enabled — unauthenticated login is live on this deployment")
	return jwks, login
}

// mockStaffRoute binds the staff grant handler to the owner DSN.
func mockStaffRoute(dsn string, logger *slog.Logger) http.Handler {
	return gateway.MockStaffHandler(func(ctx context.Context, userID uuid.UUID) error {
		return db.GrantStaff(ctx, dsn, userID)
	}, logger)
}

// mockMemberRoute binds the membership grant handler to the owner DSN.
func mockMemberRoute(dsn string, logger *slog.Logger) http.Handler {
	return gateway.MockMemberHandler(func(ctx context.Context, g gateway.MemberGrant) error {
		return db.GrantMembership(ctx, dsn, db.MemberGrant(g))
	}, logger)
}
