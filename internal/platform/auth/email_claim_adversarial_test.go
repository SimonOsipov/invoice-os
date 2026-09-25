package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// Signs a raw map, not gotrueClaims, so a renamed struct tag cannot agree with itself.
func TestVerify_GoTrueEmailKeyReachesIdentity(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":          iss.issuer,
		"sub":          testSubject,
		"aud":          "authenticated",
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
		"role":         "authenticated",
		"app_metadata": map[string]any{},
		"email":        "ada@example.test",
	})
	tok.Header["kid"] = iss.kid
	s, err := tok.SignedString(iss.key)
	if err != nil {
		t.Fatal(err)
	}

	id, err := v.Verify(t.Context(), s)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Email != "ada@example.test" || id.Subject != testSubject || id.TenantID != "" {
		t.Fatalf("identity = %+v, want tenant-less with email ada@example.test", id)
	}
}
