//go:build mockissuer

package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const mockIssuerCompiled = true

// mockIssuerRoutes builds the JWKS and login handlers, or returns nil, nil where
// gateway.MockIssuerEnabled refuses.
func mockIssuerRoutes(environment, flag string, withCORS func(http.Handler) http.Handler, logger *slog.Logger) (jwks, login http.Handler) {
	if !gateway.MockIssuerEnabled(environment, flag) {
		return nil, nil
	}
	issuer, err := auth.NewMockIssuer(mustEnv("AUTH_ISSUER"))
	if err != nil {
		platform.Fatal(logger, "gateway: mock issuer: %v", err)
	}

	jwks = issuer.JWKSHandler()
	// The browser calls /auth/login cross-origin, so it gets the same CORS layer as /api/.
	login = withCORS(gateway.MockLoginHandler(issuer))
	logger.Warn("mock issuer enabled — unauthenticated login is live on this deployment")
	return jwks, login
}

// mockMemberRoute binds the membership grant handler to the owner DSN.
func mockMemberRoute(dsn string, logger *slog.Logger) http.Handler {
	return gateway.MockMemberHandler(func(ctx context.Context, g gateway.MemberGrant) error {
		return db.GrantMembership(ctx, dsn, db.MemberGrant(g))
	}, logger)
}

// mockInvitationTokenRoute binds the invite token setter to the owner DSN.
func mockInvitationTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return gateway.MockInvitationTokenHandler(func(ctx context.Context, tenantID, invitationID uuid.UUID, token string) (bool, error) {
		return db.SetInvitationToken(ctx, dsn, tenantID, invitationID, token)
	}, logger)
}

// mockDeletionLinkTokenRoute binds the cancel link token setter to the owner DSN.
func mockDeletionLinkTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return gateway.MockDeletionLinkTokenHandler(func(ctx context.Context, tenantID, userID uuid.UUID, token string) (bool, error) {
		return db.SetDeletionLinkToken(ctx, dsn, tenantID, userID, token)
	}, logger)
}
