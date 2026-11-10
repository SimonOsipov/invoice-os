package gateway

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestRLS_GatewayReadinessCheckIPCountIsSharedAndHashed(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	const ip = "203.0.113.7"
	sum := sha256.Sum256([]byte(ip))
	clean := func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE scope = 'readiness-check-ip' AND key_hash = $1`, sum[:])
	}
	clean()
	t.Cleanup(clean)
	a := instance("readiness-check-ip", ReadinessCheckPerIP, d.a)
	b := instance("readiness-check-ip", ReadinessCheckPerIP, d.b)
	for i := range ReadinessCheckPerIP {
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
			t.Errorf("11th Admit on %s = %v, %v, want refused: the count is not shared", name, held, err)
		}
	}

	var row string
	if err := d.super.QueryRow(context.Background(), `SELECT t::text FROM gateway_throttle_counts t WHERE scope = 'readiness-check-ip' AND key_hash = $1`, sum[:]).Scan(&row); err != nil {
		t.Fatalf("no row keyed by sha256(%q): %v", ip, err)
	}
	if strings.Contains(row, ip) {
		t.Errorf("a column holds the address text: %s", row)
	}
}
