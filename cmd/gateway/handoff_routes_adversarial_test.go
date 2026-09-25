package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// countingGoTrue records every request; answer decides the reply.
func countingGoTrue(t *testing.T, answer http.HandlerFunc) (*url.URL, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func signInJSON(email string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": "pw", "state": handoffState})
	return string(b)
}

// A GoTrue 3xx is an answer, not a hop: following it could turn a refusal into a code.
func TestHandoffHandlers_DoNotFollowGoTrueRedirects(t *testing.T) {
	authURL, calls := countingGoTrue(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/landed" {
			_, _ = w.Write([]byte(`{"access_token":"tok-redirected"}`))
			return
		}
		http.Redirect(w, r, "/landed", http.StatusFound)
	})
	mux := handoffMux(t, authURL, true)

	rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("a@example.com"))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /auth/sign-in on a GoTrue 302 = %d %s, want 502", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"code"`) {
		t.Errorf("a redirected sign-in minted a code: %s", rec.Body)
	}
	if got, want := calls(), []string{"POST /token"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

// handoffHandlers is read, not run, for what no seam exposes: the client and the constructor arguments.
func TestHandoffHandlersWiring(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == "handoffHandlers" {
			fn = d
		}
	}
	if fn == nil {
		t.Fatal("main.go declares no handoffHandlers")
	}

	var clients []map[string]string
	calls := map[string][]string{}  // callee -> argument lists
	inLit := map[string]bool{}      // callee called inside a func literal
	assigned := map[string]string{} // callee -> the one name its result is bound to
	var walk func(n ast.Node, lit bool)
	walk = func(n ast.Node, lit bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				if n.Body != nil {
					walk(n.Body, true)
				}
				return false
			case *ast.AssignStmt:
				if len(n.Lhs) == 1 && len(n.Rhs) == 1 {
					if c, ok := n.Rhs[0].(*ast.CallExpr); ok {
						assigned[types.ExprString(c.Fun)] = types.ExprString(n.Lhs[0])
					}
				}
			case *ast.CompositeLit:
				if types.ExprString(n.Type) == "http.Client" {
					clients = append(clients, clientFields(n))
				}
			case *ast.CallExpr:
				callee := types.ExprString(n.Fun)
				var args []string
				for _, a := range n.Args {
					args = append(args, types.ExprString(a))
				}
				calls[callee] = append(calls[callee], strings.Join(args, ", "))
				inLit[callee] = inLit[callee] || lit
			}
			return true
		})
	}
	walk(fn.Body, false)

	if len(clients) != 1 {
		t.Fatalf("handoffHandlers builds %d http.Client literals, want 1", len(clients))
	}
	if got := clients[0]["Timeout"]; got != "10 * time.Second" {
		t.Errorf("Timeout = %q, want 10 * time.Second", got)
	}
	if got := clients[0]["CheckRedirect"]; got != "return http.ErrUseLastResponse" {
		t.Errorf("CheckRedirect returns %q, want only http.ErrUseLastResponse", got)
	}

	store, throttle := assigned["gateway.NewHandoffStore"], assigned["gateway.NewSignInThrottle"]
	for callee, want := range map[string]string{
		"gateway.NewHandoffStore":   "gateway.HandoffTTL, time.Now",
		"gateway.NewSignInThrottle": "gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now",
		"gateway.SignInHandler":     "authURL, client, " + store + ", " + throttle + ", log",
		"gateway.ExchangeHandler":   store,
	} {
		// One construction outside any closure: the store and throttle outlive a request.
		if got := calls[callee]; len(got) != 1 || got[0] != want {
			t.Errorf("%s called with %v, want exactly once with (%s)", callee, got, want)
		}
		if inLit[callee] {
			t.Errorf("%s is called inside a func literal; it would be rebuilt per call", callee)
		}
	}
	if store == "" || throttle == "" || store == throttle {
		t.Errorf("store bound to %q, throttle to %q; want two distinct names", store, throttle)
	}
}

// clientFields records an http.Client literal's fields; a func literal records what it returns.
func clientFields(cl *ast.CompositeLit) map[string]string {
	fields := map[string]string{}
	for _, e := range cl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		fields[types.ExprString(kv.Key)] = types.ExprString(kv.Value)
		if lit, ok := kv.Value.(*ast.FuncLit); ok {
			var rets []string
			ast.Inspect(lit.Body, func(n ast.Node) bool {
				if r, ok := n.(*ast.ReturnStmt); ok {
					for _, x := range r.Results {
						rets = append(rets, "return "+types.ExprString(x))
					}
				}
				return true
			})
			fields[types.ExprString(kv.Key)] = strings.Join(rets, "; ")
		}
	}
	return fields
}

// The throttle built by handoffHandlers counts across requests: the 11th wrong attempt never reaches GoTrue.
func TestHandoffThrottleSpansRequests(t *testing.T) {
	authURL, calls := countingGoTrue(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_code":"invalid_credentials","msg":"Invalid login credentials"}`))
	})
	mux := handoffMux(t, authURL, true)

	for i := 0; i < gateway.SignInMaxFailures; i++ {
		if rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("victim@example.com")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d %s, want 401", i+1, rec.Code, rec.Body)
		}
	}
	if n := len(calls()); n != gateway.SignInMaxFailures {
		t.Fatalf("GoTrue saw %d calls, want %d", n, gateway.SignInMaxFailures)
	}
	// Case and whitespace name the same address.
	if rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("  Victim@Example.com ")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("attempt %d = %d %s, want 429", gateway.SignInMaxFailures+1, rec.Code, rec.Body)
	}
	if n := len(calls()); n != gateway.SignInMaxFailures {
		t.Errorf("a throttled attempt reached GoTrue: %d calls, want %d", n, gateway.SignInMaxFailures)
	}
	// Positive pair: another address is not throttled.
	if rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("other@example.com")); rec.Code != http.StatusUnauthorized {
		t.Errorf("another address = %d %s, want 401 from GoTrue", rec.Code, rec.Body)
	}
}

// The store built by handoffHandlers spans requests: a code redeems once, then never again.
func TestHandoffCodeRedeemsOnceAcrossRequests(t *testing.T) {
	authURL, _ := countingGoTrue(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-B","refresh_token":"ref-B"}`))
	})
	mux := handoffMux(t, authURL, true)

	rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("b@example.com"))
	var in struct{ Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &in); rec.Code != http.StatusOK || err != nil || in.Code == "" {
		t.Fatalf("POST /auth/sign-in = %d %s, want 200 with a code", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tok-B") || strings.Contains(rec.Body.String(), "ref-B") {
		t.Errorf("sign-in answered a token, not a code: %s", rec.Body)
	}
	body, _ := json.Marshal(map[string]string{"code": in.Code, "state": handoffState})
	if rec := postJSON(mux, "/auth/exchange", handoffAllowedOrigin, string(body)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tok-B"`) {
		t.Fatalf("first POST /auth/exchange = %d %s, want 200 with tok-B", rec.Code, rec.Body)
	}
	if rec := postJSON(mux, "/auth/exchange", handoffAllowedOrigin, string(body)); rec.Code != http.StatusBadRequest {
		t.Errorf("second POST /auth/exchange = %d %s, want 400", rec.Code, rec.Body)
	}
}

// CORS answers every preflight itself; none reaches the handler or GoTrue. An OPTIONS with no Origin is not a preflight.
func TestHandoffPreflightIsAnsweredByCORS(t *testing.T) {
	authURL, calls := countingGoTrue(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-C"}`))
	})
	mux := handoffMux(t, authURL, true)

	for _, path := range handoffPaths() {
		for _, origin := range []string{handoffAllowedOrigin, "https://evil.example", "null"} {
			// An empty body proves CORS answered; the handler would write a 405 envelope.
			if rec := preflight(mux, path, origin); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Errorf("OPTIONS %s from %q = %d %q, want 204 with no body", path, origin, rec.Code, rec.Body)
			}
		}
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("OPTIONS %s with no Origin = %d Allow %q, want 405 Allow POST from the handler", path, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a preflight reached GoTrue: %v", got)
	}
	// Positive pair: a POST on the same mux does reach GoTrue.
	if rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin, signInJSON("c@example.com")); rec.Code != http.StatusOK {
		t.Errorf("POST /auth/sign-in = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("GoTrue saw %v after one POST, want one call", got)
	}
}
