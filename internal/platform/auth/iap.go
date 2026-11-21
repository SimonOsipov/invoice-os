package auth

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Mode A stub: compiles the CSSO-01-02 red tests. The executor replaces it.

const (
	IAPJWKSURL = "https://www.gstatic.com/iap/verify/public_key-jwk"
	IAPIssuer  = "https://cloud.google.com/iap"
)

// IAPIdentity is the Google account an IAP assertion proves.
type IAPIdentity struct {
	Email   string
	Subject string
}

// IAPConfig configures an IAPVerifier.
type IAPConfig struct {
	Audiences []string
	JWKSURL   string
	Client    *http.Client
	Now       func() time.Time
}

// IAPVerifier verifies IAP signed-header assertions.
type IAPVerifier struct{}

// ParseIAPAudiences parses IAP_AUDIENCES.
func ParseIAPAudiences(v string) ([]string, error) { return nil, nil }

// NewIAPVerifier returns a verifier for cfg.
func NewIAPVerifier(cfg IAPConfig) (*IAPVerifier, error) { return &IAPVerifier{}, nil }

// Verify checks an assertion and returns the identity it proves.
func (v *IAPVerifier) Verify(ctx context.Context, assertion string) (IAPIdentity, error) {
	return IAPIdentity{}, errors.New("auth: iap verifier not implemented")
}
