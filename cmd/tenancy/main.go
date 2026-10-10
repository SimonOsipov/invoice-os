// Command tenancy is the 01 Tenancy context service. It serves the platform kit's
// /healthz + /readyz plus GET /v1/me — the first endpoint that reads real data:
// it resolves the gateway-injected caller to their tenant through an RLS-scoped
// query (M2-13, the mock-login round trip's server side).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("tenancy")
	if err != nil {
		platform.Fatal(slog.Default(), "tenancy: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — a
	// tenancy service that cannot reach its database is misconfigured, not degraded.
	// pgxpool.New is lazy (it connects on first use), so an unreachable DB surfaces
	// via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "tenancy: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/tenancy/v1/ping). Real endpoints live
	// alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"tenancy","status":"ok"}`))
	})

	// GET /v1/me — the caller's tenant + user, resolved under RLS. Reached via the
	// gateway as /api/tenancy/v1/me (the prefix is stripped upstream).
	store := tenancy.NewStore(pool)
	app.Mux.HandleFunc("GET /v1/me", tenancy.MeHandler(store.Me, app.Logger))

	// GET /v1/memberships — the caller's tenant's membership list, resolved under
	// RLS. Reached via the gateway as /api/tenancy/v1/memberships.
	app.Mux.HandleFunc("GET /v1/memberships", tenancy.MembershipsHandler(store.ListMemberships, app.Logger))

	// PATCH /v1/memberships/{user_id} — admin-only suspend/reactivate. The
	// gateway proxies it unchanged: its mount carries no method.
	app.Mux.HandleFunc("PATCH /v1/memberships/{user_id}", tenancy.SetMembershipStatusHandler(store.SetMembershipStatus, app.Logger))

	// POST /v1/workspaces — a tenant-less caller provisions their first workspace;
	// the gateway lets a token without a tenant reach this route only.
	app.Mux.HandleFunc("POST /v1/workspaces", tenancy.ProvisionHandler(store.ProvisionWorkspace, app.Logger))

	// Invite routes: the sender is chosen once at boot from the environment.
	inviter := &tenancy.Inviter{
		Store:  store,
		Sender: inviteSender(os.Getenv, http.DefaultTransport.(*http.Transport).Clone(), app.Logger),
		Logger: app.Logger,
	}
	app.Mux.HandleFunc("POST /v1/invitations", tenancy.InvitationsCreateHandler(inviter.Invite, app.Logger))
	app.Mux.HandleFunc("GET /v1/invitations", tenancy.InvitationsListHandler(store.ListInvitations, app.Logger))
	app.Mux.HandleFunc("POST /v1/invitations/{id}/resend", tenancy.InvitationResendHandler(inviter.Resend, app.Logger))
	// The invitee holds no membership (or no tenant) yet: accept is the caller's own act; preview is token-only.
	app.Mux.HandleFunc("POST /v1/invitations/accept", tenancy.AcceptInvitationHandler(store.AcceptInvitation, app.Logger))
	app.Mux.HandleFunc("POST /internal/invitations/preview", tenancy.InvitationPreviewHandler(store.PreviewInvitation, app.Logger))
	app.Mux.HandleFunc("POST /internal/invitations/pending", tenancy.InvitationPendingHandler(store.InvitationPendingForEmail, app.Logger))
	app.Mux.HandleFunc("POST /internal/invitations/register", tenancy.InvitationRegisterClaimHandler(store.ClaimInvitationRegistration, app.Logger))
	app.Mux.HandleFunc("POST /internal/invitations/release", tenancy.InvitationRegisterReleaseHandler(store.ReleaseInvitationRegistration, app.Logger))

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"))

	if err := app.Run(context.Background()); err != nil {
		platform.Fatal(app.Logger, "tenancy: %v", err)
	}
}

// inviteSender picks the invite mail sender: capture in a preview, Resend with a key, else off.
func inviteSender(getenv func(string) string, rt http.RoundTripper, logger *slog.Logger) accountmail.Sender {
	mode, key := accountmail.ModeFromEnv(getenv, platform.Posture(getenv("RAILWAY_ENVIRONMENT_NAME")) == platform.PosturePreview)
	logger.Info("tenancy: invite mail mode", slog.String("mode", string(mode)))
	return accountmail.NewSender(mode, key, rt)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "tenancy: %s is required", key)
	}
	return v
}
