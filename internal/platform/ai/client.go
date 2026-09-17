// Package ai is the single client used to call the extraction and check LLM
// services over OpenRouter's chat-completions API.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
)

// Request is one call's input. FakeHint is read only by the fake transport
// added in AIR-02-02; it is never sent on the wire and never logged.
type Request struct {
	Purpose    Purpose
	System     string
	Text       string
	Pages      [][]byte // PNG
	FakeHint   string
	SchemaName string
	Schema     json.RawMessage
}

// ErrUnavailable marks a call that exhausted its retry budget.
var ErrUnavailable = errors.New("ai: unavailable")

type config struct {
	key      string
	endpoint string
	fake     bool // unused until AIR-02-02
	budget   time.Duration
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

type Client struct {
	cfg    config
	http   *http.Client
	logger *slog.Logger // unused until AIR-02-03
}

func newClient(cfg config, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, http: &http.Client{}, logger: logger}
}

// Call sends req and returns the parsed answer, retrying within the budget.
// Not implemented yet (AIR-02-03).
func (c *Client) Call(ctx context.Context, req Request) (map[string]any, error) {
	r := c.call(ctx, req)
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
	outcome  string // "ok" | "unavailable" | "refused"
	attempts int
	usage    usage
}

// call carries validation, the wire request, and the retry loop. Stubbed
// until AIR-02-03.
func (c *Client) call(ctx context.Context, req Request) result {
	return result{err: errors.New("ai: not implemented")}
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

func validateRequest(req Request) error {
	return errors.New("ai: not implemented")
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
