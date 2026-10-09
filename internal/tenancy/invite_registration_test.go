package tenancy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// regInviter issues and resends invites through the real Inviter; the capture sender yields each mailed token.
type regInviter struct {
	invWorld
	inv    *Inviter
	sender *apiSender
}

func newRegInviter(t *testing.T) regInviter {
	t.Helper()
	w := newInvWorld(t, "Registration Tenant "+uuid.NewString()[:8], "Ada Obi")
	s := &apiSender{}
	return regInviter{invWorld: w, sender: s, inv: &Inviter{Store: w.store, Sender: s, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
}

func (r regInviter) lastToken(t *testing.T) string {
	t.Helper()
	msgs := r.sender.messages()
	if len(msgs) == 0 {
		t.Fatal("the capture sender holds no mail")
	}
	return mailToken(t, msgs[len(msgs)-1])
}

// invite issues one invite and returns its id and mailed token.
func (r regInviter) invite(t *testing.T, email string) (id, token string) {
	t.Helper()
	res, err := r.inv.Invite(r.adminCtx(), []string{email}, "reviewer")
	if err != nil || len(res) != 1 {
		t.Fatalf("Invite(%s) = %v, %v; want one result", email, res, err)
	}
	return res[0].ID, r.lastToken(t)
}

func (r regInviter) resend(t *testing.T, id string) string {
	t.Helper()
	if _, err := r.inv.Resend(r.adminCtx(), id); err != nil {
		t.Fatalf("Resend(%s): %v", id, err)
	}
	return r.lastToken(t)
}

func (r regInviter) setState(t *testing.T, id, set string) {
	t.Helper()
	if _, err := r.super.Exec(context.Background(), `UPDATE invitations SET `+set+` WHERE id = $1`, id); err != nil {
		t.Fatalf("set invite state: %v", err)
	}
}

// registeredHash reads (registered_token_hash, token_hash) of an invite as superuser.
func (r regInviter) registeredHash(t *testing.T, id string) (registered, token []byte) {
	t.Helper()
	if err := r.super.QueryRow(context.Background(),
		`SELECT registered_token_hash, token_hash FROM invitations WHERE id = $1`, id).Scan(&registered, &token); err != nil {
		t.Fatalf("read registered_token_hash: %v", err)
	}
	return registered, token
}

func (r regInviter) requireClaimStored(t *testing.T, id string) {
	t.Helper()
	registered, token := r.registeredHash(t, id)
	if len(token) == 0 || !bytes.Equal(registered, token) {
		t.Errorf("registered_token_hash = %x, want the token_hash %x", registered, token)
	}
}

func (r regInviter) requireNoClaim(t *testing.T, id string) {
	t.Helper()
	if registered, _ := r.registeredHash(t, id); registered != nil {
		t.Errorf("registered_token_hash = %x, want NULL", registered)
	}
}

func claim(t *testing.T, s *Store, token string) (string, bool) {
	t.Helper()
	email, first, err := s.ClaimInvitationRegistration(context.Background(), token)
	if err != nil {
		t.Fatalf("ClaimInvitationRegistration: %v", err)
	}
	return email, first
}

// uniqueAddr keeps the cross-tenant pending lookup clear of rows other tests leave behind.
func uniqueAddr(prefix string) string { return prefix + "-" + uuid.NewString()[:8] + "@firm.test" }

func TestInviteRegistration_PendingForEmailSeesEveryTenantWithoutACaller(t *testing.T) {
	_ = newRegInviter(t) // a first tenant, so the invite below is in "any" tenant, not the only one
	r := newRegInviter(t)
	addr := uniqueAddr("a")
	r.invite(t, addr)

	got, err := r.store.InvitationPendingForEmail(context.Background(), addr)
	if err != nil || !got {
		t.Errorf("InvitationPendingForEmail(%s) = %v, %v; want true, nil", addr, got, err)
	}
}

func TestInviteRegistration_PendingForEmailFoldsCaseAsGoTrue(t *testing.T) {
	r := newRegInviter(t)
	addr := uniqueAddr("a")
	r.invite(t, addr)

	for _, probe := range []string{strings.ToUpper(addr), strings.ToUpper(addr[:1]) + addr[1:], addr} {
		got, err := r.store.InvitationPendingForEmail(context.Background(), probe)
		if err != nil || !got {
			t.Errorf("InvitationPendingForEmail(%q) = %v, %v; want true, nil", probe, got, err)
		}
	}
}

func TestInviteRegistration_PendingForEmailIsFalseForSpentExpiredOrOther(t *testing.T) {
	r := newRegInviter(t)
	liveAddr, acceptedAddr, revokedAddr, expiredAddr := uniqueAddr("live"), uniqueAddr("acc"), uniqueAddr("rev"), uniqueAddr("exp")
	r.invite(t, liveAddr)
	accID, _ := r.invite(t, acceptedAddr)
	revID, _ := r.invite(t, revokedAddr)
	expID, _ := r.invite(t, expiredAddr)
	r.setState(t, accID, `status = 'accepted'`)
	r.setState(t, revID, `status = 'revoked'`)
	r.setState(t, expID, `expires_at = now() - interval '1 second'`)

	for _, c := range []struct {
		name, addr string
		want       bool
	}{
		{"live (control)", liveAddr, true},
		{"accepted", acceptedAddr, false},
		{"revoked", revokedAddr, false},
		{"expired", expiredAddr, false},
		{"uninvited", uniqueAddr("nobody"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.store.InvitationPendingForEmail(context.Background(), c.addr)
			if err != nil || got != c.want {
				t.Errorf("InvitationPendingForEmail(%s) = %v, %v; want %v, nil", c.addr, got, err, c.want)
			}
		})
	}
}

func TestInviteRegistration_ClaimIsFirstOnceThenNot(t *testing.T) {
	r := newRegInviter(t)
	addr := uniqueAddr("claim")
	id, token := r.invite(t, addr)
	r.requireNoClaim(t, id)

	if email, first := claim(t, r.store, token); email != addr || !first {
		t.Errorf("first claim = (%q, %v), want (%q, true)", email, first, addr)
	}
	r.requireClaimStored(t, id)
	for range 2 {
		if email, first := claim(t, r.store, token); email != addr || first {
			t.Errorf("later claim = (%q, %v), want (%q, false)", email, first, addr)
		}
	}
	r.requireClaimStored(t, id)
}

func TestInviteRegistration_ClaimUnderRaceIsFirstOnce(t *testing.T) {
	r := newRegInviter(t)
	_, token := r.invite(t, uniqueAddr("race"))

	const n = 10
	type result struct {
		first bool
		err   error
	}
	results := make([]result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, first, err := r.store.ClaimInvitationRegistration(context.Background(), token)
			results[i] = result{first, err}
		}()
	}
	close(start)
	wg.Wait()

	firsts := 0
	for i, res := range results {
		if res.err != nil {
			t.Errorf("claim %d: %v", i, res.err)
		}
		if res.first {
			firsts++
		}
	}
	if firsts != 1 {
		t.Errorf("first=true results = %d of %d, want exactly 1", firsts, n)
	}
}

func TestInviteRegistration_ResentTokenClaimsAgain(t *testing.T) {
	r := newRegInviter(t)
	addr := uniqueAddr("resent")
	id, t1 := r.invite(t, addr)
	if _, first := claim(t, r.store, t1); !first {
		t.Fatal("the first token's first claim is not first=true")
	}

	t2 := r.resend(t, id)
	if t2 == t1 {
		t.Fatal("a resend issued the same token")
	}
	if email, first := claim(t, r.store, t2); email != addr || !first {
		t.Errorf("new token's claim = (%q, %v), want (%q, true)", email, first, addr)
	}
	if _, first := claim(t, r.store, t2); first {
		t.Error("the new token claims first=true twice")
	}
	if _, _, err := r.store.ClaimInvitationRegistration(context.Background(), t1); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("old token's claim err = %v, want ErrInvitationNotValid", err)
	}
}

func TestInviteRegistration_ReleaseReArmsTheClaim(t *testing.T) {
	r := newRegInviter(t)
	id, token := r.invite(t, uniqueAddr("release"))
	if _, first := claim(t, r.store, token); !first {
		t.Fatal("first claim is not first=true")
	}
	r.requireClaimStored(t, id)

	if err := r.store.ReleaseInvitationRegistration(context.Background(), token); err != nil {
		t.Fatalf("ReleaseInvitationRegistration: %v", err)
	}
	r.requireNoClaim(t, id)
	if _, first := claim(t, r.store, token); !first {
		t.Error("claim after a release is not first=true")
	}
	r.requireClaimStored(t, id)
}

func TestInviteRegistration_ReleaseTouchesOnlyItsOwnClaim(t *testing.T) {
	r := newRegInviter(t)

	idA, unclaimed := r.invite(t, uniqueAddr("unclaimed"))
	if err := r.store.ReleaseInvitationRegistration(context.Background(), unclaimed); err != nil {
		t.Fatalf("release of an unclaimed token: %v", err)
	}
	r.requireNoClaim(t, idA)
	if _, first := claim(t, r.store, unclaimed); !first {
		t.Error("an unclaimed token's release spent or blocked its claim: want first=true")
	}

	idB, t1 := r.invite(t, uniqueAddr("resend"))
	if _, first := claim(t, r.store, t1); !first {
		t.Fatal("T1's first claim is not first=true")
	}
	t2 := r.resend(t, idB)
	if _, first := claim(t, r.store, t2); !first {
		t.Fatal("T2's first claim is not first=true")
	}
	if err := r.store.ReleaseInvitationRegistration(context.Background(), t1); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("release of the replaced token err = %v, want ErrInvitationNotValid", err)
	}
	r.requireClaimStored(t, idB)
	if _, first := claim(t, r.store, t2); first {
		t.Error("T2's claim was cleared by T1's release: want first=false")
	}
}

func TestInviteRegistration_ClaimRefusesUnusableTokens(t *testing.T) {
	r := newRegInviter(t)
	unknown, _, err := mintToken()
	if err != nil {
		t.Fatal(err)
	}
	accID, accepted := r.invite(t, uniqueAddr("acc"))
	revID, revoked := r.invite(t, uniqueAddr("rev"))
	expID, expired := r.invite(t, uniqueAddr("exp"))
	_, live := r.invite(t, uniqueAddr("live"))
	r.setState(t, accID, `status = 'accepted'`)
	r.setState(t, revID, `status = 'revoked'`)
	r.setState(t, expID, `expires_at = now() - interval '1 second'`)

	if _, first := claim(t, r.store, live); !first {
		t.Error("control: a live token's first claim is not first=true")
	}
	for _, c := range []struct{ name, token string }{
		{"unknown", unknown},
		{"accepted", accepted},
		{"revoked", revoked},
		{"expired", expired},
	} {
		t.Run(c.name, func(t *testing.T) {
			email, first, err := r.store.ClaimInvitationRegistration(context.Background(), c.token)
			if !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("err = %v, want ErrInvitationNotValid", err)
			}
			if email != "" || first {
				t.Errorf("result = (%q, %v), want the zero values", email, first)
			}
		})
	}
}

func TestInviteRegistration_MalformedTokenSendsNoStatement(t *testing.T) {
	r := newRegInviter(t)
	store, tr := tracedStore(t)
	_, valid := r.invite(t, uniqueAddr("malformed"))

	for _, c := range []struct{ name, token string }{
		{"empty", ""},
		{"42 characters", valid[:42]},
		{"44 characters", valid + "A"},
		{"plus sign", valid[:42] + "+"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := tr.count()
			if _, _, err := store.ClaimInvitationRegistration(context.Background(), c.token); !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("claim err = %v, want ErrInvitationNotValid", err)
			}
			if err := store.ReleaseInvitationRegistration(context.Background(), c.token); !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("release err = %v, want ErrInvitationNotValid", err)
			}
			if n := tr.count() - before; n != 0 {
				t.Errorf("a malformed token sent %d statements, want none", n)
			}
		})
	}

	// Control: a well-formed claim reaches the pool, so a zero count above is not a dead tracer.
	before := tr.count()
	if _, first := claim(t, store, valid); !first {
		t.Error("control claim is not first=true")
	}
	if tr.count() == before {
		t.Error("the traced pool saw no statement for a well-formed claim, so the zero counts above prove nothing")
	}
}
