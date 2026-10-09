// Command gateway is the ASComply API edge (M2-11). It verifies caller JWTs,
// injects the verified tenant/user/role context that downstream services and RLS
// depend on, and reverse-proxies each request to the owning context service.
// A mock issuer (mint + JWKS) is compiled in only under -tags mockissuer (PR
// environments and local test builds); the production build carries no minting code.
package main

import (
	"context"
	"fmt"
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
// none of them has a public domain, so the roll-up is CI's only view of them, and
// nothing outside the private network should be able to call them.
// TestGatewayHandlersPublishNoProxyRouteForAProbedService holds that line.
var probedServices = []string{"docling", "auth", "reconciliation"}

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("gateway")
	if err != nil {
		platform.Fatal(slog.Default(), "gateway: startup: %v", err)
	}

	// Parsed before Provision so a malformed value stops boot before any bootstrap, reset or seed.
	additional := mustParseIssuers(os.Getenv("AUTH_ADDITIONAL_ISSUERS"))
	siteURL := mustParseSiteURL(os.Getenv("AUTH_SITE_URL"), app.Logger)
	registerMinResponse := mustParseRegisterMinResponse(os.Getenv("AUTH_REGISTER_MIN_RESPONSE"), app.Logger)
	gatewayToken := mustEnv("GATEWAY_TOKEN")

	// Bootstrap (gated) -> migrate (unconditional) -> reset (gated, PR
	// environments only, persona-handoff-fix Decision [pr-only-reset]) -> purge
	// (gated, DEMO-04) -> seed (gated), all complete before
	// app.Run opens the listener, so a green /healthz continues to mean "fully
	// provisioned" (task-128). Every step is fatal on error except the purge,
	// which logs and continues — see db.Provision's doc comment. The gateway remains the fleet's single in-network migrator:
	// migrate is unconditional regardless of the
	// guard below, exactly as before.
	//
	// The bootstrap/seed guard reads the RAW
	// os.Getenv("ENVIRONMENT")/os.Getenv("GATEWAY_DB_BOOTSTRAP") — never
	// app.Config.Environment. internal/platform/config.go:44 substitutes
	// "development" for an unset ENVIRONMENT, which would silently re-open the
	// fail-open hole BootstrapEnabled's allowlist exists to close (QA F1). With
	// the guard off, none of DATABASE_SUPERUSER_URL / MIGRATOR_PASSWORD /
	// APP_PASSWORD / READER_PASSWORD / AUTH_ADMIN_PASSWORD (nor their deprecated INVOICE_*_PASSWORD
	// fallbacks, see resolveRolePassword below) are required — production boots
	// without any of them set. The reset guard is separate — see
	// RailwayEnvironmentName/ResetFlag below and db.ResetEnabled's doc comment.
	provisionCfg := db.ProvisionConfig{
		Environment:   os.Getenv("ENVIRONMENT"),
		BootstrapFlag: os.Getenv("GATEWAY_DB_BOOTSTRAP"),
		// RAILWAY_ENVIRONMENT_NAME, NOT ENVIRONMENT: the destructive reset step
		// (db.Reset, gated by db.ResetEnabled) keys on a name no one hand-sets.
		// ENVIRONMENT is an ordinary app variable that CI writes in every fork
		// (.claude/rules/ci-railway.md).
		// RAILWAY_ENVIRONMENT_NAME is a Railway-injected system variable
		// (.claude/rules/add-service.md; never set manually): "pr-<N>" inside a fork,
		// the persistent environment's real name on that environment. See
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
			// No deprecated name ever existed, so no fallback to resolve.
			AuthAdmin: os.Getenv("AUTH_ADMIN_PASSWORD"),
		},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
		ConnectWait:  dbConnectWait,
		Logger:       app.Logger,
	}
	if err := db.Provision(context.Background(), provisionCfg); err != nil {
		platform.Fatal(app.Logger, "gateway: provision: %v", err)
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
		Issuer:     mustEnv("AUTH_ISSUER"),
		JWKSURL:    mustEnv("AUTH_JWKS_URL"),
		Additional: additional,
		HTTPClient: newJWKSClient(),
		Logger:     app.Logger,
	})
	if err != nil {
		platform.Fatal(app.Logger, "gateway: verifier: %v", err)
	}
	// The primary issuer plus the additional set; the deploy gate asserts the count.
	platform.AuthIssuers = strconv.Itoa(1 + len(additional))

	routed, probed, err := loadUpstreams()
	if err != nil {
		// ceiling: the error quotes a raw <NAME>_URL; internal hosts carry no credentials today
		platform.Fatal(app.Logger, "gateway: upstreams: %v", err)
	}

	// CORS layer, composed OUTSIDE the JWT verifier: the app SPA and the gateway are
	// separate origins, so a browser preflight (OPTIONS, no bearer) must be answered
	// before the verifier would 401 it. Allowed origins come from CORS_ALLOWED_ORIGINS
	// (comma-separated); empty grants no browser origin (the production default).
	withCORS := gateway.CORS(strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ","))

	sessions := gateway.NewSessionChecker(probed["auth"], &http.Client{Timeout: gateway.SessionCheckTimeout}, time.Now, app.Logger)
	apiHandler, fleetHandler := gatewayHandlers(verifier, sessions, routed, probed, map[string]string{"auth": ".well-known/jwks.json"}, app.Logger, gatewayToken)
	app.Mux.Handle(routePrefix, withCORS(apiHandler))

	// Public fleet-health roll-up, outside /api/ and outside the verifier —
	// operational, not tenant data.
	app.Mux.HandleFunc("GET /healthz/fleet", fleetHandler)

	// Public account-mail template and logo for GoTrue and mail clients; static, no CORS.
	confirmationMail, err := gateway.MailTemplate("confirmation")
	if err != nil {
		platform.Fatal(app.Logger, "gateway: account mail: %v", err)
	}
	app.Mux.Handle("GET /emails/confirmation.html", confirmationMail)
	recoveryMail, err := gateway.MailTemplate("recovery")
	if err != nil {
		platform.Fatal(app.Logger, "gateway: account mail: %v", err)
	}
	app.Mux.Handle("GET /emails/recovery.html", recoveryMail)
	app.Mux.Handle("GET /emails/mark.png", gateway.MailLogo())
	verifyPage, err := gateway.VerifyPageHandler(siteURL)
	if err != nil {
		platform.Fatal(app.Logger, "gateway: verify page: %v", err)
	}

	// One sink for both hand-off paths; the sink bounds each call and never follows a redirect.
	sink := gateway.NewHTTPContactSink(routed["notifications"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)

	pending := gateway.NewHTTPPendingInviteLookup(routed["tenancy"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)

	// Public registration, outside /api/ and the verifier, in every build. Register is
	// CORS-wrapped for the landing page; the OPTIONS route stops the POST route 405ing the preflight.
	reg := registrationHandlers(probed["auth"], siteURL, registerMinResponse, app.Logger, sink, pending)
	app.Mux.Handle("POST /auth/register", withCORS(reg.Register))
	app.Mux.Handle("OPTIONS /auth/register", withCORS(reg.Register))
	app.Mux.Handle("POST /auth/resend-verification", withCORS(reg.ResendVerification))
	app.Mux.Handle("OPTIONS /auth/resend-verification", withCORS(reg.ResendVerification))
	app.Mux.Handle("POST /auth/request-password-reset", withCORS(reg.RequestPasswordReset))
	app.Mux.Handle("OPTIONS /auth/request-password-reset", withCORS(reg.RequestPasswordReset))
	app.Mux.Handle("GET /auth/reset-password", gateway.ResetPasswordPageHandler(siteURL))
	app.Mux.Handle("GET /auth/verify", verifyPage)
	app.Mux.Handle("POST /auth/verify", reg.Verify)
	previewer := gateway.NewHTTPInvitationPreviewer(routed["tenancy"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)
	invitation, inviteeRegister := invitationHandlers(probed["auth"], siteURL, registerMinResponse, reg.RegisterPerIP, previewer, app.Logger)
	app.Mux.Handle("POST /auth/invitation", withCORS(invitation))
	app.Mux.Handle("OPTIONS /auth/invitation", withCORS(invitation))
	app.Mux.Handle("POST /auth/invitation/register", withCORS(inviteeRegister))
	app.Mux.Handle("OPTIONS /auth/invitation/register", withCORS(inviteeRegister))
	app.Mux.Handle("POST /contacts/demo-request", withCORS(reg.DemoRequest))
	app.Mux.Handle("OPTIONS /contacts/demo-request", withCORS(reg.DemoRequest))

	// Public sign-in hand-off, session renewal and sign-out, outside the verifier, in every build.
	// The OPTIONS route stops the method-scoped POST from 405ing the preflight.
	h := handoffHandlers(probed["auth"], sessions, app.Logger, sink)
	app.Mux.Handle("POST /auth/sign-in", withCORS(h.SignIn))
	app.Mux.Handle("OPTIONS /auth/sign-in", withCORS(h.SignIn))
	app.Mux.Handle("POST /auth/exchange", withCORS(h.Exchange))
	app.Mux.Handle("OPTIONS /auth/exchange", withCORS(h.Exchange))
	app.Mux.Handle("POST /auth/refresh", withCORS(h.Refresh))
	app.Mux.Handle("OPTIONS /auth/refresh", withCORS(h.Refresh))
	app.Mux.Handle("POST /auth/sign-out", withCORS(h.SignOut))
	app.Mux.Handle("OPTIONS /auth/sign-out", withCORS(h.SignOut))
	app.Mux.Handle("POST /auth/reset-password", resetPasswordHandler(probed["auth"], siteURL, sessions, h.SignInThrottle, app.Logger))
	app.Mux.Handle("POST /auth/invitation/password", invitationPasswordHandler(probed["auth"], siteURL, sessions, h.SignInThrottle, sink, app.Logger))

	// Mint routes exist only in a -tags mockissuer build; ENVIRONMENT is read raw, as for provisioning.
	platform.MockIssuer = "absent"
	if mockIssuerCompiled {
		platform.MockIssuer = "off"
	}
	if jwks, login := mockIssuerRoutes(os.Getenv("ENVIRONMENT"), os.Getenv("GATEWAY_MOCK_ISSUER"), withCORS, app.Logger); jwks != nil {
		app.Mux.Handle("GET /.well-known/jwks.json", jwks)
		// OPTIONS too: a POST-only route would 405 the CORS preflight.
		app.Mux.Handle("POST /auth/login", login)
		app.Mux.Handle("OPTIONS /auth/login", login)
		app.Mux.Handle("POST /auth/mock/staff", mockStaffRoute(provisionCfg.MigrationDSN, app.Logger))
		app.Mux.Handle("POST /auth/mock/member", mockMemberRoute(provisionCfg.MigrationDSN, app.Logger))
		app.Mux.Handle("POST /auth/mock/invitation-token", mockInvitationTokenRoute(provisionCfg.MigrationDSN, app.Logger))
		platform.MockIssuer = "on"
	}

	if err := app.Run(context.Background()); err != nil {
		platform.Fatal(app.Logger, "gateway: %v", err)
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
// attempts x 500ms) and MigrateUp gave it none at all, then exited via platform.Fatal — a
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
// TestRLS_EveryRegisteredRouteHasAVerdict (the app.Mux calls) and
// TestGatewayApiMountIsCORSWrappedAndNotMethodScoped (withCORS at the mount).
func gatewayHandlers(
	verifier *auth.Verifier,
	sessions *gateway.SessionChecker,
	routed, probed map[string]*url.URL,
	healthPaths map[string]string,
	log *slog.Logger,
	gatewayToken string,
) (api http.Handler, fleet http.HandlerFunc) {
	api = gateway.Handler(gateway.Options{
		Verifier:  verifier,
		Sessions:  sessions,
		Upstreams: routed,
		Logger:    log,

		GatewayToken: gatewayToken,
	})

	all := make(map[string]*url.URL, len(routed)+len(probed))
	maps.Copy(all, routed)
	maps.Copy(all, probed)
	return api, gateway.FleetHealthHandler(all, healthPaths, log)
}

// registration holds the public registration handlers main mounts outside /api/.
type registration struct {
	Register, Verify, DemoRequest, ResendVerification, RequestPasswordReset http.Handler
	// RegisterPerIP is the register throttle, nil when unconfigured; invitee registration shares it.
	RegisterPerIP *gateway.SignInThrottle
}

// invitationHandlers builds the accept-page preview handler and the invitee-registration handler.
// Registration shares perIP with /auth/register and answers 503 while any of authURL, siteURL or perIP is nil; the preview needs none of them.
func invitationHandlers(authURL, siteURL *url.URL, minResponse time.Duration, perIP *gateway.SignInThrottle, preview gateway.InvitationPreviewer, log *slog.Logger) (invitation, register http.Handler) {
	invitation = gateway.InvitationHandler(preview, log)
	if authURL == nil || siteURL == nil || perIP == nil {
		return invitation, gateway.RegistrationNotConfigured()
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	enforce := platform.Posture(os.Getenv("RAILWAY_ENVIRONMENT_NAME")) != platform.PosturePreview
	return invitation, gateway.InvitationRegisterHandler(authURL, client, minResponse, perIP, enforce, log, preview)
}

// newJWKSClient builds the JWKS fetch client.
func newJWKSClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: platform.TraceTransport(nil)}
}

// registrationHandlers builds the registration handlers against GoTrue at authURL.
// A nil authURL or siteURL (AUTH_SITE_URL unset) makes Register, ResendVerification, RequestPasswordReset and Verify answer 503 (TestRegistrationHandlers_NotConfigured503). A nil pending lookup makes Register alone answer 503 (TestRegistrationHandlers_NilPendingLookupIsNotConfigured).
// On a PR preview the per-client limits log but do not refuse: a preview sends no mail (TestRegistrationHandlers_PreviewOnlyLogs).
func registrationHandlers(authURL, siteURL *url.URL, minResponse time.Duration, log *slog.Logger, sink gateway.ContactSink, pending gateway.PendingInviteLookup) registration {
	if authURL == nil || siteURL == nil {
		nc := gateway.RegistrationNotConfigured()
		return registration{Register: nc, Verify: nc, ResendVerification: nc, RequestPasswordReset: nc, DemoRequest: gateway.DemoRequestHandler(sink, log)}
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	enforce := platform.Posture(os.Getenv("RAILWAY_ENVIRONMENT_NAME")) != platform.PosturePreview
	// Resend and reset share both throttles: one budget per address and per client.
	perAddress := gateway.NewSignInThrottle("resend-address", gateway.ResendPerAddress, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	perIP := gateway.NewSignInThrottle("resend-ip", gateway.ResendPerIP, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	registerPerIP := gateway.NewSignInThrottle("register", gateway.RegisterPerIP, gateway.RegisterMaxKeys, gateway.RegisterWindow, time.Now)
	return registration{
		Register:             gateway.RegisterHandler(authURL, client, minResponse, registerPerIP, enforce, log, pending),
		RegisterPerIP:        registerPerIP,
		Verify:               gateway.VerifyHandler(authURL, siteURL, client, log, sink),
		ResendVerification:   gateway.ResendVerificationHandler(authURL, client, minResponse, perAddress, perIP, enforce, log),
		RequestPasswordReset: gateway.RequestPasswordResetHandler(authURL, client, minResponse, perAddress, perIP, enforce, log),
		DemoRequest:          gateway.DemoRequestHandler(sink, log),
	}
}

// resetPasswordHandler builds the reset-form handler; a nil authURL or siteURL makes it answer 503.
func resetPasswordHandler(authURL, siteURL *url.URL, sessions *gateway.SessionChecker, signIn *gateway.SignInThrottle, log *slog.Logger) http.Handler {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return gateway.ResetPasswordHandler(authURL, siteURL, client, sessions, signIn, log)
}

// invitationPasswordHandler builds the invitee set-password handler; a nil authURL or siteURL makes it answer 503.
func invitationPasswordHandler(authURL, siteURL *url.URL, sessions *gateway.SessionChecker, signIn *gateway.SignInThrottle, sink gateway.ContactSink, log *slog.Logger) http.Handler {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return gateway.InvitationPasswordHandler(authURL, siteURL, client, sessions, signIn, sink, log)
}

// handoff holds the public sign-in hand-off, renewal and sign-out handlers main mounts outside /api/.
type handoff struct {
	SignIn, Exchange, Refresh, SignOut http.Handler
	SignInThrottle                     *gateway.SignInThrottle
}

// handoffHandlers builds the sign-in, exchange, refresh and sign-out handlers against GoTrue at authURL.
// Sign-out evicts from sessions, the API's own checker.
// Sign-in and exchange share one code store: a code minted by sign-in is redeemable only through exchange.
func handoffHandlers(authURL *url.URL, sessions *gateway.SessionChecker, log *slog.Logger, sink gateway.ContactSink) handoff {
	store := gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)
	throttle := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	// Same settings as registrationHandlers; TestRegistrationClientTimeoutAndNoFollow pins that literal in place.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return handoff{
		SignIn:   gateway.SignInHandler(authURL, client, store, throttle, log, sink),
		Exchange: gateway.ExchangeHandler(store),
		Refresh:  gateway.RefreshHandler(authURL, client, log),
		SignOut:  gateway.SignOutHandler(authURL, client, sessions, log),

		SignInThrottle: throttle,
	}
}

// mustParseSiteURL parses AUTH_SITE_URL. Unset is allowed and logged; a value that is not
// an absolute http(s) URL, or carries user info, a query or a fragment, stops boot.
func mustParseSiteURL(raw string, log *slog.Logger) *url.URL {
	if raw == "" {
		log.Warn("gateway: AUTH_SITE_URL is unset; /auth/register, /auth/resend-verification, /auth/request-password-reset, GET and POST /auth/verify and /auth/reset-password answer 503")
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		platform.Fatal(log, "gateway: AUTH_SITE_URL is not an absolute http(s) URL")
	}
	// The verify routes append "/?verified=1" and "/?verify=failed" to this value.
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		platform.Fatal(log, "gateway: AUTH_SITE_URL must not carry user info, a query or a fragment")
	}
	return u
}

// mustParseRegisterMinResponse parses AUTH_REGISTER_MIN_RESPONSE, a Go duration. Unset gives the
// default; a value that does not parse or is not above zero stops boot without echoing it.
func mustParseRegisterMinResponse(raw string, log *slog.Logger) time.Duration {
	if raw == "" {
		return gateway.DefaultRegisterMinResponse
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		platform.Fatal(log, "gateway: AUTH_REGISTER_MIN_RESPONSE is not a positive duration such as 2s")
	}
	return d
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

// mustParseIssuers parses AUTH_ADDITIONAL_ISSUERS and stops boot on a malformed value.
func mustParseIssuers(raw string) []auth.TrustedIssuer {
	issuers, err := auth.ParseTrustedIssuers(raw)
	if err != nil {
		platform.Fatal(slog.Default(), "gateway: AUTH_ADDITIONAL_ISSUERS: %v", err)
	}
	return issuers
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		// slog.Default(): mustEnv runs inside argument lists where app.Logger is not in scope.
		platform.Fatal(slog.Default(), "gateway: %s is required", key)
	}
	return v
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
