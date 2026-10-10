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

const (
	// Design § API contracts; msgInvalidBody (signin_test.go) is the 400 text.
	msgStaffGrantUnavailable = "staff grant unavailable"
	// 1 KiB, the cap the sibling handlers share as maxExchangeBodyBytes (signin.go).
	mockStaffBodyCap = 1024
)

type staffGrantRecorder struct {
	got       []uuid.UUID
	rulesRole []bool
	err       error
}

func (g *staffGrantRecorder) grant(_ context.Context, id uuid.UUID, rulesRole bool) error {
	g.got = append(g.got, id)
	g.rulesRole = append(g.rulesRole, rulesRole)
	return g.err
}

func postMockStaff(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/mock/staff", strings.NewReader(body)))
	return rec
}

// padLeft pads body with leading spaces to n bytes. Trailing padding is not read once the
// JSON value completes, so only leading padding puts the cap in play.
func padLeft(body string, n int) string {
	return strings.Repeat(" ", n-len(body)) + body
}

func TestMockStaff_GrantsTheGivenUser(t *testing.T) {
	log, _ := captureLog()
	rec := &staffGrantRecorder{}
	id := uuid.New()

	resp := postMockStaff(MockStaffHandler(rec.grant, log), `{"user_id":"`+id.String()+`"}`)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("POST valid user_id = %d (body %s), want 204", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if resp.Body.Len() != 0 {
		t.Errorf("204 body = %q, want empty", resp.Body.String())
	}
	if len(rec.got) != 1 || rec.got[0] != id {
		t.Errorf("grant calls = %v, want exactly one with %s", rec.got, id)
	}
}

func TestMockStaff_RefusesABadBody(t *testing.T) {
	log, _ := captureLog()
	rec := &staffGrantRecorder{}
	h := MockStaffHandler(rec.grant, log)
	valid := `{"user_id":"` + uuid.NewString() + `"}`

	cases := []struct{ name, body string }{
		{"malformed json", `{`},
		{"over 1 KiB", padLeft(valid, mockStaffBodyCap+1)},
		{"missing user_id", `{}`},
		{"user_id not a uuid", `{"user_id":"x"}`},
		{"empty body", ``},
		{"user_id null", `{"user_id":null}`},
		{"user_id not a string", `{"user_id":5}`},
		{"valid user_id then a mistyped duplicate", `{"user_id":"` + uuid.NewString() + `","user_id":5}`},
		{"urn form of a uuid", `{"user_id":"urn:uuid:` + uuid.NewString() + `"}`},
		{"braced uuid", `{"user_id":"{` + uuid.NewString() + `}"}`},
		{"uuid without hyphens", `{"user_id":"` + strings.ReplaceAll(uuid.NewString(), "-", "") + `"}`},
		{"body is an array", `["` + uuid.NewString() + `"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := postMockStaff(h, c.body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("POST %q = %d (body %s), want 400", c.name, resp.Code, resp.Body.String())
			}
			var out map[string]string
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out["error"] != msgInvalidBody {
				t.Errorf("body = %s (decode err %v), want {\"error\":%q}", resp.Body.String(), err, msgInvalidBody)
			}
			if got := resp.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
	if len(cases[1].body) != mockStaffBodyCap+1 {
		t.Fatalf("oversize fixture is %d bytes, want %d", len(cases[1].body), mockStaffBodyCap+1)
	}
	if len(rec.got) != 0 {
		t.Errorf("grant ran %d time(s) for a refused body: %v", len(rec.got), rec.got)
	}

	// Positive control: the same handler and recorder do grant a valid body.
	if resp := postMockStaff(h, valid); resp.Code != http.StatusNoContent || len(rec.got) != 1 {
		t.Errorf("control: valid body = %d with %d grant call(s), want 204 and 1", resp.Code, len(rec.got))
	}
}

func TestMockStaff_ExactlyOneKiBIsAccepted(t *testing.T) {
	log, _ := captureLog()
	rec := &staffGrantRecorder{}
	id := uuid.New()
	body := padLeft(`{"user_id":"`+id.String()+`"}`, mockStaffBodyCap)
	if len(body) != mockStaffBodyCap {
		t.Fatalf("boundary fixture is %d bytes, want %d", len(body), mockStaffBodyCap)
	}

	resp := postMockStaff(MockStaffHandler(rec.grant, log), body)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("POST a %d-byte body = %d (body %s), want 204", len(body), resp.Code, resp.Body.String())
	}
	if len(rec.got) != 1 || rec.got[0] != id {
		t.Errorf("grant calls = %v, want exactly one with %s", rec.got, id)
	}
}

func TestMockStaff_GrantFailureIs502(t *testing.T) {
	log, buf := captureLog()
	const failure = "grant-boom-7f3a"
	rec := &staffGrantRecorder{err: errors.New(failure)}
	id := uuid.NewString()
	body := `{"user_id":"` + id + `"}`

	resp := postMockStaff(MockStaffHandler(rec.grant, log), body)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("POST with a failing grant = %d (body %s), want 502", resp.Code, resp.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out["error"] != msgStaffGrantUnavailable {
		t.Errorf("body = %s (decode err %v), want {\"error\":%q}", resp.Body.String(), err, msgStaffGrantUnavailable)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if len(rec.got) != 1 {
		t.Errorf("grant calls = %d, want 1", len(rec.got))
	}

	logged := buf.String()
	if !strings.Contains(logged, failure) || !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("log = %s, want the grant error at WARN", logged)
	}
	if strings.Contains(logged, id) || strings.Contains(logged, "user_id") {
		t.Errorf("log carries the request body: %s", logged)
	}
}

func TestMockStaff_GrantsTheParsedUUIDOfAnEquivalentBody(t *testing.T) {
	id := uuid.New()
	other := uuid.New()
	cases := []struct{ name, body string }{
		{"uppercase uuid", `{"user_id":"` + strings.ToUpper(id.String()) + `"}`},
		{"extra fields", `{"role":"admin","user_id":"` + id.String() + `","n":1}`},
		{"a second value after the first", `{"user_id":"` + id.String() + `"}{"user_id":"` + other.String() + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &staffGrantRecorder{}

			resp := postMockStaff(MockStaffHandler(rec.grant, log), c.body)

			if resp.Code != http.StatusNoContent {
				t.Fatalf("POST = %d (body %s), want 204", resp.Code, resp.Body.String())
			}
			if len(rec.got) != 1 || rec.got[0] != id {
				t.Errorf("grant calls = %v, want exactly one with %s", rec.got, id)
			}
		})
	}
}

func TestMockStaff_RefusesOtherMethodsWithoutGranting(t *testing.T) {
	log, _ := captureLog()
	rec := &staffGrantRecorder{}
	h := MockStaffHandler(rec.grant, log)
	body := `{"user_id":"` + uuid.NewString() + `"}`

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(method, "/auth/mock/staff", strings.NewReader(body)))
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s = %d (body %s), want 405", method, rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
		})
	}
	if len(rec.got) != 0 {
		t.Errorf("grant ran %d time(s) for a non-POST request: %v", len(rec.got), rec.got)
	}
	if resp := postMockStaff(h, body); resp.Code != http.StatusNoContent || len(rec.got) != 1 {
		t.Errorf("control: POST = %d with %d grant call(s), want 204 and 1", resp.Code, len(rec.got))
	}
}

type staffCtxKey struct{}

func TestMockStaff_GrantRunsUnderTheRequestContext(t *testing.T) {
	log, _ := captureLog()
	var seen any
	h := MockStaffHandler(func(ctx context.Context, _ uuid.UUID, _ bool) error {
		seen = ctx.Value(staffCtxKey{})
		return nil
	}, log)
	req := httptest.NewRequest(http.MethodPost, "/auth/mock/staff", strings.NewReader(`{"user_id":"`+uuid.NewString()+`"}`))
	req = req.WithContext(context.WithValue(req.Context(), staffCtxKey{}, "marker"))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST = %d (body %s), want 204", rr.Code, rr.Body.String())
	}
	if seen != "marker" {
		t.Errorf("grant context value = %v, want the request's marker", seen)
	}
}

// google/uuid.Parse drops the first and last byte of a 38-byte string without checking them.
func TestMockStaff_RefusesAUserIDWrappedInJunk(t *testing.T) {
	id := uuid.NewString()
	cases := []struct{ name, userID string }{
		{"surrounding spaces", " " + id + " "},
		{"leading and trailing junk", "X" + id + "!"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &staffGrantRecorder{}

			resp := postMockStaff(MockStaffHandler(rec.grant, log), `{"user_id":"`+c.userID+`"}`)

			if resp.Code != http.StatusBadRequest {
				t.Errorf("POST user_id %q = %d, want 400", c.userID, resp.Code)
			}
			if len(rec.got) != 0 {
				t.Errorf("grant ran for user_id %q: %v", c.userID, rec.got)
			}
		})
	}
}

func TestMockStaff_PassesRulesRoleToTheGrant(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"true", `{"user_id":"` + id + `","rules_role":true}`, true},
		{"false", `{"user_id":"` + id + `","rules_role":false}`, false},
		{"absent", `{"user_id":"` + id + `"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &staffGrantRecorder{}

			resp := postMockStaff(MockStaffHandler(rec.grant, log), c.body)

			if resp.Code != http.StatusNoContent {
				t.Fatalf("POST = %d (body %s), want 204", resp.Code, resp.Body.String())
			}
			if len(rec.rulesRole) != 1 || rec.rulesRole[0] != c.want {
				t.Errorf("grant rulesRole args = %v, want [%v]", rec.rulesRole, c.want)
			}
		})
	}
}

func TestMockStaff_RefusesANonBooleanRulesRole(t *testing.T) {
	id := uuid.NewString()
	for _, v := range []string{`"yes"`, `1`, `null`} {
		t.Run(v, func(t *testing.T) {
			log, _ := captureLog()
			rec := &staffGrantRecorder{}

			resp := postMockStaff(MockStaffHandler(rec.grant, log), `{"user_id":"`+id+`","rules_role":`+v+`}`)

			if resp.Code != http.StatusBadRequest {
				t.Errorf("POST rules_role %s = %d, want 400", v, resp.Code)
			}
			if len(rec.got) != 0 {
				t.Errorf("grant ran for rules_role %s: %v", v, rec.got)
			}
		})
	}
}
