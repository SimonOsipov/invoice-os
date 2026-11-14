package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const linkAddressCall = `SELECT auth.confirm_link_address($1::text)`

// gatewayConn connects as invoice_gateway, the only role that may call the function.
func gatewayConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), mailEnv(t, "DATABASE_GATEWAY_URL"))
	if err != nil {
		t.Fatalf("connect as invoice_gateway: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// linkAddress calls the function as invoice_gateway; a nil result is SQL NULL.
func linkAddress(t *testing.T, conn *pgx.Conn, hash string) *string {
	t.Helper()
	var masked *string
	if err := conn.QueryRow(context.Background(), linkAddressCall, hash).Scan(&masked); err != nil {
		t.Fatalf("auth.confirm_link_address(%.12s...) as invoice_gateway: %v (SQLSTATE %q)", hash, err, sqlState(err))
	}
	return masked
}

func requireMasked(t *testing.T, conn *pgx.Conn, what, hash, want string) {
	t.Helper()
	got := linkAddress(t, conn, hash)
	if got == nil || *got != want {
		t.Errorf("%s: auth.confirm_link_address = %v, want %q", what, deref(got), want)
	}
}

func requireNoAddress(t *testing.T, conn *pgx.Conn, what, hash string) {
	t.Helper()
	if got := linkAddress(t, conn, hash); got != nil {
		t.Errorf("%s: auth.confirm_link_address = %q, want NULL", what, *got)
	}
}

func deref(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

// linkHash is the mailed link's token, which is the stored hash.
func linkHash(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("mailed link %q carries no token (%v)", link, err)
	}
	return u.Query().Get("token")
}

func TestIdP_ConfirmLinkAddressMasksTheLinksAccount(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	u := signUpMailed(t, base, "olaf-", "example.test")

	requireMasked(t, gwDB, "live signup link", linkHash(t, confirmationLink(t, u.email)), "o***@example.test")
}

func TestIdP_ConfirmLinkAddressMasksAnInviteLink(t *testing.T) {
	idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	w := newInviteWorldFor(t, "iris-"+uuid.NewString()+"@example.com", nil)
	w.register(t)

	requireMasked(t, gwDB, "live invite link", linkHash(t, confirmationLink(t, w.email)), "i***@example.com")
}

// The first character is a rune, not a byte. GoTrue refuses a non-ASCII local part at sign-up, so the address is renamed in the row.
func TestIdP_ConfirmLinkAddressKeepsAMultibyteFirstCharacter(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	u := signUpMailed(t, base, "olaf-", "example.test")
	link := confirmationLink(t, u.email)
	renamed := "ö" + u.email
	su := superConn(t)
	exec(t, su, `UPDATE auth.users SET email = $2 WHERE email = $1`, u.email, renamed)
	t.Cleanup(func() { _, _ = su.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, renamed) })

	requireMasked(t, gwDB, "multibyte local part", linkHash(t, link), "ö***@example.test")
}

func TestIdP_ConfirmLinkAddressIsNullForAnUnknownHash(t *testing.T) {
	idpMailURL(t)
	grantAccountStateRead(t)
	raw := make([]byte, 28)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}

	requireNoAddress(t, gatewayConn(t), "random 56-hex hash", hex.EncodeToString(raw))
}

func TestIdP_ConfirmLinkAddressIsNullOnceTheLinkIsRedeemed(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	u := signUpMailed(t, base, "olaf-", "example.test")
	link := confirmationLink(t, u.email)
	hash := linkHash(t, link)
	requireMasked(t, gwDB, "before the press", hash, "o***@example.test")

	if got := follow(t, link, u.password); got != siteURL+"/?verified=1" {
		t.Fatalf("set-password redirect = %q, want %s/?verified=1", got, siteURL)
	}

	requireNoAddress(t, gwDB, "after the link was redeemed", hash)
}

func TestIdP_ConfirmLinkAddressIsNullForAReplacedLink(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	u := signUpMailed(t, base, "olaf-", "example.test")
	first := confirmationLink(t, u.email)
	backdateCooldown(t, u.email)
	u.register(t, base)
	var second string
	for _, l := range confirmationLinks(t, u.email, 2) {
		if l != first {
			second = l
		}
	}
	if second == "" {
		t.Fatal("the repeat registration mailed no new link")
	}

	requireMasked(t, gwDB, "the newest link", linkHash(t, second), "o***@example.test")
	requireNoAddress(t, gwDB, "the replaced link", linkHash(t, first))
}

func TestIdP_ConfirmLinkAddressIsNullForARecoveryHash(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := confirmedRegistrant(t, gw)
	requestResetAccepted(t, gw, u.email)
	recovery := linkHash(t, recoveryLink(t, u.email, 2))
	if n := recoveryTokenRows(t, u.email); n != 1 {
		t.Fatalf("control: auth.one_time_tokens holds %d recovery rows, want 1", n)
	}

	requireNoAddress(t, gwDB, "a live recovery token hash", recovery)
}

// GoTrue redeems an expired link up to its own expiry check, so the lookup is never stricter.
func TestIdP_ConfirmLinkAddressAnswersAnExpiredLink(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	gwDB := gatewayConn(t)
	u := signUpMailed(t, base, "olaf-", "example.test")
	link := confirmationLink(t, u.email)
	exec(t, superConn(t), `UPDATE auth.users SET confirmation_sent_at = now() - interval '2 hours' WHERE email = $1`, u.email)

	requireMasked(t, gwDB, "an expired, unredeemed link", linkHash(t, link), "o***@example.test")
	if got := follow(t, link, u.password); got != siteURL+"/?verify=failed" {
		t.Errorf("POST on the expired link = %q, want %s/?verify=failed", got, siteURL)
	}
}

func TestIdP_ConfirmLinkAddressOnlyTheGatewayMayCall(t *testing.T) {
	idpMailURL(t)
	grantAccountStateRead(t)
	ctx := context.Background()
	probe := hex.EncodeToString([]byte("0123456789abcdefghijklmnopqr"))

	var got *string
	err := appConn(t).QueryRow(ctx, linkAddressCall, probe).Scan(&got)
	if sqlState(err) != "42501" {
		t.Errorf("invoice_app call: err %v, SQLSTATE %q; want 42501", err, sqlState(err))
	}
	// auth_hook_reader holds USAGE on auth, so only a PUBLIC grant could let it in.
	su := superConn(t)
	tx, err := su.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
		t.Fatalf("SET ROLE auth_hook_reader: %v", err)
	}
	err = tx.QueryRow(ctx, linkAddressCall, probe).Scan(&got)
	if sqlState(err) != "42501" {
		t.Errorf("auth_hook_reader call: err %v, SQLSTATE %q; want 42501 (PUBLIC must not execute)", err, sqlState(err))
	}

	gwDB := gatewayConn(t)
	for _, table := range []string{"auth.users", "auth.one_time_tokens"} {
		_, err := gwDB.Exec(ctx, `SELECT 1 FROM `+table)
		if sqlState(err) != "42501" {
			t.Errorf("invoice_gateway SELECT from %s: err %v, SQLSTATE %q; want 42501", table, err, sqlState(err))
		}
	}
	if got := linkAddress(t, gwDB, probe); got != nil {
		t.Errorf("invoice_gateway call with an unknown hash = %q, want NULL", *got)
	}
}

func TestIdP_ConfirmLinkAddressGrantIsIdempotentAndSweepsStrays(t *testing.T) {
	idpMailURL(t)
	dsn := mailEnv(t, "DATABASE_AUTH_ADMIN_URL")
	ctx := context.Background()
	su := superConn(t)
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), dsn); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})
	run := func(what string) {
		t.Helper()
		if granted, err := db.GrantAccountStateRead(ctx, dsn); err != nil || !granted {
			t.Fatalf("GrantAccountStateRead %s = (%v, %v); want (true, nil)", what, granted, err)
		}
	}
	run("first run")
	var exists bool
	if err := su.QueryRow(ctx, `SELECT to_regprocedure('auth.confirm_link_address(text)') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("auth.confirm_link_address(text) exists = %v, err %v after GrantAccountStateRead; want true", exists, err)
	}

	exec(t, su, `GRANT EXECUTE ON FUNCTION auth.confirm_link_address(text) TO invoice_app`)
	run("second run")
	run("third run")

	rows, err := su.Query(ctx, `SELECT pg_get_userbyid(a.grantee), a.privilege_type FROM pg_proc p, aclexplode(p.proacl) a
		WHERE p.oid = 'auth.confirm_link_address(text)'::regprocedure ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grantee, priv string
		if err := rows.Scan(&grantee, &priv); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grantee+":"+priv)
	}
	if want := []string{"invoice_gateway:EXECUTE", "supabase_auth_admin:EXECUTE"}; !slices.Equal(grants, want) {
		t.Errorf("ACL after the stray grant and two reruns = %v, want exactly %v", grants, want)
	}
}

// addressSpan is what the set-password page prints for a found link (SECAUD-26-02 page-copy).
func addressSpan(masked string) string { return `<span class="address">` + masked + `</span>` }

// statelessBounce opens the link as a mail scanner does: no state, no cookie jar, no redirect followed.
func statelessBounce(t *testing.T, link string) string {
	t.Helper()
	resp, err := noRedirect.Get(link)
	if err != nil {
		t.Fatalf("GET %s: %v", link, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("stateless GET of the mailed link: status %d, want 303", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// requireNamedThenConfirmed is the press: the stated page names masked (and never email), the POST answers ?verified=1 and mints no session.
func requireNamedThenConfirmed(t *testing.T, link, email, masked, password string) {
	t.Helper()
	page := open(t, withState(link, newState(t)), http.MethodGet)
	if !strings.Contains(page, addressSpan(masked)) {
		t.Errorf("the stated page lacks %s", addressSpan(masked))
	}
	if strings.Contains(page, email) {
		t.Errorf("the stated page holds the full address %q", email)
	}
	if got := clickWithState(t, link, newState(t), password); got != siteURL+"/?verified=1" {
		t.Fatalf("set-password POST answered %q, want %s/?verified=1 exactly", got, siteURL)
	}
	if n := sessionRows(t, email); n != 0 {
		t.Errorf("auth.sessions holds %d rows for the account, want 0: a confirm mints no session", n)
	}
}

func TestIdP_AStatelessConfirmLinkNamesAccountAAndMintsNoSession(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	a := registrant(t, gw)
	link := confirmationLink(t, a.email)

	if got, want := statelessBounce(t, link), siteURL+"/?confirm=1#token="+linkHash(t, link); got != want {
		t.Fatalf("stateless open Location = %q, want %q", got, want)
	}
	requireNamedThenConfirmed(t, link, a.email, "i***@example.test", a.password)
	requireSignIn(t, base, a, a.password, http.StatusOK, "")
}

func TestIdP_AStatelessInviteLinkNamesTheInviteeAndMintsNoSession(t *testing.T) {
	w := newInviteWorld(t, "logfix11a-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	w.register(t)
	link := confirmationLink(t, w.email)

	if got, want := statelessBounce(t, link), siteURL+"/?confirm=invite#token="+linkHash(t, link); got != want {
		t.Fatalf("stateless invite open Location = %q, want %q", got, want)
	}
	requireNamedThenConfirmed(t, link, w.email, "l***@gmail.com", u.password)
	access := accessToken(t, w.base, u)
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
}

// A POST may answer ?verified=1 only for a link whose page named the address first.
func TestIdP_EveryRedeemableLinkWasNamed(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	const named = `class="address"`
	press := func(link, password string) (page, location string) {
		page = open(t, withState(link, newState(t)), http.MethodGet)
		return page, clickWithState(t, link, newState(t), password)
	}

	live := registrant(t, gw)
	page, loc := press(confirmationLink(t, live.email), live.password)
	if !strings.Contains(page, named) || loc != siteURL+"/?verified=1" {
		t.Errorf("live link: named=%t, answer %q; want named and %s/?verified=1", strings.Contains(page, named), loc, siteURL)
	}

	spent := registrant(t, gw)
	spentLink := confirmationLink(t, spent.email)
	if got := clickWithState(t, spentLink, newState(t), spent.password); got != siteURL+"/?verified=1" {
		t.Fatalf("control: the first press answered %q", got)
	}
	page, loc = press(spentLink, spent.password)
	if strings.Contains(page, named) || loc != siteURL+"/?verify=failed" {
		t.Errorf("spent link: named=%t, answer %q; want unnamed and %s/?verify=failed", strings.Contains(page, named), loc, siteURL)
	}

	replaced := registrant(t, gw)
	first := confirmationLink(t, replaced.email)
	backdateCooldown(t, replaced.email)
	if status, body, err := register(gw, replaced.email, replaced.password); err != nil || status != http.StatusAccepted {
		t.Fatalf("repeat registration: status %d, body %s, err %v; want 202", status, body, err)
	}
	var second string
	for _, l := range confirmationLinks(t, replaced.email, 2) {
		if l != first {
			second = l
		}
	}
	if second == "" {
		t.Fatal("the repeat registration mailed no new link")
	}
	page, loc = press(first, replaced.password)
	if strings.Contains(page, named) || loc != siteURL+"/?verify=failed" {
		t.Errorf("replaced link: named=%t, answer %q; want unnamed and %s/?verify=failed", strings.Contains(page, named), loc, siteURL)
	}
	page, loc = press(second, replaced.password)
	if !strings.Contains(page, named) || loc != siteURL+"/?verified=1" {
		t.Errorf("newest link: named=%t, answer %q; want named and %s/?verified=1", strings.Contains(page, named), loc, siteURL)
	}
}

// Guessed tokens spend the link-redeem count, so a run of them shuts out the lookup before the real token is answered.
func TestIdP_GuessedConfirmTokensAreCountedBeforeTheRealOneIsAnswered(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	a := registrant(t, gw)
	link := confirmationLink(t, a.email)
	stated := func(token string) (int, string, string) {
		q := url.Values{"token": {token}, "type": {"signup"}, "state": {newState(t)}}
		resp, err := noRedirect.Get(gw + "/auth/verify?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Location"), string(b)
	}

	for i := range gateway.LinkRedeemPerIP {
		otp := fmt.Sprintf("%010d", mrand.IntN(10_000_000_000))
		sum := sha256.Sum224([]byte(a.email + otp))
		status, _, body := stated(hex.EncodeToString(sum[:]))
		if status != http.StatusOK || strings.Contains(body, "***@") {
			t.Fatalf("guess %d: status %d, body names an address=%t; want 200 and unnamed", i+1, status, strings.Contains(body, "***@"))
		}
	}

	status, loc, body := stated(linkHash(t, link))
	if status != http.StatusSeeOther || loc != siteURL+"/?verify=failed" || strings.Contains(body, "***@") {
		t.Errorf("real token after %d guesses: status %d, Location %q; want 303 %s/?verify=failed with no address", gateway.LinkRedeemPerIP, status, loc, siteURL)
	}
}
