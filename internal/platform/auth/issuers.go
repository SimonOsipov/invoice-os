package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// TrustedIssuer is one issuer the verifier accepts, with the JWKS that signs its tokens.
type TrustedIssuer struct {
	Issuer  string `json:"issuer"`
	JWKSURL string `json:"jwks_url"`
}

// ParseTrustedIssuers parses AUTH_ADDITIONAL_ISSUERS, a JSON array of
// {"issuer","jwks_url"}. Empty or whitespace input means no additional issuers.
func ParseTrustedIssuers(raw string) ([]TrustedIssuer, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var set []TrustedIssuer
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("auth: trusted issuers: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("auth: trusted issuers: trailing data after the array")
	}
	for i, ti := range set {
		if ti.Issuer == "" || ti.JWKSURL == "" {
			return nil, fmt.Errorf("auth: trusted issuers: entry %d needs issuer and jwks_url", i)
		}
		u, err := url.Parse(ti.JWKSURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("auth: trusted issuers: entry %d jwks_url must be http(s)", i)
		}
	}
	return set, nil
}
