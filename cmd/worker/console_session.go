package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/consolesession"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// mountConsoleSession registers the recovery-token route on app.Mux when IAP_AUDIENCES is set.
// Hosted Cloud Run refuses to boot without it; elsewhere unset leaves the route unmounted.
// The route sits behind app.RequireGateway, which main calls with no open pattern.
func mountConsoleSession(app *platform.App, getenv func(string) string, admin consolesession.RecoveryLinker, iap auth.IAPConfig) error {
	auds, err := auth.ParseIAPAudiences(getenv("IAP_AUDIENCES"))
	if err != nil {
		return fmt.Errorf("IAP_AUDIENCES: %w", err)
	}
	if len(auds) == 0 {
		if platform.Posture(platform.DeployEnvFrom(getenv)) == platform.PostureHosted && getenv("K_SERVICE") != "" {
			return errors.New("IAP_AUDIENCES must be set on hosted Cloud Run")
		}
		return nil
	}
	if admin == nil {
		return errors.New("IAP_AUDIENCES is set but the GoTrue admin client is not configured (AUTH_URL, GOTRUE_JWT_KEYS)")
	}
	iap.Audiences = auds
	v, err := auth.NewIAPVerifier(iap)
	if err != nil {
		return fmt.Errorf("IAP_AUDIENCES: %w", err)
	}
	app.Mux.Handle("POST /internal/console-sessions/recovery-token", consolesession.RecoveryTokenHandler(v, admin, time.Now, app.Logger))
	return nil
}
