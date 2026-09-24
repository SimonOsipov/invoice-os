package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// A hook that errors or lacks EXECUTE also answers 500; only the message names GoTrue's schema check.
func TestIdP_RoleDropRefusalIsGoTruesSchemaCheck(t *testing.T) {
	idpURL(t)
	base := strings.TrimRight(requireEnv(t, "IDP_REBUILD_URL"), "/")
	conn := superConn(t)
	setRebuildHook(t, conn, `event->'claims'`)
	u := signUp(t, conn, base)
	accessToken(t, base, u)

	setRebuildHook(t, conn, `(event->'claims') - 'role'`)
	status, body := signIn(t, base, u)
	msg, _ := body["msg"].(string)
	if status != http.StatusInternalServerError || !strings.Contains(msg, "expected schema") || !strings.Contains(msg, "role is required") {
		t.Errorf("sign in with role dropped: status %d msg %q, want 500 naming the schema check and role", status, msg)
	}
}

// The ES256 leg must refuse for the alg, not because the JWKS was unreachable.
func TestIdP_HS256RefusedByAVerifierThatAcceptsES256(t *testing.T) {
	es256 := idpURL(t)
	hs256 := strings.TrimRight(requireEnv(t, "IDP_HS256_URL"), "/")
	conn := superConn(t)
	v := idpVerifier(t, es256)

	good := signUp(t, conn, es256)
	if _, err := v.Verify(context.Background(), accessToken(t, es256, good)); err != nil {
		t.Fatalf("control: an idp-es256 token must verify on this verifier: %v", err)
	}
	bad := signUp(t, conn, hs256)
	tok := accessToken(t, hs256, bad)
	if alg := jwtPart(t, tok, 0)["alg"]; alg != "HS256" {
		t.Fatalf("idp-hs256 header alg = %v, want HS256", alg)
	}
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("Verify HS256 token: err = %v, want ErrUnauthorized", err)
	}
}

func readGolden(t *testing.T) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	var fx goldenFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}
	return fx
}

// An empty keys array would pass the private-member scan vacuously.
func TestGoldenFixtureJWKSIsTheOneServedPublicKey(t *testing.T) {
	fx := readGolden(t)
	keys := jwksKeys(t, fx.JWKS)
	if len(keys) != 1 {
		t.Fatalf("fixture jwks holds %d keys, want the one served key", len(keys))
	}
	k := keys[0]
	if k["kty"] != "EC" || k["alg"] != "ES256" || k["crv"] != "P-256" {
		t.Errorf("fixture key kty/alg/crv = %v/%v/%v, want EC/ES256/P-256", k["kty"], k["alg"], k["crv"])
	}
	for _, m := range []string{"x", "y"} {
		if s, _ := k[m].(string); s == "" {
			t.Errorf("fixture key has no public coordinate %q", m)
		}
	}
	if kid, _ := fx.DecodedHeader["kid"].(string); kid == "" || k["kid"] != kid {
		t.Errorf("fixture key kid = %v, want the decoded header kid %v", k["kid"], fx.DecodedHeader["kid"])
	}
	if fx.Source.SigningAlg != "ES256" || fx.DecodedHeader["alg"] != "ES256" {
		t.Errorf("fixture signing_alg/header alg = %q/%v, want ES256", fx.Source.SigningAlg, fx.DecodedHeader["alg"])
	}
	if fx.Source.Image == "" || fx.Source.Issuer == "" || fx.Source.CapturedAt == "" || fx.Source.JWKSPath != jwksPath {
		t.Errorf("fixture source incomplete: %+v", fx.Source)
	}
}

// shape maps a JSON value to its kind, recursing into objects and every array element.
func shape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = shape(e)
		}
		return out
	case []any:
		seen := map[string]any{}
		for _, e := range x {
			s := shape(e)
			b, _ := json.Marshal(s)
			seen[string(b)] = s
		}
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []any{"array"}
		for _, k := range keys {
			out = append(out, seen[k])
		}
		return out
	default:
		return kind(v)
	}
}

// assertSameShape stops at arrays, so an amr element's keys go unchecked there.
func TestIdP_TokenArrayElementsMatchGolden(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	seedActiveMembership(t, conn, seedTenant(t, conn), u.id)
	accessToken(t, base, u)
	tok := accessToken(t, base, u)
	fx := readGolden(t)

	claims := jwtPart(t, tok, 1)
	arrays := 0
	for k, gv := range fx.DecodedClaims {
		if _, ok := gv.([]any); !ok {
			continue
		}
		arrays++
		if got, want := shape(claims[k]), shape(gv); !reflect.DeepEqual(got, want) {
			t.Errorf("claims.%s: live shape %v, fixture shape %v", k, got, want)
		}
	}
	am, _ := fx.DecodedClaims["app_metadata"].(map[string]any)
	if arrays == 0 || am == nil {
		t.Fatalf("fixture has %d array claims and app_metadata %v; nothing to compare", arrays, am)
	}
	if got, want := shape(claims["app_metadata"]), shape(am); !reflect.DeepEqual(got, want) {
		t.Errorf("claims.app_metadata: live shape %v, fixture shape %v", got, want)
	}
}
