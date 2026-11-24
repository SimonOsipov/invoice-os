package erp

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

func TestQuickBooks_SurfaceBeforeSignIn(t *testing.T) {
	redirect, _ := url.Parse("https://gw.example/erp/callback")
	q := NewQuickBooks(redirect, intuitConfig{clientID: "v-" + uuid.NewString()})
	ctx, c := context.Background(), Credentials{}
	if q.System() != "quickbooks" || q.Capabilities() != (Capabilities{}) {
		t.Errorf("System %q, Capabilities %+v", q.System(), q.Capabilities())
	}
	if m := q.DefaultMapping(); m == nil || len(m) != 0 {
		t.Errorf("DefaultMapping = %#v, want empty non-nil", m)
	}
	errs := map[string]error{}
	_, errs["Authorize"] = q.Authorize(ctx, AuthorizeRequest{})
	_, errs["Exchange"] = q.Exchange(ctx, ExchangeRequest{})
	_, errs["Refresh"] = q.Refresh(ctx, c)
	errs["Validate"] = q.Validate(ctx, c, "")
	_, errs["ListChanged"] = q.ListChanged(ctx, c, "", nil)
	_, errs["Fetch"] = q.Fetch(ctx, c, DocRef{})
	_, errs["FetchParty"] = q.FetchParty(ctx, c, "", "")
	_, errs["WriteBack"] = q.WriteBack(ctx, c, DocRef{}, WriteBack{})
	_, errs["VerifyWebhook"] = q.VerifyWebhook(ctx, nil, nil)
	errs["EnsureSubscriptions"] = q.EnsureSubscriptions(ctx, c, "")
	if len(errs) != 10 {
		t.Fatalf("%d methods checked", len(errs))
	}
	for name, err := range errs {
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
