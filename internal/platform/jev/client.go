// Package jev is the client that asks TypeSafe's Jev model typed questions
// over POST /v1/systemone.
package jev

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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

// ErrCheckSkipped wraps every error Ask returns; the caller skips the check.
var ErrCheckSkipped = errors.New("jev: check skipped")

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
	return &Client{}
}

// Ask sends req and returns the typed answers.
func (c *Client) Ask(ctx context.Context, req Request) (Response, error) {
	return Response{}, nil
}
