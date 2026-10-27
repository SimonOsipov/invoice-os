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

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// routedServices are the seven context services the gateway fronts, in wedge
// order. Each has a corresponding <NAME>_URL env var giving its Cloud Run base URL.
var routedServices = []string{
	"tenancy", "portfolio", "invoice", "validation",
	"submission", "dashboard", "notifications",
}

// probedServices are reached by /healthz/fleet but get NO public proxy route:
// none of them has a public domain, so the roll-up is CI's only view of them, and
// nothing outside the private network should be able to call them.
// TestGatewayHandlersPublishNoProxyRouteForAProbedService holds that line.
var probedServices = []string{"docling", "auth", "worker"}

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("gateway")
	if err != nil {
		platform.Fatal(slog.Default(), "gateway: startup: %v", err)
	}

	additional := mustParseIssuers(os.Getenv("AUTH_ADDITIONAL_ISSUERS"))
	siteURL := mustParseSiteURL(os.Getenv("AUTH_SITE_URL"), app.Logger)
	registerMinResponse := mustParseRegisterMinResponse(os.Getenv("AUTH_REGISTER_MIN_RESPONSE"), app.Logger)
	gatewayToken := mustEnv("GATEWAY_TOKEN")

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

	gwPool := gatewayPool(app.Logger)
	sessions := gateway.NewSessionChecker(probed["auth"], &http.Client{Timeout: gateway.SessionCheckTimeout, Transport: platform.IDTokenTransport(nil)}, time.Now, app.Logger)
	sessions.Shared(gwPool)
	apiHandler, fleetHandler := gatewayHandlers(verifier, sessions, routed, probed, map[string]string{"auth": ".well-known/jwks.json"}, app.Logger, gatewayToken)
	app.Mux.Handle(routePrefix, withCORS(apiHandler))

	// Fleet-health roll-up, outside /api/ and the verifier. It needs the gateway token: each call
	// wakes every scaled-to-zero service.
	app.Mux.Handle("GET /healthz/fleet", platform.RequireToken(platform.HeaderGatewayToken, gatewayToken)(fleetHandler))

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
	sink := registrantSink(gwPool, gateway.NewHTTPContactSink(routed["notifications"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken))

	pending := gateway.NewHTTPPendingInviteLookup(routed["tenancy"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)

	// Public registration, outside /api/ and the verifier, in every build. Register is
	// CORS-wrapped for the landing page; the OPTIONS route stops the POST route 405ing the preflight.
	reg := registrationHandlers(probed["auth"], siteURL, registerMinResponse, app.Logger, sink, pending, gwPool)
	app.Mux.Handle("POST /auth/register", withCORS(reg.Register))
	app.Mux.Handle("OPTIONS /auth/register", withCORS(reg.Register))
	app.Mux.Handle("POST /auth/resend-verification", withCORS(reg.ResendVerification))
	app.Mux.Handle("OPTIONS /auth/resend-verification", withCORS(reg.ResendVerification))
	app.Mux.Handle("POST /auth/request-password-reset", withCORS(reg.RequestPasswordReset))
	app.Mux.Handle("OPTIONS /auth/request-password-reset", withCORS(reg.RequestPasswordReset))
	app.Mux.Handle("GET /auth/reset-password", gateway.ResetPasswordPageHandler(siteURL))
	app.Mux.Handle("GET /auth/verify", verifyPage)
	previewer := gateway.NewHTTPInvitationPreviewer(routed["tenancy"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)
	registrations := gateway.NewHTTPInvitationRegistrations(routed["tenancy"], &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken)
	invitation, inviteeRegister := invitationHandlers(probed["auth"], siteURL, registerMinResponse, previewer, registrations, app.Logger)
	app.Mux.Handle("POST /auth/invitation", withCORS(invitation))
	app.Mux.Handle("OPTIONS /auth/invitation", withCORS(invitation))
	app.Mux.Handle("POST /auth/invitation/register", withCORS(inviteeRegister))
	app.Mux.Handle("OPTIONS /auth/invitation/register", withCORS(inviteeRegister))
	inviteResend := invitationResendHandler(probed["auth"], siteURL, reg.ResendByAddress, previewer, app.Logger)
	app.Mux.Handle("POST /auth/invitation/resend", withCORS(inviteResend))
	app.Mux.Handle("OPTIONS /auth/invitation/resend", withCORS(inviteResend))
	app.Mux.Handle("POST /contacts/demo-request", withCORS(reg.DemoRequest))
	app.Mux.Handle("OPTIONS /contacts/demo-request", withCORS(reg.DemoRequest))
	blogRead := blogHandlers(routed["notifications"], gatewayToken, app.Logger)
	app.Mux.Handle("GET /blog/categories", withCORS(blogRead.Categories))
	app.Mux.Handle("OPTIONS /blog/categories", withCORS(blogRead.Categories))
	app.Mux.Handle("GET /blog/posts", withCORS(blogRead.Posts))
	app.Mux.Handle("OPTIONS /blog/posts", withCORS(blogRead.Posts))
	app.Mux.Handle("GET /blog/posts/{slug}", withCORS(blogRead.Post))
	app.Mux.Handle("OPTIONS /blog/posts/{slug}", withCORS(blogRead.Post))
	app.Mux.Handle("GET /blog/covers/{id}/{version}", blogRead.Cover)
	app.Mux.Handle("GET /blog/posts/", withCORS(blogRead.NotFound))
	app.Mux.Handle("GET /blog/covers/", blogRead.NotFound)
	blogSubscribe := blogSubscribeHandlers(routed["notifications"], gatewayToken, registerMinResponse, app.Logger, gwPool)
	app.Mux.Handle("POST /blog/subscriptions", withCORS(blogSubscribe.Subscribe))
	app.Mux.Handle("OPTIONS /blog/subscriptions", withCORS(blogSubscribe.Subscribe))
	app.Mux.Handle("POST /blog/subscriptions/confirm", withCORS(blogSubscribe.Confirm))
	app.Mux.Handle("OPTIONS /blog/subscriptions/confirm", withCORS(blogSubscribe.Confirm))

	// Public sign-in hand-off, renewal and sign-out, outside the verifier; console-handoff verifies its own bearer.
	// The OPTIONS route stops the method-scoped POST from 405ing the preflight.
	store := handoffStore(gwPool, app.Logger)
	h := handoffHandlers(probed["auth"], siteURL, sessions, app.Logger, sink, verifier, store, gwPool)
	app.Mux.Handle("POST /auth/verify", h.Verify)
	app.Mux.Handle("POST /auth/sign-in", withCORS(h.SignIn))
	app.Mux.Handle("OPTIONS /auth/sign-in", withCORS(h.SignIn))
	app.Mux.Handle("POST /auth/exchange", withCORS(h.Exchange))
	app.Mux.Handle("OPTIONS /auth/exchange", withCORS(h.Exchange))
	app.Mux.Handle("POST /auth/console-handoff", withCORS(h.ConsoleHandoff))
	app.Mux.Handle("OPTIONS /auth/console-handoff", withCORS(h.ConsoleHandoff))
	exportCode := verifier.Middleware(sessions.Middleware(gateway.ExportCodeHandler(store, app.Logger)))
	app.Mux.Handle("POST /auth/export-code", withCORS(exportCode))
	app.Mux.Handle("OPTIONS /auth/export-code", withCORS(exportCode))
	// A form navigation: no CORS, no bearer; the redeemed code supplies the token.
	app.Mux.Handle("POST /auth/export-download", gateway.ExportDownloadHandler(store, apiHandler, app.Logger))
	app.Mux.Handle("POST /auth/refresh", withCORS(h.Refresh))
	app.Mux.Handle("OPTIONS /auth/refresh", withCORS(h.Refresh))
	app.Mux.Handle("POST /auth/sign-out", withCORS(h.SignOut))
	app.Mux.Handle("OPTIONS /auth/sign-out", withCORS(h.SignOut))
	app.Mux.Handle("POST /auth/reset-password", resetPasswordHandler(probed["auth"], siteURL, sessions, h.SignInThrottle, app.Logger))
	app.Mux.Handle("POST /auth/invitation/password", h.InvitationPassword)

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
		app.Mux.Handle("POST /auth/mock/member", mockMemberRoute(os.Getenv("DATABASE_MIGRATION_URL"), app.Logger))
		app.Mux.Handle("POST /auth/mock/invitation-token", mockInvitationTokenRoute(os.Getenv("DATABASE_MIGRATION_URL"), app.Logger))
		app.Mux.Handle("POST /auth/mock/deletion-link-token", mockDeletionLinkTokenRoute(os.Getenv("DATABASE_MIGRATION_URL"), app.Logger))
		platform.MockIssuer = "on"
	}

	if err := app.Run(context.Background()); err != nil {
		platform.Fatal(app.Logger, "gateway: %v", err)
	}
}

// routePrefix must match the gateway package's mount point.
const routePrefix = "/api/"

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
	Register, DemoRequest, ResendVerification, RequestPasswordReset http.Handler
	// ResendByAddress is the resend budget that ResendVerification and RequestPasswordReset share; nil when unconfigured.
	ResendByAddress *gateway.SignInThrottle
}

// invitationHandlers builds the accept-page preview handler and the invitee-registration handler.
// Registration answers 503 while any of authURL, siteURL, registrations.Claim or registrations.Release is nil; the preview needs none of them.
func invitationHandlers(authURL, siteURL *url.URL, minResponse time.Duration, preview gateway.InvitationPreviewer, registrations gateway.InvitationRegistrations, log *slog.Logger) (invitation, register http.Handler) {
	invitation = gateway.InvitationHandler(preview, log)
	if authURL == nil || siteURL == nil || registrations.Claim == nil || registrations.Release == nil {
		return invitation, gateway.RegistrationNotConfigured()
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(nil),
	}
	return invitation, gateway.InvitationRegisterHandler(authURL, client, minResponse, log, preview, registrations)
}

// invitationResendHandler builds POST /auth/invitation/resend on the resend budget that ResendVerification shares.
// It answers 503 while any of authURL, siteURL or the throttle is nil.
func invitationResendHandler(authURL, siteURL *url.URL, perAddress *gateway.SignInThrottle, preview gateway.InvitationPreviewer, log *slog.Logger) http.Handler {
	if authURL == nil || siteURL == nil || perAddress == nil {
		return gateway.RegistrationNotConfigured()
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(nil),
	}
	enforce := platform.Posture(platform.DeployEnv()) != platform.PosturePreview
	return gateway.InvitationResendHandler(authURL, client, perAddress, enforce, log, preview)
}

// newJWKSClient builds the JWKS fetch client.
func newJWKSClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: platform.TraceTransport(nil)}
}

// registrationHandlers builds the registration handlers against GoTrue at authURL.
// A nil authURL or siteURL (AUTH_SITE_URL unset) makes Register, ResendVerification and RequestPasswordReset answer 503 (TestRegistrationHandlers_NotConfigured503). A nil pending lookup makes Register alone answer 503 (TestRegistrationHandlers_NilPendingLookupIsNotConfigured).
// On a PR preview the per-address limit (resend, reset) logs but does not refuse: a preview sends no mail (TestRegistrationHandlers_PreviewOnlyLogs).
func registrationHandlers(authURL, siteURL *url.URL, minResponse time.Duration, log *slog.Logger, sink gateway.ContactSink, pending gateway.PendingInviteLookup, pool *pgxpool.Pool) registration {
	if authURL == nil || siteURL == nil {
		nc := gateway.RegistrationNotConfigured()
		return registration{Register: nc, ResendVerification: nc, RequestPasswordReset: nc, DemoRequest: gateway.DemoRequestHandler(sink, log)}
	}
	enforce := platform.Posture(platform.DeployEnv()) != platform.PosturePreview
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(nil),
	}
	// Resend and reset share one budget per address.
	perAddress := gateway.NewSignInThrottle("resend-address", gateway.ResendPerAddress, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	perAddress.Shared(pool, log)
	return registration{
		Register:             gateway.RegisterHandler(authURL, client, minResponse, log, pending),
		ResendByAddress:      perAddress,
		ResendVerification:   gateway.ResendVerificationHandler(authURL, client, minResponse, perAddress, enforce, log),
		RequestPasswordReset: gateway.RequestPasswordResetHandler(authURL, client, minResponse, perAddress, enforce, log),
		DemoRequest:          gateway.DemoRequestHandler(sink, log),
	}
}

// blogHandlers builds the public blog reads.
func blogHandlers(notifications *url.URL, gatewayToken string, log *slog.Logger) gateway.BlogRoutes {
	return gateway.BlogHandlers(notifications, &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken, log)
}

// blogSubscribeHandlers builds the public newsletter routes; the per-email cap logs and admits on a PR fork.
func blogSubscribeHandlers(notifications *url.URL, gatewayToken string, minResponse time.Duration, log *slog.Logger, pool *pgxpool.Pool) gateway.BlogSubscribeRoutes {
	enforce := platform.Posture(platform.DeployEnv()) != platform.PosturePreview
	perEmail := gateway.NewSignInThrottle("blog-subscribe-email", gateway.BlogSubscribePerEmail, gateway.BlogSubscribeMaxKeys, gateway.BlogSubscribeWindow, time.Now)
	perEmail.Shared(pool, log)
	return gateway.BlogSubscribeHandlers(notifications, &http.Client{Transport: platform.TraceTransport(nil)}, gatewayToken, minResponse, perEmail, enforce, log)
}

// resetPasswordHandler builds the reset-form handler; a nil authURL or siteURL makes it answer 503.
func resetPasswordHandler(authURL, siteURL *url.URL, sessions *gateway.SessionChecker, signIn *gateway.SignInThrottle, log *slog.Logger) http.Handler {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(nil),
	}
	return gateway.ResetPasswordHandler(authURL, siteURL, client, sessions, signIn, log)
}

// gatewayPool connects as invoice_gateway when DATABASE_GATEWAY_URL is set, else nil.
// Cloud Run runs several instances and pauses idle ones, so it requires the URL there.
func gatewayPool(log *slog.Logger) *pgxpool.Pool {
	dsn := os.Getenv("DATABASE_GATEWAY_URL")
	if dsn == "" {
		if os.Getenv("K_SERVICE") != "" {
			platform.Fatal(log, "gateway: DATABASE_GATEWAY_URL is required on Cloud Run")
		}
		return nil
	}
	pool, err := db.NewPool(context.Background(), dsn)
	if err != nil {
		platform.Fatal(log, "gateway: database pool: %v", err)
	}
	return pool
}

// handoffStore shares hand-off codes through Postgres when pool is set, else keeps them in-process.
func handoffStore(pool *pgxpool.Pool, log *slog.Logger) gateway.Handoffs {
	if pool == nil {
		return gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)
	}
	return gateway.NewPGHandoffStore(pool, gateway.HandoffTTL, log)
}

// registrantSink writes registrants to the outbox when pool is set; without it they go to sink in the background.
func registrantSink(pool *pgxpool.Pool, sink gateway.ContactSink) gateway.ContactSink {
	if pool == nil {
		return sink
	}
	return gateway.NewRegistrantOutbox(pool, sink)
}

// handoff holds the public sign-in hand-off, renewal and sign-out handlers main mounts outside /api/.
type handoff struct {
	SignIn, Exchange, Refresh, SignOut, Verify, InvitationPassword, ConsoleHandoff http.Handler
	SignInThrottle                                                                 *gateway.SignInThrottle
}

// handoffHandlers builds the sign-in, exchange, refresh and sign-out handlers against GoTrue at authURL.
// Sign-out evicts from sessions, the API's own checker.
// Every minter and exchange share one code store: a sign-in code is redeemable only through exchange; an export code only through export-download.
// A nil siteURL makes Verify answer 503 (TestRegistrationHandlers_NotConfigured503).
// A nil store means the in-process one; a nil pool keeps the sign-in throttle in-process.
func handoffHandlers(authURL, siteURL *url.URL, sessions *gateway.SessionChecker, log *slog.Logger, sink gateway.ContactSink, verifier *auth.Verifier, store gateway.Handoffs, pool *pgxpool.Pool) handoff {
	if store == nil {
		store = gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)
	}
	throttle := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	throttle.Shared(pool, log)
	// Same settings as registrationHandlers; TestRegistrationClientTimeoutAndNoFollow pins that literal in place.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     platform.IDTokenTransport(nil),
	}
	return handoff{
		SignIn:   gateway.SignInHandler(authURL, client, store, throttle, log, sink),
		Exchange: gateway.ExchangeHandler(store),
		Refresh:  gateway.RefreshHandler(authURL, client, log),
		SignOut:  gateway.SignOutHandler(authURL, client, sessions, log),
		Verify:   gateway.VerifyHandler(authURL, siteURL, client, log, sink, store),

		InvitationPassword: gateway.InvitationPasswordHandler(authURL, siteURL, client, sessions, throttle, sink, log, store),
		ConsoleHandoff:     verifier.Middleware(sessions.Middleware(auth.RequireStaff(gateway.ConsoleHandoffHandler(store, log)))),

		SignInThrottle: throttle,
	}
}

// mustParseSiteURL parses AUTH_SITE_URL. Unset is allowed and logged; a value that is not
// an absolute http(s) URL, or carries user info, a query or a fragment, stops boot.
func mustParseSiteURL(raw string, log *slog.Logger) *url.URL {
	if raw == "" {
		log.Warn("gateway: AUTH_SITE_URL is unset; /auth/register, /auth/resend-verification, /auth/request-password-reset, /auth/invitation/resend, GET and POST /auth/verify and /auth/reset-password answer 503")
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
