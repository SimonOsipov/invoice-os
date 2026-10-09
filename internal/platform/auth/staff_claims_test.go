package auth

import (
	"context"
	"errors"
	"fmt"
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

func TestVerify_StaffAndRulesRoleReachTheIdentity(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	cases := []struct {
		name                string
		staff, rules        bool
		wantStaff, wantRule bool
	}{
		{"staff only", true, false, true, false},
		{"staff and rules role", true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := staffOpts(t, MintOptions{TenantID: "tenant-1", Email: "s@example.test"}, c.staff, c.rules)
			id, err := v.Verify(context.Background(), mustMint(t, iss, opts))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if staff, rules := flagsOf(t, id); staff != c.wantStaff || rules != c.wantRule {
				t.Errorf("(Staff, RulesRole) = (%v, %v), want (%v, %v)", staff, rules, c.wantStaff, c.wantRule)
			}
			if id.TenantID != "tenant-1" || id.Email != "s@example.test" {
				t.Errorf("tenant/email = %q/%q, want unchanged", id.TenantID, id.Email)
			}
		})
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
	if staff, rules := flagsOf(t, minted); staff || rules {
		t.Errorf("minted: (Staff, RulesRole) = (%v, %v), want (false, false)", staff, rules)
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
	if staff, rules := flagsOf(t, raw); staff || rules {
		t.Errorf("raw: (Staff, RulesRole) = (%v, %v), want (false, false)", staff, rules)
	}
}

func TestVerify_RulesRoleWithoutStaffIsNotARulesRole(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)

	minted, err := v.Verify(context.Background(), mustMint(t, iss, staffOpts(t, MintOptions{TenantID: "tenant-1"}, false, true)))
	if err != nil {
		t.Fatalf("Verify minted: %v", err)
	}
	if staff, rules := flagsOf(t, minted); staff || rules {
		t.Errorf("minted rules_role only: (Staff, RulesRole) = (%v, %v), want (false, false)", staff, rules)
	}

	// Control: with staff beside it, the same claim counts.
	both, err := verifyAppMeta(t, `{"staff":true,"rules_role":true}`)
	if err != nil {
		t.Fatalf("Verify staff+rules: %v", err)
	}
	if staff, rules := flagsOf(t, both); !staff || !rules {
		t.Errorf("control: (Staff, RulesRole) = (%v, %v), want (true, true)", staff, rules)
	}
}

func TestVerify_NonBooleanStaffClaimIsRefused(t *testing.T) {
	// Control first: a boolean in the same slot verifies.
	if _, err := verifyAppMeta(t, `{"staff":true,"rules_role":true}`); err != nil {
		t.Fatalf("control: boolean claims refused: %v", err)
	}
	cases := []string{
		`{"staff":"true"}`,
		`{"staff":"false"}`,
		`{"staff":1}`,
		`{"staff":0}`,
		`{"staff":{}}`,
		`{"staff":[true]}`,
		`{"staff":true,"rules_role":"true"}`,
		`{"staff":true,"rules_role":1}`,
		`{"staff":true,"rules_role":{}}`,
		`{"rules_role":"true"}`,
		`{"rules_role":1}`,
		`{"rules_role":{}}`,
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
	for _, c := range []struct {
		appMeta   string
		wantStaff bool
		wantRules bool
	}{
		{`{"staff":null,"rules_role":null}`, false, false},
		{`{"staff":true,"rules_role":null}`, true, false},
		{`{"staff":null,"rules_role":true}`, false, false},
	} {
		t.Run(c.appMeta, func(t *testing.T) {
			id, err := verifyAppMeta(t, c.appMeta)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if staff, rules := flagsOf(t, id); staff != c.wantStaff || rules != c.wantRules {
				t.Errorf("(Staff, RulesRole) = (%v, %v), want (%v, %v)", staff, rules, c.wantStaff, c.wantRules)
			}
		})
	}
}

func TestVerify_ExplicitFalseStaffClaimIsFalse(t *testing.T) {
	for _, c := range []struct {
		appMeta   string
		wantStaff bool
	}{
		{`{"staff":false,"rules_role":false}`, false},
		{`{"staff":true,"rules_role":false}`, true},
		{`{"staff":false,"rules_role":true}`, false},
	} {
		t.Run(c.appMeta, func(t *testing.T) {
			id, err := verifyAppMeta(t, c.appMeta)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if staff, rules := flagsOf(t, id); staff != c.wantStaff || rules {
				t.Errorf("(Staff, RulesRole) = (%v, %v), want (%v, false)", staff, rules, c.wantStaff)
			}
		})
	}
}

func TestVerify_CaseVariantClaimKeysAreIgnored(t *testing.T) {
	cases := []struct {
		appMeta              string
		wantStaff, wantRules bool
		wantTenant           string
		name                 string
	}{
		{`{"Staff":true,"RULES_ROLE":true}`, false, false, "", "both variants"},
		{`{"STAFF":true,"rules_role":true}`, false, false, "", "staff variant beside an exact rules_role"},
		{`{"Staff":true,"rules_role":true}`, false, false, "", "staff variant"},
		{`{"staff":true,"RULES_ROLE":true}`, true, false, "", "rules_role variant beside an exact staff"},
		{`{"Tenant_ID":"t-x"}`, false, false, "", "tenant_id variant"},
		{`{"TENANT_ID":"t-x","staff":true}`, true, false, "", "tenant_id variant beside an exact staff"},
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
			if staff, rules := flagsOf(t, id); staff != c.wantStaff || rules != c.wantRules {
				t.Errorf("%s: (Staff, RulesRole) = (%v, %v), want (%v, %v)", c.appMeta, staff, rules, c.wantStaff, c.wantRules)
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
		if staff, _ := flagsOf(t, id); staff != c.wantStaff {
			t.Errorf("%s: Staff = %v, want %v", c.appMeta, staff, c.wantStaff)
		}
	}
}

func TestMint_StaffKeysOnlyWhenSet(t *testing.T) {
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
	for _, k := range []string{"staff", "rules_role"} {
		if _, ok := plain[k]; ok {
			t.Errorf("plain mock token carries %q, want it absent", k)
		}
	}

	both := appMeta(staffOpts(t, MintOptions{TenantID: "tenant-1"}, true, true))
	if both["staff"] != true || both["rules_role"] != true {
		t.Errorf("staff+rules token: staff=%v rules_role=%v, want both true", both["staff"], both["rules_role"])
	}

	staffOnly := appMeta(staffOpts(t, MintOptions{}, true, false))
	if staffOnly["staff"] != true {
		t.Errorf("staff-only token: staff = %v, want true", staffOnly["staff"])
	}
	if _, ok := staffOnly["rules_role"]; ok {
		t.Error("staff-only token carries rules_role, want it absent")
	}

	rulesOnly := appMeta(staffOpts(t, MintOptions{}, false, true))
	if rulesOnly["rules_role"] != true {
		t.Errorf("rules-only token: rules_role = %v, want true", rulesOnly["rules_role"])
	}
	if _, ok := rulesOnly["staff"]; ok {
		t.Error("rules-only token carries staff, want it absent")
	}
}
