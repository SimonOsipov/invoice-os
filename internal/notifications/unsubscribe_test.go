package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// usRig serves both unsubscribe routes over the invoice_app pool and records the vendor calls.
type usRig struct {
	e      env
	rs     *qaRecResend
	sink   *qaLogSink
	lookup http.HandlerFunc
	press  http.HandlerFunc
}

func usNewRig(t *testing.T) *usRig {
	t.Helper()
	e := newEnv(t)
	r := &usRig{e: e, rs: &qaRecResend{}, sink: &qaLogSink{}}
	store := NewUnsubscribeStore(e.app)
	r.lookup = UnsubscribeLookupHandler(store, r.sink.logger())
	r.press = UnsubscribeHandler(store, r.rs, r.sink.logger())
	return r
}

// usAddress makes an address with a token row and removes the row at cleanup.
func usAddress(t *testing.T, e env, local string) (email, token string) {
	t.Helper()
	email = fmt.Sprintf("%s-%s@ascomply.test", local, uuid.NewString()[:8])
	if err := e.app.QueryRow(context.Background(), `SELECT public.mail_unsubscribe_token($1)::text`, email).Scan(&token); err != nil {
		t.Fatalf("mint token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.admin.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, email)
	})
	return email, token
}

func usUnsubscribedAt(t *testing.T, e env, email string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := e.admin.QueryRow(context.Background(), `SELECT unsubscribed_at FROM mail_unsubscribes WHERE email = $1`, email).Scan(&at); err != nil {
		t.Fatalf("read unsubscribed_at: %v", err)
	}
	return at
}

// usCall posts {"token": token} (or raw when non-empty) and times out instead of waiting on a leaked row lock.
func usCall(t *testing.T, h http.HandlerFunc, token, raw string, hdr map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	body := raw
	if body == "" {
		b, _ := json.Marshal(map[string]string{"token": token})
		body = string(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/internal/contacts/unsubscribe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func usPressStatus(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("press body %q: %v", w.Body.String(), err)
	}
	return b.Status
}

func usWantPress(t *testing.T, w *httptest.ResponseRecorder, code int, status string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("press status = %d (%s), want %d", w.Code, w.Body.String(), code)
	}
	if code == http.StatusOK {
		if got := usPressStatus(t, w); got != status {
			t.Fatalf("press status field = %q, want %q", got, status)
		}
	}
}

func usSetUnsubscribed(t *testing.T, e env, email string, at time.Time) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), `UPDATE mail_unsubscribes SET unsubscribed_at = $2 WHERE email = $1`, email, at); err != nil {
		t.Fatalf("set unsubscribed_at: %v", err)
	}
}

// usRefuseLocalWrite adds a trigger that refuses setting unsubscribed_at on this one row; the message holds the address.
func usRefuseLocalWrite(t *testing.T, e env, email string) (drop func()) {
	t.Helper()
	name := "us_refuse_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	ctx := context.Background()
	stmts := []string{
		fmt.Sprintf(`CREATE FUNCTION public.%[1]s() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RAISE EXCEPTION 'refused for %%', NEW.email; END $f$`, name),
		fmt.Sprintf(`CREATE TRIGGER %[1]s BEFORE UPDATE OF unsubscribed_at ON public.mail_unsubscribes FOR EACH ROW WHEN (NEW.email = '%[2]s' AND NEW.unsubscribed_at IS NOT NULL) EXECUTE FUNCTION public.%[1]s()`, name, email),
	}
	for _, s := range stmts {
		if _, err := e.admin.Exec(ctx, s); err != nil {
			t.Fatalf("fault trigger: %v", err)
		}
	}
	var once sync.Once
	drop = func() {
		once.Do(func() {
			_, _ = e.admin.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON public.mail_unsubscribes`, name))
			_, _ = e.admin.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS public.%s()`, name))
		})
	}
	t.Cleanup(drop)
	return drop
}

func (r *usRig) requireNoVendorCall(t *testing.T) {
	t.Helper()
	if n := r.rs.readCount(); n != 0 {
		t.Errorf("ContactState calls = %d, want 0", n)
	}
	if c := r.rs.optOutCalls(); len(c) != 0 {
		t.Errorf("OptOutMarketing calls = %d, want 0", len(c))
	}
	if c := r.rs.got(); len(c) != 0 {
		t.Errorf("Sync calls = %d, want 0", len(c))
	}
}

func TestUnsubscribeLookup_MasksAndWritesNothing(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "sam")
	wantMasked := "s***@ascomply.test"

	w := usCall(t, r.lookup, token, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("lookup status = %d (%s), want 200", w.Code, w.Body.String())
	}
	var got struct {
		Masked       string `json:"masked"`
		Unsubscribed *bool  `json:"unsubscribed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("lookup body %q: %v", w.Body.String(), err)
	}
	if got.Masked != wantMasked {
		t.Errorf("masked = %q, want %q", got.Masked, wantMasked)
	}
	if got.Unsubscribed == nil || *got.Unsubscribed {
		t.Errorf("unsubscribed = %v, want false", got.Unsubscribed)
	}
	if strings.Contains(w.Body.String(), email) {
		t.Errorf("lookup body %q holds the full address", w.Body.String())
	}
	if at := usUnsubscribedAt(t, r.e, email); at != nil {
		t.Errorf("unsubscribed_at after lookup = %v, want NULL", at)
	}
	var tokenAfter string
	if err := r.e.admin.QueryRow(context.Background(), `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&tokenAfter); err != nil || tokenAfter != token {
		t.Errorf("token after lookup = %q (err %v), want %q", tokenAfter, err, token)
	}
	r.requireNoVendorCall(t)

	// A recorded unsubscribe reads back as true, and the lookup keeps the first timestamp.
	first := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Microsecond)
	usSetUnsubscribed(t, r.e, email, first)
	w = usCall(t, r.lookup, token, "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unsubscribed":true`) {
		t.Errorf("lookup of an unsubscribed token = %d %s, want 200 with unsubscribed true", w.Code, w.Body.String())
	}
	if at := usUnsubscribedAt(t, r.e, email); at == nil || !at.Equal(first) {
		t.Errorf("unsubscribed_at after second lookup = %v, want %v", at, first)
	}
	r.requireNoVendorCall(t)
}

func TestMaskEmail_KeepsTheFirstCharacterAndTheDomain(t *testing.T) {
	for in, want := range map[string]string{
		"a@x.test":          "a***@x.test",
		"sam@ascomply.com":  "s***@ascomply.com",
		"ñandu@x.test":      "ñ***@x.test",
		"first.last@a.b.cd": "f***@a.b.cd",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnsubscribe_ABadTokenIsRefusedBeforeAnyRead(t *testing.T) {
	r := usNewRig(t)
	unknown := uuid.NewString()
	var rowsBefore int
	_ = r.e.admin.QueryRow(context.Background(), `SELECT count(*) FROM mail_unsubscribes WHERE token = $1`, unknown).Scan(&rowsBefore)

	for _, tc := range []struct {
		name, token, raw string
		code             int
	}{
		{"not a uuid", "abc", "", http.StatusBadRequest},
		{"empty", "", "", http.StatusBadRequest},
		{"almost a uuid", "00000000-0000-0000-0000-00000000000g", "", http.StatusBadRequest},
		{"no token field", "", `{}`, http.StatusBadRequest},
		{"urn form", "urn:uuid:" + unknown, "", http.StatusBadRequest},
		{"braced", "{" + unknown + "}", "", http.StatusBadRequest},
		{"no dashes", strings.ReplaceAll(unknown, "-", ""), "", http.StatusBadRequest},
		{"unknown uuid", unknown, "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for route, h := range map[string]http.HandlerFunc{"lookup": r.lookup, "press": r.press} {
				w := usCall(t, h, tc.token, tc.raw, nil)
				if w.Code != tc.code {
					t.Errorf("%s status = %d (%s), want %d", route, w.Code, w.Body.String(), tc.code)
				}
				if route == "lookup" && tc.code == http.StatusBadRequest && !strings.Contains(w.Body.String(), `"token is invalid"`) {
					t.Errorf("lookup 400 body = %s, want error \"token is invalid\"", w.Body.String())
				}
				if route == "lookup" && tc.code == http.StatusNotFound && !strings.Contains(w.Body.String(), `"unknown link"`) {
					t.Errorf("lookup 404 body = %s, want error \"unknown link\"", w.Body.String())
				}
			}
		})
	}
	r.requireNoVendorCall(t)
	var rowsAfter int
	if err := r.e.admin.QueryRow(context.Background(), `SELECT count(*) FROM mail_unsubscribes WHERE token = $1`, unknown).Scan(&rowsAfter); err != nil || rowsAfter != rowsBefore {
		t.Errorf("rows for the unknown token = %d (err %v), want %d: a refused call created a row", rowsAfter, err, rowsBefore)
	}
}

// A mail client or a person may upper-case the link: the same row answers.
func TestUnsubscribe_AnUpperCaseTokenResolvesTheSameRow(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "upper")
	up := strings.ToUpper(token)
	if up == token {
		t.Fatalf("control: token %q has no letter to upper-case", token)
	}
	w := usCall(t, r.lookup, up, "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unsubscribed":false`) {
		t.Errorf("lookup of the upper-case token: %d %s, want 200 for the same row", w.Code, w.Body.String())
	}
	usWantPress(t, usCall(t, r.press, up, "", nil), http.StatusOK, "unsubscribed")
	if usUnsubscribedAt(t, r.e, email) == nil {
		t.Error("the press of the upper-case token left unsubscribed_at NULL")
	}
}

func TestPressUnsubscribe_OptsOutAPresentContact(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "present")

	before := time.Now().Add(-2 * time.Second)
	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "unsubscribed")

	if got := r.rs.optOutCalls(); !slices.Equal(got, []string{email}) {
		t.Errorf("OptOutMarketing calls = %v, want exactly [%s]", got, email)
	}
	if n := r.rs.readCount(); n != 1 {
		t.Errorf("ContactState calls = %d, want 1", n)
	}
	if c := r.rs.got(); len(c) != 0 {
		t.Errorf("Sync calls = %d, want 0: a press never creates or opts in", len(c))
	}
	at := usUnsubscribedAt(t, r.e, email)
	if at == nil || at.Before(before) || at.After(time.Now().Add(2*time.Second)) {
		t.Errorf("unsubscribed_at = %v, want about now", at)
	}
}

func TestPressUnsubscribe_AMissingContactIsRecordedLocally(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "missing")
	r.rs.state = func(int) (ResendState, error) { return ResendState{Missing: true}, nil }

	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "unsubscribed")

	if n := r.rs.readCount(); n != 1 {
		t.Errorf("ContactState calls = %d, want 1", n)
	}
	if got := r.rs.optOutCalls(); len(got) != 0 {
		t.Errorf("OptOutMarketing calls = %v, want none for a missing contact", got)
	}
	if at := usUnsubscribedAt(t, r.e, email); at == nil {
		t.Error("unsubscribed_at is NULL, want set")
	}
}

func TestPressUnsubscribe_OffModeRecordsLocally(t *testing.T) {
	e := newEnv(t)
	email, token := usAddress(t, e, "off")
	press := UnsubscribeHandler(NewUnsubscribeStore(e.app), nil, (&qaLogSink{}).logger())

	usWantPress(t, usCall(t, press, token, "", nil), http.StatusOK, "unsubscribed")

	if at := usUnsubscribedAt(t, e, email); at == nil {
		t.Error("unsubscribed_at is NULL, want set")
	}
}

func TestPressUnsubscribe_AFailedReadRecordsNothing(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "readfail")
	r.rs.state = func(n int) (ResendState, error) {
		if n == 1 {
			return ResendState{}, &DeliveryError{Status: 503}
		}
		return ResendState{}, nil
	}

	w := usCall(t, r.press, token, "", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "unsubscribe is unavailable") {
		t.Fatalf("press with a failed read = %d %s, want 503 \"unsubscribe is unavailable\"", w.Code, w.Body.String())
	}
	if at := usUnsubscribedAt(t, r.e, email); at != nil {
		t.Errorf("unsubscribed_at = %v, want NULL", at)
	}
	if got := r.rs.optOutCalls(); len(got) != 0 {
		t.Errorf("OptOutMarketing calls = %v, want none after a failed read", got)
	}

	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "unsubscribed")
	if at := usUnsubscribedAt(t, r.e, email); at == nil {
		t.Error("unsubscribed_at is NULL after the retry, want set")
	}
}

func TestPressUnsubscribe_AFailedOptOutRecordsNothing(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "patchfail")
	r.rs.optOutFn = func(n int, _ string) error {
		if n == 1 {
			return &DeliveryError{Status: 500}
		}
		return nil
	}

	w := usCall(t, r.press, token, "", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "unsubscribe is unavailable") {
		t.Fatalf("press with a failed opt-out = %d %s, want 503 \"unsubscribe is unavailable\"", w.Code, w.Body.String())
	}
	if at := usUnsubscribedAt(t, r.e, email); at != nil {
		t.Errorf("unsubscribed_at = %v, want NULL: a failed PATCH rolls the local write back", at)
	}

	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "unsubscribed")
	if got := r.rs.optOutCalls(); len(got) != 2 {
		t.Errorf("OptOutMarketing calls after the retry = %d, want 2 (one failed, one sent)", len(got))
	}
	if at := usUnsubscribedAt(t, r.e, email); at == nil {
		t.Error("unsubscribed_at is NULL after the retry, want set")
	}
}

func TestPressUnsubscribe_AFailedLocalWriteSendsNoOptOut(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "writefail")
	drop := usRefuseLocalWrite(t, r.e, email)

	w := usCall(t, r.press, token, "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("press with a refused local write = %d %s, want 503", w.Code, w.Body.String())
	}
	if got := r.rs.optOutCalls(); len(got) != 0 {
		t.Errorf("OptOutMarketing calls = %v, want none: the local write precedes the PATCH (D4)", got)
	}
	if at := usUnsubscribedAt(t, r.e, email); at != nil {
		t.Errorf("unsubscribed_at = %v, want NULL", at)
	}

	drop()
	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "unsubscribed")
	if got := r.rs.optOutCalls(); !slices.Equal(got, []string{email}) {
		t.Errorf("OptOutMarketing calls after the retry = %v, want exactly [%s]", got, email)
	}
}

func TestPressUnsubscribe_AlreadyUnsubscribedMakesNoVendorCall(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "already")
	first := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	usSetUnsubscribed(t, r.e, email, first)

	usWantPress(t, usCall(t, r.press, token, "", nil), http.StatusOK, "already")

	r.requireNoVendorCall(t)
	if at := usUnsubscribedAt(t, r.e, email); at == nil || !at.Equal(first) {
		t.Errorf("unsubscribed_at = %v, want the first value %v", at, first)
	}
}

func TestPressUnsubscribe_SendsOnlyAMarketingOptOut(t *testing.T) {
	e := newEnv(t)
	email, token := usAddress(t, e, "wire")
	v := newVendor(t, func(s seenReq) (int, string) {
		switch {
		case s.Method == http.MethodGet && strings.HasSuffix(s.path(), "/topics"):
			return 200, `{"has_more":false,"data":[{"id":"blog-1","subscription":"opt_in"},{"id":"top-1","subscription":"opt_in"}]}`
		case s.Method == http.MethodGet:
			return 200, `{"object":"contact","unsubscribed":false}`
		case s.Method == http.MethodPatch && strings.HasSuffix(s.path(), "/topics"):
			return 200, `{"object":"contact_topics"}`
		}
		return 500, `{"message":"unexpected request"}`
	})
	press := UnsubscribeHandler(NewUnsubscribeStore(e.app), resendAt(v, nil), (&qaLogSink{}).logger())

	usWantPress(t, usCall(t, press, token, "", nil), http.StatusOK, "unsubscribed")

	var patches []seenReq
	for _, c := range v.calls() {
		switch c.Method {
		case http.MethodGet:
			if !strings.HasPrefix(c.path(), "/contacts/"+email) {
				t.Errorf("GET %s reads outside the contact", c.path())
			}
		case http.MethodPatch:
			patches = append(patches, c)
		default:
			t.Errorf("%s %s: a press sends only GETs and one PATCH", c.Method, c.path())
		}
		if strings.Contains(c.path(), "/suppressions") {
			t.Errorf("%s %s: a press never writes a suppression", c.Method, c.path())
		}
		for _, bad := range []string{"unsubscribed", "opt_in", "blog-1"} {
			if strings.Contains(string(c.Body), bad) {
				t.Errorf("%s %s body %q holds %q", c.Method, c.path(), c.Body, bad)
			}
		}
	}
	if len(patches) != 1 {
		t.Fatalf("PATCH calls = %d, want exactly 1", len(patches))
	}
	if got := patches[0].path(); got != "/contacts/"+email+"/topics" {
		t.Errorf("PATCH path = %q, want /contacts/%s/topics", got, email)
	}
	var body []map[string]string
	if err := json.Unmarshal(patches[0].Body, &body); err != nil || len(body) != 1 || body[0]["id"] != "top-1" || body[0]["subscription"] != "opt_out" || len(body[0]) != 2 {
		t.Errorf("PATCH body = %s, want [{id: top-1, subscription: opt_out}] (err %v)", patches[0].Body, err)
	}
}

func TestUnsubscribeHandlers_LogNoAddress(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "leakcheck")
	local := strings.SplitN(email, "@", 2)[0]
	pressFor := func(rs ResendClient, pool *pgxpool.Pool, sink *qaLogSink) http.HandlerFunc {
		return UnsubscribeHandler(NewUnsubscribeStore(pool), rs, sink.logger())
	}
	var sinks []*qaLogSink
	var bodies []string
	run := func(name string, h http.HandlerFunc, tok string, code int) {
		t.Helper()
		w := usCall(t, h, tok, "", nil)
		if w.Code != code {
			t.Errorf("%s: status = %d (%s), want %d", name, w.Code, w.Body.String(), code)
		}
		bodies = append(bodies, w.Body.String())
	}

	// Vendor read fails, then PATCH fails.
	readFails := &qaRecResend{state: func(int) (ResendState, error) { return ResendState{}, &DeliveryError{Status: 503} }}
	s1 := &qaLogSink{}
	run("read fails", pressFor(readFails, r.e.app, s1), token, http.StatusServiceUnavailable)
	patchFails := &qaRecResend{optOutFn: func(int, string) error { return &DeliveryError{Status: 500} }}
	s2 := &qaLogSink{}
	run("patch fails", pressFor(patchFails, r.e.app, s2), token, http.StatusServiceUnavailable)

	// The local write is refused with a database message that names the address.
	drop := usRefuseLocalWrite(t, r.e, email)
	s3 := &qaLogSink{}
	run("local write refused", pressFor(&qaRecResend{}, r.e.app, s3), token, http.StatusServiceUnavailable)
	drop()

	// A closed pool fails the lookup and the press with a 500.
	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	s4, s5 := &qaLogSink{}, &qaLogSink{}
	run("lookup on a closed pool", UnsubscribeLookupHandler(NewUnsubscribeStore(closed), s4.logger()), token, http.StatusInternalServerError)
	run("press on a closed pool", pressFor(&qaRecResend{}, closed, s5), token, http.StatusInternalServerError)

	// Refused tokens.
	s6 := &qaLogSink{}
	run("bad token", pressFor(&qaRecResend{}, r.e.app, s6), "abc", http.StatusBadRequest)
	run("unknown token", pressFor(&qaRecResend{}, r.e.app, s6), uuid.NewString(), http.StatusNotFound)

	// The store's own errors.
	store := NewUnsubscribeStore(r.e.app)
	drop = usRefuseLocalWrite(t, r.e, email)
	_, pressErr := store.Press(context.Background(), token, &qaRecResend{})
	drop()
	if pressErr == nil {
		t.Error("Press with a refused local write returned a nil error")
	}
	_, _, lookupErr := NewUnsubscribeStore(closed).Lookup(context.Background(), token)
	if lookupErr == nil {
		t.Error("Lookup on a closed pool returned a nil error")
	}

	sinks = append(sinks, s1, s2, s3, s4, s5, s6)
	qaRequireNoLeak(t, pressErr, sinks, email, local)
	qaRequireNoLeak(t, lookupErr, nil, email, local)
	for _, b := range bodies {
		if strings.Contains(b, email) || strings.Contains(b, local) {
			t.Errorf("a response body holds the address: %s", b)
		}
	}
}

func TestUnsubscribeHandlers_RefuseAUserHeader(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "hdr")

	// The same call without the header succeeds, so the 404 below is the header's doing.
	if w := usCall(t, r.lookup, token, "", nil); w.Code != http.StatusOK {
		t.Fatalf("lookup without X-User-ID = %d, want 200", w.Code)
	}
	for name, hdr := range map[string]map[string][]string{
		"a user id": {"X-User-Id": {"u-1"}},
		"empty":     {"X-User-Id": {""}},
	} {
		for route, h := range map[string]http.HandlerFunc{"lookup": r.lookup, "press": r.press} {
			if w := usCall(t, h, token, "", hdr); w.Code != http.StatusNotFound {
				t.Errorf("%s with %s: status = %d (%s), want 404", route, name, w.Code, w.Body.String())
			}
		}
	}
	r.requireNoVendorCall(t)
	if at := usUnsubscribedAt(t, r.e, email); at != nil {
		t.Errorf("unsubscribed_at = %v, want NULL: a refused press records nothing", at)
	}
}

func TestPressUnsubscribe_TwoTabsSendOneOptOut(t *testing.T) {
	r := usNewRig(t)
	email, token := usAddress(t, r.e, "tabs")
	entered, release := make(chan struct{}, 4), make(chan struct{})
	var open sync.Once
	t.Cleanup(func() { open.Do(func() { close(release) }) })
	r.rs.optOutFn = func(int, string) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-time.After(20 * time.Second):
			return errors.New("test: opt-out never released")
		}
	}

	type result struct {
		code   int
		status string
	}
	results := make(chan result, 2)
	press := func() {
		w := usCall(t, r.press, token, "", nil)
		res := result{code: w.Code}
		var b struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &b)
		res.status = b.Status
		results <- res
	}
	go press()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first press never reached the opt-out")
	}
	go press()
	// The second press must wait on the row lock; give it time to run past it if it could.
	time.Sleep(500 * time.Millisecond)
	if got := r.rs.optOutCalls(); len(got) != 1 {
		t.Fatalf("OptOutMarketing calls while the first press is parked = %d, want 1: the second press did not wait on the row lock", len(got))
	}
	open.Do(func() { close(release) })

	var statuses []string
	for range 2 {
		select {
		case res := <-results:
			if res.code != http.StatusOK {
				t.Errorf("a press answered %d, want 200", res.code)
			}
			statuses = append(statuses, res.status)
		case <-time.After(15 * time.Second):
			t.Fatal("a press never answered")
		}
	}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []string{"already", "unsubscribed"}) {
		t.Errorf("statuses = %v, want [already unsubscribed]", statuses)
	}
	if got := r.rs.optOutCalls(); !slices.Equal(got, []string{email}) {
		t.Errorf("OptOutMarketing calls = %v, want exactly [%s]", got, email)
	}
	if n := r.rs.readCount(); n != 1 {
		t.Errorf("ContactState calls = %d, want 1: the second press answers already before any read", n)
	}
	if at := usUnsubscribedAt(t, r.e, email); at == nil {
		t.Error("unsubscribed_at is NULL, want set")
	}
}
