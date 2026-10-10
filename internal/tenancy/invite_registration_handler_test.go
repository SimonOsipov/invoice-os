package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

const regToken43 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// emailBodyOf is a valid {"email": ...} body of exactly n bytes.
func emailBodyOf(n int) string {
	return `{"email":"` + strings.Repeat("a", n-len(`{"email":""}`)) + `"}`
}

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", rec.Body, err)
	}
	return m
}

func TestInvitationPendingHandler_Contract(t *testing.T) {
	const path = "/internal/invitations/pending"
	answer := func(v bool) func(context.Context, string) (bool, error) {
		return func(context.Context, string) (bool, error) { return v, nil }
	}

	for _, want := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending %v", want), func(t *testing.T) {
			var calls []string
			fn := func(ctx context.Context, email string) (bool, error) {
				calls = append(calls, email)
				return answer(want)(ctx, email)
			}
			rec := apiDo(InvitationPendingHandler(fn, nil), context.Background(), http.MethodPost, path, `{"email":"a@x.test"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			m := decodeObject(t, rec)
			if !slices.Equal(keysOf(m), []string{"pending"}) || m["pending"] != want {
				t.Errorf("body = %s, want exactly {\"pending\":%v}", rec.Body, want)
			}
			if len(calls) != 1 || calls[0] != "a@x.test" {
				t.Errorf("lookup calls = %v, want one with the body's address", calls)
			}
		})
	}

	t.Run("exactly 1 KiB", func(t *testing.T) {
		rec := apiDo(InvitationPendingHandler(answer(true), nil), context.Background(), http.MethodPost, path, emailBodyOf(1024))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown fields are ignored", func(t *testing.T) {
		var got []string
		fn := func(_ context.Context, email string) (bool, error) { got = append(got, email); return true, nil }
		rec := apiDo(InvitationPendingHandler(fn, nil), context.Background(), http.MethodPost, path, `{"email":"a@x.test","password":"hunter2","extra":{"n":1}}`)
		if rec.Code != http.StatusOK || !slices.Equal(got, []string{"a@x.test"}) {
			t.Errorf("status = %d, lookups = %v, want 200 and one lookup of a@x.test: %s", rec.Code, got, rec.Body)
		}
	})

	for _, c := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"blank address", `{"email":"  "}`},
		{"not json", `not json`},
		{"empty body", ``},
		{"truncated object", `{"email":"a@x.test"`},
		{"email of the wrong type", `{"email":5}`},
		{"array body", `["a@x.test"]`},
		{"null body", `null`},
		{"over 1 KiB", emailBodyOf(1025)},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fn := func(context.Context, string) (bool, error) { calls++; return true, nil }
			assertErrorBody(t, apiDo(InvitationPendingHandler(fn, nil), context.Background(), http.MethodPost, path, c.body),
				http.StatusBadRequest, "invalid request body")
			if calls != 0 {
				t.Errorf("lookup calls = %d, want 0", calls)
			}
		})
	}

	t.Run("lookup error is a logged opaque 500", func(t *testing.T) {
		var logs syncBuf
		fn := func(context.Context, string) (bool, error) { return false, errors.New("boom") }
		rec := apiDo(InvitationPendingHandler(fn, slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, path, `{"email":"a@x.test"}`)
		assertErrorBody(t, rec, http.StatusInternalServerError, "internal server error")
		if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
			t.Errorf("ERROR log lines = %d, want 1:\n%s", n, logs.String())
		}
	})
}

func TestInvitationRegisterClaimHandler_Contract(t *testing.T) {
	const path = "/internal/invitations/register"
	okBody := `{"token":"` + regToken43 + `"}`
	claimer := func(email string, first bool, err error) func(context.Context, string) (string, bool, error) {
		return func(context.Context, string) (string, bool, error) { return email, first, err }
	}

	for _, first := range []bool{true, false} {
		t.Run(fmt.Sprintf("first %v", first), func(t *testing.T) {
			var calls []string
			fn := func(ctx context.Context, tok string) (string, bool, error) {
				calls = append(calls, tok)
				return claimer("a@x.test", first, nil)(ctx, tok)
			}
			rec := apiDo(InvitationRegisterClaimHandler(fn, nil), context.Background(), http.MethodPost, path, okBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			m := decodeObject(t, rec)
			if !slices.Equal(keysOf(m), []string{"email", "first"}) || m["email"] != "a@x.test" || m["first"] != first {
				t.Errorf("body = %s, want exactly {\"email\":\"a@x.test\",\"first\":%v}", rec.Body, first)
			}
			if len(calls) != 1 || calls[0] != regToken43 {
				t.Errorf("claim calls = %v, want one with the body's token", calls)
			}
		})
	}

	t.Run("exactly 1 KiB", func(t *testing.T) {
		rec := apiDo(InvitationRegisterClaimHandler(claimer("a@x.test", true, nil), nil), context.Background(), http.MethodPost, path, bodyOf(1024))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown fields are ignored", func(t *testing.T) {
		var got []string
		fn := func(_ context.Context, tok string) (string, bool, error) {
			got = append(got, tok)
			return "a@x.test", true, nil
		}
		rec := apiDo(InvitationRegisterClaimHandler(fn, nil), context.Background(), http.MethodPost, path, `{"token":"`+regToken43+`","password":"hunter2"}`)
		if rec.Code != http.StatusOK || !slices.Equal(got, []string{regToken43}) {
			t.Errorf("status = %d, claims = %v, want 200 and one claim of the token: %s", rec.Code, got, rec.Body)
		}
	})

	for _, c := range []struct {
		name, body string
		err        error
		status     int
		msg        string
		called     int
	}{
		{"not valid", okBody, ErrInvitationNotValid, http.StatusNotFound, "this invite is no longer valid", 1},
		{"not valid, wrapped", okBody, fmt.Errorf("claim: %w", ErrInvitationNotValid), http.StatusNotFound, "this invite is no longer valid", 1},
		{"malformed JSON", `{`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"empty body", ``, nil, http.StatusBadRequest, "invalid request body", 0},
		{"token of the wrong type", `{"token":5}`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"array body", `["x"]`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"over 1 KiB", bodyOf(1025), nil, http.StatusBadRequest, "invalid request body", 0},
		{"other error", okBody, errors.New("boom"), http.StatusInternalServerError, "internal server error", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fn := func(ctx context.Context, tok string) (string, bool, error) {
				calls++
				return claimer("", false, c.err)(ctx, tok)
			}
			rec := apiDo(InvitationRegisterClaimHandler(fn, nil), context.Background(), http.MethodPost, path, c.body)
			assertErrorBody(t, rec, c.status, c.msg)
			if calls != c.called {
				t.Errorf("claim calls = %d, want %d", calls, c.called)
			}
		})
	}

	t.Run("other error is logged once", func(t *testing.T) {
		var logs syncBuf
		fn := claimer("", false, errors.New("boom"))
		apiDo(InvitationRegisterClaimHandler(fn, slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, path, okBody)
		if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
			t.Errorf("ERROR log lines = %d, want 1:\n%s", n, logs.String())
		}
	})
}

func TestInvitationRegisterReleaseHandler_Contract(t *testing.T) {
	const path = "/internal/invitations/release"
	okBody := `{"token":"` + regToken43 + `"}`
	releaser := func(err error) func(context.Context, string) error {
		return func(context.Context, string) error { return err }
	}

	t.Run("released", func(t *testing.T) {
		var calls []string
		fn := func(ctx context.Context, tok string) error {
			calls = append(calls, tok)
			return nil
		}
		rec := apiDo(InvitationRegisterReleaseHandler(fn, nil), context.Background(), http.MethodPost, path, okBody)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body)
		}
		if len(calls) != 1 || calls[0] != regToken43 {
			t.Errorf("release calls = %v, want one with the body's token", calls)
		}
	})

	t.Run("exactly 1 KiB", func(t *testing.T) {
		rec := apiDo(InvitationRegisterReleaseHandler(releaser(nil), nil), context.Background(), http.MethodPost, path, bodyOf(1024))
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204: %s", rec.Code, rec.Body)
		}
	})

	for _, c := range []struct {
		name, body string
		err        error
		status     int
		msg        string
		called     int
	}{
		{"not valid", okBody, ErrInvitationNotValid, http.StatusNotFound, "this invite is no longer valid", 1},
		{"not valid, wrapped", okBody, fmt.Errorf("release: %w", ErrInvitationNotValid), http.StatusNotFound, "this invite is no longer valid", 1},
		{"malformed JSON", `{`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"empty body", ``, nil, http.StatusBadRequest, "invalid request body", 0},
		{"token of the wrong type", `{"token":5}`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"array body", `["x"]`, nil, http.StatusBadRequest, "invalid request body", 0},
		{"over 1 KiB", bodyOf(1025), nil, http.StatusBadRequest, "invalid request body", 0},
		{"other error", okBody, errors.New("boom"), http.StatusInternalServerError, "internal server error", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fn := func(ctx context.Context, tok string) error {
				calls++
				return releaser(c.err)(ctx, tok)
			}
			rec := apiDo(InvitationRegisterReleaseHandler(fn, nil), context.Background(), http.MethodPost, path, c.body)
			assertErrorBody(t, rec, c.status, c.msg)
			if calls != c.called {
				t.Errorf("release calls = %d, want %d", calls, c.called)
			}
		})
	}
}

func TestInviteRegistrationHandlers_LogsCarryNoAddressOrToken(t *testing.T) {
	const addr = "secret.person@leaky.test"
	boom := errors.New("store down")
	handlers := []struct {
		name, path, body string
		h                func(*slog.Logger, error) http.HandlerFunc
	}{
		{"pending", "/internal/invitations/pending", `{"email":"` + addr + `"}`, func(l *slog.Logger, err error) http.HandlerFunc {
			return InvitationPendingHandler(func(context.Context, string) (bool, error) { return false, err }, l)
		}},
		{"register", "/internal/invitations/register", `{"token":"` + regToken43 + `"}`, func(l *slog.Logger, err error) http.HandlerFunc {
			return InvitationRegisterClaimHandler(func(context.Context, string) (string, bool, error) { return addr, false, err }, l)
		}},
		{"release", "/internal/invitations/release", `{"token":"` + regToken43 + `"}`, func(l *slog.Logger, err error) http.HandlerFunc {
			return InvitationRegisterReleaseHandler(func(context.Context, string) error { return err }, l)
		}},
	}
	for _, c := range handlers {
		for name, err := range map[string]error{"store failure": boom, "invite not valid": ErrInvitationNotValid} {
			t.Run(c.name+"/"+name, func(t *testing.T) {
				var logs syncBuf
				rec := apiDo(c.h(slog.New(slog.NewTextHandler(&logs, nil)), err), context.Background(), http.MethodPost, c.path, c.body)
				if rec.Code < 400 {
					t.Fatalf("status = %d, want a refusal: %s", rec.Code, rec.Body)
				}
				if err == boom && !strings.Contains(logs.String(), "level=ERROR") {
					t.Error("a store failure logged no error, so the absence checks below prove nothing")
				}
				for _, secret := range []string{addr, regToken43} {
					if strings.Contains(logs.String(), secret) {
						t.Errorf("the log carries %q:\n%s", secret, logs.String())
					}
				}
			})
		}
	}
}

// The gateway keys on the exact 404 body, so these run the three handlers over the real store.
func TestInvitationRegistrationHandlers_OverTheStore(t *testing.T) {
	r := newRegInviter(t)
	addr := uniqueAddr("wire")
	_, token := r.invite(t, addr)
	accID, accepted := r.invite(t, uniqueAddr("wire-acc"))
	r.setState(t, accID, `status = 'accepted'`)
	unknown, _, err := mintToken()
	if err != nil {
		t.Fatal(err)
	}
	pending := InvitationPendingHandler(r.store.InvitationPendingForEmail, nil)
	register := InvitationRegisterClaimHandler(r.store.ClaimInvitationRegistration, nil)
	release := InvitationRegisterReleaseHandler(r.store.ReleaseInvitationRegistration, nil)
	post := func(h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
		return apiDo(h, context.Background(), http.MethodPost, path, body)
	}
	tokBody := func(tok string) string { return `{"token":"` + tok + `"}` }
	wantClaim := func(first bool) {
		t.Helper()
		rec := post(register, "/internal/invitations/register", tokBody(token))
		m := decodeObject(t, rec)
		if rec.Code != http.StatusOK || !slices.Equal(keysOf(m), []string{"email", "first"}) || m["email"] != addr || m["first"] != first {
			t.Errorf("register = %d %s, want 200 {email:%q, first:%v}", rec.Code, rec.Body, addr, first)
		}
	}

	if rec := post(pending, "/internal/invitations/pending", `{"email":"`+strings.ToUpper(addr)+`"}`); rec.Code != http.StatusOK || decodeObject(t, rec)["pending"] != true {
		t.Errorf("pending of the invited address = %d %s, want 200 {pending:true}", rec.Code, rec.Body)
	}
	if rec := post(pending, "/internal/invitations/pending", `{"email":"`+uniqueAddr("nobody")+`"}`); rec.Code != http.StatusOK || decodeObject(t, rec)["pending"] != false {
		t.Errorf("pending of an uninvited address = %d %s, want 200 {pending:false}", rec.Code, rec.Body)
	}

	wantClaim(true)
	wantClaim(false)
	rec := post(release, "/internal/invitations/release", tokBody(token))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("release = %d %q, want 204 and an empty body", rec.Code, rec.Body)
	}
	wantClaim(true)

	for _, c := range []struct{ name, body string }{
		{"unknown token", tokBody(unknown)},
		{"accepted token", tokBody(accepted)},
		{"42 characters", tokBody(unknown[:42])},
		{"empty token", tokBody("")},
		{"no token key", `{}`},
		{"null body", `null`},
	} {
		for _, route := range []struct {
			name string
			h    http.HandlerFunc
		}{{"register", register}, {"release", release}} {
			t.Run(route.name+"/"+c.name, func(t *testing.T) {
				rec := post(route.h, "/internal/invitations/"+route.name, c.body)
				assertErrorBody(t, rec, http.StatusNotFound, "this invite is no longer valid")
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
			})
		}
	}
}
