package readinesscheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
)

const (
	MaxRunsPerEmail = 3
	RunBudget       = 240 * time.Second
	MappingBudget   = 60 * time.Second
	MailTimeout     = 15 * time.Second
	ConfirmTimeout  = 25 * time.Second
	HoldTimeout     = 5 * time.Second
	MaxConcurrent   = 2

	maxFieldBytes = 1 << 20 // the largest non-file part: a mapping
	bodySlack     = 1 << 20 // multipart framing and fields around a 15 MiB file
)

// CheckStore is what the handlers need of the summary store; *Store satisfies it.
type CheckStore interface {
	Count(ctx context.Context, email string) (int, error)
	Hold(ctx context.Context, h Held) (token string, err error)
	Release(ctx context.Context, token string) error
	Confirm(ctx context.Context, token, consentText string, send func(ctx context.Context, h Held, count int) error) (h Held, fresh bool, err error)
	PurgeExpired(ctx context.Context) error
}

// Service runs one public check. runBudget, mailTimeout and holdTimeout override their defaults in tests when non-zero.
type Service struct {
	Checker     *publiccheck.Checker
	Store       CheckStore
	Sender      accountmail.Sender
	Logger      *slog.Logger
	runBudget   time.Duration
	mailTimeout time.Duration
	holdTimeout time.Duration
	slots       chan struct{}
	inFlight    map[string]struct{}
	mu          sync.Mutex
}

func NewService(checker *publiccheck.Checker, store CheckStore, sender accountmail.Sender, logger *slog.Logger) *Service {
	return &Service{Checker: checker, Store: store, Sender: sender, Logger: logger, slots: make(chan struct{}, MaxConcurrent), inFlight: map[string]struct{}{}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, word string) {
	writeJSON(w, status, map[string]string{"error": word})
}

const badBody = "invalid request body"

// acquire takes a slot without waiting; the caller defers the returned release.
func (s *Service) acquire() (release func(), ok bool) {
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, true
	default:
		return nil, false
	}
}

// upload holds the parts of one multipart request; the file stays in memory.
type upload struct {
	email, format, headerRow, mapping string
	file                              []byte
	hasFile                           bool
}

// readUpload streams the parts with MultipartReader: no temp file ever holds the visitor's file.
func readUpload(w http.ResponseWriter, r *http.Request) (upload, int) {
	var u upload
	r.Body = http.MaxBytesReader(w, r.Body, publiccheck.MaxFileBytes+bodySlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return u, http.StatusBadRequest
	}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return u, 0
		}
		if err != nil {
			return u, bodyErrStatus(err)
		}
		if p.FormName() == "file" {
			u.file, err = io.ReadAll(io.LimitReader(p, publiccheck.MaxFileBytes+1))
			u.hasFile = true
		} else {
			err = u.field(p)
		}
		if err != nil {
			return u, bodyErrStatus(err)
		}
	}
}

var errBadPart = errors.New("readinesscheck: bad part")

func (u *upload) field(p *multipart.Part) error {
	b, err := io.ReadAll(io.LimitReader(p, maxFieldBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxFieldBytes {
		return errBadPart
	}
	switch v := string(b); p.FormName() {
	case "email":
		u.email = v
	case "format":
		u.format = v
	case "header_row":
		u.headerRow = v
	case "mapping":
		u.mapping = v
	}
	return nil
}

func bodyErrStatus(err error) int {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// request turns the parts into an engine request; ok is false for a malformed mapping or header_row.
func (u upload) request() (publiccheck.Request, bool) {
	req := publiccheck.Request{File: u.file, Format: u.format}
	if u.headerRow != "" {
		n, err := strconv.Atoi(u.headerRow)
		if err != nil || n < 0 || strings.TrimLeft(u.headerRow, "0123456789") != "" {
			return req, false
		}
		req.HeaderRow = n
	}
	if u.mapping != "" && json.Unmarshal([]byte(u.mapping), &req.Mapping) != nil {
		return req, false
	}
	return req, true
}

// FormatRefusal is the gateway's checkFormatSentence; a test keeps them equal.
const FormatRefusal = "file must be CSV, XLSX, PDF or DOCX"

// engineStatus maps an engine error to its status and error word.
func engineStatus(err error) (status int, word string) {
	switch {
	case errors.Is(err, publiccheck.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "too large"
	case errors.Is(err, publiccheck.ErrFormat):
		return http.StatusBadRequest, FormatRefusal
	case errors.Is(err, importer.ErrValidation):
		return http.StatusBadRequest, strings.TrimPrefix(err.Error(), importer.ErrValidation.Error()+": ")
	case errors.Is(err, publiccheck.ErrBadFile):
		return http.StatusUnprocessableEntity, "unreadable"
	case errors.Is(err, publiccheck.ErrReadUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, "failed"
	}
	return http.StatusInternalServerError, "internal error"
}

func (s *Service) answerEngineError(ctx context.Context, w http.ResponseWriter, err error) {
	status, word := engineStatus(err)
	if status == http.StatusInternalServerError {
		s.Logger.ErrorContext(ctx, "readiness check: engine error")
	}
	writeError(w, status, word)
}

// claim marks an address as running; one run per address at a time keeps Count honest.
func (s *Service) claim(email string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inFlight[email]; busy {
		return nil, false
	}
	s.inFlight[email] = struct{}{}
	return func() {
		s.mu.Lock()
		delete(s.inFlight, email)
		s.mu.Unlock()
	}, true
}

func (s *Service) mailBudget() time.Duration {
	if s.mailTimeout != 0 {
		return s.mailTimeout
	}
	return MailTimeout
}

func (s *Service) holdBudget() time.Duration {
	if s.holdTimeout != 0 {
		return s.holdTimeout
	}
	return HoldTimeout
}

func (s *Service) budget() time.Duration {
	if s.runBudget != 0 {
		return s.runBudget
	}
	return RunBudget
}

func (s *Service) RunHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := s.acquire()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		defer release()

		u, status := readUpload(w, r)
		email := strings.ToLower(strings.TrimSpace(u.email))
		if status != 0 {
			writeError(w, status, badBody)
			return
		}
		req, ok := u.request()
		if !ok || email == "" || !u.hasFile {
			writeError(w, http.StatusBadRequest, badBody)
			return
		}

		releaseAddr, ok := s.claim(email)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		defer releaseAddr()

		ctx, cancel := context.WithTimeout(r.Context(), s.budget())
		defer cancel()
		count, err := s.Store.Count(ctx, email)
		if err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness check: count failed", slog.String("error", err.Error()))
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}

		res, err := s.Checker.Check(ctx, req)
		if err != nil {
			s.answerEngineError(r.Context(), w, err)
			return
		}
		if count >= MaxRunsPerEmail {
			s.answerUsed(w, r, email)
			return
		}
		s.answerHeld(w, r, Held{Email: email, Mail: MailFor(res, 0),
			Summary: Summary{Email: email, FileType: req.Format, RowCount: res.RowsTotal, FailedRuleIDs: FailedRuleIDs(res)}})
	})
}

// sendOne sends one mail within the mail budget.
func (s *Service) sendOne(ctx context.Context, to, subject, html string) error {
	mctx, cancel := context.WithTimeout(ctx, s.mailBudget())
	defer cancel()
	return s.Sender.Send(mctx, []accountmail.Message{{To: to, Subject: subject, HTML: html}})
}

var confirmed = map[string]string{"status": "confirm"}

// answerUsed mails the used-checks notice; the answer equals a held check's.
func (s *Service) answerUsed(w http.ResponseWriter, r *http.Request, email string) {
	subject, html, err := accountmail.RenderReadinessUsed(accountmail.ReadinessUsedMail{Email: email})
	if err != nil {
		s.Logger.ErrorContext(r.Context(), "readiness check: render failed")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.sendOne(r.Context(), email, subject, html); err != nil {
		s.Logger.ErrorContext(r.Context(), "readiness check: send failed", slog.String("error", err.Error()))
		writeError(w, http.StatusServiceUnavailable, "failed")
		return
	}
	writeJSON(w, http.StatusOK, confirmed)
}

// answerHeld holds the result and mails the confirm link; a failed send releases the row.
func (s *Service) answerHeld(w http.ResponseWriter, r *http.Request, h Held) {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.holdBudget())
	defer cancel()
	token, err := s.Store.Hold(hctx, h)
	if err != nil {
		s.Logger.ErrorContext(r.Context(), "readiness check: hold error", slog.String("error", err.Error()))
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	release := func() {
		if err := s.Store.Release(context.WithoutCancel(r.Context()), token); err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness check: release failed", slog.String("error", err.Error()))
		}
	}
	subject, html, err := accountmail.RenderReadinessConfirm(accountmail.ReadinessConfirmMail{Email: h.Email, Token: token, FileType: h.Summary.FileType})
	if err != nil {
		release()
		s.Logger.ErrorContext(r.Context(), "readiness check: render failed")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.sendOne(r.Context(), h.Email, subject, html); err != nil {
		release()
		s.Logger.ErrorContext(r.Context(), "readiness check: send failed", slog.String("error", err.Error()))
		writeError(w, http.StatusServiceUnavailable, "failed")
		return
	}
	writeJSON(w, http.StatusOK, confirmed)
}

const (
	maxConfirmBody   = 2 << 10
	maxConsentLength = 500
	tokenLength      = 43
)

type confirmRequest struct {
	Token       string `json:"token"`
	ConsentText string `json:"consent_text"`
}

func wellShapedToken(t string) bool {
	if len(t) != tokenLength {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ConfirmHandler is the press: it sends the held results (or the used-checks mail) and records the check in one transaction.
func (s *Service) ConfirmHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in confirmRequest
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfirmBody))
		if err != nil || json.Unmarshal(body, &in) != nil || !wellShapedToken(in.Token) {
			writeError(w, http.StatusGone, "gone")
			return
		}
		if n := utf8.RuneCountInString(in.ConsentText); strings.TrimSpace(in.ConsentText) == "" || n > maxConsentLength {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		// The gateway may drop the connection; the press must still commit or roll back whole.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ConfirmTimeout)
		defer cancel()
		_, fresh, err := s.Store.Confirm(ctx, in.Token, in.ConsentText, func(ctx context.Context, h Held, count int) error {
			var subject, html string
			var err error
			if count >= MaxRunsPerEmail {
				subject, html, err = accountmail.RenderReadinessUsed(accountmail.ReadinessUsedMail{Email: h.Email})
			} else {
				h.Mail.ChecksLeft = MaxRunsPerEmail - count - 1
				subject, html, err = accountmail.RenderReadinessCheck(h.Mail)
			}
			if err != nil {
				return fmt.Errorf("render: %w", err)
			}
			return s.sendOne(ctx, h.Email, subject, html)
		})
		switch {
		case errors.Is(err, ErrLinkInvalid):
			writeError(w, http.StatusGone, "gone")
		case err != nil:
			s.Logger.ErrorContext(r.Context(), "readiness check: confirm failed", slog.String("error", err.Error()))
			writeError(w, http.StatusServiceUnavailable, "failed")
		case fresh:
			writeJSON(w, http.StatusOK, map[string]string{"outcome": "sent"})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"outcome": "used"})
		}
	})
}

type mappingResponse struct {
	Columns    []string          `json:"columns"`
	SampleRows [][]string        `json:"sample_rows"`
	HeaderRow  int               `json:"header_row"`
	RowsTotal  int               `json:"rows_total"`
	Mapping    map[string]string `json:"mapping"`
	Source     string            `json:"source"`
}

func (s *Service) MappingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := s.acquire()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		defer release()

		u, status := readUpload(w, r)
		if status != 0 {
			writeError(w, status, badBody)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), MappingBudget)
		defer cancel()
		sug, err := s.Checker.SuggestMapping(ctx, u.file, u.format)
		if err != nil {
			s.answerEngineError(r.Context(), w, err)
			return
		}
		writeJSON(w, http.StatusOK, mappingResponse{Columns: sug.Columns, SampleRows: sug.SampleRows,
			HeaderRow: sug.HeaderRow, RowsTotal: sug.RowsTotal, Mapping: sug.Mapping, Source: sug.Source})
	})
}
