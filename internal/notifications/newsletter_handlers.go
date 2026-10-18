package notifications

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// SubscriptionStore is what the newsletter handlers need; *NewsletterStore satisfies it.
type SubscriptionStore interface {
	Subscribe(ctx context.Context, in SubscribeIntake) (token string, topicNames []string, err error)
	Confirm(ctx context.Context, tokenHash []byte) (email string, err error)
}

var _ SubscriptionStore = (*NewsletterStore)(nil)

// Newsletter is what both routes share. Enabled is false when the newsletter or the mail sender is off.
type Newsletter struct {
	Store   SubscriptionStore
	Sender  accountmail.Sender
	Resend  BlogResendClient
	Enabled bool
}

const lookupTimeout = 3 * time.Second

var categoryIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)

type subscribeBody struct {
	Email       string   `json:"email"`
	CategoryIDs []string `json:"category_ids"`
	Frequency   string   `json:"frequency"`
}

func newsletterUnavailable(w http.ResponseWriter) {
	writeJSONBody(w, http.StatusServiceUnavailable, map[string]string{"error": "subscriptions are unavailable"})
}

// validateSubscribe repeats the gateway rules and returns the 400 message, or "".
func validateSubscribe(b *subscribeBody) string {
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if n := len(b.Email); n < 3 || n > 254 || strings.Count(b.Email, "@") != 1 ||
		strings.IndexFunc(b.Email, unicode.IsSpace) >= 0 || strings.ContainsRune(b.Email, 0) {
		return "email is invalid"
	}
	if n := len(b.CategoryIDs); n < 1 || n > 50 {
		return "category_ids must have 1 to 50 entries"
	}
	for _, id := range b.CategoryIDs {
		if !categoryIDPattern.MatchString(id) {
			return "category_ids is invalid"
		}
	}
	slices.Sort(b.CategoryIDs)
	b.CategoryIDs = slices.Compact(b.CategoryIDs)
	if !slices.Contains([]string{"weekly", "every_post", "monthly"}, b.Frequency) {
		return "frequency is invalid"
	}
	return ""
}

// SubscribeHandler answers 202 for every valid request, subscribed or not. Neither the email nor
// the token reaches a log line.
func SubscribeHandler(n Newsletter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !n.Enabled {
			newsletterUnavailable(w)
			return
		}
		var b subscribeBody
		if !decodeIntake(w, r, &b) {
			return
		}
		if msg := validateSubscribe(&b); msg != "" {
			writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		token, names, err := n.Store.Subscribe(r.Context(), SubscribeIntake{Email: b.Email, CategoryIDs: b.CategoryIDs, Frequency: b.Frequency})
		if errors.Is(err, ErrUnknownCategory) {
			writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "unknown category"})
			return
		}
		if err != nil {
			log.ErrorContext(r.Context(), "blog: subscribe failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "subscribe failed"})
			return
		}
		subject, html, err := accountmail.RenderBlogConfirm(accountmail.BlogConfirmMail{Email: b.Email, Token: token, Topics: names, Frequency: b.Frequency})
		if err != nil {
			log.ErrorContext(r.Context(), "blog: confirmation mail render failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "subscribe failed"})
			return
		}
		// The request row is committed; a client that hangs up must not drop the mail.
		err = n.Sender.Send(context.WithoutCancel(r.Context()), []accountmail.Message{{To: b.Email, Subject: subject, HTML: html}})
		if err != nil {
			var se *accountmail.SendError
			status := 0
			if errors.As(err, &se) {
				status = se.Status
			}
			log.ErrorContext(r.Context(), "blog: confirmation mail failed", slog.Int("status", status))
			writeJSONBody(w, http.StatusBadGateway, map[string]string{"error": "confirmation mail failed"})
			return
		}
		writeJSONBody(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

// ConfirmHandler consumes the token, then asks Resend whether the contact unsubscribed everywhere.
// The confirm is committed by then, so a failed lookup answers false.
func ConfirmHandler(n Newsletter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !n.Enabled {
			newsletterUnavailable(w)
			return
		}
		var b struct {
			Token string `json:"token"`
		}
		if !decodeIntake(w, r, &b) {
			return
		}
		hash := sha256.Sum256([]byte(b.Token))
		email, err := n.Store.Confirm(r.Context(), hash[:])
		if errors.Is(err, ErrLinkInvalid) {
			writeJSONBody(w, http.StatusGone, map[string]string{"error": "link expired or used"})
			return
		}
		if err != nil {
			log.ErrorContext(r.Context(), "blog: confirm failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "confirm failed"})
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), lookupTimeout)
		defer cancel()
		everywhere, err := n.Resend.ContactUnsubscribed(ctx, email)
		if err != nil {
			var de *DeliveryError
			status := 0
			if errors.As(err, &de) {
				status = de.Status
			}
			log.WarnContext(r.Context(), "blog: unsubscribe lookup failed", slog.Int("status", status))
			everywhere = false
		}
		writeJSONBody(w, http.StatusOK, map[string]any{"status": "confirmed", "unsubscribed_everywhere": everywhere})
	}
}
