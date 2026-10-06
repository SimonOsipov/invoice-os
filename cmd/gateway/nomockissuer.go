//go:build !mockissuer

package main

import (
	"log/slog"
	"net/http"
)

const mockIssuerCompiled = false

// mockIssuerRoutes never serves mint routes: this build carries no minting code.
func mockIssuerRoutes(environment, flag string, withCORS func(http.Handler) http.Handler, logger *slog.Logger) (jwks, login http.Handler) {
	return nil, nil
}

// mockStaffRoute never serves: this build carries no staff grant code.
func mockStaffRoute(dsn string, logger *slog.Logger) http.Handler {
	return http.NotFoundHandler()
}

// mockMemberRoute never serves: this build carries no membership grant code.
func mockMemberRoute(dsn string, logger *slog.Logger) http.Handler {
	return http.NotFoundHandler()
}

// mockInvitationTokenRoute never serves: this build carries no invite token code.
func mockInvitationTokenRoute(dsn string, logger *slog.Logger) http.Handler {
	return http.NotFoundHandler()
}
