package erp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
)

// fakeWebhookKey is a fixture, not a secret: the fake runs only on a preview, the nightly and a local ERP_FAKE run.
const (
	fakeWebhookKey      = "erpf-fake-webhook-key"
	fakeSignatureHeader = "X-Fake-Signature"
)

// FakeWebhookSignature is the header value the fake accepts for body.
func FakeWebhookSignature(body []byte) string {
	m := hmac.New(sha256.New, []byte(fakeWebhookKey))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifyWebhook checks the signature, then reads one hint per event; it does not check the seed.
func (*Fake) VerifyWebhook(_ context.Context, h http.Header, body []byte) ([]Hint, error) {
	if !hmac.Equal([]byte(h.Get(fakeSignatureHeader)), []byte(FakeWebhookSignature(body))) {
		return nil, errors.New("erp: fake webhook signature mismatch")
	}
	var p struct {
		Events []struct{ Company, Invoice string } `json:"events"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	var hints []Hint
	for _, e := range p.Events {
		hints = append(hints, Hint{Scope: e.Company, SourceDocID: e.Invoice})
	}
	return hints, nil
}
