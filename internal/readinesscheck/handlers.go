package readinesscheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
)

const (
	MaxRunsPerEmail = 3
	RunBudget       = 240 * time.Second
	MappingBudget   = 60 * time.Second
	MailTimeout     = 15 * time.Second
	InsertTimeout   = 5 * time.Second
	MaxConcurrent   = 2

	maxFieldBytes = 1 << 20 // the largest non-file part: a mapping
	bodySlack     = 1 << 20 // multipart framing and fields around a 15 MiB file
)

// CheckStore is what the handlers need of the summary store; *Store satisfies it.
type CheckStore interface {
	Count(ctx context.Context, email string) (int, error)
	Insert(ctx context.Context, m Summary) error
}

// Service runs one public check. runBudget and mailTimeout override their defaults in tests when non-zero.
type Service struct {
	Checker     *publiccheck.Checker
	Store       CheckStore
	Sender      accountmail.Sender
	Logger      *slog.Logger
	runBudget   time.Duration
	mailTimeout time.Duration
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

// engineStatus maps an engine error to its status and error word.
func engineStatus(err error) (status int, word string) {
	switch {
	case errors.Is(err, publiccheck.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "too large"
	case errors.Is(err, publiccheck.ErrFormat):
		return http.StatusBadRequest, "unsupported format"
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
		if count >= MaxRunsPerEmail {
			writeError(w, http.StatusConflict, "sign_up")
			return
		}

		res, err := s.Checker.Check(ctx, req)
		if err != nil {
			s.answerEngineError(r.Context(), w, err)
			return
		}
		left := MaxRunsPerEmail - count - 1
		subject, html, err := accountmail.RenderReadinessCheck(MailFor(res, left))
		if err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness check: render failed")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		mctx, mcancel := context.WithTimeout(r.Context(), s.mailBudget())
		defer mcancel()
		if err := s.Sender.Send(mctx, []accountmail.Message{{To: email, Subject: subject, HTML: html}}); err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness check: send failed", slog.String("error", err.Error()))
			writeError(w, http.StatusServiceUnavailable, "failed")
			return
		}
		sum := Summary{Email: email, FileType: req.Format, RowCount: res.RowsTotal, FailedRuleIDs: FailedRuleIDs(res)}
		ictx, icancel := context.WithTimeout(context.WithoutCancel(r.Context()), InsertTimeout)
		defer icancel()
		if err := s.Store.Insert(ictx, sum); err != nil {
			s.Logger.ErrorContext(r.Context(), "readiness check: summary not recorded", slog.String("error", err.Error()))
		}
		writeJSON(w, http.StatusOK, map[string]int{"checks_left": left})
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
