package auth_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// serviceRoleToken signs what gotrueadmin.Client.do signs, with the key idp-up.sh wrote to IDP_ES256_KEYS_FILE.
// gotrueadmin has no generate_link method and the worker's admin calls stay a fixed set.
func serviceRoleToken(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(requireEnv(t, "IDP_ES256_KEYS_FILE"))
	if err != nil {
		t.Fatalf("read IDP_ES256_KEYS_FILE: %v", err)
	}
	var keys []struct {
		Kid    string   `json:"kid"`
		KeyOps []string `json:"key_ops"`
		D      string   `json:"d"`
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("IDP_ES256_KEYS_FILE is not a JWK array: %v", err)
	}
	var signing []int
	for i, k := range keys {
		if slices.Contains(k.KeyOps, "sign") {
			signing = append(signing, i)
		}
	}
	if len(signing) != 1 {
		t.Fatalf("IDP_ES256_KEYS_FILE holds %d signing keys, want 1", len(signing))
	}
	k := keys[signing[0]]
	d, err := base64.RawURLEncoding.DecodeString(k.D)
	if err != nil {
		t.Fatalf("decode the signing key: %v", err)
	}
	priv := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(d)}
	priv.Curve = elliptic.P256()
	priv.X, priv.Y = priv.Curve.ScalarBaseMult(d)

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"role": "service_role", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	tok.Header["kid"] = k.Kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign the service-role token: %v", err)
	}
	return signed
}

// generateRecoveryCode asks the admin API for a recovery link and returns its raw code and hashed token.
func generateRecoveryCode(t *testing.T, base, email string) (code, hashed string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"type": "recovery", "email": email})
	req, err := http.NewRequest(http.MethodPost, base+"/admin/generate_link", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+serviceRoleToken(t))
	req.Header.Set("Content-Type", "application/json")
	resp, err := idpHTTP.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/generate_link: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		EmailOTP    string `json:"email_otp"`
		HashedToken string `json:"hashed_token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.EmailOTP == "" || out.HashedToken == "" {
		t.Fatalf("POST /admin/generate_link: status %d, body %s; want 200 with email_otp and hashed_token", resp.StatusCode, b)
	}
	return out.EmailOTP, out.HashedToken
}

// verifyRecoveryCode posts a recovery code to /verify and returns the status and error_code.
func verifyRecoveryCode(t *testing.T, base, email, code string) (int, string) {
	t.Helper()
	status, body := postJSON(t, base+"/verify", map[string]string{"type": "recovery", "email": email, "token": code})
	errCode, _ := body["error_code"].(string)
	return status, errCode
}

func sha224Hex(s string) string {
	sum := sha256.Sum224([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestIdP_LinkCodeIsTenDigits(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := confirmedRegistrant(t, gw)

	code, hashed := generateRecoveryCode(t, base, u.email)
	if !regexp.MustCompile(`^[0-9]{10}$`).MatchString(code) {
		t.Errorf("email_otp = %q, want exactly 10 digits", code)
	}
	if want := sha224Hex(u.email + code); hashed != want {
		t.Errorf("hashed_token = %q, want sha224(email+email_otp) = %q", hashed, want)
	}
	if status, errCode := verifyRecoveryCode(t, base, u.email, code); status != http.StatusOK {
		t.Errorf("POST /verify with the full code: status %d %s, want 200", status, errCode)
	}

	t.Run("six_digit_prefix_is_refused", func(t *testing.T) {
		code, _ := generateRecoveryCode(t, base, u.email)
		if len(code) != 10 {
			t.Fatalf("control: email_otp = %q, want 10 digits", code)
		}
		if status, errCode := verifyRecoveryCode(t, base, u.email, code[:6]); status != http.StatusForbidden || errCode != "otp_expired" {
			t.Errorf("POST /verify with the first 6 digits: status %d %s, want 403 otp_expired", status, errCode)
		}
		if status, errCode := verifyRecoveryCode(t, base, u.email, code); status != http.StatusOK {
			t.Errorf("POST /verify with the full code after the refused prefix: status %d %s, want 200", status, errCode)
		}
	})
}

// The mail carries only sha224(email+code), so this is the one check on the mailed path ([code-length-evidence]).
func TestIdP_MailedResetLinkIsNotASixDigitCode(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := confirmedRegistrant(t, gw)
	requestResetAccepted(t, gw, u.email)

	link, err := url.Parse(recoveryLink(t, u.email, 2))
	if err != nil {
		t.Fatal(err)
	}
	token := link.Query().Get("token")
	if !regexp.MustCompile(`^[0-9a-f]{56}$`).MatchString(token) {
		t.Fatalf("mailed token = %q, want 56 lowercase hex characters", token)
	}
	want, err := hex.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c <= 999999; c++ {
		sum := sha256.Sum224(fmt.Appendf(nil, "%s%06d", u.email, c))
		if bytes.Equal(sum[:], want) {
			t.Fatalf("mailed token = sha224(email + %06d): the image issues 6-digit codes", c)
		}
	}
}

// The first 30 requests of a key pass the limiter; the same key then gets a 429. Other keys and a request with no header do not.
func TestIdP_VerifyLimitKeysOnTheAddressHeader(t *testing.T) {
	idpURL(t)
	base := strings.TrimRight(requireEnv(t, "IDP_HS256_URL"), "/")

	// A fresh 56-hex token_hash is unknown to GoTrue, so /verify answers 403 without side effects.
	verify := func(key string) int {
		raw := make([]byte, 28)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{"type": "signup", "token_hash": hex.EncodeToString(raw)})
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/verify", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set(platform.HeaderUserIP, key)
		}
		resp, err := idpHTTP.Do(req)
		if err != nil {
			t.Fatalf("POST /verify: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	n, err := rand.Int(rand.Reader, big.NewInt(250))
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("198.51.100.%d", n.Int64()+1)
	other := fmt.Sprintf("198.51.100.%d", n.Int64()+2)

	for i := 1; i <= 30; i++ {
		if got := verify(key); got != http.StatusForbidden {
			t.Fatalf("request %d of key %s: status %d, want 403 (inside the burst)", i, key, got)
		}
	}
	if a, b := verify(key), verify(key); a != http.StatusTooManyRequests && b != http.StatusTooManyRequests {
		t.Errorf("requests 31 and 32 of key %s: status %d and %d, want a 429 among them", key, a, b)
	}
	if got := verify(other); got != http.StatusForbidden {
		t.Errorf("first request of key %s: status %d, want 403; the limit must key on the %s header", other, got, platform.HeaderUserIP)
	}
	if got := verify(""); got != http.StatusForbidden {
		t.Errorf("request with no %s header: status %d, want 403; GoTrue limits nothing without a key", platform.HeaderUserIP, got)
	}
}
