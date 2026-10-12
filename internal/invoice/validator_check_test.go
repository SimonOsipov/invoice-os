package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const checkS2SToken = "check-qa-test-s2s-token"

// startCheck04 serves the real check handler behind the real peer-auth middleware.
func startCheck04(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(validation.S2SMiddleware(checkS2SToken)(validation.CheckCustomRuleHandler(validation.NewDefaultEngine())))
	t.Cleanup(srv.Close)
	return srv
}

func goodCheckInput() customrule.CheckInput {
	return customrule.CheckInput{Type: "required", Target: "buyer.name", Params: json.RawMessage(`{}`),
		Severity: "error", Message: "Buyer name is required."}
}

// handlerReason asks the real handler directly, so the expected text is the handler's own.
func handlerReason(t *testing.T, in customrule.CheckInput) string {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/custom-rules/check", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	validation.CheckCustomRuleHandler(validation.NewDefaultEngine()).ServeHTTP(rec, req)
	var out struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Error == "" {
		t.Fatalf("handler answered %d %q for %+v, want a 422 with a reason", rec.Code, rec.Body.String(), in)
	}
	return out.Error
}

func TestValidatorCheckCustomRule_MapsTheValidationAnswers(t *testing.T) {
	srv := startCheck04(t)
	ctx := context.Background()
	v := NewValidator(srv.URL, checkS2SToken, nil)

	t.Run("a valid rule answers nil", func(t *testing.T) {
		if err := v.CheckCustomRule(ctx, goodCheckInput()); err != nil {
			t.Errorf("CheckCustomRule(valid) = %v, want nil", err)
		}
	})

	refusals := map[string]customrule.CheckInput{
		"cel does not compile": {Type: "cel", Target: "total", Params: json.RawMessage(`{"expr":"invoice.total >"}`),
			Severity: "error", Message: "Total must be positive."},
		"severity info": func() customrule.CheckInput { in := goodCheckInput(); in.Severity = "info"; return in }(),
		"when guard does not compile": func() customrule.CheckInput {
			in := goodCheckInput()
			bad := "1 +"
			in.When = &bad
			return in
		}(),
	}
	for name, in := range refusals {
		t.Run(name, func(t *testing.T) {
			want := handlerReason(t, in)
			err := v.CheckCustomRule(ctx, in)
			var ie customrule.InvalidError
			if !errors.As(err, &ie) {
				t.Fatalf("CheckCustomRule = %v, want customrule.InvalidError", err)
			}
			if ie.Msg != want {
				t.Errorf("InvalidError.Msg = %q, want the handler's text %q", ie.Msg, want)
			}
			if errors.Is(err, ErrUpstream) {
				t.Errorf("a refusal wraps ErrUpstream: %v", err)
			}
		})
	}

	t.Run("a probe fault reaches the customer path as the plain sentence", func(t *testing.T) {
		in := goodCheckInput()
		in.Type, in.Target, in.Params = "cel", "total", json.RawMessage(`{"expr":"invoice.total > 5"}`)
		var ie customrule.InvalidError
		if err := v.CheckCustomRule(ctx, in); !errors.As(err, &ie) {
			t.Fatalf("CheckCustomRule = %v, want customrule.InvalidError", err)
		}
		if want := "the rule does not evaluate: no such key: total"; ie.Msg != want {
			t.Errorf("InvalidError.Msg = %q, want %q", ie.Msg, want)
		}
	})

	t.Run("a wrong token is an upstream error", func(t *testing.T) {
		err := NewValidator(srv.URL, "not-the-token", nil).CheckCustomRule(ctx, goodCheckInput())
		if !errors.Is(err, ErrUpstream) {
			t.Errorf("CheckCustomRule with a wrong token = %v, want ErrUpstream", err)
		}
		var ie customrule.InvalidError
		if errors.As(err, &ie) {
			t.Errorf("a wrong token read as a rule refusal: %v", err)
		}
	})

	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			canned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			}))
			defer canned.Close()
			err := NewValidator(canned.URL, checkS2SToken, nil).CheckCustomRule(ctx, goodCheckInput())
			if !errors.Is(err, ErrUpstream) {
				t.Errorf("CheckCustomRule on a %d = %v, want ErrUpstream", status, err)
			}
			var ie customrule.InvalidError
			if errors.As(err, &ie) {
				t.Errorf("a %d read as a rule refusal: %v", status, err)
			}
		})
	}

	t.Run("a closed connection is an upstream error", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		if err := NewValidator(url, checkS2SToken, nil).CheckCustomRule(ctx, goodCheckInput()); !errors.Is(err, ErrUpstream) {
			t.Errorf("CheckCustomRule on a closed server = %v, want ErrUpstream", err)
		}
	})
}
