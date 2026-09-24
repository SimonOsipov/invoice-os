package auth

// TrustedIssuer is one issuer the verifier accepts, with the JWKS that signs its tokens.
type TrustedIssuer struct {
	Issuer  string
	JWKSURL string
}

// ParseTrustedIssuers parses AUTH_ADDITIONAL_ISSUERS.
func ParseTrustedIssuers(raw string) ([]TrustedIssuer, error) {
	return nil, nil
}
