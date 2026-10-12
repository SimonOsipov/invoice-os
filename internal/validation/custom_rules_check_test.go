package validation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const checkToken = "check-test"

func postCheck(t *testing.T, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/custom-rules/check", strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	S2SMiddleware(checkToken)(CheckCustomRuleHandler(NewDefaultEngine())).ServeHTTP(rec, r)
	return rec
}

func checkBody(typ RuleType, params string) map[string]any {
	return map[string]any{"type": typ, "target": "buyer.name", "params": json.RawMessage(params),
		"severity": "error", "when": nil, "message": "needs attention"}
}

func sendCheck(t *testing.T, b map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return postCheck(t, string(raw), map[string]string{headerS2SToken: checkToken})
}

func TestCheckCustomRule_EveryRuleTypeAccepted(t *testing.T) {
	valid := map[RuleType]string{
		TypeRequired:    `{}`,
		TypeFormat:      `{"pattern":"^[A-Z]+$"}`,
		TypeEnum:        `{"values":["a","b"]}`,
		TypeRange:       `{"min":0,"max":10}`,
		TypeTaxMath:     `{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.01}`,
		TypeCrossField:  `{"op":"eq","other":"buyer.tin"}`,
		TypeConditional: `{"if":{"field":"currency","op":"eq","value":"NGN"},"then":{"field":"supplier.tin","required":true}}`,
		TypeDate:        `{"not_after":"today"}`,
		TypeCEL:         `{"expr":"has(invoice.total) ? invoice.total >= 0.0 : true"}`,
		TypeLineSum:     `{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}`,
	}
	eng := NewDefaultEngine()
	for typ := range eng.registry {
		if _, ok := valid[typ]; !ok {
			t.Errorf("registered type %q has no row in this table", typ)
		}
	}
	for typ, params := range valid {
		t.Run(string(typ), func(t *testing.T) {
			if rec := sendCheck(t, checkBody(typ, params)); rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("enum list", func(t *testing.T) {
		if rec := sendCheck(t, checkBody(TypeEnum, `{"list":"currencies"}`)); rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestCheckCustomRule_WhenGuardAccepted(t *testing.T) {
	b := checkBody(TypeRequired, `{}`)
	b["when"] = "has(invoice.buyer) && has(invoice.buyer.type) && invoice.buyer.type == 'B2B'"
	if rec := sendCheck(t, b); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCheckCustomRule_InvalidRulesAre422WithAReason(t *testing.T) {
	with := func(b map[string]any, k string, v any) map[string]any { b[k] = v; return b }
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"cel does not compile", checkBody(TypeCEL, `{"expr":"invoice.total >"}`), "params.expr"},
		{"when does not compile", with(checkBody(TypeRequired, `{}`), "when", "1 +"), "when"},
		{"regex does not compile", checkBody(TypeFormat, `{"pattern":"("}`), "params.pattern"},
		{"unknown type", checkBody("nope", `{}`), "type"},
		{"severity info", with(checkBody(TypeRequired, `{}`), "severity", "info"), "severity"},
		{"empty message", with(checkBody(TypeRequired, `{}`), "message", ""), "message"},
		{"long message", with(checkBody(TypeRequired, `{}`), "message", strings.Repeat("x", 501)), "message"},
		{"params not an object", checkBody(TypeRequired, `[]`), "params"},
		{"unknown list", checkBody(TypeEnum, `{"list":"planets"}`), "params.list"},
		{"faults on the empty invoice", checkBody(TypeCEL, `{"expr":"invoice.supplier.tin.size() > 0"}`), "the rule does not evaluate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := sendCheck(t, c.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body=%s)", rec.Code, rec.Body.String())
			}
			var out struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.Error, c.want) {
				t.Errorf("error = %q, want it to name %q", out.Error, c.want)
			}
			for _, leak := range []string{"*validation.", "*json.", "*cel.", "goroutine"} {
				if strings.Contains(out.Error, leak) {
					t.Errorf("error %q leaks a Go internal %q", out.Error, leak)
				}
			}
		})
	}
}

func TestCheckCustomRule_NeedsTheS2SToken(t *testing.T) {
	for name, h := range map[string]map[string]string{"none": nil, "wrong": {headerS2SToken: "nope"}} {
		if rec := postCheck(t, `{}`, h); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token: status = %d, want 401", name, rec.Code)
		}
	}
}

func TestCheckCustomRule_MalformedAndOversizedBodies(t *testing.T) {
	h := map[string]string{headerS2SToken: checkToken}
	if rec := postCheck(t, `{`, h); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: status = %d, want 400", rec.Code)
	}
	big := `{"message":"` + strings.Repeat("x", 33<<10) + `"}`
	if rec := postCheck(t, big, h); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("33 KiB: status = %d, want 413", rec.Code)
	}
}
