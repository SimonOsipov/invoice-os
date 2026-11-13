package gateway

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRLS_GatewayLinkRedeemCountIsSharedAndHashed(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	const ip = "203.0.113.7"
	sum := sha256.Sum256([]byte(ip))
	clean := func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE scope = 'link-redeem-ip' AND key_hash = $1`, sum[:])
	}
	clean()
	t.Cleanup(clean)
	build := func(pool *pgxpool.Pool) *SignInThrottle {
		th := lrThrottle(time.Now)
		th.Shared(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
		return th
	}
	a, b := build(d.a), build(d.b)
	for i := range LinkRedeemPerIP {
		th := a
		if i%2 == 1 {
			th = b
		}
		if held, err := th.Admit(ip); !held || err != nil {
			t.Fatalf("Admit #%d = %v, %v, want true", i+1, held, err)
		}
	}
	for name, th := range map[string]*SignInThrottle{"A": a, "B": b} {
		if held, err := th.Admit(ip); held || err != nil {
			t.Errorf("31st Admit on %s = %v, %v, want refused: the count is not shared", name, held, err)
		}
	}

	var row string
	var minutes float64
	if err := d.super.QueryRow(context.Background(), `SELECT t::text, extract(epoch FROM expires_at - now()) / 60 FROM gateway_throttle_counts t
		WHERE scope = 'link-redeem-ip' AND key_hash = $1`, sum[:]).Scan(&row, &minutes); err != nil {
		t.Fatalf("no row of scope link-redeem-ip keyed by sha256(%q): %v", ip, err)
	}
	if strings.Contains(row, ip) {
		t.Errorf("a column holds the address text: %s", row)
	}
	if minutes < 59 || minutes > 60.01 {
		t.Errorf("the count expires in %.2f min, want a 1 h window", minutes)
	}
}
