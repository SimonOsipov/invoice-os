// gateway_handoff_codes is global (no tenant, no RLS). The guarantees here are single-use
// redemption across instances, expiry, the cap, and that only invoice_gateway reaches it.
// CI runs these in the RLS job, where DATABASE_GATEWAY_URL is set.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type handoffDB struct {
	a, b, super, app *pgxpool.Pool // a and b stand in for two gateway instances
}

func requireHandoffDB(t *testing.T) handoffDB {
	t.Helper()
	gw, super, app := os.Getenv("DATABASE_GATEWAY_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_URL")
	if gw == "" || super == "" || app == "" {
		t.Skip("set DATABASE_GATEWAY_URL, DATABASE_SUPERUSER_URL and DATABASE_URL (or run `make test-rls`)")
	}
	open := func(url string) *pgxpool.Pool {
		p, err := pgxpool.New(context.Background(), url)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(p.Close)
		return p
	}
	return handoffDB{a: open(gw), b: open(gw), super: open(super), app: open(app)}
}

func handoffKey(t *testing.T, d handoffDB) [32]byte {
	t.Helper()
	var k [32]byte
	rand.Read(k[:])
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_handoff_codes WHERE code_hash = $1`, k[:])
	})
	return k
}

func expire(t *testing.T, d handoffDB, k [32]byte) {
	t.Helper()
	if _, err := d.super.Exec(context.Background(), `UPDATE gateway_handoff_codes SET expires_at = now() - interval '1 second' WHERE code_hash = $1`, k[:]); err != nil {
		t.Fatal(err)
	}
}

func rowCount(t *testing.T, d handoffDB, k [32]byte) int {
	t.Helper()
	var n int
	if err := d.super.QueryRow(context.Background(), `SELECT count(*) FROM gateway_handoff_codes WHERE code_hash = $1`, k[:]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRLS_GatewayHandoff_AStoreOnOneInstanceRedeemsOnceOnAnother(t *testing.T) {
	d := requireHandoffDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	minter, redeemer := NewPGHandoffStore(d.a, HandoffTTL, log), NewPGHandoffStore(d.b, HandoffTTL, log)
	code, ok := minter.Put(`{"answer":1}`, sha256.Sum256([]byte("state")))
	if !ok {
		t.Fatal("Put on instance A failed")
	}
	t.Cleanup(func() {
		h := sha256.Sum256([]byte(code))
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_handoff_codes WHERE code_hash = $1`, h[:])
	})
	if answer, ok := redeemer.Take(code, "state"); !ok || answer != `{"answer":1}` {
		t.Fatalf("Take on instance B = (%q, %v), want the stored answer", answer, ok)
	}
	for _, s := range []*PGHandoffStore{minter, redeemer} {
		if _, ok := s.Take(code, "state"); ok {
			t.Error("second Take succeeded: a code is single-use across instances")
		}
	}
}

func TestRLS_GatewayHandoff_AWrongStateSpendsTheCode(t *testing.T) {
	d := requireHandoffDB(t)
	s := NewPGHandoffStore(d.a, HandoffTTL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	code, ok := s.Put("x", sha256.Sum256([]byte("state")))
	if !ok {
		t.Fatal("Put failed")
	}
	if _, ok := s.Take(code, "other"); ok {
		t.Fatal("Take with the wrong state succeeded")
	}
	if _, ok := s.Take(code, "state"); ok {
		t.Error("Take with the right state succeeded after a wrong-state attempt; the attempt must spend the code")
	}
}

func TestRLS_GatewayHandoff_AnExpiredCodeIsSpentAndRefused(t *testing.T) {
	d := requireHandoffDB(t)
	ctx := context.Background()
	k := handoffKey(t, d)
	if ok, err := putHandoff(ctx, d.a, k, k, "x", time.Minute, HandoffMaxLive); !ok || err != nil {
		t.Fatalf("putHandoff = (%v, %v)", ok, err)
	}
	expire(t, d, k)
	if _, _, ok, err := takeHandoff(ctx, d.a, k); ok || err != nil {
		t.Fatalf("takeHandoff(expired) = (ok %v, err %v), want (false, nil)", ok, err)
	}
	if n := rowCount(t, d, k); n != 0 {
		t.Errorf("expired row left after redeem: %d", n)
	}
}

func TestRLS_GatewayHandoff_AFullStoreRefusesAndSweepsExpiredRows(t *testing.T) {
	d := requireHandoffDB(t)
	ctx := context.Background()
	var live int
	if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_handoff_codes WHERE expires_at > now()`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	first, second := handoffKey(t, d), handoffKey(t, d)
	if ok, err := putHandoff(ctx, d.a, first, first, "x", time.Minute, live+1); !ok || err != nil {
		t.Fatalf("putHandoff under the cap = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := putHandoff(ctx, d.a, second, second, "x", time.Minute, live+1); ok || err != nil {
		t.Fatalf("putHandoff at the cap = (%v, %v), want (false, nil)", ok, err)
	}
	expire(t, d, first)
	if ok, err := putHandoff(ctx, d.a, second, second, "x", time.Minute, live+1); !ok || err != nil {
		t.Fatalf("putHandoff after expiry = (%v, %v), want (true, nil)", ok, err)
	}
	if n := rowCount(t, d, first); n != 0 {
		t.Errorf("expired row not swept by putHandoff: %d", n)
	}
}

func TestRLS_GatewayHandoff_OnlyTheGatewayRoleReachesTheTable(t *testing.T) {
	d := requireHandoffDB(t)
	ctx := context.Background()
	denied := func(who string, p *pgxpool.Pool, sql string) {
		t.Helper()
		if _, err := p.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s %q: err %v, want permission denied", who, sql, err)
		}
	}
	denied("invoice_app", d.app, `SELECT 1 FROM gateway_handoff_codes LIMIT 1`)
	denied("invoice_gateway", d.a, `SELECT 1 FROM tenants LIMIT 1`)
	denied("invoice_gateway", d.a, `UPDATE gateway_handoff_codes SET answer = answer`)
	var super, bypass bool
	if err := d.super.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = 'invoice_gateway'`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Errorf("invoice_gateway rolsuper %v rolbypassrls %v (err %v), want both false", super, bypass, err)
	}
}
