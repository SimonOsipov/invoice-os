package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

type spiedRequest struct{ Method, URL, Auth string }

// spyTransport records every request and answers 200.
type spyTransport struct {
	mu   sync.Mutex
	reqs []spiedRequest
}

func (s *spyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, spiedRequest{r.Method, r.URL.String(), r.Header.Get("Authorization")})
	s.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func (s *spyTransport) seen() []spiedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spiedRequest(nil), s.reqs...)
}

func getenvOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func textLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

var oneMail = []accountmail.Message{{To: "b@x.test", Subject: "s", HTML: "<p>h</p>"}}

func assertModeLine(t *testing.T, logs, mode string) {
	t.Helper()
	if !strings.Contains(logs, "tenancy: invite mail mode") || !strings.Contains(logs, "mode="+mode) || !strings.Contains(logs, "level=INFO") {
		t.Errorf("log = %q, want an INFO line %q with mode=%s", logs, "tenancy: invite mail mode", mode)
	}
}

func TestInviteSender_PreviewCapturesAndDiscardsTheKey(t *testing.T) {
	for _, env := range []string{"pr-123", "dev-pr-7"} {
		t.Run(env, func(t *testing.T) {
			spy := &spyTransport{}
			var logs bytes.Buffer
			env := getenvOf(map[string]string{"DEPLOY_ENV": env, "RESEND_SENDING_KEY": "k_live"})

			s := inviteSender(env, spy, textLogger(&logs))

			c, ok := s.(*accountmail.Capture)
			if !ok {
				t.Fatalf("sender = %T, want *accountmail.Capture", s)
			}
			if err := s.Send(context.Background(), oneMail); err != nil {
				t.Errorf("Send err = %v, want nil", err)
			}
			if got := spy.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) %+v, want none", len(got), got)
			}
			if got := c.Messages(); len(got) != 1 || got[0].To != "b@x.test" {
				t.Errorf("captured = %+v, want the one mail", got)
			}
			assertModeLine(t, logs.String(), "capture")
			if strings.Contains(logs.String(), "k_live") {
				t.Errorf("log holds the key: %s", logs.String())
			}
		})
	}
}

func TestInviteSender_ProductionWithKeySendsThroughTheTransport(t *testing.T) {
	spy := &spyTransport{}
	var logs bytes.Buffer
	env := getenvOf(map[string]string{"DEPLOY_ENV": "production", "RESEND_SENDING_KEY": "k_live"})

	s := inviteSender(env, spy, textLogger(&logs))

	if _, ok := s.(*accountmail.Resend); !ok {
		t.Fatalf("sender = %T, want *accountmail.Resend", s)
	}
	if err := s.Send(context.Background(), oneMail); err != nil {
		t.Fatalf("Send err = %v, want nil", err)
	}
	got := spy.seen()
	if len(got) != 1 {
		t.Fatalf("the transport saw %d request(s) %+v, want 1", len(got), got)
	}
	if got[0].Method != http.MethodPost || got[0].URL != "https://api.resend.com/emails/batch" || got[0].Auth != "Bearer k_live" {
		t.Errorf("request = %+v, want POST https://api.resend.com/emails/batch with Authorization: Bearer k_live", got[0])
	}
	assertModeLine(t, logs.String(), "real")
	if strings.Contains(logs.String(), "k_live") {
		t.Errorf("log holds the key: %s", logs.String())
	}
}

func TestInviteSender_ProductionWithoutKeyIsOff(t *testing.T) {
	spy := &spyTransport{}
	var logs bytes.Buffer
	env := getenvOf(map[string]string{"DEPLOY_ENV": "production"})

	s := inviteSender(env, spy, textLogger(&logs))

	if _, ok := s.(accountmail.Off); !ok {
		t.Fatalf("sender = %T, want accountmail.Off", s)
	}
	if err := s.Send(context.Background(), oneMail); !errors.Is(err, accountmail.ErrNotConfigured) {
		t.Errorf("Send err = %v, want ErrNotConfigured", err)
	}
	if got := spy.seen(); len(got) != 0 {
		t.Errorf("the transport saw %d request(s), want none", len(got))
	}
	assertModeLine(t, logs.String(), "off")
}

func TestInviteSender_EnvironmentNamesThatAreNotAPullRequestFork(t *testing.T) {
	cases := []struct {
		name, env, key string
		wantReal       bool
		wantMode       string
	}{
		{"unknown name with the key", "staging", "k_live", true, "real"},
		{"unknown name without the key", "staging", "", false, "off"},
		{"suffix after the pr number", "pr-123-hotfix", "k_live", true, "real"},
		{"upper-case pr", "PR-123", "k_live", true, "real"},
		{"pr without a number", "pr-", "", false, "off"},
		{"local, no environment name, no key", "", "", false, "off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := &spyTransport{}
			var logs bytes.Buffer
			env := getenvOf(map[string]string{"DEPLOY_ENV": c.env, "RESEND_SENDING_KEY": c.key})

			s := inviteSender(env, spy, textLogger(&logs))

			if _, ok := s.(*accountmail.Resend); ok != c.wantReal {
				t.Errorf("sender = %T, want real %v", s, c.wantReal)
			}
			if _, ok := s.(*accountmail.Capture); ok {
				t.Errorf("sender = %T: only a pr-<n> environment may capture", s)
			}
			if !c.wantReal {
				if err := s.Send(context.Background(), oneMail); !errors.Is(err, accountmail.ErrNotConfigured) {
					t.Errorf("Send err = %v, want ErrNotConfigured", err)
				}
			}
			if got := spy.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) before any real send", len(got))
			}
			assertModeLine(t, logs.String(), c.wantMode)
		})
	}
}

// Nothing in Go reads a mux pattern: a PUT spelling or another handler compiles clean.
func TestTenancyMain_StaffTenantsRouteIsRegistered(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", string(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string][]string{}
	loaders := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern := strings.Trim(lit.Value, `"`)
		name, loader := "?", "?"
		if h, ok := call.Args[1].(*ast.CallExpr); ok {
			if hs, ok := h.Fun.(*ast.SelectorExpr); ok {
				if x, ok := hs.X.(*ast.Ident); ok {
					name = x.Name + "." + hs.Sel.Name
				}
			}
			if len(h.Args) > 0 {
				if ls, ok := h.Args[0].(*ast.SelectorExpr); ok {
					if x, ok := ls.X.(*ast.Ident); ok {
						loader = x.Name + "." + ls.Sel.Name
					}
				}
			}
		}
		handlers[pattern] = append(handlers[pattern], name)
		loaders[pattern] = append(loaders[pattern], loader)
		return true
	})
	// Control: the scan sees a route that is registered today.
	if got := handlers["GET /v1/me"]; len(got) != 1 || got[0] != "tenancy.MeHandler" {
		t.Fatalf(`"GET /v1/me" registered with %v, want exactly [tenancy.MeHandler]: the scan reads nothing`, got)
	}
	for _, want := range []struct{ pattern, handler, loader string }{
		{"GET /v1/staff/tenants", "tenancy.StaffTenantsHandler", "store.StaffTenants"},
		{"GET /v1/staff/tenants/{id}", "tenancy.StaffTenantHandler", "store.StaffTenant"},
	} {
		if got := handlers[want.pattern]; len(got) != 1 || got[0] != want.handler {
			t.Errorf("%q registered with %v, want exactly [%s]", want.pattern, got, want.handler)
		}
		if got := loaders[want.pattern]; len(got) != 1 || got[0] != want.loader {
			t.Errorf("%q loader = %v, want exactly [%s]", want.pattern, got, want.loader)
		}
	}
}
