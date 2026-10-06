package tenancy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	maxInviteBodyBytes = 16 * 1024
	maxInviteEmails    = 20
	maxEmailBytes      = 254
)

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// InviteResult is one create or resend response item.
type InviteResult struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	Delivery  string    `json:"delivery"`
}

// Inviter writes invites through the store, then mails and records each outcome.
type Inviter struct {
	Store  *Store
	Sender accountmail.Sender
	Logger *slog.Logger
}

// Invite issues the invites. A mail failure yields delivery "failed", not an error.
func (i *Inviter) Invite(ctx context.Context, emails []string, role string) ([]InviteResult, error) {
	issued, err := i.Store.CreateInvitations(ctx, emails, role)
	if err != nil {
		return nil, err
	}
	return i.deliver(ctx, issued), nil
}

// Resend issues a new token for one pending invite and mails it.
func (i *Inviter) Resend(ctx context.Context, id string) (InviteResult, error) {
	inv, err := i.Store.ResendInvitation(ctx, id)
	if err != nil {
		return InviteResult{}, err
	}
	return i.deliver(ctx, []IssuedInvite{inv})[0], nil
}

// deliver runs on a context that outlives the client, so a send after the commit is always recorded.
// A failed outcome write is logged and the answer still reports the send, so the admin is not told to retry a sent mail.
func (i *Inviter) deliver(ctx context.Context, issued []IssuedInvite) []InviteResult {
	ctx2 := context.WithoutCancel(ctx)
	sent := make([]bool, len(issued))
	var msgs []accountmail.Message
	var idx []int
	var firstErr error
	failed := 0
	for n, inv := range issued {
		subject, html, err := accountmail.RenderInvite(accountmail.InviteMail{
			Workspace: inv.Workspace, Inviter: inv.Inviter, Email: inv.Email, Role: inv.Role, Token: inv.Token,
		})
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		msgs = append(msgs, accountmail.Message{To: inv.Email, Subject: subject, HTML: html})
		idx = append(idx, n)
	}
	if len(msgs) > 0 {
		if err := i.Sender.Send(ctx2, msgs); err != nil {
			failed += len(msgs)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, n := range idx {
				sent[n] = true
			}
		}
	}
	if firstErr != nil {
		i.Logger.ErrorContext(ctx2, "tenancy: invite mail failed", slog.Int("count", failed), slog.Any("err", firstErr))
		platform.CaptureError(ctx2, firstErr)
	}

	out := make([]InviteResult, len(issued))
	for n, inv := range issued {
		if err := i.Store.RecordInviteDelivery(ctx2, inv.ID, inv.TokenHash, sent[n]); err != nil {
			i.Logger.ErrorContext(ctx2, "tenancy: invite delivery not recorded", slog.Any("err", err))
		}
		delivery := "failed"
		if sent[n] {
			delivery = "sent"
		}
		out[n] = InviteResult{ID: inv.ID, Email: inv.Email, Role: inv.Role, Status: "pending", ExpiresAt: inv.ExpiresAt, Delivery: delivery}
	}
	return out
}

// InviteFunc is Inviter.Invite; the handler takes the function so its contract tests without a database.
type InviteFunc func(ctx context.Context, emails []string, role string) ([]InviteResult, error)

// InvitationsLister is Store.ListInvitations.
type InvitationsLister func(ctx context.Context) ([]Invitation, error)

// InviteResender is Inviter.Resend.
type InviteResender func(ctx context.Context, id string) (InviteResult, error)

type inviteRequest struct {
	Emails []string `json:"emails"`
	Role   string   `json:"role"`
}

// normaliseEmail returns the trimmed, lower-cased address and whether it is acceptable.
func normaliseEmail(raw string) (string, bool) {
	a := strings.ToLower(strings.TrimSpace(raw))
	if len(a) > maxEmailBytes || !emailRE.MatchString(a) {
		return "", false
	}
	for _, r := range a {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Name != "" || p.Address != a {
		return "", false
	}
	return a, true
}

func invitationFailure(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	status, msg := statusForErr(err)
	if status == http.StatusInternalServerError {
		log.ErrorContext(r.Context(), op, slog.Any("err", err))
	}
	writeError(w, status, msg)
}

// InvitationsCreateHandler returns POST /v1/invitations.
func InvitationsCreateHandler(invite InviteFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxInviteBodyBytes)
		var req inviteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Role != "admin" && req.Role != "preparer" && req.Role != "reviewer" {
			writeError(w, http.StatusBadRequest, `role must be "admin", "preparer" or "reviewer"`)
			return
		}
		var emails, bad []string
		seen := map[string]bool{}
		for _, raw := range req.Emails {
			a, ok := normaliseEmail(raw)
			switch {
			case !ok:
				bad = append(bad, fmt.Sprintf("%q", raw))
			case !seen[a]:
				seen[a] = true
				emails = append(emails, a)
			}
		}
		if len(bad) > 0 {
			writeError(w, http.StatusBadRequest, "invalid email address: "+strings.Join(bad, ", "))
			return
		}
		if len(emails) < 1 || len(emails) > maxInviteEmails {
			writeError(w, http.StatusBadRequest, "emails must hold 1 to 20 addresses")
			return
		}
		items, err := invite(r.Context(), emails, req.Role)
		if err != nil {
			invitationFailure(w, r, log, "tenancy: create invitations", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]InviteResult{"invitations": items})
	}
}

// InvitationsListHandler returns GET /v1/invitations.
func InvitationsListHandler(list InvitationsLister, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		items, err := list(r.Context())
		if err != nil {
			invitationFailure(w, r, log, "tenancy: list invitations", err)
			return
		}
		if items == nil {
			items = []Invitation{}
		}
		writeJSON(w, http.StatusOK, map[string][]Invitation{"invitations": items})
	}
}

// InvitationResendHandler returns POST /v1/invitations/{id}/resend.
func InvitationResendHandler(resend InviteResender, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "invitation not found")
			return
		}
		item, err := resend(r.Context(), id.String())
		if err != nil {
			invitationFailure(w, r, log, "tenancy: resend invitation", err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
