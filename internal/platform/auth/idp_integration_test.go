package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// idpURL returns the idp-es256 base URL. The CI idp job runs these through
// rls-test-gate.sh, so a skip there fails the build.
func idpURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("IDP_ES256_URL")
	if u == "" {
		t.Skip("IDP_ES256_URL unset; run `make test-idp` or the CI idp job")
	}
	return strings.TrimRight(u, "/")
}

// requireEnv fails rather than skips: once a container is declared, a half-set env is a harness defect.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("IDP_ES256_URL is set but %s is not", name)
	}
	return v
}

type idpHealth struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

func getHealth(t *testing.T, base string) idpHealth {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v (nothing answers at IDP_ES256_URL)", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/health: status %d, want 200", base, resp.StatusCode)
	}
	var h idpHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("GET %s/health: body is not JSON: %v", base, err)
	}
	return h
}

func TestIdP_HealthReportsPinnedTag(t *testing.T) {
	base := idpURL(t)
	want := requireEnv(t, "IDP_PINNED_TAG")

	h := getHealth(t, base)
	t.Logf("measured /health: version=%q name=%q", h.Version, h.Name)
	if h.Name != "GoTrue" {
		t.Errorf("/health name = %q, want \"GoTrue\"", h.Name)
	}
	if h.Version != want {
		t.Errorf("/health version = %q, want the pinned tag %q", h.Version, want)
	}
}

func TestIdP_ProviderConnectsAsAuthAdmin(t *testing.T) {
	base := idpURL(t)
	dsn := requireEnv(t, "DATABASE_SUPERUSER_URL")
	getHealth(t, base)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	defer conn.Close(ctx)

	var owner string
	err = conn.QueryRow(ctx,
		`SELECT tableowner FROM pg_tables WHERE schemaname = 'auth' AND tablename = 'users'`).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("auth.users does not exist; GoTrue never ran its migrations against this database")
	}
	if err != nil {
		t.Fatalf("read auth.users owner: %v", err)
	}
	if owner != "supabase_auth_admin" {
		t.Errorf("auth.users is owned by %q, want supabase_auth_admin (the provider connected as the wrong role)", owner)
	}

	// GoTrue's ledger is unqualified, so it lands wherever search_path points.
	var inAuth, inPublic bool
	err = conn.QueryRow(ctx,
		`SELECT to_regclass('auth.schema_migrations') IS NOT NULL, to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&inAuth, &inPublic)
	if err != nil {
		t.Fatalf("look up schema_migrations: %v", err)
	}
	if !inAuth {
		t.Error("auth.schema_migrations does not exist; GoTrue's migration ledger is not under search_path = auth")
	}
	if inPublic {
		t.Error("public.schema_migrations exists; GoTrue ran with a search_path that reaches public")
	}
}
