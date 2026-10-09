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

	for _, c := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"blank address", `{"email":"  "}`},
		{"not json", `not json`},
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
