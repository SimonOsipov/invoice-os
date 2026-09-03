// Command gateway is the ASComply API edge (M2-11). It verifies caller JWTs,
// injects the verified tenant/user/role context that downstream services and RLS
// depend on, and reverse-proxies each request to the owning context service.
// Outside production it also embeds a mock issuer (mint + JWKS) so a token can be
// minted and verified with the exact code path used against Supabase GoTrue after
// M8 — the cutover is then a change to AUTH_ISSUER/AUTH_JWKS_URL, not to code.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// routedServices are the seven context services the gateway fronts, in wedge
// order. Each has a corresponding <NAME>_URL env var giving its base URL over
// Railway private networking (wired in M2-12). opsconsole joins at M7.
var routedServices = []string{
	"tenancy", "portfolio", "invoice", "validation",
	"submission", "dashboard", "notifications",
}

// probedServices are reached by /healthz/fleet but get NO public proxy route:
// the sidecar has no public domain, so the roll-up is CI's only view of it, and
// nothing outside the private network should be able to call it.
// TestGatewayHandlersPublishNoProxyRouteForAProbedService holds that line.
var probedServices = []string{"docling"}

func main() {
	app, err := platform.New("gateway")
	if err != nil {
		log.Fatalf("gateway: startup: %v", err)
	}

	// Bootstrap (gated) -> migrate (unconditional) -> reset (gated, PR
	// environments only, persona-handoff-fix Decision [pr-only-reset]) -> purge
	// (gated, every environment, DEMO-04) -> seed (gated), all complete before
	// app.Run opens the listener, so a green /healthz continues to mean "fully
	// provisioned" (task-128). Every step is fatal on error except the purge,
	// which logs and continues — see db.Provision's doc comment. The gateway remains the fleet's single in-network migrator
	// (docs/migrations.md §2): migrate is unconditional regardless of the
	// guard below, exactly as before.
	//
	// The bootstrap/seed guard reads the RAW
	// os.Getenv("ENVIRONMENT")/os.Getenv("GATEWAY_DB_BOOTSTRAP") — never
	// app.Config.Environment. internal/platform/config.go:44 substitutes
	// "development" for an unset ENVIRONMENT, which would silently re-open the
	// fail-open hole BootstrapEnabled's allowlist exists to close (QA F1). With
	// the guard off, none of DATABASE_SUPERUSER_URL / MIGRATOR_PASSWORD /
	// APP_PASSWORD / READER_PASSWORD (nor their deprecated INVOICE_*_PASSWORD
	// fallbacks, see resolveRolePassword below) are required — production boots
	// without any of them set. The reset guard is separate — see
	// RailwayEnvironmentName/ResetFlag below and db.ResetEnabled's doc comment.
	provisionCfg := db.ProvisionConfig{
		Environment:   os.Getenv("ENVIRONMENT"),
		BootstrapFlag: os.Getenv("GATEWAY_DB_BOOTSTRAP"),
		// RAILWAY_ENVIRONMENT_NAME, NOT ENVIRONMENT: the destructive reset step
		// (db.Reset, gated by db.ResetEnabled) needs a signal that actually
		// differs between a PR fork and its persistent source. ENVIRONMENT is an
		// ordinary app variable that forks verbatim, so it reads the literal
		// string "development" inside every PR environment too
		// (docs/deploy-model.md "ENVIRONMENT is decorative in a fork") — it
		// cannot tell the two apart. RAILWAY_ENVIRONMENT_NAME is a Railway-
		// injected system variable (docs/add-a-service.md; never set manually)
		// that always reflects the CURRENT environment's real name: "pr-<N>"
		// inside a fork, whatever the persistent environment is actually named
		// ("production", post-2026-07-27-rename) on that environment. See
		// db.ResetEnabled's doc comment for the full reasoning.
		RailwayEnvironmentName: os.Getenv("RAILWAY_ENVIRONMENT_NAME"),
		// A SEPARATE opt-in from GATEWAY_DB_BOOTSTRAP: Reset is strictly more
		// dangerous than Bootstrap/Seed (it destroys data before recreating it),
		// so it gets its own explicit switch rather than piggybacking on the
		// existing one.
		ResetFlag:    os.Getenv("GATEWAY_DB_RESET"),
		SuperuserDSN: os.Getenv("DATABASE_SUPERUSER_URL"),
		MigrationDSN: mustEnv("DATABASE_MIGRATION_URL"),
		Passwords: db.RolePasswords{
			Migrator: resolveRolePassword("MIGRATOR_PASSWORD", "INVOICE_MIGRATOR_PASSWORD", app.Logger),
			App:      resolveRolePassword("APP_PASSWORD", "INVOICE_APP_PASSWORD", app.Logger),
			Reader:   resolveRolePassword("READER_PASSWORD", "INVOICE_TENANT_READER_PASSWORD", app.Logger),
		},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
		ConnectWait:  dbConnectWait,
		Logger:       app.Logger,
	}
	if err := db.Provision(context.Background(), provisionCfg); err != nil {
		fatal(app.Logger, "gateway: provision: %v", err)
	}

	// Publish what the sequence above actually did, off the same predicate it
	// branched on, before app.Run opens the listener — so the first /healthz any
	// caller can reach already carries it. Both of the reset's inputs are
	// hand-set Railway variables that fail closed and silent, so without this
	// the destructive PR-environment reset can stop happening with no failure
	// anywhere: the fleet greens, the E2E suites run against a fork still
	// holding every row prior runs left in the persistent environment, and the
	// only symptom is tests that get harder to keep passing. dev-env.yml's
	// health-gate asserts db_reset on every PR run.
	platform.DBReset = strconv.FormatBool(provisionCfg.ResetWillRun())
	// The purge is the one non-fatal step in Provision, so this field is the
	// only thing that tells a green boot from a swallowed purge failure.
	platform.DemoPurge = string(db.DemoPurgeOutcome)

	verifier, err := auth.NewVerifier(auth.Config{
		Issuer:  mustEnv("AUTH_ISSUER"),
		JWKSURL: mustEnv("AUTH_JWKS_URL"),
		Logger:  app.Logger,
	})
	if err != nil {
		fatal(app.Logger, "gateway: verifier: %v", err)
	}

	routed, probed, err := loadUpstreams()
	if err != nil {
		fatal(app.Logger, "gateway: upstreams: %v", err)
	}

	// CORS layer, composed OUTSIDE the JWT verifier: the app SPA and the gateway are
	// separate origins, so a browser preflight (OPTIONS, no bearer) must be answered
	// before the verifier would 401 it. Allowed origins come from CORS_ALLOWED_ORIGINS
	// (comma-separated); empty grants no browser origin (the production default).
	withCORS := gateway.CORS(strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ","))

	apiHandler, fleetHandler := gatewayHandlers(verifier, routed, probed, app.Logger)
	app.Mux.Handle(routePrefix, withCORS(apiHandler))

	// Public fleet-health roll-up, outside /api/ and outside the verifier —
	// operational, not tenant data.
	app.Mux.HandleFunc("GET /healthz/fleet", fleetHandler)

	// Embed the mock issuer wherever ENVIRONMENT is not production and the flag is
	// set — the public demo included; under Hosted posture the mint serves only
	// seeded personas. ENVIRONMENT is read raw, for the reason gate 1 above gives.
	if gateway.MockIssuerEnabled(os.Getenv("ENVIRONMENT"), os.Getenv("GATEWAY_MOCK_ISSUER")) {
		issuer, err := auth.NewMockIssuer(mustEnv("AUTH_ISSUER"))
		if err != nil {
			fatal(app.Logger, "gateway: mock issuer: %v", err)
		}
		// Read raw rather than off app.Config, which substitutes a literal default for
		// an unset value — that would classify a local or CI run as a real deployment.
		posture := platform.Posture(os.Getenv("RAILWAY_ENVIRONMENT_NAME"))

		app.Mux.Handle("GET /.well-known/jwks.json", issuer.JWKSHandler())
		// /auth/login is called cross-origin by the browser, so wrap it in the same CORS
		// layer. Register POST (the mint) and OPTIONS (the preflight CORS answers) — a
		// method-scoped POST route alone would 405 the preflight instead of letting CORS
		// handle it.
		login := withCORS(gateway.MockLoginHandler(issuer, posture))
		app.Mux.Handle("POST /auth/login", login)
		app.Mux.Handle("OPTIONS /auth/login", login)
		app.Logger.Warn("mock issuer enabled — unauthenticated login is live on this deployment")
	}

	if err := app.Run(context.Background()); err != nil {
		fatal(app.Logger, "gateway: %v", err)
	}
}

// routePrefix must match the gateway package's mount point.
const routePrefix = "/api/"

// dbConnectWait is how long boot-time provisioning waits for Postgres to accept
// its first connection before giving up (db.ProvisionConfig.ConnectWait).
//
// The gateway is the ONE binary that boots against a Postgres which may not be
// serving yet: in a freshly forked PR environment its database container has
// only just been deployed onto a brand-new volume and is still running initdb.
// Before this, provisioning gave that container 2.5s (db/bootstrap.go's 5
// attempts x 500ms) and MigrateUp gave it none at all, then log.Fatal'd — a
// crash before the listener opens, which Railway can only report as "service
// unavailable" for the whole healthcheck window.
//
// 120s is chosen to sit comfortably INSIDE Railway's 300s healthcheck window, so
// a Postgres that is genuinely broken (rather than merely slow) still produces a
// named, readable failure with time to spare instead of being reported as a
// healthcheck timeout with no cause attached.
const dbConnectWait = 120 * time.Second

// gatewayHandlers builds the two public handlers. Proxy routes come from
// `routed` alone — that argument, not a filter, is what keeps a probed service
// off /api/. The fleet roll-up sees both lists.
//
// It returns handlers rather than registering them, and leaves the CORS wrap to
// main, so both source scans still see what they assert on:
// TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute (the app.Mux calls) and
// TestGatewayApiMountIsCORSWrappedAndNotMethodScoped (withCORS at the mount).
func gatewayHandlers(
	verifier *auth.Verifier,
	routed, probed map[string]*url.URL,
	log *slog.Logger,
) (api http.Handler, fleet http.HandlerFunc) {
	api = gateway.Handler(gateway.Options{
		Verifier:  verifier,
		Upstreams: routed,
		Logger:    log,
	})

	all := make(map[string]*url.URL, len(routed)+len(probed))
	maps.Copy(all, routed)
	maps.Copy(all, probed)
	return api, gateway.FleetHealthHandler(all, log)
}

// loadUpstreams reads each service's base URL from <NAME>_URL, returning the
// routed and probed maps separately. A missing or invalid URL fails startup —
// including a probed one: a gateway reporting a fleet it cannot see is worse
// than one that refuses to boot.
func loadUpstreams() (routed, probed map[string]*url.URL, err error) {
	load := func(names []string) (map[string]*url.URL, error) {
		out := make(map[string]*url.URL, len(names))
		for _, svc := range names {
			key := strings.ToUpper(svc) + "_URL"
			raw := os.Getenv(key)
			if raw == "" {
				return nil, fmt.Errorf("%s is required", key)
			}
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid %s=%q: %w", key, raw, err)
			}
			out[svc] = u
		}
		return out, nil
	}
	if routed, err = load(routedServices); err != nil {
		return nil, nil, err
	}
	if probed, err = load(probedServices); err != nil {
		return nil, nil, err
	}
	return routed, probed, nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		// slog.Default(), not app.Logger: mustEnv is called while building the
		// db.ProvisionConfig / auth.Config literals, i.e. inside argument lists
		// where app.Logger is not in scope. platform.New has already run
		// slog.SetDefault by then, so this is the same process logger — and
		// going through fatal keeps this failure at ERROR like every other boot
		// failure. See fatal's doc comment for why that matters.
		fatal(slog.Default(), "gateway: %s is required", key)
	}
	return v
}

// fatal logs a boot failure at ERROR and exits non-zero.
//
// It exists because log.Fatalf does NOT do that here. platform.New calls
// slog.SetDefault (internal/platform/server.go), which routes the standard log
// package through slog at INFO — so every boot failure this binary reported was
// emitted as {"level":"INFO"}, and would have been emitted NOWHERE AT ALL under
// LOG_LEVEL=warn or error. A gateway that crash-loops before its listener opens
// is invisible to Railway except as "service unavailable", so the boot log is
// the only diagnostic there is; it must not be filterable by log level or
// mislabelled as routine.
func fatal(logger *slog.Logger, format string, args ...any) {
	logger.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

// resolveRolePassword resolves one role's password, preferring newName
// (the unprefixed variable Makefile/CI already set) and falling back to
// oldName (the deprecated INVOICE_-prefixed variable) when newName is unset
// or empty (M4-22-09/task-168). When the fallback fires -- or the deprecated
// name is merely present alongside the new one, even though unused -- it
// logs a warning naming both variables, so a stale Railway variable is
// observable in gateway logs and gets cleaned up (escalation E3/E4). Empty
// input from both leaves the value empty: validateRolePasswords
// (internal/platform/db/bootstrap.go) is the single source of fail-fast on
// an empty password and is intentionally NOT duplicated here.
//
// This fallback is temporary. Once escalations E3/E4 confirm every Railway
// environment sets the new unprefixed name and no longer sets the deprecated
// INVOICE_-prefixed one, delete the oldName argument and this function's
// fallback branch from each of the three call sites above.
func resolveRolePassword(newName, oldName string, logger *slog.Logger) string {
	newVal := os.Getenv(newName)
	oldVal := os.Getenv(oldName)

	if oldVal != "" {
		logger.Warn(oldName + " is deprecated; set " + newName + " instead")
	}

	if newVal != "" {
		return newVal
	}
	return oldVal
}
