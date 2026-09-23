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
