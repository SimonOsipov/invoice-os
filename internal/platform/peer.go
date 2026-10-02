package platform

import "net/http"

// Test-spec seam: no-op stubs so the AUTH-13-01 red tests compile. The implementation replaces this file.

// HeaderGatewayToken carries the gateway's credential to a context service.
const HeaderGatewayToken = "X-Gateway-Token"

// RequireToken is a pass-through until AUTH-13-01 lands.
func RequireToken(header, token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// RequireGateway is a no-op until AUTH-13-01 lands.
func (a *App) RequireGateway(token string, open ...string) {}
