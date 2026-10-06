package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const resendAccepted = `{"status":"accepted"}`

// resend posts one resend through the gateway handler and times the answer.
func resend(t *testing.T, gw, email string) (int, string, time.Duration) {
	t.Helper()
	return postMailRequest(t, gw+"/auth/resend-verification", email)
}

// postMailRequest posts {"email"} to a mail-link handler and times the answer.
func postMailRequest(t *testing.T, endpoint, email string) (int, string, time.Duration) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"email": email})
	start := time.Now()
	resp, err := noRedirect.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	return resp.StatusCode, body.String(), elapsed
}

// backdateCooldown moves the last confirmation mail two minutes back, past GoTrue's 60 s cooldown.
func backdateCooldown(t *testing.T, email string) {
	t.Helper()
	if _, err := superConn(t).Exec(context.Background(),
		`UPDATE auth.users SET confirmation_sent_at = now() - interval '2 minutes' WHERE email = $1`, email); err != nil {
		t.Fatal(err)
	}
}

func requireResendAccepted(t *testing.T, what string, status int, body string) {
	t.Helper()
	if status != http.StatusAccepted || strings.TrimSpace(body) != resendAccepted {
		t.Fatalf("%s: status %d, body %s; want 202 %s", what, status, body, resendAccepted)
	}
}

// Five account states answer the same bytes, none earlier than the floor.
func TestIdP_ResendAnswersAlikeForEveryAccountState(t *testing.T) {
	base := idpMailURL(t)
	const floor = 400 * time.Millisecond
	gw, logs := startGateway(t, base, floor, nil)
	unknownAddress := func() string { return "idp-mail-" + uuid.NewString() + "@example.test" }

	type answer struct {
		name, body string
		elapsed    time.Duration
	}
	var answers []answer
	record := func(name, email string) {
		t.Helper()
		status, body, elapsed := resend(t, gw, email)
		requireResendAccepted(t, name, status, body)
		answers = append(answers, answer{name, body, elapsed})
	}

	record("unknown address", unknownAddress())

	unconfirmed := registrant(t, gw)
	backdateCooldown(t, unconfirmed.email)
	record("unconfirmed address outside the cooldown", unconfirmed.email)
	if n := mailCount(t, unconfirmed.email); n != 2 {
		t.Errorf("mailpit holds %d mails after a resend outside the cooldown, want 2 (registration and resend)", n)
	}
	record("unconfirmed address inside the cooldown", unconfirmed.email)
	record("unconfirmed address inside the cooldown, again", unconfirmed.email)
	record("unconfirmed address inside the cooldown, a third time", unconfirmed.email)
	// Three counted sends would have spent the address's limit: the third repeat must still reach GoTrue.
	if n := strings.Count(logs.String(), "resend-verification: gotrue email send rate limit"); n != 3 {
		t.Errorf("%d email-send-rate-limit lines in the gateway log, want 3, so the repeats did not all take the 429 path (refund missing?): %s", n, logs.String())
	}
	if n := mailCount(t, unconfirmed.email); n != 2 {
		t.Errorf("mailpit holds %d mails after three resends inside the cooldown, want still 2", n)
	}

	overLimit := unknownAddress()
	for i := 1; i <= 4; i++ {
		record(fmt.Sprintf("unknown address over its limit, request %d", i), overLimit)
	}
	if n := strings.Count(logs.String(), "resend-verification: limit reached"); n != 1 || !strings.Contains(logs.String(), `"limit":"address"`) {
		t.Errorf("want exactly one limit=address line for the fourth request, got %d: %s", n, logs.String())
	}

	confirmed := registrant(t, gw)
	if got := follow(t, confirmationLink(t, confirmed.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	record("confirmed address", confirmed.email)
	if n := mailCount(t, confirmed.email); n != 1 {
		t.Errorf("mailpit holds %d mails for a confirmed address, want 1: it must get no mail", n)
	}

	for _, a := range answers {
		if a.body != answers[0].body {
			t.Errorf("%s body = %q, want byte-equal to the unknown-address body %q", a.name, a.body, answers[0].body)
		}
		if a.elapsed < floor {
			t.Errorf("%s answered after %v, want no earlier than %v", a.name, a.elapsed, floor)
		}
	}
}

// GoTrue retires the earlier link on each resend; the per-address limit stops the fourth.
func TestIdP_ResentLinkVerifiesAndEarlierLinksStop(t *testing.T) {
	base := idpMailURL(t)
	gw, logs := startGateway(t, base, 0, nil)
	r := registrant(t, gw)
	l1 := confirmationLink(t, r.email)
	parsed, err := url.Parse(l1)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	tokenRows := func() int {
		t.Helper()
		var n int
		if err := superConn(t).QueryRow(context.Background(),
			`SELECT count(*) FROM auth.one_time_tokens WHERE token_hash = $1`, token).Scan(&n); err != nil {
			t.Fatalf("count auth.one_time_tokens: %v", err)
		}
		return n
	}
	if n := tokenRows(); n != 1 {
		t.Fatalf("auth.one_time_tokens holds %d rows for the registration link's token, want 1", n)
	}

	links := []string{l1}
	for i := 1; i <= 3; i++ {
		backdateCooldown(t, r.email)
		status, body, _ := resend(t, gw, r.email)
		requireResendAccepted(t, fmt.Sprintf("resend %d", i), status, body)
		all := confirmationLinks(t, r.email, i+1)
		if len(all) != i+1 {
			t.Fatalf("after resend %d: %d mail links, want %d", i, len(all), i+1)
		}
		var fresh []string
		for _, l := range all {
			if !slices.Contains(links, l) {
				fresh = append(fresh, l)
			}
		}
		if len(fresh) != 1 {
			t.Fatalf("after resend %d: %d links not seen before, want 1", i, len(fresh))
		}
		links = append(links, fresh[0])
		if i == 1 {
			if n := tokenRows(); n != 0 {
				t.Errorf("auth.one_time_tokens holds %d rows for the registration link's token after a resend, want 0", n)
			}
		}
	}
	if n := mailCount(t, r.email); n != 4 {
		t.Fatalf("mailpit holds %d mails after three resends, want 4", n)
	}

	backdateCooldown(t, r.email)
	status, body, _ := resend(t, gw, r.email)
	requireResendAccepted(t, "fourth resend", status, body)
	if n := mailCount(t, r.email); n != 4 {
		t.Errorf("mailpit holds %d mails after a fourth resend, want still 4", n)
	}
	if !strings.Contains(logs.String(), `"limit":"address"`) {
		t.Errorf("no limit=address line in the gateway log: %s", logs.String())
	}

	for i, l := range links {
		if j := slices.Index(links, l); j != i {
			t.Errorf("links %d and %d are equal, want every mail to carry a distinct link", j, i)
		}
	}
	for i, m := range mailsFor(t, r.email, 4) {
		if m.Subject != accountMailSubject {
			t.Errorf("mail %d subject = %q, want %q", i, m.Subject, accountMailSubject)
		}
	}

	l3, l4 := links[2], links[3]
	for name, l := range map[string]string{"the registration link": l1, "the previous resend's link": l3} {
		if got := follow(t, l); got != siteURL+"/?verify=failed" {
			t.Errorf("%s = %q, want %s/?verify=failed", name, got, siteURL)
		}
	}
	if emailConfirmed(t, r.email) {
		t.Fatal("an earlier link confirmed the account")
	}
	if got := follow(t, l4); got != siteURL+"/?verified=1" {
		t.Errorf("the newest link = %q, want %s/?verified=1", got, siteURL)
	}
	accessToken(t, base, r)
}
