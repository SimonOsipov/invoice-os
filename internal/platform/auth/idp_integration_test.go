package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden_token.json from the live idp-es256 token")

// idpURL returns the idp-es256 base URL. The CI idp job runs these through
// rls-test-gate.sh, so a skip there fails the build.
func idpURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("IDP_ES256_URL")
	if u == "" {
		t.Skip("IDP_ES256_URL unset; run `make test-idp` or the CI idp job")
	}
	return strings.TrimRight(u, "/")
}

// requireEnv fails rather than skips: once a container is declared, a half-set env is a harness defect.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("IDP_ES256_URL is set but %s is not", name)
	}
	return v
}

type idpHealth struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

func getHealth(t *testing.T, base string) idpHealth {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v (nothing answers at IDP_ES256_URL)", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/health: status %d, want 200", base, resp.StatusCode)
	}
	var h idpHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("GET %s/health: body is not JSON: %v", base, err)
	}
	return h
}

func TestIdP_HealthReportsPinnedTag(t *testing.T) {
	base := idpURL(t)
	want := requireEnv(t, "IDP_PINNED_TAG")

	h := getHealth(t, base)
	t.Logf("measured /health: version=%q name=%q", h.Version, h.Name)
	if h.Name != "GoTrue" {
		t.Errorf("/health name = %q, want \"GoTrue\"", h.Name)
	}
	if h.Version != want {
		t.Errorf("/health version = %q, want the pinned tag %q", h.Version, want)
	}
}

func TestIdP_ProviderConnectsAsAuthAdmin(t *testing.T) {
	base := idpURL(t)
	dsn := requireEnv(t, "DATABASE_SUPERUSER_URL")
	getHealth(t, base)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	defer conn.Close(ctx)

	var owner string
	err = conn.QueryRow(ctx,
		`SELECT tableowner FROM pg_tables WHERE schemaname = 'auth' AND tablename = 'users'`).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("auth.users does not exist; GoTrue never ran its migrations against this database")
	}
	if err != nil {
		t.Fatalf("read auth.users owner: %v", err)
	}
	if owner != "supabase_auth_admin" {
		t.Errorf("auth.users is owned by %q, want supabase_auth_admin (the provider connected as the wrong role)", owner)
	}

	// GoTrue's ledger is unqualified, so it lands wherever search_path points.
	var inAuth, inPublic bool
	err = conn.QueryRow(ctx,
		`SELECT to_regclass('auth.schema_migrations') IS NOT NULL, to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&inAuth, &inPublic)
	if err != nil {
		t.Fatalf("look up schema_migrations: %v", err)
	}
	if !inAuth {
		t.Error("auth.schema_migrations does not exist; GoTrue's migration ledger is not under search_path = auth")
	}
	if inPublic {
		t.Error("public.schema_migrations exists; GoTrue ran with a search_path that reaches public")
	}
}

const (
	rebuildHook = "public.test_rebuild_claims_hook"
	goldenPath  = "testdata/golden_token.json"
	jwksPath    = "/.well-known/jwks.json"
)

var idpHTTP = &http.Client{Timeout: 10 * time.Second}

func superConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, requireEnv(t, "DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := idpHTTP.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", url, err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("POST %s: status %d, body is not a JSON object: %s", url, resp.StatusCode, b)
	}
	return resp.StatusCode, out
}

type idpUser struct {
	id, email, password string
}

// signUp creates a user through the provider's own API; the superuser deletes it afterwards.
func signUp(t *testing.T, conn *pgx.Conn, base string) idpUser {
	t.Helper()
	u := idpUser{email: "idp-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email)
	})
	status, body := postJSON(t, base+"/signup", map[string]string{"email": u.email, "password": u.password})
	if status != http.StatusOK {
		t.Fatalf("POST %s/signup: status %d, body %v", base, status, body)
	}
	user, _ := body["user"].(map[string]any)
	u.id, _ = user["id"].(string)
	if u.id == "" {
		t.Fatalf("POST %s/signup: no user.id in %v", base, body)
	}
	return u
}

func signIn(t *testing.T, base string, u idpUser) (int, map[string]any) {
	t.Helper()
	return postJSON(t, base+"/token?grant_type=password", map[string]string{"email": u.email, "password": u.password})
}

func accessToken(t *testing.T, base string, u idpUser) string {
	t.Helper()
	status, body := signIn(t, base, u)
	tok, _ := body["access_token"].(string)
	if status != http.StatusOK || tok == "" {
		t.Fatalf("sign in on %s: status %d, body %v", base, status, body)
	}
	return tok
}

func seedTenant(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	id := uuid.NewString()
	exec(t, conn, `INSERT INTO tenants (id, name) VALUES ($1, 'idp integration throwaway tenant')`, id)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1`, id)
		_, _ = conn.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

func seedActiveMembership(t *testing.T, conn *pgx.Conn, tenantID, userID string) {
	t.Helper()
	exec(t, conn, `INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, tenantID, userID)
}

// setRebuildHook points idp-rebuild's hook at the claims expr builds. GoTrue calls it as
// supabase_auth_admin, so EXECUTE mirrors the committed hook's grant.
func setRebuildHook(t *testing.T, conn *pgx.Conn, expr string) {
	t.Helper()
	exec(t, conn, fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(event jsonb) RETURNS jsonb
		LANGUAGE sql AS $$ SELECT jsonb_build_object('claims', %s) $$`, rebuildHook, expr))
	exec(t, conn, fmt.Sprintf(`REVOKE EXECUTE ON FUNCTION %s(jsonb) FROM PUBLIC`, rebuildHook))
	exec(t, conn, fmt.Sprintf(`GRANT EXECUTE ON FUNCTION %s(jsonb) TO supabase_auth_admin`, rebuildHook))
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s(jsonb)`, rebuildHook))
	})
}

func idpVerifier(t *testing.T, base string) *auth.Verifier {
	t.Helper()
	v, err := auth.NewVerifier(auth.Config{
		Issuer:  requireEnv(t, "IDP_ISSUER"),
		JWKSURL: base + jwksPath,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func jwtPart(t *testing.T, tok string, i int) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		t.Fatalf("decode segment %d: %v", i, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal segment %d: %v", i, err)
	}
	return m
}

func getJWKS(t *testing.T, base string) map[string]any {
	t.Helper()
	resp, err := idpHTTP.Get(base + jwksPath)
	if err != nil {
		t.Fatalf("GET %s%s: %v", base, jwksPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s%s: status %d", base, jwksPath, resp.StatusCode)
	}
	var set map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("GET %s%s: %v", base, jwksPath, err)
	}
	return set
}

func jwksKeys(t *testing.T, set map[string]any) []map[string]any {
	t.Helper()
	raw, ok := set["keys"].([]any)
	if !ok {
		t.Fatalf("jwks has no keys array: %v", set)
	}
	keys := make([]map[string]any, 0, len(raw))
	for _, k := range raw {
		m, ok := k.(map[string]any)
		if !ok {
			t.Fatalf("jwks key is not an object: %v", k)
		}
		keys = append(keys, m)
	}
	return keys
}

// privateJWKMembers are the RFC 7518 private and symmetric key members.
var privateJWKMembers = []string{"d", "p", "q", "dp", "dq", "qi", "k"}

func assertPublicOnly(t *testing.T, label string, keys []map[string]any) {
	t.Helper()
	for i, k := range keys {
		for _, m := range privateJWKMembers {
			if _, ok := k[m]; ok {
				t.Errorf("%s key %d carries private member %q", label, i, m)
			}
		}
		if k["kty"] == "oct" {
			t.Errorf("%s key %d is a symmetric (oct) key", label, i)
		}
	}
}

func TestIdP_ES256TokenVerifies(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	tok := accessToken(t, base, u)

	h := jwtPart(t, tok, 0)
	if h["alg"] != "ES256" {
		t.Errorf("header alg = %v, want ES256", h["alg"])
	}
	kid, _ := h["kid"].(string)
	if served := jwksKeys(t, getJWKS(t, base)); kid == "" || len(served) != 1 || served[0]["kid"] != kid {
		t.Errorf("header kid = %q, want the served key's kid", kid)
	}

	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != u.id {
		t.Errorf("Subject = %q, want the GoTrue user id %q", id.Subject, u.id)
	}
	if id.Role != "authenticated" {
		t.Errorf("Role = %q, want authenticated", id.Role)
	}
}

func TestIdP_JWKSServesOnlyTheES256PublicKey(t *testing.T) {
	base := idpURL(t)
	keys := jwksKeys(t, getJWKS(t, base))
	if len(keys) != 1 {
		t.Fatalf("jwks serves %d keys, want 1", len(keys))
	}
	k := keys[0]
	if k["kty"] != "EC" || k["alg"] != "ES256" || k["crv"] != "P-256" {
		t.Errorf("served key kty/alg/crv = %v/%v/%v, want EC/ES256/P-256", k["kty"], k["alg"], k["crv"])
	}
	assertPublicOnly(t, "served", keys)
}

func TestIdP_DefaultHS256TokenRefused(t *testing.T) {
	es256 := idpURL(t)
	hs256 := strings.TrimRight(requireEnv(t, "IDP_HS256_URL"), "/")
	conn := superConn(t)
	u := signUp(t, conn, hs256)
	tok := accessToken(t, hs256, u)

	if alg := jwtPart(t, tok, 0)["alg"]; alg != "HS256" {
		t.Fatalf("idp-hs256 header alg = %v, want HS256 (the provider's default)", alg)
	}
	if keys := jwksKeys(t, getJWKS(t, hs256)); len(keys) != 0 {
		t.Errorf("idp-hs256 jwks serves %d keys, want none", len(keys))
	}

	for label, base := range map[string]string{"ES256 issuer jwks": es256, "idp-hs256's own jwks": hs256} {
		// A fresh verifier per JWKS: primary and additional issuers cannot share the one iss.
		if _, err := idpVerifier(t, base).Verify(context.Background(), tok); !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("Verify against %s: err = %v, want ErrUnauthorized", label, err)
		}
	}
}

func TestIdP_TenantProjectedFromActiveMembership(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	tenant := seedTenant(t, conn)
	seedActiveMembership(t, conn, tenant, u.id)
	tok := accessToken(t, base, u)

	am, _ := jwtPart(t, tok, 1)["app_metadata"].(map[string]any)
	if am["tenant_id"] != tenant {
		t.Errorf("claims app_metadata.tenant_id = %v, want %s", am["tenant_id"], tenant)
	}
	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.TenantID != tenant {
		t.Errorf("Identity.TenantID = %q, want %s", id.TenantID, tenant)
	}
}

func TestIdP_NoMembershipNoTenant(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	tok := accessToken(t, base, u)

	am, _ := jwtPart(t, tok, 1)["app_metadata"].(map[string]any)
	if v, ok := am["tenant_id"]; ok {
		t.Errorf("claims app_metadata.tenant_id = %v, want absent", v)
	}
	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.TenantID != "" {
		t.Errorf("Identity.TenantID = %q, want empty", id.TenantID)
	}
}

// Signup issues a token too, so each rebuild test signs up under a pass-through hook first.
func TestIdP_RebuiltHookDroppingRequiredClaimIssuesNoToken(t *testing.T) {
	idpURL(t)
	base := strings.TrimRight(requireEnv(t, "IDP_REBUILD_URL"), "/")
	conn := superConn(t)
	setRebuildHook(t, conn, `event->'claims'`)
	u := signUp(t, conn, base)

	setRebuildHook(t, conn, `(event->'claims') - 'role'`)
	status, body := signIn(t, base, u)
	if status != http.StatusInternalServerError {
		t.Errorf("sign in with role dropped: status %d, want 500 (body %v)", status, body)
	}
	if _, ok := body["access_token"]; ok {
		t.Error("sign in with role dropped returned an access_token")
	}
}

func TestIdP_RebuiltHookDroppingIssIsRefusedByVerifier(t *testing.T) {
	idpURL(t)
	base := strings.TrimRight(requireEnv(t, "IDP_REBUILD_URL"), "/")
	conn := superConn(t)
	// idp-rebuild's own JWKS: against another container's key the refusal would be the kid, not iss.
	v := idpVerifier(t, base)
	setRebuildHook(t, conn, `event->'claims'`)
	u := signUp(t, conn, base)
	if _, err := v.Verify(context.Background(), accessToken(t, base, u)); err != nil {
		t.Fatalf("control: a pass-through hook token must verify against idp-rebuild's jwks: %v", err)
	}

	setRebuildHook(t, conn, `(event->'claims') - 'iss'`)
	tok := accessToken(t, base, u)
	if iss, ok := jwtPart(t, tok, 1)["iss"]; ok {
		t.Fatalf("token still carries iss %v; the hook did not drop it", iss)
	}
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("Verify with iss dropped: err = %v, want ErrUnauthorized", err)
	}
}

type goldenFixture struct {
	Comment       string         `json:"_comment"`
	Source        goldenSource   `json:"source"`
	DecodedHeader map[string]any `json:"decoded_header"`
	DecodedClaims map[string]any `json:"decoded_claims"`
	JWKS          map[string]any `json:"jwks"`
}

type goldenSource struct {
	Image      string `json:"image"`
	Issuer     string `json:"issuer"`
	JWKSPath   string `json:"jwks_path"`
	SigningAlg string `json:"signing_alg"`
	CapturedAt string `json:"captured_at"`
}

const goldenComment = "Shape of a real access token from the self-hosted supabase/auth image in sidecar/auth/Dockerfile " +
	"(CI container idp-es256, second sign-in, one active membership). Tests compare keys and JSON kinds, never values. " +
	"The raw JWT is omitted (public repo); jwks holds only the served public key. " +
	"Refresh: go test -run TestIdP_TokenShapeMatchesGolden ./internal/platform/auth/ -update, with the env scripts/ci/idp-up.sh prints."

var (
	fromLine = regexp.MustCompile(`(?m)^FROM\s+(\S+)`)
	rawJWT   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ`)
)

func imageRef(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "sidecar", "auth", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sidecar/auth/Dockerfile: %v", err)
	}
	m := fromLine.FindSubmatch(b)
	if m == nil {
		t.Fatal("sidecar/auth/Dockerfile has no FROM line")
	}
	return string(m[1])
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// assertSameShape compares key sets and JSON kinds, recursing into objects.
func assertSameShape(t *testing.T, path string, live, golden map[string]any) {
	t.Helper()
	for k, gv := range golden {
		lv, ok := live[k]
		if !ok {
			t.Errorf("%s.%s: in the fixture, absent from the live token", path, k)
			continue
		}
		if kind(lv) != kind(gv) {
			t.Errorf("%s.%s: live %s, fixture %s", path, k, kind(lv), kind(gv))
			continue
		}
		if lm, ok := lv.(map[string]any); ok {
			assertSameShape(t, path+"."+k, lm, gv.(map[string]any))
		}
	}
	for k := range live {
		if _, ok := golden[k]; !ok {
			t.Errorf("%s.%s: in the live token, absent from the fixture", path, k)
		}
	}
}

func TestIdP_TokenShapeMatchesGolden(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	seedActiveMembership(t, conn, seedTenant(t, conn), u.id)
	// The first token after a GoTrue boot can carry aud as an array; the second never does.
	accessToken(t, base, u)
	tok := accessToken(t, base, u)
	header, claims := jwtPart(t, tok, 0), jwtPart(t, tok, 1)
	if _, ok := claims["aud"].(string); !ok {
		t.Fatalf("second sign-in aud is %s, want a JSON string", kind(claims["aud"]))
	}

	if *updateGolden {
		served := getJWKS(t, base)
		assertPublicOnly(t, "served", jwksKeys(t, served))
		iss, _ := claims["iss"].(string)
		fx := goldenFixture{
			Comment: goldenComment,
			Source: goldenSource{
				Image: imageRef(t), Issuer: iss, JWKSPath: jwksPath,
				SigningAlg: fmt.Sprint(header["alg"]), CapturedAt: time.Now().UTC().Format("2006-01-02"),
			},
			DecodedHeader: header, DecodedClaims: claims, JWKS: served,
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(fx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write %s: %v", goldenPath, err)
		}
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}
	for _, k := range []string{"raw", "jwt", "token", "access_token", "refresh_token"} {
		if _, ok := top[k]; ok {
			t.Errorf("fixture carries a %q member; the raw token must not be committed", k)
		}
	}
	// "eyJ" is base64url for `{"`, so a JWT starts with two such segments.
	if rawJWT.Match(raw) {
		t.Error("fixture contains a base64url JSON segment; a raw token was committed")
	}
	var fx goldenFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}
	assertPublicOnly(t, "fixture jwks", jwksKeys(t, fx.JWKS))
	if _, ok := fx.DecodedClaims["aud"].(string); !ok {
		t.Errorf("fixture aud is %s, want a JSON string", kind(fx.DecodedClaims["aud"]))
	}
	assertSameShape(t, "header", header, fx.DecodedHeader)
	assertSameShape(t, "claims", claims, fx.DecodedClaims)
}
