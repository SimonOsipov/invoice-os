package erp

import (
	"context"
	"net/http"
	"net/url"
)

const quickBooksSystem = "quickbooks"

// intuitConfig is the Intuit app's credentials, read once at boot.
type intuitConfig struct {
	clientID, clientSecret, webhookVerifier string
}

// QuickBooks is the QuickBooks Online connector; it cannot sign in yet, so every call but the
// static ones answers ErrUnsupported.
type QuickBooks struct {
	redirect *url.URL
	cfg      intuitConfig
}

func NewQuickBooks(redirect *url.URL, cfg intuitConfig) *QuickBooks {
	return &QuickBooks{redirect: redirect, cfg: cfg}
}

func (*QuickBooks) System() string             { return quickBooksSystem }
func (*QuickBooks) Capabilities() Capabilities { return Capabilities{} }
func (*QuickBooks) DefaultMapping() Mapping    { return Mapping{} }

func (*QuickBooks) Authorize(context.Context, AuthorizeRequest) (AuthorizeResult, error) {
	return AuthorizeResult{}, ErrUnsupported
}

func (*QuickBooks) Exchange(context.Context, ExchangeRequest) (ExchangeResult, error) {
	return ExchangeResult{}, ErrUnsupported
}

func (*QuickBooks) Refresh(context.Context, Credentials) (GrantMaterial, error) {
	return GrantMaterial{}, ErrUnsupported
}

func (*QuickBooks) Validate(context.Context, Credentials, string) error { return ErrUnsupported }

func (*QuickBooks) ListChanged(context.Context, Credentials, string, []byte) (Changes, error) {
	return Changes{}, ErrUnsupported
}

func (*QuickBooks) Fetch(context.Context, Credentials, DocRef) (Document, error) {
	return Document{}, ErrUnsupported
}

func (*QuickBooks) FetchParty(context.Context, Credentials, string, string) (Party, error) {
	return Party{}, ErrUnsupported
}

func (*QuickBooks) WriteBack(context.Context, Credentials, DocRef, WriteBack) (DocRef, error) {
	return DocRef{}, ErrUnsupported
}

func (*QuickBooks) VerifyWebhook(context.Context, http.Header, []byte) ([]Hint, error) {
	return nil, ErrUnsupported
}

func (*QuickBooks) EnsureSubscriptions(context.Context, Credentials, string) error {
	return ErrUnsupported
}
