package erp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"golang.org/x/oauth2"
)

const (
	quickBooksSystem = "quickbooks"

	intuitAuthURL    = "https://appcenter.intuit.com/connect/oauth2"
	intuitTokenURL   = "https://oauth.platform.intuit.com/oauth2/v1/tokens/bearer"
	intuitRevokeURL  = "https://developer.api.intuit.com/v2/oauth2/tokens/revoke"
	intuitSandboxAPI = "https://sandbox-quickbooks.api.intuit.com"
	intuitScope      = "com.intuit.quickbooks.accounting"
	intuitCallTime   = 10 * time.Second

	intuitMinorVersion = "75"
	intuitMaxBody      = 1 << 20
	intuitRetryAfter   = 60 * time.Second
)

// realmPattern bounds the realm before it reaches a URL path.
var realmPattern = regexp.MustCompile(`^[0-9]{1,64}$`)

// intuitConfig is the Intuit app's credentials, read once at boot.
type intuitConfig struct {
	clientID, clientSecret, webhookVerifier string
}

// intuitEndpoints are Intuit's URLs; tests point them at a stub through newQuickBooksAt.
type intuitEndpoints struct{ auth, token, revoke, api string }

// QuickBooks is the QuickBooks Online connector; it can sign in and refresh, and every other call but the
// static ones answers ErrUnsupported.
type QuickBooks struct {
	redirect *url.URL
	cfg      intuitConfig
	oauth    oauth2.Config
	ep       intuitEndpoints
	http     *http.Client
}

// qbToken is the grant secret.
type qbToken struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

func NewQuickBooks(redirect *url.URL, cfg intuitConfig) *QuickBooks {
	// Third-party: no platform.TraceTransport.
	return newQuickBooksAt(redirect, cfg,
		intuitEndpoints{auth: intuitAuthURL, token: intuitTokenURL, revoke: intuitRevokeURL, api: intuitSandboxAPI},
		&http.Client{Timeout: intuitCallTime})
}

func newQuickBooksAt(redirect *url.URL, cfg intuitConfig, ep intuitEndpoints, hc *http.Client) *QuickBooks {
	return &QuickBooks{
		redirect: redirect, cfg: cfg, ep: ep, http: hc,
		oauth: oauth2.Config{
			ClientID:     cfg.clientID,
			ClientSecret: cfg.clientSecret,
			RedirectURL:  redirect.String(),
			Scopes:       []string{intuitScope},
			Endpoint:     oauth2.Endpoint{AuthURL: ep.auth, TokenURL: ep.token, AuthStyle: oauth2.AuthStyleInHeader},
		},
	}
}

// intuitErr names the operation and the HTTP status only: a RetrieveError's text quotes Intuit's
// reply body, and the cause can carry the code or a token.
func intuitErr(op string, err error) error {
	var re *oauth2.RetrieveError
	var ne net.Error
	switch {
	case errors.As(err, &re):
		return fmt.Errorf("erp: quickbooks %s: status %d", op, re.Response.StatusCode)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("erp: quickbooks %s: timed out", op)
	default:
		return fmt.Errorf("erp: quickbooks %s: request failed", op)
	}
}

func (*QuickBooks) System() string             { return quickBooksSystem }
func (*QuickBooks) Capabilities() Capabilities { return Capabilities{} }
func (*QuickBooks) DefaultMapping() Mapping    { return Mapping{} }

func (q *QuickBooks) Authorize(_ context.Context, req AuthorizeRequest) (AuthorizeResult, error) {
	if req.State == "" {
		return AuthorizeResult{}, errors.New("erp: quickbooks authorize: empty state")
	}
	return AuthorizeResult{ConsentURL: q.oauth.AuthCodeURL(req.State)}, nil
}

func (q *QuickBooks) Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
	if req.Code == "" {
		return ExchangeResult{}, errors.New("erp: quickbooks exchange: empty code")
	}
	if !realmPattern.MatchString(req.RealmID) {
		return ExchangeResult{}, errors.New("erp: quickbooks exchange: bad realm")
	}
	tok, err := q.oauth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, q.http), req.Code)
	if err != nil {
		return ExchangeResult{}, intuitErr("exchange", err)
	}
	if tok.RefreshToken == "" || tok.Expiry.IsZero() {
		return ExchangeResult{}, errors.New("erp: quickbooks exchange: incomplete reply")
	}
	secret, err := json.Marshal(qbToken{Access: tok.AccessToken, Refresh: tok.RefreshToken})
	if err != nil {
		return ExchangeResult{}, errors.New("erp: quickbooks exchange: encode failed")
	}
	exp := tok.Expiry
	return ExchangeResult{
		ExternalKey: req.RealmID,
		Grant:       GrantMaterial{Kind: GrantOAuthRefresh, Secret: secret, ExpiresAt: &exp},
	}, nil
}

func (q *QuickBooks) Refresh(ctx context.Context, c Credentials) (GrantMaterial, error) {
	var old qbToken
	if err := json.Unmarshal(c.Secret, &old); err != nil || old.Refresh == "" {
		return GrantMaterial{}, errors.New("erp: quickbooks refresh: unreadable secret")
	}
	// No access token and no expiry on the seed, so the source always calls Intuit. It keeps the
	// refresh token we sent when the reply omits one.
	tok, err := q.oauth.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, q.http),
		&oauth2.Token{RefreshToken: old.Refresh}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return GrantMaterial{}, fmt.Errorf("erp: quickbooks refresh: %w", ErrGrantRevoked)
		}
		return GrantMaterial{}, intuitErr("refresh", err)
	}
	if tok.Expiry.IsZero() {
		return GrantMaterial{}, errors.New("erp: quickbooks refresh: incomplete reply")
	}
	secret, err := json.Marshal(qbToken{Access: tok.AccessToken, Refresh: tok.RefreshToken})
	if err != nil {
		return GrantMaterial{}, errors.New("erp: quickbooks refresh: encode failed")
	}
	exp := tok.Expiry
	return GrantMaterial{Kind: GrantOAuthRefresh, Secret: secret, ExpiresAt: &exp}, nil
}

// Validate proves the grant and the realm by reading the company's CompanyInfo. Errors name the
// status only: Intuit's reply is never quoted.
func (q *QuickBooks) Validate(ctx context.Context, c Credentials, scope string) error {
	if !realmPattern.MatchString(scope) {
		return fmt.Errorf("erp: quickbooks validate: %w", ErrUnknownScope)
	}
	var tok qbToken
	if err := json.Unmarshal(c.Secret, &tok); err != nil || tok.Access == "" {
		return errors.New("erp: quickbooks validate: unreadable secret")
	}
	u := q.ep.api + "/v3/company/" + scope + "/companyinfo/" + scope + "?minorversion=" + intuitMinorVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("erp: quickbooks validate: request failed")
	}
	req.Header.Set("Authorization", "Bearer "+tok.Access)
	req.Header.Set("Accept", "application/json")
	resp, err := q.http.Do(req)
	if err != nil {
		return intuitErr("validate", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("erp: quickbooks validate: status %d: %w", resp.StatusCode, ErrUnknownScope)
	case http.StatusTooManyRequests:
		return fmt.Errorf("erp: quickbooks validate: %w", &RateLimitError{RetryAfter: intuitRetryAfter})
	default:
		return fmt.Errorf("erp: quickbooks validate: status %d", resp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, intuitMaxBody)).Decode(&body); err != nil {
		return errors.New("erp: quickbooks validate: unreadable reply")
	}
	if _, ok := body["CompanyInfo"]; !ok {
		return errors.New("erp: quickbooks validate: reply has no CompanyInfo")
	}
	return nil
}

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
