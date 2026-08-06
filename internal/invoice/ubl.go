package invoice

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

// The em dash (U+2014) with single spaces matches revalidateBlockedReason's copy.
const ublBlockedPrefix = "This invoice cannot be rendered as a UBL document — it is missing "

// ublBlockedReason is nil when nothing is missing -- never a pointer to "".
// BUG-04-03 populates the detail payload's ubl_blocked_reason from here, so the
// 409 body and the payload are one string by construction.
func ublBlockedReason(missing []string) *string {
	if len(missing) == 0 {
		return nil
	}
	// Commas between all but the last pair, " and " before the last, no Oxford comma.
	list := missing[len(missing)-1]
	if len(missing) > 1 {
		list = strings.Join(missing[:len(missing)-1], ", ") + " and " + list
	}
	reason := ublBlockedPrefix + list + "."
	return &reason
}

// ublGate is the SINGLE derivation behind both GET /{id}'s can_view_ubl/
// ubl_blocked_reason and this route's 409 -- one return, so the two cannot
// drift. ok == (reason == nil) by construction.
func ublGate(c submission.Canonical) (bool, *string) {
	reason := ublBlockedReason(ubl.Missing(c))
	return reason == nil, reason
}

// UBLHandler returns GET /v1/invoices/{id}/ubl -- same identity-first-401 order
// as GetHandler. Render runs after Store.Get's tenant tx has committed: it is
// pure CPU work over an already-hydrated invoice.
func UBLHandler(get func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		inv, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: ubl", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// Refuse BEFORE Render: ubl.ErrIncomplete is not an invoice sentinel, so
		// routing it through statusForErr would hit the default arm and 500.
		c := SubmissionCanonical(inv)
		if ok, reason := ublGate(c); !ok {
			writeError(w, http.StatusConflict, *reason)
			return
		}

		body, err := ubl.Render(c)
		if err != nil {
			// Unreachable past the gate above; kept so no future Render failure
			// can ship a partial XML body.
			log.ErrorContext(r.Context(), "invoice: render ubl", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		// Headers only once the render has succeeded, so a refusal never carries them.
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Hardening for a direct navigation, not the SPA's download path: fetch
		// ignores this header and BUG-04-06 names the file client-side
		// ([download-filename-sanitised-client-side]). FormatMediaType quotes the
		// invoice number rather than sanitising it -- an <a download> cannot quote,
		// which is why the two transformations differ.
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": inv.InvoiceNumber + ".xml",
		}))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
