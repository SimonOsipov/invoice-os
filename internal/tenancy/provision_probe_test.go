package tenancy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cancelOnProbe cancels the request context when the removed-row probe starts.
type cancelOnProbe struct{ cancel context.CancelFunc }

func (c cancelOnProbe) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "status = 'removed'") {
		c.cancel()
	}
	return ctx
}

func (cancelOnProbe) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestProvision_FailedRemovedRowProbeIs500NotAlreadyProvisioned(t *testing.T) {
	r := removedCreator(t)
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(r.ctx())
	defer cancel()
	cfg.ConnConfig.Tracer = cancelOnProbe{cancel}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)
	tenantsBefore := tenantCount(t, r.super)

	if _, _, err := store.ProvisionWorkspace(ctx, ProvisionInput{WorkspaceName: "Second Works", DisplayName: "Ada Again"}); err == nil || err == ErrAlreadyProvisioned {
		t.Fatalf("err = %v, want the probe's read error, not ErrAlreadyProvisioned", err)
	}

	ctx2, cancel2 := context.WithCancel(r.ctx())
	defer cancel2()
	cfg.ConnConfig.Tracer = cancelOnProbe{cancel2}
	pool2, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool2.Close)
	var logs syncBuf
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/workspaces", strings.NewReader(`{"workspace_name":"Second Works","display_name":"Ada Again"}`)).WithContext(ctx2)
	ProvisionHandler(NewStore(pool2).ProvisionWorkspace, slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, req)
	assertErrorBody(t, rec, http.StatusInternalServerError, "internal server error")
	for _, leak := range []string{"Second Works", "Ada Again", r.id.Email} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("log carries %q: %s", leak, logs.String())
		}
	}
	if got := tenantCount(t, r.super); got != tenantsBefore {
		t.Errorf("tenants %d -> %d, want no new workspace", tenantsBefore, got)
	}
}
