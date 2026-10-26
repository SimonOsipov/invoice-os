package gotrueadmin_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

type jwk struct {
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv"`
	Alg    string   `json:"alg"`
	Kid    string   `json:"kid"`
	KeyOps []string `json:"key_ops"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
	D      string   `json:"d,omitempty"`
}

var b64 = base64.RawURLEncoding.EncodeToString

// signKey is a fresh P-256 key as GoTrue's GOTRUE_JWT_KEYS element.
type signKey struct {
	priv *ecdsa.PrivateKey
	jwk  jwk
}

func newKey(t *testing.T, curve elliptic.Curve, alg string, ops ...string) signKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	size := (curve.Params().BitSize + 7) / 8
	return signKey{priv: k, jwk: jwk{
		Kty: "EC", Crv: curve.Params().Name, Alg: alg, Kid: uuid.NewString(), KeyOps: ops,
		X: b64(k.X.FillBytes(make([]byte, size))), Y: b64(k.Y.FillBytes(make([]byte, size))),
		D: b64(k.D.FillBytes(make([]byte, size))),
	}}
}

func keysJSON(t *testing.T, keys ...jwk) string {
	t.Helper()
	raw, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func signing(t *testing.T) signKey {
	t.Helper()
	return newKey(t, elliptic.P256(), "ES256", "sign", "verify")
}

// verifyOnly drops the private part, as a published key would.
func (k signKey) verifyOnly() jwk {
	v := k.jwk
	v.D, v.KeyOps = "", []string{"verify"}
	return v
}

func newClient(t *testing.T, base string, k signKey) *gotrueadmin.Client {
	t.Helper()
	c, err := gotrueadmin.New(base, keysJSON(t, k.jwk), &http.Client{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// parseAdminToken verifies the bearer with k's public key and returns its header and claims.
func parseAdminToken(t *testing.T, auth string, k signKey) (map[string]any, jwt.MapClaims) {
	t.Helper()
	raw, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || raw == "" {
		t.Fatalf("Authorization %q is not a bearer token", auth)
	}
	var header map[string]any
	claims := jwt.MapClaims{}
	_, err := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"})).ParseWithClaims(raw, claims, func(tok *jwt.Token) (any, error) {
		header = tok.Header
		return &k.priv.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("bearer does not verify as ES256 with the key's public half: %v", err)
	}
	return header, claims
}

type recorded struct {
	method, path, auth string
	body               string
}

func recordingServer(t *testing.T, status int, reply string) (*httptest.Server, *[]recorded) {
	t.Helper()
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestNew_TakesTheOneSigningKey(t *testing.T) {
	sign := signing(t)
	other := signing(t) // only its public half is listed
	srv, got := recordingServer(t, http.StatusOK, "{}")
	c, err := gotrueadmin.New(srv.URL, keysJSON(t, other.verifyOnly(), sign.jwk), &http.Client{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := uuid.NewString()
	if err := c.DeleteUser(context.Background(), id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(*got))
	}
	header, _ := parseAdminToken(t, (*got)[0].auth, sign)
	if header["kid"] != sign.jwk.Kid {
		t.Errorf("header kid = %v, want the signing key's %q (not the verify-only %q)", header["kid"], sign.jwk.Kid, other.jwk.Kid)
	}
}

func TestNew_RefusesUnusableKeys(t *testing.T) {
	good := signing(t)
	second := signing(t)
	p384 := newKey(t, elliptic.P384(), "ES256", "sign", "verify")
	p384alg := newKey(t, elliptic.P384(), "ES384", "sign", "verify")
	oct := `[{"kty":"oct","alg":"HS256","kid":"k1","key_ops":["sign","verify"],"k":"SECRETOCTKEYBYTES"}]`
	noSign := good.verifyOnly()
	// edit returns good's key with one field changed, so each case breaks exactly one rule.
	edit := func(f func(*jwk)) string { k := good.jwk; f(&k); return keysJSON(t, k) }
	verifyOnlyWithD := edit(func(k *jwk) { k.KeyOps = []string{"verify"} })

	cases := []struct{ name, in string }{
		{"object, not an array", `{"kty":"EC"}`},
		{"null", "null"},
		{"P-256 material labelled ES384", edit(func(k *jwk) { k.Alg = "ES384" })},
		{"P-256 material with no alg", edit(func(k *jwk) { k.Alg = "" })},
		{"P-256 material labelled P-384", edit(func(k *jwk) { k.Crv = "P-384" })},
		{"P-256 material labelled OKP", edit(func(k *jwk) { k.Kty = "OKP" })},
		{"no kid", edit(func(k *jwk) { k.Kid = "" })},
		{"public half of another key", edit(func(k *jwk) { k.X, k.Y = second.jwk.X, second.jwk.Y })},
		{"d is not base64url", edit(func(k *jwk) { k.D = "!!!not base64!!!" })},
		{"d is 31 bytes", edit(func(k *jwk) { k.D = k.D[:len(k.D)-2] })},
		{"x is empty", edit(func(k *jwk) { k.X = "" })},
		{"private key present, sign not listed", verifyOnlyWithD},
		{"not json", "not json SECRETMARKER"},
		{"empty array", "[]"},
		{"empty string", ""},
		{"two signing keys", keysJSON(t, good.jwk, second.jwk)},
		{"no signing key", keysJSON(t, noSign)},
		{"oct HS256", oct},
		{"P-384 labelled ES256", keysJSON(t, p384.jwk)},
		{"P-384 ES384", keysJSON(t, p384alg.jwk)},
	}
	secrets := []string{good.jwk.D, good.jwk.X, second.jwk.D, p384.jwk.D, p384alg.jwk.D, "SECRETOCTKEYBYTES", "SECRETMARKER", "not base64"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := gotrueadmin.New("http://auth.invalid", tc.in, &http.Client{})
			if err == nil {
				t.Fatalf("New accepted %q", tc.name)
			}
			if c != nil {
				t.Error("New returned a client with its error")
			}
			if !strings.Contains(err.Error(), "GOTRUE_JWT_KEYS") {
				t.Errorf("error %q does not name GOTRUE_JWT_KEYS", err)
			}
			for _, s := range secrets {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error text carries key material %q", s)
				}
			}
		})
	}
}

func TestDeleteUser_SendsAServiceRoleTokenSignedWithTheKid(t *testing.T) {
	k := signing(t)
	srv, got := recordingServer(t, http.StatusOK, "{}")
	c := newClient(t, srv.URL, k)
	id := uuid.NewString()

	before := time.Now()
	if err := c.DeleteUser(context.Background(), id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	after := time.Now()
	if len(*got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(*got))
	}
	r := (*got)[0]
	if r.method != http.MethodDelete || r.path != "/admin/users/"+id {
		t.Errorf("request = %s %s, want DELETE /admin/users/%s", r.method, r.path, id)
	}
	if r.body != "" {
		t.Errorf("request body = %q, want none", r.body)
	}
	header, claims := parseAdminToken(t, r.auth, k)
	if header["kid"] != k.jwk.Kid || header["alg"] != "ES256" {
		t.Errorf("header = %v, want alg ES256 and kid %q", header, k.jwk.Kid)
	}
	names := make([]string, 0, len(claims))
	for n := range claims {
		names = append(names, n)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"exp", "iat", "role"}) {
		t.Errorf("claims = %v, want exactly exp, iat, role (no sub, no session_id)", names)
	}
	if claims["role"] != "service_role" {
		t.Errorf("role = %v, want service_role", claims["role"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat != 60 {
		t.Errorf("exp - iat = %v s, want 60", exp-iat)
	}
	if int64(iat) < before.Add(-2*time.Second).Unix() || int64(iat) > after.Add(2*time.Second).Unix() {
		t.Errorf("iat %v is not the call time %v", int64(iat), before.Unix())
	}
}

func TestDeleteUser_NotFoundIsSuccess(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, "{}"},
		{http.StatusNotFound, `{"error_code":"user_not_found"}`},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv, got := recordingServer(t, tc.status, tc.body)
			c := newClient(t, srv.URL, signing(t))
			if err := c.DeleteUser(context.Background(), uuid.NewString()); err != nil {
				t.Errorf("DeleteUser on %d = %v, want nil", tc.status, err)
			}
			if len(*got) != 1 {
				t.Errorf("server got %d requests, want 1", len(*got))
			}
		})
	}
}

func TestDeleteUser_OtherStatusIsAStatusError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusNoContent} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv, _ := recordingServer(t, status, `{"msg":"SECRETBODYTEXT"}`)
			c := newClient(t, srv.URL, signing(t))
			id := uuid.NewString()
			err := c.DeleteUser(context.Background(), id)
			var se *gotrueadmin.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("DeleteUser on %d = %v, want a *StatusError", status, err)
			}
			if se.Status != status {
				t.Errorf("StatusError.Status = %d, want %d", se.Status, status)
			}
			text := err.Error()
			if !strings.Contains(text, fmt.Sprint(status)) {
				t.Errorf("error %q does not hold the status", text)
			}
			for _, banned := range []string{id, "SECRETBODYTEXT", srv.URL, strings.TrimPrefix(srv.URL, "http://")} {
				if strings.Contains(text, banned) {
					t.Errorf("error %q carries %q", text, banned)
				}
			}
		})
	}
}

func TestDeleteUser_TransportFailureIsStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := newClient(t, url, signing(t))
	id := uuid.NewString()
	err := c.DeleteUser(context.Background(), id)
	var se *gotrueadmin.StatusError
	if !errors.As(err, &se) || se.Status != 0 {
		t.Fatalf("DeleteUser against a closed server = %#v, want *StatusError{0}", err)
	}
	for _, banned := range []string{strings.TrimPrefix(url, "http://"), id, "connect", "refused"} {
		if strings.Contains(err.Error(), banned) {
			t.Errorf("error %q carries %q", err, banned)
		}
	}
}

func TestDeleteUser_DoesNotFollowARedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var second atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				second.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(target.Close)
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+r.URL.Path, status)
			}))
			t.Cleanup(first.Close)
			c := newClient(t, first.URL, signing(t))

			err := c.DeleteUser(context.Background(), uuid.NewString())
			var se *gotrueadmin.StatusError
			if !errors.As(err, &se) || se.Status != status {
				t.Fatalf("DeleteUser on a %d = %v, want *StatusError{%d}", status, err, status)
			}
			if n := second.Load(); n != 0 {
				t.Errorf("the redirect target got %d requests, want 0", n)
			}
		})
	}
}

func TestDeleteUser_KeepsTheCallersClientAndMintsAFreshTokenPerCall(t *testing.T) {
	k := signing(t)
	srv, got := recordingServer(t, http.StatusOK, "{}")
	caller := &http.Client{}
	c, err := gotrueadmin.New(srv.URL+"/", keysJSON(t, k.jwk), caller)
	if err != nil {
		t.Fatal(err)
	}
	if caller.CheckRedirect != nil {
		t.Error("New changed the caller's http.Client")
	}
	id := uuid.NewString()
	for i := range 2 {
		if i == 1 {
			time.Sleep(1100 * time.Millisecond) // iat has second resolution
		}
		if err := c.DeleteUser(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if len(*got) != 2 {
		t.Fatalf("server got %d requests, want 2", len(*got))
	}
	if (*got)[0].path != "/admin/users/"+id {
		t.Errorf("path = %q, want a single slash after the trailing-slash base", (*got)[0].path)
	}
	_, c0 := parseAdminToken(t, (*got)[0].auth, k)
	_, c1 := parseAdminToken(t, (*got)[1].auth, k)
	if c0["iat"] == c1["iat"] {
		t.Errorf("both calls carry iat %v; each call must mint its own token", c0["iat"])
	}
}

func TestDeleteUser_ACancelledContextIsStatusZero(t *testing.T) {
	srv, got := recordingServer(t, http.StatusOK, "{}")
	c := newClient(t, srv.URL, signing(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := uuid.NewString()
	err := c.DeleteUser(ctx, id)
	var se *gotrueadmin.StatusError
	if !errors.As(err, &se) || se.Status != 0 {
		t.Fatalf("DeleteUser with a cancelled context = %#v, want *StatusError{0}", err)
	}
	if strings.Contains(err.Error(), id) || strings.Contains(err.Error(), "context") {
		t.Errorf("error %q carries the id or the cause", err)
	}
	if len(*got) != 0 {
		t.Errorf("server got %d requests, want 0", len(*got))
	}
}

func TestDeleteUser_RefusesANonUUID(t *testing.T) {
	srv, got := recordingServer(t, http.StatusOK, "{}")
	c := newClient(t, srv.URL, signing(t))
	// uuid.Parse also takes the urn and brace forms; only the canonical form may reach the path.
	for _, id := range []string{"../users", "", " ", "not-a-uuid", uuid.NewString() + "/../x", uuid.NewString() + "?x=1", "urn:uuid:" + uuid.NewString(), "{" + uuid.NewString() + "}"} {
		if err := c.DeleteUser(context.Background(), id); err == nil {
			t.Errorf("DeleteUser(%q) = nil, want an error", id)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("server got %d requests for refused ids, want 0: %v", len(*got), *got)
	}
	// Control: a valid id reaches the server.
	if err := c.DeleteUser(context.Background(), uuid.NewString()); err != nil || len(*got) != 1 {
		t.Errorf("control: valid id: err = %v, requests = %d, want nil and 1", err, len(*got))
	}
}

// infiniteBody counts how much of an answer the client reads.
type infiniteBody struct{ read *int }

func (b infiniteBody) Read(p []byte) (int, error) {
	if *b.read >= 8<<20 {
		return 0, io.EOF
	}
	*b.read += len(p)
	return len(p), nil
}
func (infiniteBody) Close() error { return nil }

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeleteUser_ReadsAtMost16KiBOfTheAnswer(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var read int
			hc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: infiniteBody{&read}, Header: http.Header{}, Request: r}, nil
			})}
			c, err := gotrueadmin.New("http://auth.invalid", keysJSON(t, signing(t).jwk), hc)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_ = c.DeleteUser(context.Background(), uuid.NewString())
			if read == 0 {
				t.Fatal("control: the answer was never reached")
			}
			if read > 16<<10 {
				t.Errorf("read %d bytes of the answer, want at most %d", read, 16<<10)
			}
		})
	}
}

func TestFromEnv_FollowsTheRules(t *testing.T) {
	k := signing(t)
	keys := keysJSON(t, k.jwk)
	const authURL = "http://auth.internal.example:4321"
	badKeys := keysJSON(t, k.jwk, signing(t).jwk)

	cases := []struct {
		name       string
		cloud      bool
		url, keys  string
		wantClient bool
		wantErr    []string // variable names the error must hold; nil with wantClient false means (nil, nil)
	}{
		{"cloud, none", true, "", "", false, []string{"AUTH_URL", "GOTRUE_JWT_KEYS"}},
		{"cloud, no AUTH_URL", true, "", keys, false, []string{"AUTH_URL"}},
		{"cloud, no keys", true, authURL, "", false, []string{"GOTRUE_JWT_KEYS"}},
		{"cloud, both", true, authURL, keys, true, nil},
		{"off, none", false, "", "", false, nil},
		{"off, AUTH_URL alone", false, authURL, "", false, []string{"GOTRUE_JWT_KEYS"}},
		{"off, keys alone", false, "", keys, false, []string{"AUTH_URL"}},
		{"off, both", false, authURL, keys, true, nil},
		{"both, two signing keys", false, authURL, badKeys, false, []string{"GOTRUE_JWT_KEYS"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"AUTH_URL": tc.url, "GOTRUE_JWT_KEYS": tc.keys}
			c, err := gotrueadmin.FromEnv(func(n string) string { return env[n] }, tc.cloud, http.DefaultTransport)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("FromEnv = %v, want no error", err)
				}
				if (c != nil) != tc.wantClient {
					t.Fatalf("client non-nil = %v, want %v", c != nil, tc.wantClient)
				}
				return
			}
			if err == nil || c != nil {
				t.Fatalf("FromEnv = (%v, %v), want (nil, error)", c, err)
			}
			for _, name := range tc.wantErr {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name %s", err, name)
				}
			}
			for _, banned := range []string{authURL, "auth.internal.example", k.jwk.D, k.jwk.X, k.jwk.Kid} {
				if strings.Contains(err.Error(), banned) {
					t.Errorf("error %q carries a value %q", err, banned)
				}
			}
		})
	}
}

func TestFromEnv_SendsThroughTheGivenTransport(t *testing.T) {
	k := signing(t)
	var seen *http.Request
	var deadline time.Time
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		deadline, _ = r.Context().Deadline()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: r}, nil
	})
	env := map[string]string{"AUTH_URL": "http://auth.internal.example:4321", "GOTRUE_JWT_KEYS": keysJSON(t, k.jwk)}
	c, err := gotrueadmin.FromEnv(func(n string) string { return env[n] }, false, rt)
	if err != nil || c == nil {
		t.Fatalf("FromEnv = (%v, %v), want a client", c, err)
	}
	id := uuid.NewString()
	start := time.Now()
	if err := c.DeleteUser(context.Background(), id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if seen == nil {
		t.Fatal("the request did not go through the given transport")
	}
	if seen.Method != http.MethodDelete || seen.URL.Host != "auth.internal.example:4321" || seen.URL.Path != "/admin/users/"+id {
		t.Errorf("request = %s %s%s", seen.Method, seen.URL.Host, seen.URL.Path)
	}
	parseAdminToken(t, seen.Header.Get("Authorization"), k)
	if d := deadline.Sub(start); deadline.IsZero() || d < 9*time.Second || d > 10*time.Second+time.Second {
		t.Errorf("request deadline is %v after the call, want the client's 10 s timeout", d)
	}
}

func TestUserEmail_SendsAServiceRoleTokenAndReadsTheEmail(t *testing.T) {
	k := signing(t)
	id := uuid.NewString()
	srv, got := recordingServer(t, http.StatusOK, `{"id":"`+id+`","aud":"authenticated","email":"ada@example.invalid","role":"authenticated"}`)
	c := newClient(t, srv.URL, k)

	email, err := c.UserEmail(context.Background(), id)
	if err != nil {
		t.Fatalf("UserEmail: %v", err)
	}
	if email != "ada@example.invalid" {
		t.Errorf("email = %q, want ada@example.invalid", email)
	}
	if len(*got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(*got))
	}
	r := (*got)[0]
	if r.method != http.MethodGet || r.path != "/admin/users/"+id {
		t.Errorf("request = %s %s, want GET /admin/users/%s", r.method, r.path, id)
	}
	header, claims := parseAdminToken(t, r.auth, k)
	if header["kid"] != k.jwk.Kid || header["alg"] != "ES256" {
		t.Errorf("header = %v, want alg ES256 and kid %q", header, k.jwk.Kid)
	}
	if claims["role"] != "service_role" {
		t.Errorf("role = %v, want service_role", claims["role"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat != 60 {
		t.Errorf("exp - iat = %v s, want 60", exp-iat)
	}
}

func TestUserEmail_NotFoundIsEmpty(t *testing.T) {
	srv, got := recordingServer(t, http.StatusNotFound, `{"error_code":"user_not_found"}`)
	c := newClient(t, srv.URL, signing(t))
	email, err := c.UserEmail(context.Background(), uuid.NewString())
	if err != nil || email != "" {
		t.Errorf("UserEmail on 404 = %q, %v; want \"\", nil", email, err)
	}
	if len(*got) != 1 {
		t.Errorf("server got %d requests, want 1", len(*got))
	}
}

func TestUserEmail_ARefusalIsAStatusError(t *testing.T) {
	// The body holds an email so a leaking error or a returned value shows.
	const body = `{"msg":"SECRETBODYTEXT","email":"leak@example.invalid"}`
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv, _ := recordingServer(t, status, body)
			c := newClient(t, srv.URL, signing(t))
			id := uuid.NewString()
			email, err := c.UserEmail(context.Background(), id)
			var se *gotrueadmin.StatusError
			if !errors.As(err, &se) || se.Status != status {
				t.Fatalf("UserEmail on %d = %v, want *StatusError{%d}", status, err, status)
			}
			if email != "" {
				t.Errorf("email = %q beside an error, want \"\"", email)
			}
			for _, banned := range []string{id, "SECRETBODYTEXT", "leak@example.invalid", srv.URL} {
				if strings.Contains(err.Error(), banned) {
					t.Errorf("error %q carries %q", err, banned)
				}
			}
		})
	}
	t.Run("closed server", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		c := newClient(t, url, signing(t))
		id := uuid.NewString()
		_, err := c.UserEmail(context.Background(), id)
		var se *gotrueadmin.StatusError
		if !errors.As(err, &se) || se.Status != 0 {
			t.Fatalf("UserEmail against a closed server = %#v, want *StatusError{0}", err)
		}
		if strings.Contains(err.Error(), id) || strings.Contains(err.Error(), strings.TrimPrefix(url, "http://")) {
			t.Errorf("error %q carries the id or the address", err)
		}
	})
}

func TestUserEmail_RefusesANonCanonicalID(t *testing.T) {
	srv, got := recordingServer(t, http.StatusOK, `{"email":"ada@example.invalid"}`)
	c := newClient(t, srv.URL, signing(t))
	id := uuid.NewString()
	for _, bad := range []string{"ABC", strings.ToUpper(id), "", "../users", "urn:uuid:" + id} {
		if email, err := c.UserEmail(context.Background(), bad); err == nil || email != "" {
			t.Errorf("UserEmail(%q) = %q, %v; want an error", bad, email, err)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("server got %d requests for refused ids, want 0", len(*got))
	}
	// Control: the canonical id reaches the same server.
	if email, err := c.UserEmail(context.Background(), id); err != nil || email != "ada@example.invalid" {
		t.Errorf("control: UserEmail(canonical) = %q, %v", email, err)
	}
	if len(*got) != 1 {
		t.Errorf("control: server got %d requests, want 1", len(*got))
	}
}
