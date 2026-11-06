package approval

// Pure specs for firstMatch: no dbTestPools, so they cannot skip.

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

func catchAll(id string) inForceVersion { return inForceVersion{VersionID: id, PolicyID: "p-" + id} }

func commercialDoc() matchDoc {
	return matchDoc{DocumentType: "commercial_invoice", EntityID: "e1", Total: dec("100.00"), Currency: ptr("NGN")}
}

func wantMatched(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matched = %#v, want %#v", got, want)
	}
}

func TestFirstMatch_FirstInSliceOrderWins(t *testing.T) {
	v1, v2 := catchAll("v1"), catchAll("v2")
	got, _, ok := firstMatch(commercialDoc(), []inForceVersion{v1, v2})
	if !ok || got.VersionID != "v1" {
		t.Errorf("two catch-alls: got %q ok=%v, want v1 ok=true", got.VersionID, ok)
	}

	v1.DocumentTypes = []string{"credit_note"}
	got, _, ok = firstMatch(commercialDoc(), []inForceVersion{v1, v2})
	if !ok || got.VersionID != "v2" {
		t.Errorf("first scoped to credit_note, commercial invoice: got %q ok=%v, want v2 ok=true", got.VersionID, ok)
	}

	credit := commercialDoc()
	credit.DocumentType = "credit_note"
	got, _, ok = firstMatch(credit, []inForceVersion{v1, v2})
	if !ok || got.VersionID != "v1" {
		t.Errorf("first scoped to credit_note, credit note: got %q ok=%v, want v1 ok=true", got.VersionID, ok)
	}
}

func TestFirstMatch_DocumentType(t *testing.T) {
	for _, c := range []struct {
		name, docType string
		types         []string
		want          bool
	}{
		{"credit note in {credit_note}", "credit_note", []string{"credit_note"}, true},
		{"commercial invoice not in {credit_note}", "commercial_invoice", []string{"credit_note"}, false},
		{"debit note in {credit_note, debit_note}", "debit_note", []string{"credit_note", "debit_note"}, true},
		{"commercial invoice in {commercial_invoice}", "commercial_invoice", []string{"commercial_invoice"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := commercialDoc()
			doc.DocumentType = c.docType
			v := catchAll("v")
			v.DocumentTypes = c.types
			got, matched, ok := firstMatch(doc, []inForceVersion{v})
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if c.want {
				if got.VersionID != "v" {
					t.Errorf("VersionID = %q, want v", got.VersionID)
				}
				wantMatched(t, matched, []string{scopeDocumentType})
			}
		})
	}
}

func TestFirstMatch_Client(t *testing.T) {
	for _, c := range []struct {
		name, entity string
		want         bool
	}{{"E1 in {E1,E2}", "E1", true}, {"E2 in {E1,E2}", "E2", true}, {"E3 not in {E1,E2}", "E3", false}} {
		t.Run(c.name, func(t *testing.T) {
			doc := commercialDoc()
			doc.EntityID = c.entity
			v := catchAll("v")
			v.EntityIDs = []string{"E1", "E2"}
			_, matched, ok := firstMatch(doc, []inForceVersion{v})
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if c.want {
				wantMatched(t, matched, []string{scopeClient})
			}
		})
	}
}

func TestFirstMatch_AmountBand(t *testing.T) {
	band := func(lo, hi, cur string) inForceVersion {
		v := catchAll("v")
		if lo != "" {
			v.BandLower = dec(lo)
		}
		if hi != "" {
			v.BandUpper = dec(hi)
		}
		v.BandCurrency = ptr(cur)
		return v
	}
	full := band("5000000.00", "50000000.00", "NGN")
	for _, c := range []struct {
		name  string
		v     inForceVersion
		total *decimal.Decimal
		cur   *string
		want  bool
	}{
		{"lower bound inclusive", full, dec("5000000.00"), ptr("NGN"), true},
		{"upper bound inclusive", full, dec("50000000.00"), ptr("NGN"), true},
		{"just below lower", full, dec("4999999.99"), ptr("NGN"), false},
		{"just above upper", full, dec("50000000.01"), ptr("NGN"), false},
		{"other currency", full, dec("10000000.00"), ptr("USD"), false},
		{"NULL total", full, nil, ptr("NGN"), false},
		{"NULL currency", full, dec("10000000.00"), nil, false},
		{"lower only, huge total", band("5000000.00", "", "NGN"), dec("1000000000000.00"), ptr("NGN"), true},
		{"upper only, zero total", band("", "100.00", "NGN"), dec("0.00"), ptr("NGN"), true},
		{"lower only, below", band("5000000.00", "", "NGN"), dec("1.00"), ptr("NGN"), false},
		{"upper only, above", band("", "100.00", "NGN"), dec("100.01"), ptr("NGN"), false},
		{"lower equals upper, on it", band("100.00", "100.00", "NGN"), dec("100.00"), ptr("NGN"), true},
		{"lower equals upper, a cent off", band("100.00", "100.00", "NGN"), dec("100.01"), ptr("NGN"), false},
		{"currency compares case-sensitively", full, dec("10000000.00"), ptr("ngn"), false},
		{"negative total below the lower bound", full, dec("-10000000.00"), ptr("NGN"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := commercialDoc()
			doc.Total, doc.Currency = c.total, c.cur
			_, matched, ok := firstMatch(doc, []inForceVersion{c.v})
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if c.want {
				wantMatched(t, matched, []string{scopeAmountBand})
			}
		})
	}
}

func TestFirstMatch_CreatorRole(t *testing.T) {
	for _, c := range []struct {
		name  string
		roles map[string]bool
		want  bool
	}{
		{"creator holds R1 of {R1,R2}", map[string]bool{"R1": true}, true},
		{"creator holds R2 of {R1,R2}", map[string]bool{"R2": true}, true},
		{"creator holds R3 only", map[string]bool{"R3": true}, false},
		{"creator holds no role", map[string]bool{}, false},
		{"a role present as false does not count", map[string]bool{"R1": false}, false},
		{"creator roles nil", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := commercialDoc()
			doc.CreatorRoles = c.roles
			v := catchAll("v")
			v.RoleIDs = []string{"R1", "R2"}
			_, matched, ok := firstMatch(doc, []inForceVersion{v})
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if c.want {
				wantMatched(t, matched, []string{scopeCreatorRole})
			}
		})
	}
}

func TestFirstMatch_EverySetScopeMustMatch(t *testing.T) {
	v := catchAll("v")
	v.DocumentTypes = []string{"credit_note"}
	v.EntityIDs = []string{"E1"}

	doc := commercialDoc()
	doc.DocumentType = "credit_note"
	doc.EntityID = "E2"
	if _, _, ok := firstMatch(doc, []inForceVersion{v}); ok {
		t.Error("credit note in E2 matched a {credit_note} + client {E1} version, want no match")
	}

	doc.EntityID = "E1"
	_, matched, ok := firstMatch(doc, []inForceVersion{v})
	if !ok {
		t.Fatal("credit note in E1: no match, want match")
	}
	wantMatched(t, matched, []string{scopeDocumentType, scopeClient})

	doc.DocumentType = "commercial_invoice"
	if _, _, ok := firstMatch(doc, []inForceVersion{v}); ok {
		t.Error("commercial invoice in E1 matched, want no match (type fails)")
	}

	// All four set: names come back in the fixed order, whatever the struct order.
	all := catchAll("all")
	all.DocumentTypes = []string{"credit_note"}
	all.EntityIDs = []string{"E1"}
	all.BandCurrency, all.BandLower = ptr("NGN"), dec("1.00")
	all.RoleIDs = []string{"R1"}
	doc = matchDoc{DocumentType: "credit_note", EntityID: "E1", Total: dec("2.00"), Currency: ptr("NGN"),
		CreatorRoles: map[string]bool{"R1": true}}
	_, matched, ok = firstMatch(doc, []inForceVersion{all})
	if !ok {
		t.Fatal("all four scopes satisfied: no match, want match")
	}
	wantMatched(t, matched, []string{scopeDocumentType, scopeClient, scopeAmountBand, scopeCreatorRole})
}

func TestFirstMatch_CatchAllMatchesWithNoScopeNamed(t *testing.T) {
	docs := []matchDoc{
		commercialDoc(),
		{DocumentType: "debit_note", EntityID: "other"},
		{DocumentType: "credit_note", EntityID: "other", Total: dec("-5.00")},
	}
	// The database hands back empty, non-nil arrays for an unset scope.
	emptyArrays := catchAll("v")
	emptyArrays.DocumentTypes, emptyArrays.EntityIDs, emptyArrays.RoleIDs = []string{}, []string{}, []string{}
	for _, v := range []inForceVersion{catchAll("v"), emptyArrays} {
		for _, doc := range docs {
			got, matched, ok := firstMatch(doc, []inForceVersion{v})
			if !ok || got.VersionID != "v" {
				t.Fatalf("catch-all vs %+v: got %q ok=%v, want v ok=true", doc, got.VersionID, ok)
			}
			if matched == nil || len(matched) != 0 {
				t.Errorf("matched = %#v, want a non-nil empty slice", matched)
			}
		}
	}
}

func TestFirstMatch_NothingMatches(t *testing.T) {
	v := catchAll("v")
	v.DocumentTypes = []string{"credit_note"}
	if _, _, ok := firstMatch(commercialDoc(), []inForceVersion{v}); ok {
		t.Error("commercial invoice matched a {credit_note} version, want ok=false")
	}
	if _, _, ok := firstMatch(commercialDoc(), nil); ok {
		t.Error("no versions: ok=true, want false")
	}
	unknown := commercialDoc()
	unknown.DocumentType = ""
	if _, _, ok := firstMatch(unknown, []inForceVersion{v}); ok {
		t.Error("document with no type matched a {credit_note} version, want ok=false")
	}
	// Control: the same version matches a credit note, so ok=false above is the scope, not a dead matcher.
	credit := commercialDoc()
	credit.DocumentType = "credit_note"
	if _, _, ok := firstMatch(credit, []inForceVersion{v}); !ok {
		t.Error("credit note vs {credit_note}: ok=false, want true")
	}
}
