package gateway

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRLS_GatewayAccountEventOutboxWritesOneRow(t *testing.T) {
	d := requireHandoffDB(t)
	ctx := context.Background()
	user := uuid.NewString()
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM account_event_handoffs WHERE user_id = $1`, user)
	})
	want := AccountEvent{Event: EventSignedIn, UserID: user, Email: "ae-" + user[:8] + "@corp.example", IP: aeIP, UserAgent: aeUA}
	outbox := NewAccountEvents(d.a, nopLog())

	if err := outbox.Record(ctx, want); err != nil {
		t.Fatalf("Record: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := outbox.Record(cancelled, want); err != nil {
		t.Fatalf("Record with a cancelled context: %v, want the write to survive it", err)
	}

	rows, err := d.super.Query(ctx, `SELECT event, user_id::text, email, ip, user_agent FROM account_event_handoffs WHERE user_id = $1`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var got AccountEvent
		if err := rows.Scan(&got.Event, &got.UserID, &got.Email, &got.IP, &got.UserAgent); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row = %+v, want %+v", got, want)
		}
		n++
	}
	if n != 2 {
		t.Errorf("rows = %d, want 2 (one per Record)", n)
	}
}
