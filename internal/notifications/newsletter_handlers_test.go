package notifications

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// nlResend answers the unsubscribe lookup per email and counts calls.
type nlResend struct {
	mu     sync.Mutex
	answer map[string]bool
	err    error
	emails []string
	syncs  int
	remain []time.Duration
}

func (c *nlResend) SyncBlog(context.Context, BlogSubscriber) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncs++
	return nil
}

func (c *nlResend) ContactUnsubscribed(ctx context.Context, email string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emails = append(c.emails, email)
	if d, ok := ctx.Deadline(); ok {
		c.remain = append(c.remain, time.Until(d))
	}
	return c.answer[email], c.err
}

type failSender struct{ err error }

func (f failSender) Send(context.Context, []accountmail.Message) error { return f.err }

type nlHandlers struct {
	*nlRig
	n    Newsletter
	mail *accountmail.Capture
	rs   *nlResend
	logs *qaLogSink
	sub  http.HandlerFunc
	conf http.HandlerFunc
}

func newNLHandlers(t *testing.T) *nlHandlers {
	t.Helper()
	h := &nlHandlers{nlRig: newNLRig(t), mail: &accountmail.Capture{}, rs: &nlResend{answer: map[string]bool{}}, logs: &qaLogSink{}}
	h.n = Newsletter{Store: h.ns, Sender: h.mail, Resend: h.rs, Enabled: true}
	h.rebuild()
	return h
}

func (h *nlHandlers) rebuild() {
	log := h.logs.logger()
	h.sub, h.conf = SubscribeHandler(h.n, log), ConfirmHandler(h.n, log)
}

func post(h http.HandlerFunc, body string, proxied bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if proxied {
		r.Header.Set("X-User-ID", "")
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func subBody(email string, ids []string, freq string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = `"` + id + `"`
	}
	return `{"email":"` + email + `","category_ids":[` + strings.Join(q, ",") + `],"frequency":"` + freq + `"}`
}

// tokenOf reads the confirm token out of a captured mail.
func tokenOf(t *testing.T, m accountmail.Message) string {
	t.Helper()
	_, after, ok := strings.Cut(m.HTML, "?token=")
	if !ok {
		t.Fatalf("mail holds no ?token=")
	}
	end := strings.IndexAny(after, `"'& <`)
	return after[:end]
}

func TestSubscribeHandler_SubscribedAndNewAnswerAlike(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	known, fresh := h.email(t), h.email(t)
	if _, err := h.ns.Confirm(context.Background(), hashOf(h.subscribe(t, known, ids, "weekly"))); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	a := post(h.sub, subBody(known, ids, "weekly"), false)
	b := post(h.sub, subBody(fresh, ids, "weekly"), false)
	if a.Code != 202 || b.Code != 202 || a.Body.String() != b.Body.String() {
		t.Fatalf("answers = %d %q / %d %q", a.Code, a.Body, b.Code, b.Body)
	}
	if len(a.Header()) == 0 || !reflect.DeepEqual(a.Header(), b.Header()) {
		t.Errorf("headers differ: %v / %v", a.Header(), b.Header())
	}
	to := map[string]int{}
	for _, m := range h.mail.Messages() {
		to[m.To]++
	}
	if to[known] != 1 || to[fresh] != 1 {
		t.Errorf("mail per address = %v, want one each", to)
	}
}

func TestSubscribeHandler_MixedCaseEmail(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	lower := "ada-" + strings.ToLower(h.email(t)[3:11]) + "@corp.co"
	t.Cleanup(func() {
		_, _ = h.e.admin.Exec(context.Background(), `DELETE FROM blog_subscription_requests WHERE email = $1`, lower)
	})
	mixed := strings.Replace(lower, "@corp.co", "@Corp.CO", 1)
	if w := post(h.sub, subBody(mixed, ids, "weekly"), false); w.Code != 202 {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	if _, ok := h.request(t, lower); !ok {
		t.Error("no request keyed by the lower-case email")
	}
	if msgs := h.mail.Messages(); len(msgs) != 1 || msgs[0].To != lower {
		t.Errorf("mail = %+v, want one to %s", msgs, lower)
	}
}

func TestSubscribeHandler_SendsOneConfirmationMail(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	post(h.sub, subBody(email, ids, "weekly"), false)
	msgs := h.mail.Messages()
	if len(msgs) != 1 || msgs[0].To != email || msgs[0].Subject != accountmail.BlogConfirmSubject {
		t.Fatalf("mail = %+v", msgs)
	}
	if want := "Confirm your ASComply blog subscription"; msgs[0].Subject != want {
		t.Errorf("subject = %q, want %q", msgs[0].Subject, want)
	}
	q, _ := h.request(t, email)
	if got := hashOf(tokenOf(t, msgs[0])); string(got) != string(q.Hash) {
		t.Error("the mailed token does not hash to the stored hash")
	}
	if len(h.rs.emails) != 0 || h.rs.syncs != 0 {
		t.Errorf("subscribe reached Resend: lookups=%v syncs=%d", h.rs.emails, h.rs.syncs)
	}
}

func TestSubscribeHandler_SendFailureIs502(t *testing.T) {
	h := newNLHandlers(t)
	h.n.Sender = failSender{&accountmail.SendError{Status: 500}}
	h.rebuild()
	ids := h.categories(t, 1)
	email := h.email(t)
	w := post(h.sub, subBody(email, ids, "weekly"), false)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "confirmation mail failed") {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	errs := h.logs.at(slog.LevelError)
	if len(errs) != 1 || !strings.Contains(errs[0].Text, "status=500") {
		t.Errorf("error lines = %+v, want one with status=500", errs)
	}
	qaRequireNoLeak(t, nil, []*qaLogSink{h.logs}, email)
}

func TestSubscribeHandler_RejectsBadFields(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	many := make([]string, 51)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, tc := range []struct{ name, body string }{
		{"no at sign", subBody("nobody.example", ids, "weekly")},
		{"no ids", subBody(email, nil, "weekly")},
		{"51 ids", subBody(email, many, "weekly")},
		{"bad id", subBody(email, []string{"Bad_ID"}, "weekly")},
		{"bad frequency", subBody(email, ids, "each")},
	} {
		if w := post(h.sub, tc.body, false); w.Code != 400 {
			t.Errorf("%s: status = %d, want 400", tc.name, w.Code)
		}
	}
	if _, ok := h.request(t, email); ok {
		t.Error("a request row was stored for a rejected body")
	}
	if len(h.mail.Messages()) != 0 {
		t.Error("a mail was sent for a rejected body")
	}
}

func TestNewsletterHandlers_ProxiedRequestIs404(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	if w := post(h.sub, subBody(email, ids, "weekly"), true); w.Code != 404 {
		t.Errorf("subscribe status = %d, want 404", w.Code)
	}
	if w := post(h.conf, `{"token":"x"}`, true); w.Code != 404 {
		t.Errorf("confirm status = %d, want 404", w.Code)
	}
	if _, ok := h.request(t, email); ok {
		t.Error("a request row was stored for a proxied call")
	}
}

func TestNewsletterHandlers_DisabledIs503(t *testing.T) {
	h := newNLHandlers(t)
	h.n.Enabled = false
	h.rebuild()
	ids := h.categories(t, 1)
	email := h.email(t)
	for name, w := range map[string]*httptest.ResponseRecorder{
		"subscribe": post(h.sub, subBody(email, ids, "weekly"), false),
		"confirm":   post(h.conf, `{"token":"x"}`, false),
	} {
		if w.Code != 503 || !strings.Contains(w.Body.String(), "subscriptions are unavailable") {
			t.Errorf("%s: answer = %d %s", name, w.Code, w.Body)
		}
	}
	if _, ok := h.request(t, email); ok {
		t.Error("a request row was stored while disabled")
	}
}

// confirmable subscribes email through the handler and returns the mailed token.
func (h *nlHandlers) confirmable(t *testing.T, email string, ids []string) string {
	t.Helper()
	before := len(h.mail.Messages())
	if w := post(h.sub, subBody(email, ids, "weekly"), false); w.Code != 202 {
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	return tokenOf(t, h.mail.Messages()[before])
}

func TestConfirmHandler_ReportsUnsubscribedEverywhere(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	yes, no := h.email(t), h.email(t)
	h.rs.answer[yes] = true
	w := post(h.conf, `{"token":"`+h.confirmable(t, yes, ids)+`"}`, false)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"confirmed","unsubscribed_everywhere":true}` {
		t.Errorf("yes: %d %s", w.Code, w.Body)
	}
	w = post(h.conf, `{"token":"`+h.confirmable(t, no, ids)+`"}`, false)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"confirmed","unsubscribed_everywhere":false}` {
		t.Errorf("no: %d %s", w.Code, w.Body)
	}
	if got := strings.Join(h.rs.emails, ","); got != yes+","+no {
		t.Errorf("lookups = %q, want %q", got, yes+","+no)
	}
}

func TestConfirmHandler_LookupFailureStillConfirms(t *testing.T) {
	h := newNLHandlers(t)
	h.rs.err = &DeliveryError{Status: 503}
	ids := h.categories(t, 1)
	email := h.email(t)
	tok := h.confirmable(t, email, ids)
	w := post(h.conf, `{"token":"`+tok+`"}`, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unsubscribed_everywhere":false`) {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	if _, ok := h.subscriber(t, email); !ok {
		t.Error("no subscriber row")
	}
	if n := len(h.jobs(t, email, "blog_subscriber_sync")); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
	warns := h.logs.at(slog.LevelWarn)
	if len(warns) != 1 || !strings.Contains(warns[0].Text, "status=503") {
		t.Errorf("warn lines = %+v, want one with status=503", warns)
	}
	qaRequireNoLeak(t, nil, []*qaLogSink{h.logs}, email, tok)
}

func TestConfirmHandler_GoneTokenMakesNoLookup(t *testing.T) {
	h := newNLHandlers(t)
	w := post(h.conf, `{"token":"`+strings.Repeat("a", 43)+`"}`, false)
	if w.Code != 410 {
		t.Errorf("status = %d, want 410", w.Code)
	}
	if len(h.rs.emails) != 0 {
		t.Errorf("lookups = %v, want none", h.rs.emails)
	}
}

func TestConfirmHandler_Outcomes(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	tok := h.confirmable(t, h.email(t), ids)
	body := `{"token":"` + tok + `"}`
	if w := post(h.conf, body, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"confirmed"`) {
		t.Errorf("valid: %d %s", w.Code, w.Body)
	}
	if w := post(h.conf, body, false); w.Code != 410 || !strings.Contains(w.Body.String(), "link expired or used") {
		t.Errorf("reused: %d %s", w.Code, w.Body)
	}
	if w := post(h.conf, `{"token":"`+strings.Repeat("b", 43)+`"}`, false); w.Code != 410 {
		t.Errorf("unknown: %d", w.Code)
	}
	if w := post(h.conf, `{"token":`, false); w.Code != 400 {
		t.Errorf("malformed: %d", w.Code)
	}
}

func TestConfirmHandler_StoreFailureIs500(t *testing.T) {
	h := newNLHandlers(t)
	h.n.Store = brokenStore{}
	h.rebuild()
	if w := post(h.conf, `{"token":"x"}`, false); w.Code != 500 {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

type brokenStore struct{}

func (brokenStore) Subscribe(context.Context, SubscribeIntake) (string, []string, error) {
	return "", nil, errors.New("boom")
}

func (brokenStore) Confirm(context.Context, []byte) (string, error) { return "", errors.New("boom") }

func TestSubscribeHandler_CategoryEdgeCases(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"one unknown among known", []string{ids[0], "nope-" + ids[0]}},
		{"duplicated unknown", []string{"nope-x", "nope-x"}},
	} {
		w := post(h.sub, subBody(email, tc.ids, "weekly"), false)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "unknown category") {
			t.Errorf("%s: answer = %d %s, want 400 unknown category", tc.name, w.Code, w.Body)
		}
	}
	if _, ok := h.request(t, email); ok || len(h.mail.Messages()) != 0 {
		t.Error("a rejected category list stored a row or sent a mail")
	}
	if w := post(h.sub, subBody(email, []string{ids[0], ids[0], ids[0]}, "weekly"), false); w.Code != 202 {
		t.Fatalf("duplicate known ids: %d %s", w.Code, w.Body)
	}
	if q, ok := h.request(t, email); !ok || !reflect.DeepEqual(q.IDs, ids) {
		t.Errorf("stored ids = %v, want %v", q.IDs, ids)
	}
}

func TestNewsletterHandlers_OversizedBodyIs400(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	big := subBody(strings.Repeat("a", 20<<10)+"@x.co", ids, "weekly")
	if w := post(h.sub, big, false); w.Code != 400 {
		t.Errorf("subscribe oversized: %d, want 400", w.Code)
	}
	if w := post(h.conf, `{"token":"`+strings.Repeat("t", 20<<10)+`"}`, false); w.Code != 400 {
		t.Errorf("confirm oversized: %d, want 400", w.Code)
	}
	if w := post(h.conf, `{"token":""}`, false); w.Code != 410 {
		t.Errorf("confirm empty token: %d, want 410", w.Code)
	}
	if _, ok := h.request(t, email); ok || len(h.mail.Messages()) != 0 {
		t.Error("an oversized body stored a row or sent a mail")
	}
}

func TestSubscribeHandler_CaseVariantsShareOneRequest(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	post(h.sub, subBody(" "+strings.ToUpper(email)+" ", ids, "weekly"), false)
	post(h.sub, subBody(email, ids, "monthly"), false)
	msgs := h.mail.Messages()
	if len(msgs) != 2 || msgs[0].To != email || msgs[1].To != email {
		t.Fatalf("mail = %+v, want two to %s", msgs, email)
	}
	var n int
	if err := h.e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM blog_subscription_requests WHERE lower(btrim(email)) = $1`, email).Scan(&n); err != nil || n != 1 {
		t.Fatalf("requests for the address = %d (err %v), want 1", n, err)
	}
	if w := post(h.conf, `{"token":"`+tokenOf(t, msgs[0])+`"}`, false); w.Code != 410 {
		t.Errorf("first link after a re-subscribe: %d, want 410", w.Code)
	}
	if w := post(h.conf, `{"token":"`+tokenOf(t, msgs[1])+`"}`, false); w.Code != 200 {
		t.Fatalf("second link: %d %s", w.Code, w.Body)
	}
	if s, ok := h.subscriber(t, email); !ok || s.Frequency != "monthly" {
		t.Errorf("subscriber = %+v, want monthly under the lower-case email", s)
	}
	if jobs := h.jobs(t, email, "blog_subscriber_sync"); len(jobs) != 1 || !strings.Contains(jobs[0], email) {
		t.Errorf("jobs = %v, want one with the lower-case email", jobs)
	}
}

func TestConfirmHandler_ConcurrentPostsOneWins(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 1)
	email := h.email(t)
	body := `{"token":"` + h.confirmable(t, email, ids) + `"}`
	codes := make([]int, 6)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = post(h.conf, body, false).Code
		}()
	}
	wg.Wait()
	var ok, gone int
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 410:
			gone++
		}
	}
	if ok != 1 || gone != len(codes)-1 {
		t.Errorf("codes = %v, want one 200 and the rest 410", codes)
	}
	if len(h.rs.emails) != 1 {
		t.Errorf("lookups = %d, want 1", len(h.rs.emails))
	}
	if n := len(h.jobs(t, email, "blog_subscriber_sync")); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
}

func TestSubscribeHandler_CategoryCountBoundNeedsRealIds(t *testing.T) {
	h := newNLHandlers(t)
	ids := h.categories(t, 51)
	email := h.email(t)
	if w := post(h.sub, subBody(email, ids, "weekly"), false); w.Code != 400 || !strings.Contains(w.Body.String(), "1 to 50") {
		t.Errorf("51 real ids: %d %s, want 400 naming the bound", w.Code, w.Body)
	}
	if _, ok := h.request(t, email); ok {
		t.Error("51 ids stored a request")
	}
	if w := post(h.sub, subBody(email, ids[:50], "weekly"), false); w.Code != 202 {
		t.Errorf("50 real ids: %d %s, want 202", w.Code, w.Body)
	}
	if w := post(h.sub, subBody(email, []string{"Bad_ID"}, "weekly"), false); w.Code != 400 || !strings.Contains(w.Body.String(), "category_ids is invalid") {
		t.Errorf("bad id: %d %s, want the pattern message", w.Code, w.Body)
	}
}

func TestConfirmHandler_LookupHasAThreeSecondDeadline(t *testing.T) {
	h := newNLHandlers(t)
	email := h.email(t)
	post(h.conf, `{"token":"`+h.confirmable(t, email, h.categories(t, 1))+`"}`, false)
	if len(h.rs.remain) != 1 || h.rs.remain[0] <= 0 || h.rs.remain[0] > 3*time.Second {
		t.Errorf("lookup deadlines = %v, want one within (0, 3s]", h.rs.remain)
	}
}
