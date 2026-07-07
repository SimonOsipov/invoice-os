package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenTokenContract pins the mock issuer to the real GoTrue claim shape
// captured in testdata/golden_token.json. It compares SHAPE (claim keys + JSON
// types), never values — iss/kid/exp/sub legitimately differ. If a Supabase
// upgrade changes the contract, refresh the fixture (M8-05) and this test tells
// us whether the mock issuer still matches.
func TestGoldenTokenContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_token.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var golden struct {
		DecodedHeader map[string]any `json:"decoded_header"`
		DecodedClaims map[string]any `json:"decoded_claims"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}

	iss := mustIssuer(t)
	tok := mustMint(t, iss, MintOptions{Subject: testSubject, TenantID: "tenant-x"})
	mockHeader := decodeSegment(t, tok, 0)
	mockClaims := decodeSegment(t, tok, 1)

	// Every key the mock emits must exist in the golden token with the same JSON
	// type. The mock may be a subset of GoTrue's claims, never a different shape.
	assertShapeSubset(t, "header", mockHeader, golden.DecodedHeader)
	assertShapeSubset(t, "claims", mockClaims, golden.DecodedClaims)

	// Required header fields.
	for _, k := range []string{"alg", "kid", "typ"} {
		if _, ok := mockHeader[k].(string); !ok {
			t.Errorf("header missing string %q", k)
		}
	}
	if mockHeader["alg"] != "ES256" || mockHeader["typ"] != "JWT" {
		t.Errorf("header alg/typ = %v/%v", mockHeader["alg"], mockHeader["typ"])
	}

	// GoTrue emits aud as a single string; drifting to an array would break the
	// downstream role/tenant mapping, so pin it explicitly on both sides.
	if _, ok := golden.DecodedClaims["aud"].(string); !ok {
		t.Fatal("golden aud is not a string — fixture unexpected")
	}
	if _, ok := mockClaims["aud"].(string); !ok {
		t.Errorf("mock aud drifted from GoTrue's string contract: %T", mockClaims["aud"])
	}

	// Required claims present with the right type.
	for _, k := range []string{"iss", "sub", "role"} {
		if _, ok := mockClaims[k].(string); !ok {
			t.Errorf("claim %q missing or not a string", k)
		}
	}
	if _, ok := mockClaims["exp"].(float64); !ok {
		t.Errorf("claim exp missing or not a number")
	}

	// tenant_id rides inside app_metadata — our custom claim, contract-compatible
	// because app_metadata is an object in real GoTrue too.
	if _, ok := golden.DecodedClaims["app_metadata"].(map[string]any); !ok {
		t.Fatal("golden app_metadata is not an object — fixture unexpected")
	}
	am, ok := mockClaims["app_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("mock app_metadata is not an object: %T", mockClaims["app_metadata"])
	}
	if _, ok := am["tenant_id"].(string); !ok {
		t.Errorf("mock app_metadata.tenant_id missing or not a string")
	}
}

func decodeSegment(t *testing.T, token string, i int) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
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

// assertShapeSubset checks every key in got exists in golden with the same JSON
// kind — i.e. got is a shape-compatible subset of golden.
func assertShapeSubset(t *testing.T, label string, got, golden map[string]any) {
	t.Helper()
	for k, v := range got {
		gv, ok := golden[k]
		if !ok {
			t.Errorf("%s: mock emits %q which real GoTrue does not — shape drift", label, k)
			continue
		}
		if want, have := jsonKind(gv), jsonKind(v); want != have {
			t.Errorf("%s: claim %q is %s, golden has %s", label, k, have, want)
		}
	}
}

func jsonKind(v any) string {
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
