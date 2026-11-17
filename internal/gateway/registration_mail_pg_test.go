package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func mailScope(t *testing.T, d handoffDB) string {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	scope := "rm-" + hex.EncodeToString(b)
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE scope LIKE $1`, scope+"-%")
	})
	return scope
}

func sharedMail(d handoffDB, scope string, perHour, perDay int, now func() time.Time, onB bool) *RegistrationMail {
	m := NewRegistrationMail(scope, perHour, perDay, now)
	pool := d.a
	if onB {
		pool = d.b
	}
	m.Shared(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m
}

func mailRows(t *testing.T, d handoffDB, scope string) map[string]int {
	t.Helper()
	rows, err := d.super.Query(context.Background(), `SELECT scope || '/' || encode(key_hash, 'hex'), n FROM gateway_throttle_counts WHERE scope LIKE $1`, scope+"-%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		out[k] = n
	}
	return out
}

func mailKey(scope, key string) string {
	h := sha256.Sum256([]byte(key))
	return scope + "/" + hex.EncodeToString(h[:])
}

func TestRLS_GatewayRegistrationMail_SharedHourWindowTurns(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	m := sharedMail(d, scope, 10, 100, time.Now, false)
	for i := range 10 {
		if spent, err := m.Admit(); spent != "" || err != nil {
			t.Fatalf("Admit %d = (%q, %v)", i+1, spent, err)
		}
	}
	if spent, _ := m.Admit(); spent != "hour" {
		t.Fatalf("11th Admit = %q, want hour", spent)
	}
	if _, err := d.super.Exec(context.Background(), `UPDATE gateway_throttle_counts SET expires_at = now() - interval '1 second' WHERE scope = $1`, scope+"-hour"); err != nil {
		t.Fatal(err)
	}
	if spent, err := m.Admit(); spent != "" || err != nil {
		t.Fatalf("Admit after the window turned = (%q, %v)", spent, err)
	}
	if n := mailRows(t, d, scope)[mailKey(scope+"-hour", "all")]; n != 1 {
		t.Errorf("hour row n = %d, want 1", n)
	}
}

func TestRLS_GatewayRegistrationMail_SharedDayKeyTurnsAtUTCMidnight(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	c := newMailClock(time.Date(2026, 11, 17, 23, 59, 0, 0, time.UTC))
	m := sharedMail(d, scope, 100, 1, c.Now, false)
	if spent, err := m.Admit(); spent != "" || err != nil {
		t.Fatalf("first Admit = (%q, %v)", spent, err)
	}
	if spent, _ := m.Admit(); spent != "day" {
		t.Fatalf("second Admit = %q, want day", spent)
	}
	c.Set(time.Date(2026, 11, 18, 0, 0, 1, 0, time.UTC))
	if spent, err := m.Admit(); spent != "" || err != nil {
		t.Fatalf("Admit after midnight = (%q, %v)", spent, err)
	}
	rows := mailRows(t, d, scope)
	for _, day := range []string{"day:2026-11-17", "day:2026-11-18"} {
		if n := rows[mailKey(scope+"-day", day)]; n != 1 {
			t.Errorf("%s row n = %d, want 1 (rows %v)", day, n, rows)
		}
	}
}

func TestRLS_GatewayRegistrationMail_TwoInstancesShareOneCount(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	a, b := sharedMail(d, scope, 10, 100, time.Now, false), sharedMail(d, scope, 10, 100, time.Now, true)
	for i := range 10 {
		m := a
		if i%2 == 1 {
			m = b
		}
		if spent, err := m.Admit(); spent != "" || err != nil {
			t.Fatalf("Admit %d = (%q, %v)", i+1, spent, err)
		}
	}
	for name, m := range map[string]*RegistrationMail{"a": a, "b": b} {
		if spent, _ := m.Admit(); spent != "hour" {
			t.Errorf("11th Admit on %s = %q, want hour", name, spent)
		}
	}
	if n := mailRows(t, d, scope)[mailKey(scope+"-hour", "all")]; n != 10 {
		t.Errorf("hour row n = %d, want 10", n)
	}
}

func TestRLS_GatewayRegistrationMail_SharedRowsHoldNoAddress(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	c := newMailClock(time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC))
	m := sharedMail(d, scope, 10, 30, c.Now, false)
	if spent, err := m.Admit(); spent != "" || err != nil {
		t.Fatalf("Admit = (%q, %v)", spent, err)
	}
	m.Refund()
	rows := mailRows(t, d, scope)
	want := []string{mailKey(scope+"-hour", "all"), mailKey(scope+"-day", "day:2026-11-17")}
	got := make([]string, 0, len(rows))
	for k, n := range rows {
		got = append(got, k)
		if n != 0 {
			t.Errorf("row %s n = %d, want 0", k, n)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
