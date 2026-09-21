// Package ai is the single client used to call the extraction and check LLM
// services over OpenRouter's chat-completions API.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	Model    = "google/gemini-3.5-flash-lite"
	endpoint = "https://openrouter.ai/api/v1/chat/completions"
	budget   = 15 * time.Second
)

// Purpose selects the system prompt and schema family a call belongs to.
type Purpose string

const (
	PurposeDocument    Purpose = "document"
	PurposeSpreadsheet Purpose = "spreadsheet"
	PurposeLineItems   Purpose = "line_items"
)

// Request is one call's input. FakeHint and FakeScope are read only in fake
// mode; neither is ever sent on the wire or logged. A non-empty FakeScope
// narrows the fake's steering marker to that scope's own spelling
// (AIFAKE-<SCOPE>-...) and never falls back to the unscoped one, so two
// calls sharing the same Text can be steered independently.
type Request struct {
	Purpose    Purpose
	System     string
	Text       string
	Pages      [][]byte // PNG
	FakeHint   string
	FakeScope  string
	SchemaName string
	Schema     json.RawMessage
}

// ErrUnavailable marks a call that exhausted its retry budget.
var ErrUnavailable = errors.New("ai: unavailable")

type config struct {
	key      string
	endpoint string
	fake     bool
	budget   time.Duration
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

type Client struct {
	cfg    config
	http   *http.Client
	logger *slog.Logger
}

func newClient(cfg config, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, http: &http.Client{}, logger: logger}
}

// Call sends req and returns the parsed answer, retrying within the budget.
// One log line per call, whatever the outcome.
func (c *Client) Call(ctx context.Context, req Request) (map[string]any, error) {
	start := c.cfg.now()
	r := c.call(ctx, req)
	c.logCall(ctx, req, r, c.cfg.now().Sub(start))
	return r.answer, r.err
}

// usage is the shape AIR-02-03 logs from.
type usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
}

type result struct {
	answer   map[string]any
	err      error
	outcome  string // "ok" | "unavailable" | "refused" | "off" | "fake"
	attempts int
	usage    usage
}

// responseEnvelope is OpenRouter's chat-completion response shape.
type responseEnvelope struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *usage          `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// call runs the mode order off, validation, schema, fake, then the wire
// request and the retry loop.
func (c *Client) call(ctx context.Context, req Request) result {
	if !c.Enabled() {
		return result{err: ErrOff, outcome: "off"}
	}
	if err := validateRequest(req); err != nil {
		return result{err: err, outcome: "refused"}
	}
	schema, err := checkSchema(req.Schema)
	if err != nil {
		return result{err: fmt.Errorf("ai: invalid request: %v", err), outcome: "refused"}
	}
	if c.cfg.fake {
		return c.fakeCall(req, schema)
	}

	body, err := json.Marshal(buildWireRequest(req))
	if err != nil {
		return result{err: fmt.Errorf("ai: %v", err), outcome: "refused"}
	}

	var res result
	start := c.cfg.now()
	deadline := start.Add(c.cfg.budget)
	var last error

	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, deadline.Sub(c.cfg.now()))
		status, respBody, postErr := c.post(actx, body)
		cancel()
		res.attempts = attempt

		if env, ok := decodeEnvelope(respBody); ok && env.Usage != nil {
			res.usage.PromptTokens += env.Usage.PromptTokens
			res.usage.CompletionTokens += env.Usage.CompletionTokens
			res.usage.Cost += env.Usage.Cost
		}

		// The caller's ctx, never actx: actx expiring is just this attempt's
		// timeout, not the caller giving up.
		if ctx.Err() != nil {
			res.err = fmt.Errorf("ai: %w", ctx.Err())
			res.outcome = "unavailable"
			return res
		}

		switch out, answer, cause := classify(status, respBody, postErr, schema); out {
		case classifyOK:
			res.answer = answer
			res.outcome = "ok"
			return res
		case classifyRefused:
			res.err = cause
			res.outcome = "refused"
			return res
		default:
			last = cause
		}

		wait := min(250*time.Millisecond<<(attempt-1), 2*time.Second)
		if deadline.Sub(c.cfg.now()) <= wait {
			// %v, not %w: last may itself wrap context.DeadlineExceeded,
			// and an unavailable error must not match that.
			res.err = fmt.Errorf("%w (last: %v)", ErrUnavailable, last)
			res.outcome = "unavailable"
			return res
		}
		if err := c.cfg.sleep(ctx, wait); err != nil {
			res.err = fmt.Errorf("ai: %w", err)
			res.outcome = "unavailable"
			return res
		}
	}
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

// fakeScopeRE is the shape a non-empty FakeScope must have: it is inlined into a regexp
// pattern (fake.go's scopedMarkerRe), so only plain letters are allowed.
var fakeScopeRE = regexp.MustCompile(`^[A-Z]+$`)

func validateRequest(req Request) error {
	if req.Purpose != PurposeDocument && req.Purpose != PurposeSpreadsheet && req.Purpose != PurposeLineItems {
		return fmt.Errorf("ai: invalid request: unsupported purpose %q", req.Purpose)
	}
	if req.Text == "" && len(req.Pages) == 0 {
		return errors.New("ai: invalid request: no text or pages")
	}
	if req.SchemaName == "" {
		return errors.New("ai: invalid request: schema name is required")
	}
	if req.FakeScope != "" && !fakeScopeRE.MatchString(req.FakeScope) {
		return fmt.Errorf("ai: invalid request: fake scope %q must match ^[A-Z]+$", req.FakeScope)
	}
	return nil
}

func buildWireRequest(req Request) wireRequest {
	var parts []wirePart
	if req.Text != "" {
		parts = append(parts, wirePart{Type: "text", Text: req.Text})
	}
	for _, page := range req.Pages {
		parts = append(parts, wirePart{
			Type:     "image_url",
			ImageURL: &wireURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(page)},
		})
	}
	return wireRequest{
		Model: Model,
		Messages: []wireMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: parts},
		},
		ResponseFormat: wireFormat{
			Type: "json_schema",
			JSONSchema: wireSchema{
				Name:   req.SchemaName,
				Strict: true,
				Schema: req.Schema,
			},
		},
		Provider: wireProvider{RequireParameters: true},
	}
}

// post sends one attempt and returns its status and raw body. err is a
// transport-level failure (network, attempt timeout); the caller decides
// what to do with a non-2xx status.
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

type classifyOutcome int

const (
	classifyRetry classifyOutcome = iota
	classifyOK
	classifyRefused
)

// classify turns one attempt's raw result into a retry decision.
func classify(status int, body []byte, postErr error, schema map[string]any) (classifyOutcome, map[string]any, error) {
	if postErr != nil {
		return classifyRetry, nil, postErr
	}
	if status == http.StatusTooManyRequests || (status >= 500 && status <= 599) {
		return classifyRetry, nil, fmt.Errorf("ai: HTTP %d", status)
	}
	if status < 200 || status > 299 {
		return classifyRefused, nil, fmt.Errorf("ai: refused: HTTP %d", status)
	}

	env, ok := decodeEnvelope(body)
	if !ok {
		return classifyRetry, nil, errors.New("ai: malformed response body")
	}
	if len(env.Error) > 0 && string(env.Error) != "null" {
		return classifyRetry, nil, errors.New("ai: response contained an error object")
	}
	if len(env.Choices) == 0 {
		return classifyRetry, nil, errors.New("ai: response had no choices")
	}

	answer, err := decodeAnswer(env.Choices[0].Message.Content)
	if err != nil {
		return classifyRetry, nil, fmt.Errorf("ai: response content is not a JSON object: %v", err)
	}
	if err := checkAnswer(schema, answer); err != nil {
		return classifyRetry, nil, fmt.Errorf("ai: response failed schema check: %v", err)
	}
	return classifyOK, answer, nil
}

func decodeEnvelope(body []byte) (responseEnvelope, bool) {
	var env responseEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return responseEnvelope{}, false
	}
	return env, true
}

// decodeAnswer parses content as exactly one JSON object, numbers as
// json.Number so integer/enum checks stay exact.
func decodeAnswer(content string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing data after JSON value")
		}
		return nil, err
	}
	return v, nil
}

type wireRequest struct {
	Model          string        `json:"model"`
	Messages       []wireMessage `json:"messages"`
	ResponseFormat wireFormat    `json:"response_format"`
	Provider       wireProvider  `json:"provider"`
	Temperature    float64       `json:"temperature"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string for system; []wirePart for user
}

type wirePart struct {
	Type     string   `json:"type"`
	Text     string   `json:"text,omitempty"`
	ImageURL *wireURL `json:"image_url,omitempty"`
}

type wireURL struct {
	URL string `json:"url"`
}

type wireFormat struct {
	Type       string     `json:"type"` // "json_schema"
	JSONSchema wireSchema `json:"json_schema"`
}

type wireSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type wireProvider struct {
	RequireParameters bool `json:"require_parameters"`
}
