package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// appMetaPayload is a valid signed-payload body whose app_metadata is appMeta, verbatim.
func appMetaPayload(appMeta string) string {
	now := time.Now()
	return fmt.Sprintf(`{"iss":%q,"sub":%q,"aud":"authenticated","iat":%d,"exp":%d,"role":"authenticated","app_metadata":%s}`,
		testIssuer, testSubject, now.Unix(), now.Add(time.Hour).Unix(), appMeta)
}

// verifyAppMeta signs a token carrying appMeta and verifies it.
func verifyAppMeta(t *testing.T, appMeta string) (Identity, error) {
	t.Helper()
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	return v.Verify(context.Background(), signRawPayload(t, iss, appMetaPayload(appMeta)))
}

func TestVerify_StaffReachesTheIdentity(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	for _, c := range []struct {
		name  string
		staff bool
	}{{"staff true", true}, {"staff false", false}} {
		t.Run(c.name, func(t *testing.T) {
			id, err := v.Verify(context.Background(), mustMint(t, iss, MintOptions{TenantID: "tenant-1", Email: "s@example.test", Staff: c.staff}))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if id.Staff != c.staff {
				t.Errorf("Staff = %v, want %v", id.Staff, c.staff)
			}
			if id.TenantID != "tenant-1" || id.Email != "s@example.test" {
				t.Errorf("tenant/email = %q/%q, want unchanged", id.TenantID, id.Email)
			}
		})
	}
	// Control: a raw staff:true claim reaches the identity, so the false cases above are not a dead decode.
	if id, err := verifyAppMeta(t, `{"staff":true}`); err != nil || !id.Staff {
		t.Errorf("raw staff:true = %+v, %v, want Staff true", id, err)
	}
	if id, err := verifyAppMeta(t, `{}`); err != nil || id.Staff {
		t.Errorf("absent staff = %+v, %v, want Staff false", id, err)
	}
}

func TestVerify_NoStaffKeysLeaveTheIdentityUnchanged(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)

	minted, err := v.Verify(context.Background(), mustMint(t, iss, MintOptions{Subject: testSubject, TenantID: "tenant-1", Email: "a@example.test"}))
	if err != nil {
		t.Fatalf("Verify minted: %v", err)
	}
	if want := (Identity{Subject: testSubject, Role: "authenticated", TenantID: "tenant-1", Email: "a@example.test"}); minted != want {
		t.Errorf("minted identity = %+v, want %+v", minted, want)
	}
	if minted.Staff {
		t.Error("minted: Staff = true, want false")
	}

	// Extra provider keys and a session id, as real GoTrue sends them.
	payload := fmt.Sprintf(`{"iss":%q,"sub":%q,"aud":"authenticated","exp":%d,"role":"authenticated","email":"b@example.test","session_id":"sess-1","app_metadata":{"provider":"email","providers":["email"],"tenant_id":"tenant-2"}}`,
		testIssuer, testSubject, time.Now().Add(time.Hour).Unix())
	raw, err := v.Verify(context.Background(), signRawPayload(t, iss, payload))
	if err != nil {
		t.Fatalf("Verify raw: %v", err)
	}
	if want := (Identity{Subject: testSubject, Role: "authenticated", TenantID: "tenant-2", Email: "b@example.test", SessionID: "sess-1"}); raw != want {
		t.Errorf("raw identity = %+v, want %+v", raw, want)
	}
	if raw.Staff {
		t.Error("raw: Staff = true, want false")
	}
}

// A rules_role key of any value or type verifies and changes nothing: Identity carries only Staff and the tenant.
func TestVerify_RulesRoleKeyIsIgnored(t *testing.T) {
	for _, c := range []struct {
		appMeta string
		tenant  string
		staff   bool
	}{
		{`{"staff":true,"rules_role":"x"}`, "", true},
		{`{"staff":true,"rules_role":{}}`, "", true},
		{`{"staff":true,"rules_role":1}`, "", true},
		{`{"staff":true,"rules_role":[true]}`, "", true},
		{`{"staff":true,"rules_role":null}`, "", true},
		{`{"staff":true,"rules_role":true}`, "", true},
		{`{"staff":true,"rules_role":false}`, "", true},
		{`{"rules_role":true}`, "", false},
		{`{"rules_role":"x"}`, "", false},
		{`{"staff":false,"rules_role":true}`, "", false},
		{`{"rules_role":{},"tenant_id":"t-1"}`, "t-1", false},
	} {
		t.Run(c.appMeta, func(t *testing.T) {
			id, err := verifyAppMeta(t, c.appMeta)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if want := (Identity{Subject: testSubject, Role: "authenticated", TenantID: c.tenant, Staff: c.staff}); id != want {
				t.Errorf("identity = %+v, want %+v", id, want)
			}
		})
	}
}

// No runtime path can see a dead field that nothing reads, so the type shapes are pinned by reflection.
func TestVerify_NoTypeCarriesARulesRoleField(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Identity{}), reflect.TypeOf(MintOptions{}), reflect.TypeOf(appMetadata{})} {
		if typ.NumField() == 0 {
			t.Fatalf("%s has no fields", typ)
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if strings.Contains(strings.ToLower(f.Name), "rulesrole") || strings.Contains(f.Tag.Get("json"), "rules_role") {
				t.Errorf("%s.%s (json %q) is a rules-role field", typ, f.Name, f.Tag.Get("json"))
			}
		}
	}
}

func TestVerify_NonBooleanStaffClaimIsRefused(t *testing.T) {
	// Control first: a boolean in the same slot verifies.
	if _, err := verifyAppMeta(t, `{"staff":true}`); err != nil {
		t.Fatalf("control: boolean claim refused: %v", err)
	}
	cases := []string{
		`{"staff":"true"}`,
		`{"staff":"false"}`,
		`{"staff":1}`,
		`{"staff":0}`,
		`{"staff":{}}`,
		`{"staff":[true]}`,
	}
	for _, appMeta := range cases {
		t.Run(appMeta, func(t *testing.T) {
			id, err := verifyAppMeta(t, appMeta)
			if err == nil {
				t.Fatalf("Verify accepted app_metadata %s as %+v, want an error", appMeta, id)
			}
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("error %v does not wrap ErrUnauthorized", err)
			}
		})
	}
}

func TestVerify_NullStaffClaimIsFalse(t *testing.T) {
	id, err := verifyAppMeta(t, `{"staff":null}`)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Staff {
		t.Error("Staff = true for a null claim, want false")
	}
}

func TestVerify_ExplicitFalseStaffClaimIsFalse(t *testing.T) {
	id, err := verifyAppMeta(t, `{"staff":false}`)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Staff {
		t.Error("Staff = true for an explicit false claim, want false")
	}
}

func TestVerify_CaseVariantClaimKeysAreIgnored(t *testing.T) {
	cases := []struct {
		appMeta    string
		wantStaff  bool
		wantTenant string
		name       string
	}{
		{`{"Staff":true}`, false, "", "staff variant"},
		{`{"STAFF":true}`, false, "", "upper-case staff variant"},
		{`{"Tenant_ID":"t-x"}`, false, "", "tenant_id variant"},
		{`{"TENANT_ID":"t-x","staff":true}`, true, "", "tenant_id variant beside an exact staff"},
		{"{\"\u017ftaff\":true}", false, "", "long-s fold of staff"},
		{`{"staff ":true,"tenant_id ":"t-x"}`, false, "", "trailing-space keys"},
		{`{" staff":true,"tenant_id":"t-1"}`, false, "t-1", "leading-space staff beside an exact tenant_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := verifyAppMeta(t, c.appMeta)
			if err != nil {
				t.Fatalf("Verify %s: %v", c.appMeta, err)
			}
			if id.TenantID != c.wantTenant {
				t.Errorf("%s: TenantID = %q, want %q", c.appMeta, id.TenantID, c.wantTenant)
			}
			if id.Staff != c.wantStaff {
				t.Errorf("%s: Staff = %v, want %v", c.appMeta, id.Staff, c.wantStaff)
			}
		})
	}
}

func TestVerify_ExactKeyWinsBesideACaseVariant(t *testing.T) {
	tenants := []string{
		`{"tenant_id":"A","Tenant_ID":"B"}`,
		`{"Tenant_ID":"B","tenant_id":"A"}`,
	}
	for _, appMeta := range tenants {
		id, err := verifyAppMeta(t, appMeta)
		if err != nil {
			t.Fatalf("Verify %s: %v", appMeta, err)
		}
		if id.TenantID != "A" {
			t.Errorf("%s: TenantID = %q, want A", appMeta, id.TenantID)
		}
	}
	flags := []struct {
		appMeta   string
		wantStaff bool
	}{
		{`{"staff":true,"Staff":false}`, true},
		{`{"Staff":false,"staff":true}`, true},
		{`{"staff":false,"Staff":true}`, false},
		{`{"Staff":true,"staff":false}`, false},
	}
	for _, c := range flags {
		id, err := verifyAppMeta(t, c.appMeta)
		if err != nil {
			t.Fatalf("Verify %s: %v", c.appMeta, err)
		}
		if id.Staff != c.wantStaff {
			t.Errorf("%s: Staff = %v, want %v", c.appMeta, id.Staff, c.wantStaff)
		}
	}
}

func TestVerify_NonStringTenantIDIsRefused(t *testing.T) {
	if id, err := verifyAppMeta(t, `{"tenant_id":"t-1"}`); err != nil || id.TenantID != "t-1" {
		t.Fatalf("control: string tenant_id = %+v, %v", id, err)
	}
	for _, appMeta := range []string{`{"tenant_id":1}`, `{"tenant_id":true}`, `{"tenant_id":{}}`, `{"tenant_id":["t-1"]}`} {
		t.Run(appMeta, func(t *testing.T) {
			id, err := verifyAppMeta(t, appMeta)
			if err == nil {
				t.Fatalf("Verify accepted %s as %+v, want an error", appMeta, id)
			}
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("error %v does not wrap ErrUnauthorized", err)
			}
		})
	}
}

// app_metadata shapes as at head: absent and null verify tenantless; a non-object is refused.
func TestVerify_AppMetadataShapesBehaveAsBefore(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	exp := time.Now().Add(time.Hour).Unix()
	head := fmt.Sprintf(`"iss":%q,"sub":%q,"aud":"authenticated","exp":%d,"role":"authenticated"`, testIssuer, testSubject, exp)
	for _, c := range []struct {
		name, payload string
		ok            bool
	}{
		{"absent", `{` + head + `}`, true},
		{"null", `{` + head + `,"app_metadata":null}`, true},
		{"empty object", `{` + head + `,"app_metadata":{}}`, true},
		{"string", `{` + head + `,"app_metadata":"staff"}`, false},
		{"array", `{` + head + `,"app_metadata":[{"staff":true}]}`, false},
		{"number", `{` + head + `,"app_metadata":1}`, false},
		{"true", `{` + head + `,"app_metadata":true}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, err := v.Verify(context.Background(), signRawPayload(t, iss, c.payload))
			if c.ok {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if want := (Identity{Subject: testSubject, Role: "authenticated"}); id != want {
					t.Errorf("identity = %+v, want %+v", id, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Verify accepted app_metadata shape as %+v, want an error", id)
			}
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("error %v does not wrap ErrUnauthorized", err)
			}
		})
	}
}

func TestMint_StaffKeyOnlyWhenSet(t *testing.T) {
	iss := mustIssuer(t)
	appMeta := func(opts MintOptions) map[string]any {
		t.Helper()
		am, _ := decodeSegment(t, mustMint(t, iss, opts), 1)["app_metadata"].(map[string]any)
		if am == nil {
			t.Fatal("minted token has no app_metadata object")
		}
		return am
	}

	plain := appMeta(MintOptions{TenantID: "tenant-1"})
	if plain["tenant_id"] != "tenant-1" {
		t.Errorf("plain: tenant_id = %v, want tenant-1", plain["tenant_id"])
	}
	if _, ok := plain["staff"]; ok {
		t.Error("plain mock token carries staff, want it absent")
	}

	staff := appMeta(MintOptions{Staff: true})
	if staff["staff"] != true {
		t.Errorf("staff token: staff = %v, want true", staff["staff"])
	}
	for name, am := range map[string]map[string]any{"plain": plain, "staff": staff} {
		if _, ok := am["rules_role"]; ok {
			t.Errorf("%s token carries rules_role, want it absent", name)
		}
	}
}
