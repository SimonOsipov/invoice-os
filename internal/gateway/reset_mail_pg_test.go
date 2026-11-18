package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

func sharedReset(d handoffDB, scope string, perHour, perDay, perIPHour, perIPDay int, now func() time.Time, onB bool) *ResetMail {
	m := NewResetMail(scope, perHour, perDay, perIPHour, perIPDay, now)
	pool := d.a
	if onB {
		pool = d.b
	}
	m.Shared(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m
}

func TestRLS_GatewayResetMail_TwoInstancesShareOneCount(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	a := sharedReset(d, scope, 3, 100, 100, 100, time.Now, false)
	b := sharedReset(d, scope, 3, 100, 100, 100, time.Now, true)
	for i := range 3 {
		m := a
		if i%2 == 1 {
			m = b
		}
		if _, spent, err := m.Hold(testIP(i + 1)); spent != "" || err != nil {
			t.Fatalf("Hold %d = (%q, %v)", i+1, spent, err)
		}
	}
	for name, m := range map[string]*ResetMail{"a": a, "b": b} {
		if _, spent, _ := m.Hold(testIP(50)); spent != "hour" {
			t.Errorf("4th Hold on %s = %q, want hour", name, spent)
		}
	}
	if n := mailRows(t, d, scope)[mailKey(scope+"-hour", "all")]; n != 3 {
		t.Errorf("hour row n = %d, want 3", n)
	}
}

func TestRLS_GatewayResetMail_ConcurrentResetsMakeExactlyThreeGoTrueCalls(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	fake := newFakeGoTrue(t, http.StatusOK, "{}")
	handlers := []http.Handler{
		resetHandlerOn(fake.URL, resendThrottle(time.Now), sharedReset(d, scope, ResetMailPerHour, ResetMailPerDay, ResetMailPerIPHour, ResetMailPerIPDay, time.Now, false)),
		resetHandlerOn(fake.URL, resendThrottle(time.Now), sharedReset(d, scope, ResetMailPerHour, ResetMailPerDay, ResetMailPerIPHour, ResetMailPerIPDay, time.Now, true)),
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := postResetFrom(t, handlers[i%2], testIP(i+1), "user"+testIP(i+1)+"@corp.example")
			if rec.Code != http.StatusAccepted {
				t.Errorf("reset %d = %d", i, rec.Code)
			}
		}()
	}
	wg.Wait()

	if n := callsTo(fake, "/recover"); n != ResetMailPerHour {
		t.Errorf("GoTrue saw %d /recover, want %d", n, ResetMailPerHour)
	}
	if n := mailRows(t, d, scope)[mailKey(scope+"-hour", "all")]; n != ResetMailPerHour {
		t.Errorf("hour row n = %d, want %d", n, ResetMailPerHour)
	}
}

func TestRLS_GatewayResetMail_ARefusalLeavesNoCount(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	m := sharedReset(d, scope, 10, 1, 10, 10, time.Now, false)
	if _, spent, err := m.Hold(testIP(1)); spent != "" || err != nil {
		t.Fatalf("Hold A = (%q, %v)", spent, err)
	}
	if _, spent, _ := m.Hold(testIP(2)); spent != "day" {
		t.Fatalf("Hold B = %q, want day", spent)
	}
	rows := mailRows(t, d, scope)
	for _, w := range []string{"-ip-hour", "-ip-day"} {
		if n := rows[mailKey(scope+w, testIP(2))]; n != 0 {
			t.Errorf("%s row for IP B n = %d, want 0", w, n)
		}
	}
	if n := rows[mailKey(scope+"-hour", "all")]; n != 1 {
		t.Errorf("hour row n = %d, want 1", n)
	}
}

func TestRLS_GatewayResetMail_ARefundAfterUTCMidnightReturnsTheHoldsDay(t *testing.T) {
	d := requireHandoffDB(t)
	scope := mailScope(t, d)
	c := newMailClock(time.Date(2026, 11, 17, 23, 59, 59, 0, time.UTC))
	m := sharedReset(d, scope, 10, 10, 10, 10, c.Now, false)
	first, spent, err := m.Hold(testIP(1))
	if spent != "" || err != nil {
		t.Fatalf("Hold 1 = (%q, %v)", spent, err)
	}
	c.Set(time.Date(2026, 11, 18, 0, 0, 1, 0, time.UTC))
	if _, spent, err := m.Hold(testIP(2)); spent != "" || err != nil {
		t.Fatalf("Hold 2 = (%q, %v)", spent, err)
	}
	first.Refund()

	rows := mailRows(t, d, scope)
	if n := rows[mailKey(scope+"-day", "day:2026-11-17")]; n != 0 {
		t.Errorf("day:2026-11-17 n = %d, want 0", n)
	}
	if n := rows[mailKey(scope+"-day", "day:2026-11-18")]; n != 1 {
		t.Errorf("day:2026-11-18 n = %d, want 1", n)
	}
}

func TestRLS_GatewayResetMail_RowsHoldNoAddressOrIPAndCountApart(t *testing.T) {
	d := requireHandoffDB(t)
	regScope, resetScope := mailScope(t, d), mailScope(t, d)
	c := newMailClock(time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC))
	reg := sharedMail(d, regScope, 10, 30, c.Now, false)
	reset := sharedReset(d, resetScope, 3, 20, 1, 3, c.Now, false)
	if spent, err := reg.Admit(); spent != "" || err != nil {
		t.Fatalf("Admit = (%q, %v)", spent, err)
	}
	if _, spent, err := reset.Hold("198.51.100.7"); spent != "" || err != nil {
		t.Fatalf("Hold = (%q, %v)", spent, err)
	}

	got := mailRows(t, d, resetScope)
	want := map[string]bool{
		mailKey(resetScope+"-hour", "all"):             true,
		mailKey(resetScope+"-day", "day:2026-11-17"):   true,
		mailKey(resetScope+"-ip-hour", "198.51.100.7"): true,
		mailKey(resetScope+"-ip-day", "198.51.100.7"):  true,
	}
	if len(got) != len(want) {
		t.Errorf("reset rows = %v, want keys %v", got, want)
	}
	for k, n := range got {
		if !want[k] || n != 1 {
			t.Errorf("reset row %s n=%d is not an expected hashed key", k, n)
		}
	}
	regRows := mailRows(t, d, regScope)
	if n := regRows[mailKey(regScope+"-hour", "all")]; n != 1 || len(regRows) != 2 {
		t.Errorf("registration rows = %v, want its own two rows with hour n = 1", regRows)
	}
}
