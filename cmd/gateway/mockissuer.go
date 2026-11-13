//go:build mockissuer

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const mockIssuerCompiled = true

const mockIssuerTokenHeader = "X-Mock-Issuer-Token"

func mockGateOn() bool { return platform.Posture(platform.DeployEnv()) == platform.PostureNightly }

// mockGate makes the nightly's mock routes require mockIssuerTokenHeader; an unset
// token refuses every request, since RequireToken admits an empty header against "".
func mockGate(next http.Handler) http.Handler {
	if !mockGateOn() {
		return next
	}
	token := os.Getenv("GATEWAY_MOCK_ISSUER_TOKEN")
	if token == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		})
	}
	return platform.RequireToken(mockIssuerTokenHeader, token)(next)
}

// mockIssuerRoutes builds the JWKS and login handlers, or returns nil, nil where
// gateway.MockIssuerEnabled refuses.
func mockIssuerRoutes(environment, flag string, withCORS func(http.Handler) http.Handler, logger *slog.Logger) (jwks, login http.Handler) {
	if !gateway.MockIssuerEnabled(environment, flag) {
		return nil, nil
	}
	issuer, err := newMockIssuer(mustEnv("AUTH_ISSUER"), os.Getenv("GATEWAY_MOCK_ISSUER_KEY"))
	if err != nil {
		platform.Fatal(logger, "gateway: mock issuer: %v", err)
	}

	jwks = issuer.JWKSHandler()
	// The browser calls /auth/login cross-origin, so it gets the same CORS layer as /api/.
	login = withCORS(mockGate(gateway.MockLoginHandler(issuer)))
	switch {
	case !mockGateOn():
		logger.Warn("mock issuer enabled — unauthenticated login is live on this deployment")
	case os.Getenv("GATEWAY_MOCK_ISSUER_TOKEN") == "":
		logger.Warn("gateway: GATEWAY_MOCK_ISSUER_TOKEN is unset; the mock routes refuse every request")
	default:
		logger.Warn("mock issuer enabled — the mock routes require " + mockIssuerTokenHeader)
	}
	return jwks, login
}

// newMockIssuer signs with keyJSON when set, so every instance of a preview shares
// one kid; blank generates a key per process.
func newMockIssuer(issuerURL, keyJSON string) (*auth.MockIssuer, error) {
	if strings.TrimSpace(keyJSON) == "" {
		return auth.NewMockIssuer(issuerURL)
	}
	return auth.NewMockIssuerFromJWK(issuerURL, keyJSON)
}

// mockMemberRoute binds the membership grant handler to the owner DSN.
func mockMemberRoute(dsn string, logger *slog.Logger) http.Handler {
	return mockGate(gateway.MockMemberHandler(func(ctx context.Context, g gateway.MemberGrant) error {
		return db.GrantMembership(ctx, dsn, db.MemberGrant(g))
	}, logger))
}

// mockInvitationTokenRoute binds the invite token setter to the owner DSN.
func mockInvitationTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return mockGate(gateway.MockInvitationTokenHandler(func(ctx context.Context, tenantID, invitationID uuid.UUID, token string) (bool, error) {
		return db.SetInvitationToken(ctx, dsn, tenantID, invitationID, token)
	}, logger))
}

// mockDeletionLinkTokenRoute binds the cancel link token setter to the owner DSN.
func mockDeletionLinkTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return mockGate(gateway.MockDeletionLinkTokenHandler(func(ctx context.Context, tenantID, userID uuid.UUID, token string) (bool, error) {
		return db.SetDeletionLinkToken(ctx, dsn, tenantID, userID, token)
	}, logger))
}

// mockReadinessConfirmTokenRoute binds the held readiness check token setter to the owner DSN.
func mockReadinessConfirmTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return mockGate(gateway.MockReadinessConfirmTokenHandler(func(ctx context.Context, email, token string) (bool, error) {
		return db.SetReadinessConfirmToken(ctx, dsn, email, token)
	}, logger))
}
