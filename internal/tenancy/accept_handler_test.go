package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// bodyOf is a valid {"token": ...} body of exactly n bytes.
func bodyOf(n int) string {
	return `{"token":"` + strings.Repeat("A", n-len(`{"token":""}`)) + `"}`
}

func decodeKeys(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", raw, err)
	}
	return m
}

func TestAcceptHandler_Contract(t *testing.T) {
	tenant, subject := Tenant{ID: uuid.NewString(), Name: "Obi Partners", Kind: "firm"}, uuid.NewString()
	token := strings.Repeat("A", 43)
	okBody := `{"token":"` + token + `"}`
	granted := func(context.Context, string, *string) (Tenant, string, string, error) {
		return tenant, subject, "reviewer", nil
	}
	failing := func(err error) AcceptInvitationFunc {
		return func(context.Context, string, *string) (Tenant, string, string, error) { return Tenant{}, "", "", err }
	}

	t.Run("accepted", func(t *testing.T) {
		for name, body := range map[string]string{"typical": okBody, "exactly 1 KiB": bodyOf(1024)} {
			t.Run(name, func(t *testing.T) {
				var calls []string
				accept := func(ctx context.Context, tok string, _ *string) (Tenant, string, string, error) {
					calls = append(calls, tok)
					return granted(ctx, tok, nil)
				}
				// A bare context: the handler must not gate on an identity it cannot see for a tenant-less caller.
				rec := apiDo(AcceptInvitationHandler(accept, nil), context.Background(), http.MethodPost, "/v1/invitations/accept", body)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
				}
				if len(calls) != 1 || calls[0] != strings.TrimSuffix(strings.TrimPrefix(body, `{"token":"`), `"}`) {
					t.Errorf("accept calls = %v, want one with the body's token", calls)
				}
				top := decodeKeys(t, rec.Body.Bytes())
				if got := sortedKeys(top); !slices.Equal(got, []string{"tenant", "user"}) {
					t.Fatalf("body keys = %v, want [tenant user]", got)
				}
				var ten map[string]string
				var usr map[string]string
				if err := json.Unmarshal(top["tenant"], &ten); err != nil {
					t.Fatalf("tenant %s: %v", top["tenant"], err)
				}
				if err := json.Unmarshal(top["user"], &usr); err != nil {
					t.Fatalf("user %s: %v", top["user"], err)
				}
				if len(ten) != 3 || ten["id"] != tenant.ID || ten["name"] != "Obi Partners" || ten["kind"] != "firm" {
					t.Errorf("tenant = %v, want exactly {id, name, kind}", ten)
				}
				if len(usr) != 2 || usr["id"] != subject || usr["role"] != "reviewer" {
					t.Errorf("user = %v, want exactly {id: %s, role: reviewer}", usr, subject)
				}
			})
		}
	})

	for _, c := range []struct {
		name, body string
		accept     AcceptInvitationFunc
		status     int
		msg        string
		called     int
	}{
		// The handler cannot see a tenant-less caller; the store says there is none.
		{"no caller", okBody, failing(db.ErrNoTenant), http.StatusUnauthorized, "unauthorized", 1},
		{"malformed JSON", `{`, granted, http.StatusBadRequest, "invalid request body", 0},
		{"empty body", ``, granted, http.StatusBadRequest, "invalid request body", 0},
		{"over 4 KiB", bodyOf(4097), granted, http.StatusBadRequest, "invalid request body", 0},
		{"invite not valid", okBody, failing(ErrInvitationNotValid), http.StatusNotFound, "this invite is no longer valid", 1},
		{"invite not valid, wrapped", okBody, failing(fmt.Errorf("accept: %w", ErrInvitationNotValid)), http.StatusNotFound, "this invite is no longer valid", 1},
		{"already a member", okBody, failing(ErrAlreadyMember), http.StatusConflict, "you already belong to a workspace", 1},
		{"another address", okBody, failing(ErrInvitationEmailMismatch), http.StatusForbidden, "this invite was sent to a different email address", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			accept := func(ctx context.Context, tok string, _ *string) (Tenant, string, string, error) {
				calls++
				return c.accept(ctx, tok, nil)
			}
			var logs syncBuf
			rec := apiDo(AcceptInvitationHandler(accept, slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, "/v1/invitations/accept", c.body)
			assertErrorBody(t, rec, c.status, c.msg)
			if calls != c.called {
				t.Errorf("accept calls = %d, want %d", calls, c.called)
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a refusal logged an error:\n%s", logs.String())
			}
		})
	}

	t.Run("any other error is a logged 500", func(t *testing.T) {
		var logs syncBuf
		boom := errors.New("pq: connection to 10.0.0.9 refused")
		rec := apiDo(AcceptInvitationHandler(failing(boom), slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, "/v1/invitations/accept", okBody)
		assertErrorBody(t, rec, http.StatusInternalServerError, "internal server error")
		if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
			t.Errorf("ERROR log lines = %d, want 1:\n%s", n, logs.String())
		}
		if strings.Contains(rec.Body.String(), "10.0.0.9") {
			t.Errorf("the body leaks the error text: %s", rec.Body)
		}
	})
}

func TestPreviewHandler_AStateFailureStillAnswersAndLogsTheSQLStateOnly(t *testing.T) {
	token := strings.Repeat("T", 43)
	fn := func(context.Context, string) (InvitationPreview, error) {
		return InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: "tunde@obi.test", Account: "unknown",
			accountErr: &pgconn.PgError{Code: "42501", Message: "permission denied for tunde@obi.test"}}, nil
	}
	var logs syncBuf
	rec := apiDo(InvitationPreviewHandler(fn, slog.New(slog.NewTextHandler(&logs, nil))), context.Background(),
		http.MethodPost, "/internal/invitations/preview", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["account"] != "unknown" || got["email"] != "tunde@obi.test" {
		t.Errorf("body = %v, want the three fields and account unknown", got)
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 1 || !strings.Contains(out, "42501") {
		t.Errorf("want one WARN with 42501, got:\n%s", out)
	}
	for _, leak := range []string{"tunde@obi.test", token} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q:\n%s", leak, out)
		}
	}

	var plain syncBuf
	other := func(context.Context, string) (InvitationPreview, error) {
		return InvitationPreview{Account: "unknown", accountErr: errors.New("connection reset for tunde@obi.test")}, nil
	}
	apiDo(InvitationPreviewHandler(other, slog.New(slog.NewTextHandler(&plain, nil))), context.Background(),
		http.MethodPost, "/internal/invitations/preview", `{"token":"`+token+`"}`)
	if o := plain.String(); !strings.Contains(o, "sqlstate=unknown") || strings.Contains(o, "tunde@obi.test") {
		t.Errorf("a non-SQL error must log sqlstate=unknown and no address:\n%s", o)
	}

	var quiet syncBuf
	ok := func(context.Context, string) (InvitationPreview, error) {
		return InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: "tunde@obi.test", Account: "none"}, nil
	}
	apiDo(InvitationPreviewHandler(ok, slog.New(slog.NewTextHandler(&quiet, nil))), context.Background(),
		http.MethodPost, "/internal/invitations/preview", `{"token":"`+token+`"}`)
	if strings.Contains(quiet.String(), "level=WARN") {
		t.Errorf("a read that succeeded logged a WARN:\n%s", quiet.String())
	}
}

func TestPreviewHandler_Contract(t *testing.T) {
	token := strings.Repeat("A", 43)
	okBody := `{"token":"` + token + `"}`
	preview := InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: inviteeAddr, Account: "confirmed"}
	granted := func(context.Context, string) (InvitationPreview, error) { return preview, nil }
	failing := func(err error) InvitationPreviewFunc {
		return func(context.Context, string) (InvitationPreview, error) { return InvitationPreview{}, err }
	}

	t.Run("live invite", func(t *testing.T) {
		var calls []string
		fn := func(ctx context.Context, tok string) (InvitationPreview, error) {
			calls = append(calls, tok)
			return granted(ctx, tok)
		}
		rec := apiDo(InvitationPreviewHandler(fn, nil), context.Background(), http.MethodPost, "/internal/invitations/preview", okBody)
		if full := apiDo(InvitationPreviewHandler(granted, nil), context.Background(), http.MethodPost, "/internal/invitations/preview", bodyOf(1024)); full.Code != http.StatusOK {
			t.Errorf("a body of exactly 1 KiB: status = %d, want 200: %s", full.Code, full.Body)
		}
		if len(calls) != 1 || calls[0] != token {
			t.Errorf("preview calls = %v, want one with the body's token", calls)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
		}
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body %q: %v", rec.Body, err)
		}
		if len(got) != 4 || got["workspace"] != "Obi Partners" || got["role"] != "reviewer" || got["email"] != inviteeAddr || got["account"] != "confirmed" {
			t.Errorf("body = %v, want exactly {workspace, role, email, account}", got)
		}
	})

	for _, c := range []struct {
		name, body string
		preview    InvitationPreviewFunc
		status     int
		msg        string
		called     int
	}{
		{"not valid", okBody, failing(ErrInvitationNotValid), http.StatusNotFound, "this invite is no longer valid", 1},
		{"malformed JSON", `{`, granted, http.StatusBadRequest, "invalid request body", 0},
		{"over 1 KiB", bodyOf(1025), granted, http.StatusBadRequest, "invalid request body", 0},
		{"other error", okBody, failing(errors.New("boom")), http.StatusInternalServerError, "internal server error", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fn := func(ctx context.Context, tok string) (InvitationPreview, error) {
				calls++
				return c.preview(ctx, tok)
			}
			rec := apiDo(InvitationPreviewHandler(fn, nil), context.Background(), http.MethodPost, "/internal/invitations/preview", c.body)
			assertErrorBody(t, rec, c.status, c.msg)
			if calls != c.called {
				t.Errorf("preview calls = %d, want %d", calls, c.called)
			}
		})
	}

	t.Run("any other error is a logged 500", func(t *testing.T) {
		var logs syncBuf
		rec := apiDo(InvitationPreviewHandler(failing(errors.New("boom")), slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, "/internal/invitations/preview", okBody)
		assertErrorBody(t, rec, http.StatusInternalServerError, "internal server error")
		if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
			t.Errorf("ERROR log lines = %d, want 1:\n%s", n, logs.String())
		}
	})

	t.Run("identity changes nothing", func(t *testing.T) {
		id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString(), Email: "x@y.test"}
		for name, ctx := range map[string]context.Context{
			"no caller":          context.Background(),
			"tenant-bearing":     auth.WithIdentity(context.Background(), id),
			"tenant-less caller": auth.WithTenantlessCaller(context.Background(), id),
		} {
			t.Run(name, func(t *testing.T) {
				rec := apiDo(InvitationPreviewHandler(granted, nil), ctx, http.MethodPost, "/internal/invitations/preview", okBody)
				var ok map[string]string
				_ = json.Unmarshal(rec.Body.Bytes(), &ok)
				if rec.Code != http.StatusOK || ok["workspace"] != "Obi Partners" {
					t.Errorf("live invite: %d %s, want 200 with the workspace", rec.Code, rec.Body)
				}
				assertErrorBody(t, apiDo(InvitationPreviewHandler(failing(ErrInvitationNotValid), nil), ctx, http.MethodPost, "/internal/invitations/preview", okBody),
					http.StatusNotFound, "this invite is no longer valid")
			})
		}
	})
}

// acceptRoute is one accept handler behind a spy that records the name it is handed.
type acceptRoute struct {
	name string
	path string
	// body wraps raw JSON members (without the token) into a request body.
	body func(members string) string
	// serve mounts the handler over a spy.
	serve func(spy *nameSpy, log *slog.Logger) http.Handler
}

type nameSpy struct {
	names []*string
	err   error
}

func (s *nameSpy) accept(_ context.Context, _ string, name *string) (Tenant, string, string, error) {
	s.names = append(s.names, name)
	if s.err != nil {
		return Tenant{}, "", "", s.err
	}
	return Tenant{ID: uuid.NewString(), Name: "Obi Partners", Kind: "firm"}, uuid.NewString(), "reviewer", nil
}

func acceptRoutes() []acceptRoute {
	token := strings.Repeat("A", 43)
	return []acceptRoute{
		{
			name: "token",
			path: "/v1/invitations/accept",
			body: func(members string) string {
				if members == "" {
					return `{"token":"` + token + `"}`
				}
				return `{"token":"` + token + `",` + members + `}`
			},
			serve: func(spy *nameSpy, log *slog.Logger) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("POST /v1/invitations/accept", AcceptInvitationHandler(spy.accept, log))
				return mux
			},
		},
		{
			name: "by id",
			path: "/v1/invitations/" + uuid.NewString() + "/accept",
			body: func(members string) string { return `{` + members + `}` },
			serve: func(spy *nameSpy, log *slog.Logger) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("POST /v1/invitations/{id}/accept", AcceptInvitationByIDHandler(spy.accept, log))
				return mux
			},
		},
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// gotName reads the one name a spy was handed: "<nil>" for no name.
func gotName(t *testing.T, spy *nameSpy) string {
	t.Helper()
	if len(spy.names) != 1 {
		t.Fatalf("accept calls = %d, want 1", len(spy.names))
	}
	return orNil(spy.names[0])
}

func TestAcceptInvitationHandler_PassesTheName(t *testing.T) {
	route := acceptRoutes()[0]
	for _, c := range []struct{ name, members, want string }{
		{"padded name is trimmed", `"display_name":" Ada Obi "`, "v:Ada Obi"},
		{"unicode whitespace is trimmed", `"display_name":"　Ada Obi\n"`, "v:Ada Obi"},
		{"BOM padding is trimmed", `"display_name":"\ufeffAda\ufeff"`, "v:Ada"},
		{"absent name is nil", ``, "<nil>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &nameSpy{}
			rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, route.body(c.members))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			if got := gotName(t, spy); got != c.want {
				t.Errorf("accept got name %q, want %q", got, c.want)
			}
		})
	}
}

func TestAcceptHandlers_RefuseABadName(t *testing.T) {
	const (
		lengthSentence = "display_name must be 1 to 200 characters"
		nulSentence    = "display_name must not contain a NUL byte"
	)
	for _, route := range acceptRoutes() {
		t.Run(route.name, func(t *testing.T) {
			for _, c := range []struct{ name, value, msg string }{
				{"blank", jsonString(t, "   "), lengthSentence},
				{"empty", `""`, lengthSentence},
				{"BOM only", jsonString(t, "\ufeff"), lengthSentence},
				{"BOM and spaces only", jsonString(t, " \ufeff "), lengthSentence},
				{"201 runes", jsonString(t, strings.Repeat("é", 201)), lengthSentence},
				{"NUL byte", jsonString(t, "a\x00b"), nulSentence},
				{"a lone NUL survives the trim", jsonString(t, " \x00 "), nulSentence},
				{"tab, no-break space and newline only", jsonString(t, "\t\u00a0\n"), lengthSentence},
				{"201 four-byte runes", jsonString(t, strings.Repeat("😀", 201)), lengthSentence},
				{"not a string", `5`, "invalid request body"},
			} {
				t.Run(c.name, func(t *testing.T) {
					spy := &nameSpy{}
					rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, route.body(`"display_name":`+c.value))
					assertErrorBody(t, rec, http.StatusBadRequest, c.msg)
					if len(spy.names) != 0 {
						t.Errorf("accept called %d time(s), want 0", len(spy.names))
					}
				})
			}
			// Control: a valid name on the same route reaches the accept.
			spy := &nameSpy{}
			rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, route.body(`"display_name":"Ada Obi"`))
			if rec.Code != http.StatusOK || len(spy.names) != 1 {
				t.Errorf("valid name: status %d, accept calls %d; want 200 and 1", rec.Code, len(spy.names))
			}
		})
	}
}

func TestAcceptHandlers_AcceptA200RuneName(t *testing.T) {
	four := strings.Repeat("😀", 200)
	escaped := strings.Repeat(`😀`, 200) // 2400 bytes on the wire: past the 1 KiB preview cap
	for _, route := range acceptRoutes() {
		t.Run(route.name, func(t *testing.T) {
			for _, c := range []struct{ name, members, want string }{
				{"200 four-byte runes", `"display_name":` + jsonString(t, four), "v:" + four},
				{"200 escaped surrogate pairs", `"display_name":"` + escaped + `"`, "v:" + four},
				{"one rune", `"display_name":"a"`, "v:a"},
				{"200 one-byte runes", `"display_name":` + jsonString(t, strings.Repeat("a", 200)), "v:" + strings.Repeat("a", 200)},
				{"null is no name", `"display_name":null`, "<nil>"},
			} {
				t.Run(c.name, func(t *testing.T) {
					spy := &nameSpy{}
					rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, route.body(c.members))
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
					}
					if got := gotName(t, spy); got != c.want {
						t.Errorf("accept got a %d-rune name, want the 200 sent", utf8.RuneCountInString(strings.TrimPrefix(got, "v:")))
					}
				})
			}

			// The 4 KiB cap itself: a body of exactly 4096 bytes passes, one byte more is refused.
			sized := func(n int) string {
				base := len(route.body(`"display_name":"Ada"`))
				return route.body(`"display_name":"Ada` + strings.Repeat(" ", n-base) + `"`)
			}
			if got := len(sized(4096)); got != 4096 {
				t.Fatalf("fixture: sized body = %d bytes, want 4096", got)
			}
			spy := &nameSpy{}
			rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, sized(4096))
			if got := gotName(t, spy); rec.Code != http.StatusOK || got != "v:Ada" {
				t.Errorf("4096-byte body: status %d, name %q; want 200 and Ada", rec.Code, got)
			}
			spy = &nameSpy{}
			rec = apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, sized(4097))
			assertErrorBody(t, rec, http.StatusBadRequest, "invalid request body")
			if len(spy.names) != 0 {
				t.Errorf("accept called %d time(s) for an over-cap body, want 0", len(spy.names))
			}
		})
	}
}

func TestAcceptHandlers_500LogsNoName(t *testing.T) {
	const name = "Ada Obi"
	for _, route := range acceptRoutes() {
		t.Run(route.name, func(t *testing.T) {
			spy := &nameSpy{err: errors.New("pq: connection refused")}
			var logs syncBuf
			rec := apiDo(route.serve(spy, slog.New(slog.NewTextHandler(&logs, nil))), context.Background(), http.MethodPost, route.path,
				route.body(`"display_name":"`+name+`"`))
			assertErrorBody(t, rec, http.StatusInternalServerError, "internal server error")
			// The name reached the accept, so its absence from the log is not an unread field.
			if got := gotName(t, spy); got != "v:"+name {
				t.Fatalf("accept got name %q, want the one sent", got)
			}
			if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
				t.Errorf("ERROR log lines = %d, want 1:\n%s", n, logs.String())
			}
			if strings.Contains(logs.String(), name) || strings.Contains(rec.Body.String(), name) {
				t.Errorf("the log or the body carries the display name:\n%s\n%s", logs.String(), rec.Body)
			}
		})
	}
}
