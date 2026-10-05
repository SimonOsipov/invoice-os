package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// timedRegister posts one registration through the gateway handler and times the answer.
func timedRegister(t *testing.T, gw string, u idpUser) (int, string, time.Duration) {
	t.Helper()
	start := time.Now()
	status, body, err := register(gw, u.email, u.password)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("POST /auth/register for %s: %v", u.email, err)
	}
	return status, body, elapsed
}

// All three account states answer the same bytes, none earlier than the minimum.
func TestIdP_RegisterWaitsTheMinimumForEveryAccountState(t *testing.T) {
	base := idpMailURL(t)
	const floor = 400 * time.Millisecond
	gw, logs := startGateway(t, base, floor, nil)
	conn := superConn(t)
	fresh := func() idpUser {
		u := idpUser{email: "idp-mail-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
		t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email) })
		return u
	}

	type answer struct {
		name    string
		body    string
		elapsed time.Duration
	}
	var answers []answer
	record := func(name string, u idpUser) {
		t.Helper()
		status, body, elapsed := timedRegister(t, gw, u)
		if status != http.StatusAccepted || !strings.Contains(body, `"verification_pending"`) {
			t.Fatalf("%s: status %d, body %s; want 202 verification_pending", name, status, body)
		}
		answers = append(answers, answer{name, body, elapsed})
	}

	first := fresh()
	record("new address", first)
	record("same unconfirmed address again", first)
	if !strings.Contains(logs.String(), "registration: gotrue email send rate limit") {
		t.Errorf("no email-send-rate-limit line in the gateway log, so the repeat did not take the 429 path: %s", logs.String())
	}

	confirmed := fresh()
	record("second new address", confirmed)
	if got := follow(t, confirmationLink(t, confirmed.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	record("confirmed address", confirmed)

	for _, a := range answers {
		if a.body != answers[0].body {
			t.Errorf("%s body = %q, want byte-equal to the new-address body %q", a.name, a.body, answers[0].body)
		}
		if a.elapsed < floor {
			t.Errorf("%s answered after %v, want no earlier than %v", a.name, a.elapsed, floor)
		}
	}
}
