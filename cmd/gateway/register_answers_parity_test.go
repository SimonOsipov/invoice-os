package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

// Register must accept exactly what ProvisionHandler accepts, with its wording, or an account verifies
// and then cannot provision. The accepted answers GoTrue stores must provision as the user typed them.
func TestRegisterAnswersMatchProvisionRules(t *testing.T) {
	long := func(r string, n int) string { return strings.Repeat(r, n) }
	cases := []struct {
		name   string
		fields string
		want   *tenancy.ProvisionInput // nil: both refuse
	}{
		{"valid firm", `"workspace_name":"Acme","display_name":"Ada","kind":"firm"`, &tenancy.ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada", Kind: "firm"}},
		{"valid without kind", `"workspace_name":"Acme","display_name":"Ada"`, &tenancy.ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada"}},
		{"padded and unicode whitespace", `"workspace_name":"  Acme　","display_name":" Ada\n"`, &tenancy.ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada"}},
		{"200 runes", `"workspace_name":"` + long("é", 200) + `","display_name":"` + long("a", 200) + `"`, &tenancy.ProvisionInput{WorkspaceName: long("é", 200), DisplayName: long("a", 200)}},
		{"201 runes workspace", `"workspace_name":"` + long("é", 201) + `","display_name":"Ada"`, nil},
		{"201 runes display", `"workspace_name":"Acme","display_name":"` + long("a", 201) + `"`, nil},
		{"blank workspace", `"workspace_name":" ","display_name":"Ada"`, nil},
		{"blank display", `"workspace_name":"Acme","display_name":" "`, nil},
		{"only workspace", `"workspace_name":"Acme"`, nil},
		{"only kind", `"kind":"firm"`, nil},
		{"NUL workspace", `"workspace_name":"A\u0000","display_name":"Ada"`, nil},
		{"NUL display", `"workspace_name":"Acme","display_name":"A\u0000"`, nil},
		{"kind bogus", `"workspace_name":"Acme","display_name":"Ada","kind":"bogus"`, nil},
		{"kind empty", `"workspace_name":"Acme","display_name":"Ada","kind":""`, nil},
		{"kind cased", `"workspace_name":"Acme","display_name":"Ada","kind":"Firm"`, nil},
		{"kind null", `"workspace_name":"Acme","display_name":"Ada","kind":null`, &tenancy.ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada"}},
		{"number workspace", `"workspace_name":5,"display_name":"Ada"`, nil},
		{"null workspace", `"workspace_name":null,"display_name":"Ada"`, nil},
	}

	var accepted, refused int
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var signup []byte
			gt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				signup = b
				mu.Unlock()
				_, _ = io.WriteString(w, `{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"}`)
			}))
			t.Cleanup(gt.Close)
			authURL, _ := url.Parse(gt.URL)
			site, _ := url.Parse("https://site.example")
			register := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil).Register

			var got tenancy.ProvisionInput
			var provisioned bool
			provision := tenancy.ProvisionHandler(func(_ context.Context, in tenancy.ProvisionInput) (tenancy.Tenant, string, error) {
				got, provisioned = in, true
				return tenancy.Tenant{}, "sub", nil
			}, slog.New(slog.DiscardHandler))

			regRec := httptest.NewRecorder()
			register.ServeHTTP(regRec, httptest.NewRequest(http.MethodPost, "/auth/register",
				strings.NewReader(`{"email":"new@corp.example","password":"Corr3ct-Horse",`+c.fields+`}`)))
			provRec := httptest.NewRecorder()
			provision.ServeHTTP(provRec, httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader("{"+c.fields+"}")))

			if c.want == nil {
				refused++
				if regRec.Code != http.StatusBadRequest || provRec.Code != http.StatusBadRequest {
					t.Fatalf("register %d %s, provision %d %s; want both 400", regRec.Code, regRec.Body, provRec.Code, provRec.Body)
				}
				if regRec.Body.String() != provRec.Body.String() {
					t.Errorf("register says %s, provision says %s; want the same wording", regRec.Body, provRec.Body)
				}
				if signup != nil {
					t.Errorf("GoTrue saw %s, want no call", signup)
				}
				return
			}
			accepted++
			if regRec.Code != http.StatusAccepted || provRec.Code != http.StatusCreated {
				t.Fatalf("register %d %s, provision %d %s; want 202 and 201", regRec.Code, regRec.Body, provRec.Code, provRec.Body)
			}
			if got != *c.want {
				t.Errorf("provision input = %+v, want %+v", got, *c.want)
			}

			// What GoTrue stores as user_metadata.registration is what the console posts to provision.
			var sent struct {
				Data struct {
					Registration json.RawMessage `json:"registration"`
				} `json:"data"`
			}
			if err := json.Unmarshal(signup, &sent); err != nil || len(sent.Data.Registration) == 0 {
				t.Fatalf("signup body %s carries no data.registration (decode err %v)", signup, err)
			}
			provisioned, got = false, tenancy.ProvisionInput{}
			stored := httptest.NewRecorder()
			provision.ServeHTTP(stored, httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(string(sent.Data.Registration))))
			if stored.Code != http.StatusCreated || !provisioned || got != *c.want {
				t.Errorf("provisioning the stored %s = %d %s, input %+v; want 201 and %+v", sent.Data.Registration, stored.Code, stored.Body, got, *c.want)
			}
		})
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the table ran %d accepted and %d refused cases, want both non-zero", accepted, refused)
	}
}
