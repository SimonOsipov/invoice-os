package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// metadataIdentityURL is the Cloud Run metadata server's ID-token endpoint.
const metadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

// idTokens is shared by every IDTokenTransport, so a process fetches one token per audience.
var idTokens = &idTokenCache{metadata: metadataIdentityURL, now: time.Now, tokens: map[string]idToken{}}

// IDTokenTransport signs each request for a *.run.app host with a Google ID token. TraceTransport
// includes it; a first-party client that skips TraceTransport (the gateway's GoTrue and probe clients)
// wraps itself. A nil base is http.DefaultTransport.
func IDTokenTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &idTokenTransport{base: base, cache: idTokens}
}

// idTokenTransport adds a Google ID token to each request for a *.run.app host, so Cloud Run admits
// the call to an ingress-internal service; the gateway token rides on top. It uses
// X-Serverless-Authorization because the gateway forwards a user's JWT in Authorization.
type idTokenTransport struct {
	base  http.RoundTripper
	cache *idTokenCache
}

type idTokenCache struct {
	metadata string
	now      func() time.Time

	mu     sync.Mutex
	tokens map[string]idToken
}

type idToken struct {
	value  string
	expiry time.Time
}

func newIDTokenTransport(base http.RoundTripper, metadata string, now func() time.Time) *idTokenTransport {
	return &idTokenTransport{base: base, cache: &idTokenCache{metadata: metadata, now: now, tokens: map[string]idToken{}}}
}

func (t *idTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !strings.HasSuffix(host, ".run.app") {
		return t.base.RoundTrip(req)
	}
	tok, err := t.cache.token(req.Context(), t.base, "https://"+host)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("X-Serverless-Authorization", "Bearer "+tok)
	return t.base.RoundTrip(req)
}

// token returns a cached token for audience until five minutes before it expires.
func (t *idTokenCache) token(ctx context.Context, rt http.RoundTripper, audience string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tok, ok := t.tokens[audience]; ok && t.now().Before(tok.expiry.Add(-5*time.Minute)) {
		return tok.value, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.metadata+"?audience="+url.QueryEscape(audience), nil)
	if err != nil {
		return "", fmt.Errorf("platform: id token for %s: %w", audience, err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("platform: id token for %s: %w", audience, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("platform: id token for %s: status %d", audience, resp.StatusCode)
	}
	value := strings.TrimSpace(string(body))
	expiry, err := jwtExpiry(value)
	if err != nil {
		return "", fmt.Errorf("platform: id token for %s: %w", audience, err)
	}
	t.tokens[audience] = idToken{value: value, expiry: expiry}
	return value, nil
}

// jwtExpiry reads exp from a JWT payload without verifying it: the token came from the metadata server.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("JWT payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT has no exp")
	}
	return time.Unix(claims.Exp, 0), nil
}
