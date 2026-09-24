package auth_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// GoTrue answers the losing concurrent /signup 500 with SQLSTATE 23505.
func TestIdP_ConcurrentRegistrationsAnswerTheSame(t *testing.T) {
	base := idpMailURL(t)
	gw := startGateway(t, base)
	u := idpUser{email: "idp-mail-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	conn := superConn(t)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email) })

	type result struct {
		status int
		body   string
		err    error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, b, err := register(gw, u.email, u.password)
			results[i] = result{s, b, err}
		}()
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil || r.status != http.StatusAccepted || !strings.Contains(r.body, `"verification_pending"`) {
			t.Errorf("registration %d: status %d, body %s, err %v; want 202 verification_pending", i, r.status, r.body, r.err)
		}
	}
	if results[0].body != results[1].body {
		t.Errorf("bodies differ: %q vs %q", results[0].body, results[1].body)
	}
	if n := mailCount(t, u.email); n != 1 {
		t.Errorf("mailpit holds %d mails after two concurrent registrations, want 1", n)
	}
}
