package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	ReadinessCheckPerIP    = 10
	ReadinessCheckIPWindow = 24 * time.Hour
	ReadinessCheckMaxKeys  = 10_000
	// MaxCheckFileBytes equals publiccheck.MaxFileBytes (TestReadinessCheck_FileCapIsTheEngineCap); the gateway must not link the engine.
	MaxCheckFileBytes = 15 << 20
)

const (
	maxCheckUploads         = 4
	unattributableIPKey     = "unattributable"
	readinessRunTimeout     = 295 * time.Second
	readinessMappingTimeout = 70 * time.Second
	readinessUploadTimeout  = 90 * time.Second
	checkBodySlack          = 1 << 20
	maxCheckAnswerBytes     = 4096
	maxCheckErrorBytes      = 1024
	maxMappingAnswerBytes   = 256 << 10

	checkIPSentence          = "This network has run 10 checks today. Try again tomorrow."
	checkSlowUploadSentence  = "The upload took too long. Try again on a faster connection."
	checkUnavailableSentence = "The check is unavailable. Try again in a few minutes."
	checkCeilingSentence     = "The free check has reached its limit for today. Try again tomorrow."
	checkTooLongSentence     = "This document has too much text for the free check. Try a shorter invoice."
	checkSignUpSentence      = "This address has used its 3 free checks. Sign up to check every invoice."
	checkUnreadableSentence  = "We could not read this file."
	checkTooLargeSentence    = "file is over 15 MiB"
	checkConsentSentence     = "consent_text must be 1 to 500 characters"
	checkConsentText         = "Email me the results of this check. I agree that ASComply Africa keeps my email address as a contact and may email me product news and offers. I can unsubscribe at any time."
	checkFormatSentence      = "file must be CSV, XLSX, PDF or DOCX"
	checkSheetSentence       = "file must be a CSV or XLSX spreadsheet"
)

var headerRowRE = regexp.MustCompile(`^[0-9]{1,6}$`)

var (
	runFieldCaps     = map[string]int{"email": 1 << 10, "consent_text": 4 << 10, "header_row": 16, "mapping": 16 << 10}
	mappingFieldCaps = map[string]int{}
	checkFormats     = map[string]string{".csv": "csv", ".xlsx": "xlsx", ".pdf": "pdf", ".docx": "docx"}
)

// checkIPKey keys an IPv6 address by its /64 and an IPv4 address by itself; clientIP has already unmapped.
// ceiling: a /56 or /48 holder still gets 10 runs per /64, widen the prefix if that is abused
func checkIPKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return unattributableIPKey
	}
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// ReadinessCheckSink hands a checked address and its consent text to the notifications /internal/contacts/readiness-checks intake.
type ReadinessCheckSink interface {
	ReadinessCheck(ctx context.Context, email, consentText string) error
}

// NewHTTPReadinessCheckSink posts to notifications at base with the gateway token; it never follows a redirect.
func NewHTTPReadinessCheckSink(base *url.URL, client *http.Client, gatewayToken string) ReadinessCheckSink {
	return readinessSink{NewHTTPContactSink(base, client, gatewayToken).(*httpContactSink)}
}

type readinessSink struct{ s *httpContactSink }

func (k readinessSink) ReadinessCheck(ctx context.Context, email, consentText string) error {
	return k.s.post(ctx, "readiness-checks", map[string]string{"email": email, "marketing_consent_text": consentText})
}

// ReadinessCheckRoutes holds the two public check handlers.
type ReadinessCheckRoutes struct{ Mapping, Run http.Handler }

// ReadinessCheckHandlers serves POST /readiness-check/run and /mapping by streaming the visitor's file to the worker.
// A run is counted per client IP (IPv6 per /64) in perIP before the file is read. No log line carries the address, the IP or the file name.
func ReadinessCheckHandlers(worker *url.URL, client *http.Client, gatewayToken string, sink ReadinessCheckSink, perIP *SignInThrottle, enforce bool, posture platform.PostureKind, log *slog.Logger) ReadinessCheckRoutes {
	c := newReadinessCheck(worker, client, gatewayToken, sink, perIP, enforce, posture, log)
	return ReadinessCheckRoutes{Mapping: http.HandlerFunc(c.mapping), Run: http.HandlerFunc(c.run)}
}

type readinessCheck struct {
	worker                     *url.URL
	client                     *http.Client
	token                      string
	sink                       ReadinessCheckSink
	perIP                      *SignInThrottle
	enforce                    bool
	posture                    platform.PostureKind
	log                        *slog.Logger
	slots                      chan struct{}
	runTimeout, mappingTimeout time.Duration
	uploadTimeout              time.Duration
}

func newReadinessCheck(worker *url.URL, client *http.Client, gatewayToken string, sink ReadinessCheckSink, perIP *SignInThrottle, enforce bool, posture platform.PostureKind, log *slog.Logger) *readinessCheck {
	return &readinessCheck{worker: worker, client: client, token: gatewayToken, sink: sink, perIP: perIP, enforce: enforce,
		posture: posture, log: log, slots: make(chan struct{}, maxCheckUploads),
		runTimeout: readinessRunTimeout, mappingTimeout: readinessMappingTimeout,
		uploadTimeout: readinessUploadTimeout}
}

// readFields reads the parts that precede the file part, each under its cap, and returns the file part unread.
// ok is false for a non-multipart body, an unknown or repeated part, a part over its cap, or no file part.
func readFields(mr *multipart.Reader, caps map[string]int) (fields map[string]string, file *multipart.Part, ok bool) {
	fields = map[string]string{}
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil, nil, false
		}
		name := p.FormName()
		if name == "file" {
			return fields, p, true
		}
		limit, known := caps[name]
		if _, dup := fields[name]; !known || dup {
			return nil, nil, false
		}
		b, err := io.ReadAll(io.LimitReader(p, int64(limit)+1))
		if err != nil || len(b) > limit {
			return nil, nil, false
		}
		fields[name] = string(b)
	}
}

func (c *readinessCheck) parts(w http.ResponseWriter, r *http.Request, caps map[string]int) (map[string]string, *multipart.Part) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxCheckFileBytes+checkBodySlack)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, nil
	}
	fields, file, ok := readFields(mr, caps)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, nil
	}
	return fields, file
}

// validateRunFields returns the cleaned email and the 400 message, or "".
func validateRunFields(fields map[string]string) (email, msg string) {
	_, hasEmail := fields["email"]
	_, hasConsent := fields["consent_text"]
	if !hasEmail && !hasConsent {
		return "", "invalid request body"
	}
	email = strings.TrimSpace(fields["email"])
	if invalidEmail(email) {
		return "", "email is invalid"
	}
	if fields["consent_text"] != checkConsentText {
		return "", checkConsentSentence
	}
	if hr, ok := fields["header_row"]; ok && !headerRowRE.MatchString(hr) {
		return "", "invalid request body"
	}
	if m, ok := fields["mapping"]; ok {
		var parsed map[string]string
		if json.Unmarshal([]byte(m), &parsed) != nil || parsed == nil {
			return "", "invalid request body"
		}
	}
	return email, ""
}

func (c *readinessCheck) acquire() (release func(), ok bool) {
	select {
	case c.slots <- struct{}{}:
		return func() { <-c.slots }, true
	default:
		return nil, false
	}
}

// readFile reads the file part once, in memory, within d; it writes the refusal and reports false on failure.
// The timer ends the read through the connection's read deadline; a deadline that fired is cleared on success so it never reaches the worker call.
func readFile(ctx context.Context, log *slog.Logger, w http.ResponseWriter, file *multipart.Part, d time.Duration) ([]byte, bool) {
	rc := http.NewResponseController(w)
	fired := make(chan struct{})
	t := time.AfterFunc(d, func() {
		defer close(fired)
		if err := rc.SetReadDeadline(time.Now()); err != nil {
			log.ErrorContext(ctx, "readiness-check: upload deadline not supported", slog.String("error", err.Error()))
		}
	})
	data, err := io.ReadAll(io.LimitReader(file, MaxCheckFileBytes+1))
	expired := !t.Stop()
	if expired {
		<-fired
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig) || len(data) > MaxCheckFileBytes:
		writeError(w, http.StatusRequestEntityTooLarge, checkTooLargeSentence)
	case errors.Is(err, os.ErrDeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, checkSlowUploadSentence)
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid request body")
	default:
		if expired {
			_ = rc.SetReadDeadline(time.Time{})
		}
		return data, true
	}
	return nil, false
}

func formatOf(file *multipart.Part) string {
	return checkFormats[strings.ToLower(path.Ext(file.FileName()))]
}

func (c *readinessCheck) run(w http.ResponseWriter, r *http.Request) {
	if !blogPostOnly(w, r) {
		return
	}
	fields, file := c.parts(w, r, runFieldCaps)
	if file == nil {
		return
	}
	email, msg := validateRunFields(fields)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	format := formatOf(file)
	if format == "" {
		writeError(w, http.StatusBadRequest, checkFormatSentence)
		return
	}

	ip := checkIPKey(clientIP(r, c.posture))
	held, admitErr := c.perIP.Admit(ip)
	if !held {
		// A database error is already logged by the throttle.
		if admitErr == nil {
			c.log.WarnContext(r.Context(), "readiness-check: limit reached",
				slog.String("limit", "ip"), slog.Bool("enforced", c.enforce))
		}
		if c.enforce {
			if admitErr != nil {
				writeError(w, http.StatusServiceUnavailable, checkUnavailableSentence)
			} else {
				writeError(w, http.StatusTooManyRequests, checkIPSentence)
			}
			return
		}
	}
	refund := func() {
		if held {
			c.perIP.Refund(ip)
		}
	}

	release, ok := c.acquire()
	if !ok {
		refund()
		writeError(w, http.StatusServiceUnavailable, checkUnavailableSentence)
		return
	}
	defer release()
	data, ok := readFile(r.Context(), c.log, w, file, c.uploadTimeout)
	if !ok {
		refund()
		return
	}

	forward := [][2]string{{"email", email}, {"format", format}}
	for _, name := range []string{"header_row", "mapping"} {
		if v, ok := fields[name]; ok {
			forward = append(forward, [2]string{name, v})
		}
	}
	status, answer, err := c.forward(r.Context(), "run", c.runTimeout, forward, data, maxCheckAnswerBytes)
	if err != nil {
		// A deadline means the engine may have run; any other failure came before a response.
		if !errors.Is(err, context.DeadlineExceeded) {
			refund()
		}
		c.log.WarnContext(r.Context(), "readiness-check: worker call failed", slog.Int("upstream_status", 0))
		writeError(w, http.StatusBadGateway, checkUnavailableSentence)
		return
	}

	if status == http.StatusOK {
		// The mail went: hand off even when the answer is unusable.
		if err := c.sink.ReadinessCheck(context.WithoutCancel(r.Context()), email, fields["consent_text"]); err != nil {
			c.log.WarnContext(r.Context(), "readiness-check: hand-off failed", slog.Int("status", failureStatus(err)))
		}
		var in struct {
			ChecksLeft *int `json:"checks_left"`
		}
		if json.Unmarshal(answer, &in) != nil || in.ChecksLeft == nil || *in.ChecksLeft < 0 || *in.ChecksLeft > 2 {
			c.log.ErrorContext(r.Context(), "readiness-check: worker answer has no checks_left", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, checkUnavailableSentence)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "checks_left": *in.ChecksLeft})
		return
	}

	out, word, text := workerRefusal(status, answer)
	if engineNotStarted(status, word) {
		refund()
	}
	if out == http.StatusBadGateway {
		c.log.WarnContext(r.Context(), "readiness-check: worker call failed", slog.Int("upstream_status", status))
	}
	writeError(w, out, text)
}

func (c *readinessCheck) mapping(w http.ResponseWriter, r *http.Request) {
	if !blogPostOnly(w, r) {
		return
	}
	_, file := c.parts(w, r, mappingFieldCaps)
	if file == nil {
		return
	}
	format := formatOf(file)
	if format != "csv" && format != "xlsx" {
		writeError(w, http.StatusBadRequest, checkSheetSentence)
		return
	}
	release, ok := c.acquire()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, checkUnavailableSentence)
		return
	}
	defer release()
	data, ok := readFile(r.Context(), c.log, w, file, c.uploadTimeout)
	if !ok {
		return
	}

	status, answer, err := c.forward(r.Context(), "mapping", c.mappingTimeout, [][2]string{{"format", format}}, data, maxMappingAnswerBytes)
	if err != nil {
		c.log.WarnContext(r.Context(), "readiness-check: worker call failed", slog.Int("upstream_status", 0))
		writeError(w, http.StatusBadGateway, checkUnavailableSentence)
		return
	}
	if status == http.StatusOK && len(answer) <= maxMappingAnswerBytes {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer)
		return
	}
	out, _, text := workerRefusal(status, answer)
	if out == http.StatusBadGateway {
		c.log.WarnContext(r.Context(), "readiness-check: worker call failed", slog.Int("upstream_status", status))
	}
	writeError(w, out, text)
}

// forward streams fields and the file to the worker through a pipe, so the gateway holds the one copy it read.
// The call outlives the visitor's connection (WithoutCancel) and ends at timeout; err is a failure before any response.
func (c *readinessCheck) forward(ctx context.Context, route string, timeout time.Duration, fields [][2]string, data []byte, maxAnswer int) (status int, answer []byte, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	pr, pw := io.Pipe()
	defer pr.Close()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeCheckParts(mw, fields, data))
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.worker.JoinPath("internal", "public-checks", route).String(), pr)
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	answer, _ = io.ReadAll(io.LimitReader(resp.Body, int64(maxAnswer)+1))
	return resp.StatusCode, answer, nil
}

// writeCheckParts writes the file under a fixed name: the visitor's file name never travels on.
func writeCheckParts(mw *multipart.Writer, fields [][2]string, data []byte) error {
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("file", "upload")
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	return mw.Close()
}

// workerRefusal maps a worker answer other than 200 to the visitor's status and sentence; word is the worker's error word.
func workerRefusal(status int, body []byte) (out int, word, text string) {
	var in struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &in)
	word = in.Error
	switch status {
	case http.StatusConflict:
		return http.StatusConflict, word, checkSignUpSentence
	case http.StatusBadRequest:
		if word == "" || len(word) > maxCheckErrorBytes {
			return http.StatusBadRequest, word, "invalid request body"
		}
		return http.StatusBadRequest, word, word
	case http.StatusRequestEntityTooLarge:
		return status, word, checkTooLargeSentence
	case http.StatusUnprocessableEntity:
		return status, word, checkUnreadableSentence
	case http.StatusServiceUnavailable:
		if word == "ceiling" {
			return status, word, checkCeilingSentence
		}
		return status, word, checkUnavailableSentence
	}
	return http.StatusBadGateway, word, checkUnavailableSentence
}

// engineNotStarted reports whether the worker answered before it ran the engine; the IP slot then comes back.
// The too-long refusal keeps the slot: Docling had already read the file.
func engineNotStarted(status int, word string) bool {
	switch status {
	case http.StatusBadRequest:
		return word != checkTooLongSentence
	case http.StatusConflict, http.StatusRequestEntityTooLarge:
		return true
	case http.StatusServiceUnavailable:
		return word == "busy" || word == "unavailable" || word == "ceiling"
	}
	return false
}
