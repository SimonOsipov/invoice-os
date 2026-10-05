package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Design § API contract; msgInvalidBody (signin_test.go) is the 400 text.
const msgMemberGrantUnavailable = "membership grant unavailable"

type memberGrantRecorder struct {
	got []MemberGrant
	err error
}

func (g *memberGrantRecorder) grant(_ context.Context, m MemberGrant) error {
	g.got = append(g.got, m)
	return g.err
}

// memberBody is a valid request body; set overrides one raw JSON value by key.
func memberBody(userID, tenantID string, set map[string]string) string {
	fields := map[string]string{
		"user_id":      `"` + userID + `"`,
		"tenant_id":    `"` + tenantID + `"`,
		"role":         `"reviewer"`,
		"display_name": `"Ada Okafor"`,
		"email":        `"ada@example.test"`,
	}
	for k, v := range set {
		fields[k] = v
	}
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		if v != "" {
			parts = append(parts, `"`+k+`":`+v)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func postMockMember(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/mock/member", strings.NewReader(body)))
	return rec
}

func quoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestMockMemberGrantsParsedBody(t *testing.T) {
	log, _ := captureLog()
	rec := &memberGrantRecorder{}
	user, tenant := uuid.New(), uuid.New()
	body := memberBody(user.String(), tenant.String(), map[string]string{
		"role": `"preparer"`, "display_name": `"Bola Adeyemi"`, "email": `"bola@example.test"`,
	})

	resp := postMockMember(MockMemberHandler(rec.grant, log), body)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("POST valid body = %d (body %s), want 204", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if resp.Body.Len() != 0 {
		t.Errorf("204 body = %q, want empty", resp.Body.String())
	}
	want := MemberGrant{TenantID: tenant, UserID: user, Role: "preparer", DisplayName: "Bola Adeyemi", Email: "bola@example.test"}
	if len(rec.got) != 1 || rec.got[0] != want {
		t.Errorf("grant calls = %+v, want exactly one with %+v", rec.got, want)
	}
}

func TestMockMemberRejectsBadBodies(t *testing.T) {
	log, _ := captureLog()
	rec := &memberGrantRecorder{}
	h := MockMemberHandler(rec.grant, log)
	user, tenant := uuid.NewString(), uuid.NewString()
	hex32 := func(id string) string { return strings.ReplaceAll(id, "-", "") }

	cases := []struct{ name, body string }{
		{"malformed json", `{`},
		{"empty body", ``},
		{"body is an array", `[]`},
		{"user_id 32-hex", memberBody(hex32(user), tenant, nil)},
		{"user_id braced", memberBody("{"+user+"}", tenant, nil)},
		{"user_id urn", memberBody("urn:uuid:"+user, tenant, nil)},
		{"user_id wrapped in junk", memberBody("X"+user+"!", tenant, nil)},
		{"user_id missing", memberBody(user, tenant, map[string]string{"user_id": ""})},
		{"user_id not a string", memberBody(user, tenant, map[string]string{"user_id": `5`})},
		{"user_id with a non-hex digit", memberBody(strings.Replace(user, user[:1], "g", 1), tenant, nil)},
		{"tenant_id padded with spaces", memberBody(user, " "+tenant+" ", nil)},
		// Every field is valid; only the decode error refuses it, so no later check can mask a dropped one.
		{"malformed json with all fields valid", strings.TrimSuffix(memberBody(user, tenant, nil), "}") + `,"email":5}`},
		{"tenant_id 32-hex", memberBody(user, hex32(tenant), nil)},
		{"tenant_id braced", memberBody(user, "{"+tenant+"}", nil)},
		{"tenant_id urn", memberBody(user, "urn:uuid:"+tenant, nil)},
		{"tenant_id missing", memberBody(user, tenant, map[string]string{"tenant_id": ""})},
		{"role owner", memberBody(user, tenant, map[string]string{"role": `"owner"`})},
		{"role wrong case", memberBody(user, tenant, map[string]string{"role": `"Admin"`})},
		{"role empty", memberBody(user, tenant, map[string]string{"role": `""`})},
		{"role upper case", memberBody(user, tenant, map[string]string{"role": `"REVIEWER"`})},
		{"role with trailing space", memberBody(user, tenant, map[string]string{"role": `"preparer "`})},
		{"role missing", memberBody(user, tenant, map[string]string{"role": ""})},
		{"display_name empty", memberBody(user, tenant, map[string]string{"display_name": `""`})},
		{"display_name missing", memberBody(user, tenant, map[string]string{"display_name": ""})},
		{"display_name 201 characters", memberBody(user, tenant, map[string]string{"display_name": quoted(strings.Repeat("a", 201))})},
		{"display_name 201 multibyte characters", memberBody(user, tenant, map[string]string{"display_name": quoted(strings.Repeat("é", 201))})},
		{"email empty", memberBody(user, tenant, map[string]string{"email": `""`})},
		{"email missing", memberBody(user, tenant, map[string]string{"email": ""})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := postMockMember(h, c.body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("POST = %d (body %s), want 400", resp.Code, resp.Body.String())
			}
			var out map[string]string
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out["error"] != msgInvalidBody {
				t.Errorf("body = %s (decode err %v), want {\"error\":%q}", resp.Body.String(), err, msgInvalidBody)
			}
		})
	}
	if len(rec.got) != 0 {
		t.Errorf("grant ran %d time(s) for a refused body: %+v", len(rec.got), rec.got)
	}

	// Positive control: the same handler and recorder do grant a valid body.
	if resp := postMockMember(h, memberBody(user, tenant, nil)); resp.Code != http.StatusNoContent || len(rec.got) != 1 {
		t.Errorf("control: valid body = %d with %d grant call(s), want 204 and 1", resp.Code, len(rec.got))
	}
}

func TestMockMemberAcceptsBoundaryValues(t *testing.T) {
	user, tenant := uuid.New(), uuid.New()
	for _, c := range []struct {
		name string
		body string
		want MemberGrant
	}{
		{
			"200-character name",
			memberBody(user.String(), tenant.String(), map[string]string{"display_name": quoted(strings.Repeat("a", 200))}),
			MemberGrant{TenantID: tenant, UserID: user, Role: "reviewer", DisplayName: strings.Repeat("a", 200), Email: "ada@example.test"},
		},
		{
			// Characters, not bytes: 200 of these are 400 bytes.
			"200 multibyte characters",
			memberBody(user.String(), tenant.String(), map[string]string{"display_name": quoted(strings.Repeat("é", 200))}),
			MemberGrant{TenantID: tenant, UserID: user, Role: "reviewer", DisplayName: strings.Repeat("é", 200), Email: "ada@example.test"},
		},
		{
			"uppercase hyphenated ids",
			memberBody(strings.ToUpper(user.String()), strings.ToUpper(tenant.String()), nil),
			MemberGrant{TenantID: tenant, UserID: user, Role: "reviewer", DisplayName: "Ada Okafor", Email: "ada@example.test"},
		},
		{
			"admin role",
			memberBody(user.String(), tenant.String(), map[string]string{"role": `"admin"`}),
			MemberGrant{TenantID: tenant, UserID: user, Role: "admin", DisplayName: "Ada Okafor", Email: "ada@example.test"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &memberGrantRecorder{}

			resp := postMockMember(MockMemberHandler(rec.grant, log), c.body)

			if resp.Code != http.StatusNoContent {
				t.Fatalf("POST = %d (body %s), want 204", resp.Code, resp.Body.String())
			}
			if len(rec.got) != 1 || rec.got[0] != c.want {
				t.Errorf("grant calls = %+v, want exactly one with %+v", rec.got, c.want)
			}
		})
	}
}

func TestMockMemberRejectsOversizeBody(t *testing.T) {
	log, _ := captureLog()
	rec := &memberGrantRecorder{}
	h := MockMemberHandler(rec.grant, log)
	valid := memberBody(uuid.NewString(), uuid.NewString(), nil)

	// Leading padding: trailing bytes are never read once the JSON value completes.
	over := padLeft(valid, maxExchangeBodyBytes+1)
	if len(over) != maxExchangeBodyBytes+1 {
		t.Fatalf("oversize fixture is %d bytes, want %d", len(over), maxExchangeBodyBytes+1)
	}
	if resp := postMockMember(h, over); resp.Code != http.StatusBadRequest {
		t.Errorf("POST a %d-byte body = %d (body %s), want 400", len(over), resp.Code, resp.Body.String())
	}
	if len(rec.got) != 0 {
		t.Errorf("grant ran %d time(s) for an oversize body: %+v", len(rec.got), rec.got)
	}

	// Boundary pair: exactly the cap is accepted, so the 400 above is the cap and not the body.
	exact := padLeft(valid, maxExchangeBodyBytes)
	if resp := postMockMember(h, exact); resp.Code != http.StatusNoContent || len(rec.got) != 1 {
		t.Errorf("control: a %d-byte body = %d with %d grant call(s), want 204 and 1", len(exact), resp.Code, len(rec.got))
	}
}

func TestMockMemberGrantFailureIsOpaque(t *testing.T) {
	log, buf := captureLog()
	rec := &memberGrantRecorder{err: errors.New("pq: secret detail")}

	resp := postMockMember(MockMemberHandler(rec.grant, log), memberBody(uuid.NewString(), uuid.NewString(), nil))

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("POST with a failing grant = %d (body %s), want 502", resp.Code, resp.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out["error"] != msgMemberGrantUnavailable {
		t.Errorf("body = %s (decode err %v), want {\"error\":%q}", resp.Body.String(), err, msgMemberGrantUnavailable)
	}
	if strings.Contains(resp.Body.String(), "pq") || strings.Contains(resp.Body.String(), "secret") {
		t.Errorf("body leaks the DB error: %s", resp.Body.String())
	}
	if len(rec.got) != 1 {
		t.Errorf("grant calls = %d, want 1", len(rec.got))
	}
	if logged := buf.String(); !strings.Contains(logged, "secret detail") || !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("log = %s, want the grant error at WARN", logged)
	}
}

func TestMockMemberPostOnly(t *testing.T) {
	log, _ := captureLog()
	rec := &memberGrantRecorder{}
	h := MockMemberHandler(rec.grant, log)
	body := memberBody(uuid.NewString(), uuid.NewString(), nil)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(method, "/auth/mock/member", strings.NewReader(body)))
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s = %d (body %s), want 405", method, rr.Code, rr.Body.String())
			}
		})
	}
	if len(rec.got) != 0 {
		t.Errorf("grant ran %d time(s) for a non-POST request: %+v", len(rec.got), rec.got)
	}
	if resp := postMockMember(h, body); resp.Code != http.StatusNoContent || len(rec.got) != 1 {
		t.Errorf("control: POST = %d with %d grant call(s), want 204 and 1", resp.Code, len(rec.got))
	}
}
