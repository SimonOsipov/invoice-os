// Package jev is the client that asks TypeSafe's Jev model typed questions
// over POST /v1/systemone.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

const Model = "jev-latest"

const (
	endpoint  = "https://api.typesafe.ai/v1/systemone"
	budget    = 3 * time.Second
	retryWait = 250 * time.Millisecond
)

// Purpose names the caller's check.
type Purpose string

const (
	PurposeValueCheck   Purpose = "value_check"
	PurposeDocumentType Purpose = "document_type"
	PurposeMappingCheck Purpose = "mapping_check"
)

type QuestionType string

const (
	TypeNoul   QuestionType = "noul"
	TypeChoice QuestionType = "choice"
	TypeScore  QuestionType = "score"
)

// Option is one choice option, or one score level in low-to-high order.
type Option struct {
	Name        string // choice: the option key sent to the vendor; score: the caller's handle, never sent
	Description string
}

type Question struct {
	Type         QuestionType
	Instructions string
	True, False  string   // noul only, both optional
	Options      []Option // choice and score
	Default      string   // choice and score: the Option.Name the fake answers; never sent
}

type Request struct {
	Purpose   Purpose
	State     string
	Questions map[string]Question // key = question id, returned under the same id
}

type Answer struct {
	Type       QuestionType
	Noul       float64 // noul: P(yes), 0..1
	Choice     string  // choice: the selected option's name
	Score      float64 // score: 0..len(Options)-1
	Confidence float64 // choice and score
}

type Usage struct{ InputTokens, OutputTokens int }

type Response struct {
	Answers map[string]Answer
	Usage   Usage
}

// ErrCheckSkipped means the caller skips the check.
var ErrCheckSkipped = errors.New("jev: check skipped")

// Each text names the outcome only: never a status, body, state, key or URL.
var (
	errOff         = fmt.Errorf("%w: off", ErrCheckSkipped)
	errRefused     = fmt.Errorf("%w: refused", ErrCheckSkipped)
	errUnavailable = fmt.Errorf("%w: unavailable", ErrCheckSkipped)
)

type config struct {
	key       string
	endpoint  string
	fake      bool
	budget    time.Duration
	retryWait time.Duration
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
}

type Client struct {
	cfg    config
	http   *http.Client
	logger *slog.Logger
}

func newClient(cfg config, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, http: &http.Client{}, logger: logger}
}

// Ask sends req and returns the typed answers, retrying once within the
// budget. Every error wraps ErrCheckSkipped.
func (c *Client) Ask(ctx context.Context, req Request) (Response, error) {
	start := c.cfg.now()
	r := c.call(ctx, req)
	c.logCall(ctx, req, r, c.cfg.now().Sub(start))
	return r.resp, r.err
}

type result struct {
	resp     Response
	err      error
	outcome  string // "ok" | "skipped_unavailable" | "skipped_refused" | "off" | "fake"
	attempts int
	usage    Usage // summed over attempts, failed ones included
}

// call runs the mode order off, validation, key check, fake, then the wire
// request and the retry loop.
func (c *Client) call(ctx context.Context, req Request) result {
	if !c.Enabled() {
		return result{err: errOff, outcome: "off"}
	}
	if !validRequest(req) || !validKey(c.cfg.key) {
		return result{err: errRefused, outcome: "skipped_refused"}
	}
	if c.cfg.fake {
		return fakeCall(req)
	}
	body, err := json.Marshal(buildWireRequest(req))
	if err != nil {
		return result{err: errRefused, outcome: "skipped_refused"}
	}

	var res result
	deadline := c.cfg.now().Add(c.cfg.budget)
	// The cap leaves room for the wait and a second attempt after a hung first one.
	attemptCap := (c.cfg.budget - c.cfg.retryWait) / 2

	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, min(attemptCap, deadline.Sub(c.cfg.now())))
		status, respBody, postErr := c.post(actx, body)
		cancel()
		res.attempts = attempt

		env, decoded := decodeEnvelope(respBody)
		if decoded && env.Usage != nil {
			res.usage.InputTokens += env.Usage.InputTokens
			res.usage.OutputTokens += env.Usage.OutputTokens
		}

		// The caller's ctx, never actx: actx ending is only this attempt's timeout.
		if err := ctx.Err(); err != nil {
			return res.fail(fmt.Errorf("%w: %w", errUnavailable, err), "skipped_unavailable")
		}

		switch {
		case postErr == nil && status >= 200 && status <= 299:
			answers, ok := checkAnswers(req.Questions, env.Answers)
			if !decoded || !ok {
				return res.fail(errUnavailable, "skipped_unavailable")
			}
			res.resp = Response{Answers: answers, Usage: res.usage}
			res.outcome = "ok"
			return res
		case postErr != nil || retryable(status):
			// Retried below if the budget allows.
		default:
			return res.fail(errRefused, "skipped_refused")
		}

		if attempt == 2 || deadline.Sub(c.cfg.now()) <= c.cfg.retryWait {
			return res.fail(errUnavailable, "skipped_unavailable")
		}
		if err := c.cfg.sleep(ctx, c.cfg.retryWait); err != nil {
			return res.fail(fmt.Errorf("%w: %w", errUnavailable, err), "skipped_unavailable")
		}
	}
}

func (r result) fail(err error, outcome string) result {
	r.err, r.outcome = err, outcome
	return r
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func validRequest(req Request) bool {
	switch req.Purpose {
	case PurposeValueCheck, PurposeDocumentType, PurposeMappingCheck:
	default:
		return false
	}
	if req.State == "" || len(req.Questions) == 0 {
		return false
	}
	for id, q := range req.Questions {
		if id == "" || q.Instructions == "" {
			return false
		}
		switch q.Type {
		case TypeNoul:
		case TypeChoice, TypeScore:
			if !validOptions(q) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validOptions(q Question) bool {
	if len(q.Options) < 2 {
		return false
	}
	names := make(map[string]bool, len(q.Options))
	for _, o := range q.Options {
		// A score level is sent as its description alone, so an empty one describes nothing.
		if o.Name == "" || names[o.Name] || (q.Type == TypeScore && o.Description == "") {
			return false
		}
		names[o.Name] = true
	}
	return names[q.Default]
}

// validKey applies net/http's header-value rule up front: the transport would
// refuse such a key on every attempt, which a retry cannot fix.
func validKey(key string) bool {
	for i := 0; i < len(key); i++ {
		if b := key[i]; (b < 0x20 && b != '\t') || b == 0x7f {
			return false
		}
	}
	return true
}

type wireRequest struct {
	State     string                  `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

type noulCriteria struct {
	True  string `json:"true,omitempty"`
	False string `json:"false,omitempty"`
}

func buildWireRequest(req Request) wireRequest {
	qs := make(map[string]wireQuestion, len(req.Questions))
	for id, q := range req.Questions {
		w := wireQuestion{Type: q.Type, Instructions: q.Instructions}
		switch q.Type {
		case TypeNoul:
			if q.True != "" || q.False != "" {
				w.Criteria = noulCriteria{True: q.True, False: q.False}
			}
		case TypeChoice:
			// A map, so encoding/json sorts the keys exactly as the measurement harness sent them.
			criteria := make(map[string]*string, len(q.Options))
			for _, o := range q.Options {
				var desc *string
				if o.Description != "" {
					desc = &o.Description
				}
				criteria[o.Name] = desc
			}
			w.Criteria = criteria
		case TypeScore:
			levels := make([]string, len(q.Options))
			for i, o := range q.Options {
				levels[i] = o.Description
			}
			w.Criteria = levels
		}
		qs[id] = w
	}
	return wireRequest{State: req.State, Model: Model, Questions: qs}
}

// post sends one attempt and returns its status and raw body. err is a
// transport-level failure (network, attempt timeout).
func (c *Client) post(ctx context.Context, body []byte) (status int, respBody []byte, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.key)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// envelope keeps each answer raw, so one bad answer still leaves usage readable.
type envelope struct {
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func decodeEnvelope(body []byte) (envelope, bool) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return envelope{}, false
	}
	return env, true
}

// wireAnswer decodes into pointers so a null or absent field cannot read as 0.
type wireAnswer struct {
	Type       QuestionType `json:"type"`
	Noul       *float64     `json:"noul"`
	Choice     *string      `json:"choice"`
	Score      *float64     `json:"score"`
	Confidence *float64     `json:"confidence"`
}

// checkAnswers returns an answer for every asked id, or false if any is
// missing, mistyped or out of range. Ids nobody asked are dropped.
func checkAnswers(qs map[string]Question, got map[string]json.RawMessage) (map[string]Answer, bool) {
	answers := make(map[string]Answer, len(qs))
	for id, q := range qs {
		raw, ok := got[id]
		if !ok {
			return nil, false
		}
		var w wireAnswer
		if err := json.Unmarshal(raw, &w); err != nil || w.Type != q.Type {
			return nil, false
		}
		a := Answer{Type: q.Type}
		switch q.Type {
		case TypeNoul:
			if !inRange(w.Noul, 0, 1) {
				return nil, false
			}
			a.Noul = *w.Noul
		case TypeChoice:
			if w.Choice == nil || !slices.ContainsFunc(q.Options, func(o Option) bool { return o.Name == *w.Choice }) ||
				!inRange(w.Confidence, 0, 1) {
				return nil, false
			}
			a.Choice, a.Confidence = *w.Choice, *w.Confidence
		case TypeScore:
			if !inRange(w.Score, 0, float64(len(q.Options)-1)) || !inRange(w.Confidence, 0, 1) {
				return nil, false
			}
			a.Score, a.Confidence = *w.Score, *w.Confidence
		}
		answers[id] = a
	}
	return answers, true
}

func inRange(v *float64, lo, hi float64) bool { return v != nil && *v >= lo && *v <= hi }
