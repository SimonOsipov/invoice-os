// s2s.go: peer auth for 04's batch validate surface. The check lives in
// platform.RequireToken; the gateway's credential uses the same check through
// platform.App.RequireGateway. It is defense-in-depth on top of private
// networking and establishes no identity.
package validation

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// headerS2SToken carries the shared peer secret. The gateway strips it from
// every proxied request, so it only arrives from inside the private network.
const headerS2SToken = "X-S2S-Token"

// S2SMiddleware admits only callers presenting token in X-S2S-Token, before the
// body is read. token MUST be non-empty (cmd/validation sources it via mustEnv).
func S2SMiddleware(token string) func(http.Handler) http.Handler {
	return platform.RequireToken(headerS2SToken, token)
}
