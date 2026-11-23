package erp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
)

func signedHeader(body []byte) http.Header {
	return http.Header{fakeSignatureHeader: {FakeWebhookSignature(body)}}
}

func TestFake_CapabilitiesReportWebhooks(t *testing.T) {
	if !NewFake(testRedirect).Capabilities().Webhooks {
		t.Error("Webhooks = false")
	}
}

func TestFakeWebhook_SignedBodyYieldsOneHintPerEvent(t *testing.T) {
	body := []byte(`{"events":[{"company":"fake-co-1","invoice":"fake-co-1-inv-1"},{"company":"fake-co-2","invoice":"fake-co-2-inv-1"}]}`)
	got, err := NewFake(testRedirect).VerifyWebhook(ctx, signedHeader(body), body)
	want := []Hint{{"fake-co-1", "fake-co-1-inv-1"}, {"fake-co-2", "fake-co-2-inv-1"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}

func TestFakeWebhook_EmptyEventsIsNoHint(t *testing.T) {
	body := []byte(`{"events":[]}`)
	got, err := NewFake(testRedirect).VerifyWebhook(ctx, signedHeader(body), body)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestFakeWebhook_RefusesAMissingOrWrongSignature(t *testing.T) {
	body := []byte(`{"events":[{"company":"fake-co-1","invoice":"fake-co-1-inv-1"}]}`)
	m := hmac.New(sha256.New, []byte("other-key"))
	m.Write(body)
	otherKey := hex.EncodeToString(m.Sum(nil))
	good := FakeWebhookSignature(body)
	tampered := append([]byte(nil), body...)
	tampered[3] ^= 1
	cases := map[string]struct {
		hdr  http.Header
		body []byte
	}{
		"no header":         {http.Header{}, body},
		"non-hex":           {http.Header{fakeSignatureHeader: {"sha256=zzzz"}}, body},
		"wrong prefix":      {http.Header{fakeSignatureHeader: {"sha1=" + good[len("sha256="):]}}, body},
		"other key":         {http.Header{fakeSignatureHeader: {"sha256=" + otherKey}}, body},
		"other body":        {signedHeader([]byte(`{"events":[]}`)), body},
		"body byte changed": {http.Header{fakeSignatureHeader: {good}}, tampered},
	}
	for name, c := range cases {
		got, err := NewFake(testRedirect).VerifyWebhook(ctx, c.hdr, c.body)
		if err == nil || len(got) != 0 {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
}

func TestFakeWebhook_RefusesASignedBodyThatIsNotEvents(t *testing.T) {
	for _, b := range []string{"not json", `{"events":"x"}`} {
		got, err := NewFake(testRedirect).VerifyWebhook(ctx, signedHeader([]byte(b)), []byte(b))
		if err == nil || len(got) != 0 {
			t.Errorf("%q: got %v, %v", b, got, err)
		}
	}
}

func TestFakeWebhook_UnknownCompanyIsStillAHint(t *testing.T) {
	body := []byte(`{"events":[{"company":"fake-co-none","invoice":"x"}]}`)
	got, err := NewFake(testRedirect).VerifyWebhook(ctx, signedHeader(body), body)
	if err != nil || !slices.Equal(got, []Hint{{"fake-co-none", "x"}}) {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestFake_EnsureSubscriptionsKnowsOnlySeededCompanies(t *testing.T) {
	f := NewFake(testRedirect)
	for _, c := range erpweb.FakeCompanies {
		if err := f.EnsureSubscriptions(ctx, cr, c); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{"fake-co-9", ""} {
		if err := f.EnsureSubscriptions(ctx, cr, c); !errors.Is(err, ErrUnknownScope) {
			t.Errorf("%q: %v", c, err)
		}
	}
}
