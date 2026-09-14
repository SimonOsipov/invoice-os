// Pure columnSignature specs. No DB, no HTTP.
package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"testing"
)

var hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// assertHex64 pins the key's shape: two equal non-hex strings would satisfy an
// inequality check alone.
func assertHex64(t *testing.T, s string) {
	t.Helper()
	if !hex64RE.MatchString(s) {
		t.Fatalf("signature %q is not 64 lowercase hex characters", s)
	}
}

func TestColumnSignature_EqualHeadersSignEqualAs64LowerHex(t *testing.T) {
	a := columnSignature([]string{"Invoice No", "Total"})
	b := columnSignature([]string{"Invoice No", "Total"})
	if a != b {
		t.Errorf("signatures differ for equal headers: %q vs %q", a, b)
	}
	assertHex64(t, a)
	assertHex64(t, b)
}

func TestColumnSignature_OrderChangesTheSignature(t *testing.T) {
	a := columnSignature([]string{"Invoice No", "Total"})
	b := columnSignature([]string{"Total", "Invoice No"})
	if a == b {
		t.Errorf("signature unchanged when column order changes: %q", a)
	}
	assertHex64(t, a)
	assertHex64(t, b)
}

func TestColumnSignature_CaseChangesTheSignature(t *testing.T) {
	a := columnSignature([]string{"Total"})
	b := columnSignature([]string{"total"})
	if a == b {
		t.Errorf("signature unchanged when case changes: %q", a)
	}
	assertHex64(t, a)
	assertHex64(t, b)
}

func TestColumnSignature_AddedColumnChangesTheSignature(t *testing.T) {
	a := columnSignature([]string{"A", "B"})
	b := columnSignature([]string{"A", "B", "C"})
	if a == b {
		t.Errorf("signature unchanged when a column is added: %q", a)
	}
	assertHex64(t, a)
	assertHex64(t, b)
}

func TestColumnSignature_RemovedColumnOrTrailingSpaceChangesTheSignature(t *testing.T) {
	t.Run("removed column", func(t *testing.T) {
		a := columnSignature([]string{"A", "B", "C"})
		b := columnSignature([]string{"A", "B"})
		if a == b {
			t.Errorf("signature unchanged when a column is removed: %q", a)
		}
		assertHex64(t, a)
		assertHex64(t, b)
	})
	t.Run("trailing space", func(t *testing.T) {
		a := columnSignature([]string{"Total"})
		b := columnSignature([]string{"Total "})
		if a == b {
			t.Errorf("signature unchanged for a trailing space, want no trimming: %q", a)
		}
		assertHex64(t, a)
		assertHex64(t, b)
	})
}

// wantColumnSignature computes hex(sha256(json.Marshal(header))) independently of columnSignature.
func wantColumnSignature(t *testing.T, header []string) string {
	t.Helper()
	b, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestColumnSignature_IsHexSHA256OfJSONMarshal pins the exact formula. A comma
// join passes the order, case and length specs; "a,b" separates it. A header
// holding <, > and & separates json.Marshal from an encoder that skips HTML escaping.
func TestColumnSignature_IsHexSHA256OfJSONMarshal(t *testing.T) {
	h1 := []string{"a,b"}
	h2 := []string{"a", "b"}
	if a, b := columnSignature(h1), columnSignature(h2); a == b {
		t.Errorf("signature unchanged for %v vs %v: %q", h1, h2, a)
	}
	for _, h := range [][]string{h1, h2, {"Qty <5 & >0", "Total"}} {
		want := wantColumnSignature(t, h)
		got := columnSignature(h)
		if got != want {
			t.Errorf("columnSignature(%v) = %q, want hex(sha256(json.Marshal(header))) = %q", h, got, want)
		}
	}
}
