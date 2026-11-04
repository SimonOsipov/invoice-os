package notifications

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

const (
	ngRun1    = "nightly-1-blog@x.resend.app"
	ngRun2    = "NIGHTLY-2-blog@X.RESEND.APP"
	ngForeign = "ada@corp.example"
)

type ngBodies struct {
	mu sync.Mutex
	b  strings.Builder
}

func (n *ngBodies) String() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.b.String()
}

func ngWiring(t *testing.T) (NewsletterWiring, *clSpyTransport, *ngBodies) {
	t.Helper()
	bodies := &ngBodies{}
	spy := &clSpyTransport{respond: func(_ int, r *http.Request) *http.Response {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			bodies.mu.Lock()
			bodies.b.Write(b)
			bodies.mu.Unlock()
		}
		return clReply(r, http.StatusOK, nil)
	}}
	vars := clNightlyVars("RESEND_SENDING_KEY", "sk")
	contacts, _, _, err := DeliveryClients(clGetenv(vars), "nightly", spy)
	if err != nil {
		t.Fatalf("deliveryClients: %v", err)
	}
	w, err := NewsletterClients(clGetenv(vars), "nightly", contacts, spy)
	if err != nil {
		t.Fatalf("newsletterClients: %v", err)
	}
	return w, spy, bodies
}

func TestNightlyGuard_OnlyRunAddressesAreMailed(t *testing.T) {
	w, spy, bodies := ngWiring(t)
	if err := w.Sender.Send(t.Context(), []accountmail.Message{{To: ngForeign}}); err != nil {
		t.Fatalf("send foreign: %v", err)
	}
	if n := len(spy.seen()); n != 0 {
		t.Fatalf("a foreign-only send made %d Resend requests, want 0", n)
	}
	msgs := []accountmail.Message{{To: ngRun1}, {To: ngForeign}, {To: ngRun2}}
	if err := w.Sender.Send(t.Context(), msgs); err != nil {
		t.Fatalf("send: %v", err)
	}
	reqs := spy.seen()
	if len(reqs) == 0 {
		t.Fatal("no Resend request for the run addresses")
	}
	for _, r := range reqs {
		if r.Path != "/emails/batch" {
			t.Errorf("request to %s, want /emails/batch only", r.Path)
		}
	}
	body := bodies.String()
	if strings.Contains(body, "corp.example") {
		t.Error("a Resend request body holds the foreign address")
	}
	if !strings.Contains(body, ngRun1) || !strings.Contains(body, ngRun2) {
		t.Errorf("batch body %q misses a run address", body)
	}
	g, ok := w.Sender.(*guardSender)
	if !ok {
		t.Fatalf("sender = %T, want *guardSender", w.Sender)
	}
	if got := g.capture.Messages(); len(got) != 2 || got[0].To != ngForeign || got[1].To != ngForeign {
		t.Errorf("capture = %+v, want only the two foreign messages", got)
	}
}

func TestNightlyGuard_ForeignSubscriberMakesNoResendCall(t *testing.T) {
	w, spy, _ := ngWiring(t)
	ctx := t.Context()
	if err := w.Resend.SyncBlog(ctx, BlogSubscriber{Email: ngForeign, CategoryIDs: []string{"c"}, Frequency: "weekly"}); err != nil {
		t.Fatalf("SyncBlog: %v", err)
	}
	if got, err := w.Resend.ContactUnsubscribed(ctx, ngForeign); err != nil || got {
		t.Errorf("ContactUnsubscribed = %v, %v, want false, nil", got, err)
	}
	if n := len(spy.seen()); n != 0 {
		t.Fatalf("foreign address made %d Resend requests, want 0", n)
	}
	_ = w.Resend.SyncBlog(ctx, BlogSubscriber{Email: ngRun1, CategoryIDs: []string{"c"}, Frequency: "weekly"})
	n := len(spy.seen())
	if n == 0 {
		t.Error("run address made no Resend request")
	}
	_, _ = w.Resend.ContactUnsubscribed(ctx, ngRun2)
	if len(spy.seen()) <= n {
		t.Error("ContactUnsubscribed for a run address made no Resend request")
	}
}

func TestNightlyGuard_LookalikeAddressesAreForeign(t *testing.T) {
	for _, e := range []string{"nightly-1@x.resend.app.evil.example", "xnightly-1@x.resend.app", "nightly-1@evil.example", "nightly-1@evilx.resend.app", ""} {
		if isNightlyRunAddress(e, "x.resend.app") {
			t.Errorf("%q allowed, want foreign", e)
		}
	}
	for _, e := range []string{ngRun1, ngRun2} {
		for _, d := range []string{"x.resend.app", "X.Resend.App"} {
			if !isNightlyRunAddress(e, d) {
				t.Errorf("%q on %q foreign, want allowed", e, d)
			}
		}
	}
}
