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
	email := "ae-" + user[:8] + "@corp.example"
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM account_event_handoffs WHERE user_id = $1 OR email = $2`, user, email)
	})
	want := AccountEvent{Event: EventSignedIn, UserID: user, Email: email, IP: aeIP, UserAgent: aeUA}
	outbox := NewAccountEvents(d.a, nopLog())

	if err := outbox.Record(ctx, want); err != nil {
		t.Fatalf("Record: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	first := want
	first.FirstSession = true
	if err := outbox.Record(cancelled, first); err != nil {
		t.Fatalf("Record with a cancelled context: %v, want the write to survive it", err)
	}

	rows, err := d.super.Query(ctx, `SELECT event, user_id::text, email, ip, user_agent, first_session FROM account_event_handoffs WHERE user_id = $1 ORDER BY first_session`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []AccountEvent
	for rows.Next() {
		var e AccountEvent
		if err := rows.Scan(&e.Event, &e.UserID, &e.Email, &e.IP, &e.UserAgent, &e.FirstSession); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if len(got) != 2 || got[0] != want || got[1] != first {
		t.Errorf("rows = %+v, want [%+v %+v] (one per Record)", got, want, first)
	}

	t.Run("first session without a user id", func(t *testing.T) {
		none := AccountEvent{Event: EventSignedIn, Email: email, FirstSession: true, IP: aeIP, UserAgent: aeUA}
		if err := outbox.Record(ctx, none); err != nil {
			t.Fatalf("Record: %v", err)
		}
		var n int
		var fs, nullUser bool
		err := d.super.QueryRow(ctx, `SELECT count(*), bool_and(first_session), bool_and(user_id IS NULL) FROM account_event_handoffs WHERE email = $1 AND user_id IS NULL`, email).Scan(&n, &fs, &nullUser)
		if err != nil || n != 1 || !fs || !nullUser {
			t.Errorf("rows = %d first_session = %v user_id null = %v err = %v, want one true true", n, fs, nullUser, err)
		}
	})
}
