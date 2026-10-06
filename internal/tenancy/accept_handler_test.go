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

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Messages are the Design -> API contracts texts; statusForErr holds the literals once the sentinels map.
const (
	msgInviteNotValid = "this invite is no longer valid"
	msgAlreadyMember  = "you already belong to a workspace"
	msgWrongAddress   = "this invite was sent to a different email address"
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
	granted := func(context.Context, string) (Tenant, string, string, error) { return tenant, subject, "reviewer", nil }
	failing := func(err error) AcceptInvitationFunc {
		return func(context.Context, string) (Tenant, string, string, error) { return Tenant{}, "", "", err }
	}

	t.Run("accepted", func(t *testing.T) {
		for name, body := range map[string]string{"typical": okBody, "exactly 1 KiB": bodyOf(1024)} {
			t.Run(name, func(t *testing.T) {
				var calls []string
				accept := func(ctx context.Context, tok string) (Tenant, string, string, error) {
					calls = append(calls, tok)
					return granted(ctx, tok)
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
		{"over 1 KiB", bodyOf(1025), granted, http.StatusBadRequest, "invalid request body", 0},
		{"invite not valid", okBody, failing(ErrInvitationNotValid), http.StatusNotFound, msgInviteNotValid, 1},
		{"invite not valid, wrapped", okBody, failing(fmt.Errorf("accept: %w", ErrInvitationNotValid)), http.StatusNotFound, msgInviteNotValid, 1},
		{"already a member", okBody, failing(ErrAlreadyMember), http.StatusConflict, msgAlreadyMember, 1},
		{"another address", okBody, failing(ErrInvitationEmailMismatch), http.StatusForbidden, msgWrongAddress, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			accept := func(ctx context.Context, tok string) (Tenant, string, string, error) {
				calls++
				return c.accept(ctx, tok)
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

func TestPreviewHandler_Contract(t *testing.T) {
	token := strings.Repeat("A", 43)
	okBody := `{"token":"` + token + `"}`
	preview := InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: inviteeAddr}
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
		if len(got) != 3 || got["workspace"] != "Obi Partners" || got["role"] != "reviewer" || got["email"] != inviteeAddr {
			t.Errorf("body = %v, want exactly {workspace, role, email}", got)
		}
	})

	for _, c := range []struct {
		name, body string
		preview    InvitationPreviewFunc
		status     int
		msg        string
		called     int
	}{
		{"not valid", okBody, failing(ErrInvitationNotValid), http.StatusNotFound, msgInviteNotValid, 1},
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
					http.StatusNotFound, msgInviteNotValid)
			})
		}
	})
}
