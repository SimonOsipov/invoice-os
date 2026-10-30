package invoice

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const (
	noteOnNoteReason    = "A note cannot have its own note. Open the original invoice to issue another."
	noteNotIssuedReason = "Only an accepted invoice with an IRN can carry a note."
)

type noteLineReq struct {
	LineID    string `json:"line_id"`
	Quantity  string `json:"quantity"`
	UnitPrice string `json:"unit_price"`
}

type issueNoteRequest struct {
	DocumentType  string        `json:"document_type"`
	InvoiceNumber string        `json:"invoice_number"`
	IssueDate     *time.Time    `json:"issue_date"`
	Lines         []noteLineReq `json:"lines"`
}

// issueNoteGate feeds can_issue_note; it shares noteRefusal with Store.IssueNote so the flag and the 409 agree.
func issueNoteGate(inv Invoice) (bool, *string) {
	switch noteRefusal(inv) {
	case nil:
		return true, nil
	case ErrOriginalIsANote:
		r := noteOnNoteReason
		return false, &r
	default:
		r := noteNotIssuedReason
		return false, &r
	}
}

// IssueNoteHandler returns POST /v1/invoices/{id}/notes.
func IssueNoteHandler(issue func(ctx context.Context, id string, in NoteInput) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req issueNoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		lines := make([]NoteLineInput, len(req.Lines))
		for i, l := range req.Lines {
			lines[i] = NoteLineInput{LineID: l.LineID, Quantity: l.Quantity, UnitPrice: l.UnitPrice}
		}
		inv, err := issue(r.Context(), r.PathValue("id"), NoteInput{
			DocumentType:  submission.DocumentType(req.DocumentType),
			InvoiceNumber: req.InvoiceNumber,
			IssueDate:     req.IssueDate,
			Lines:         lines,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: issue note", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusCreated, inv)
	}
}

// NotesHandler returns GET /v1/invoices/{id}/notes. Compile stub; the executor replaces it.
func NotesHandler(notes func(ctx context.Context, id string) (NotesOnInvoice, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusInternalServerError, "not implemented")
	}
}
