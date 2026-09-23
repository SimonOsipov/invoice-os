// Package jevmeasure is the standalone measurement harness for the TypeSafe
// ("Jev") checks: a minimal HTTP client, the question wording, the outcome
// row shape, and the report renderer. Stdlib only -- see deps_test.go. No
// product package imports this; CHECK-02 owns retries, fake mode and logging.
package jevmeasure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Endpoint is the vendor's documented URL (A49). Production callers pass it
// to NewClient; tests pass an httptest server URL instead.
const Endpoint = "https://api.typesafe.ai/v1/systemone"

// AuthHeaderName/AuthHeaderPrefix are the vendor's documented auth header.
const (
	AuthHeaderName   = "Authorization"
	AuthHeaderPrefix = "Bearer "
)

// Question is one entry of a request's "questions" map (A49). Criteria is
// optional for a noul question and required for a choice question.
type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

// Usage is the vendor's token count for one call. A nil field means the
// vendor did not report it -- never treat nil as zero.
type Usage struct {
	InputTokens  *json.Number `json:"input_tokens,omitempty"`
	OutputTokens *json.Number `json:"output_tokens,omitempty"`
}

// Answer is one decoded question answer. A noul answer carries only Noul
// (A49: no confidence field); a choice answer carries Choice, Confidence and
// Probabilities.
type Answer struct {
	Type          string
	Choice        string
	Confidence    *json.Number
	Probabilities map[string]json.Number
	Noul          *json.Number
}

// Response is one Ask call's decoded body.
type Response struct {
	Answers map[string]Answer
	Usage   Usage
}

type wireRequest struct {
	State     string              `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

type wireAnswer struct {
	Type          string                 `json:"type"`
	Choice        string                 `json:"choice,omitempty"`
	Confidence    *json.Number           `json:"confidence,omitempty"`
	Probabilities map[string]json.Number `json:"probabilities,omitempty"`
	Noul          *json.Number           `json:"noul,omitempty"`
}

type wireResponse struct {
	Answers map[string]wireAnswer `json:"answers"`
	Usage   Usage                 `json:"usage"`
}

// Client is the minimal caller: one POST, one bearer header, no retry.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// NewClient builds a Client against baseURL (production: Endpoint; tests: an
// httptest server URL).
func NewClient(baseURL, model, apiKey string) *Client {
	// No Timeout: ctx is the only clock, so a client-level cap can't
	// truncate the very latency this package measures.
	return &Client{baseURL: baseURL, model: model, apiKey: apiKey, http: &http.Client{}}
}

// Ask sends one POST carrying state and qs, and returns the decoded answer
// plus wall-clock elapsed -- on every return path, including an error, so
// the all-attempts latency series (A45) always has a number to add.
func (c *Client) Ask(ctx context.Context, state string, qs map[string]Question) (resp Response, elapsed time.Duration, err error) {
	start := time.Now()
	defer func() { elapsed = time.Since(start) }()

	body, marshalErr := json.Marshal(wireRequest{State: state, Model: c.model, Questions: qs})
	if marshalErr != nil {
		return Response{}, 0, fmt.Errorf("jevmeasure: encode request: %v", marshalErr)
	}

	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if reqErr != nil {
		return Response{}, 0, fmt.Errorf("jevmeasure: build request: %v", reqErr)
	}
	httpReq.Header.Set(AuthHeaderName, AuthHeaderPrefix+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, doErr := c.http.Do(httpReq)
	if doErr != nil {
		return Response{}, 0, fmt.Errorf("jevmeasure: %v", doErr)
	}
	defer httpResp.Body.Close()

	respBody, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return Response{}, 0, fmt.Errorf("jevmeasure: read response: %v", readErr)
	}
	// Never put respBody in an error string (the docling.go 4 KiB trap).
	if httpResp.StatusCode != http.StatusOK {
		return Response{}, 0, fmt.Errorf("jevmeasure: status %d", httpResp.StatusCode)
	}

	// No dec.UseNumber(): every numeric target below is already *json.Number, which
	// encoding/json decodes natively -- UseNumber only changes an any-typed target, and
	// wireResponse has none (measured, §1.13). Restore it the moment wireResponse grows one.
	dec := json.NewDecoder(bytes.NewReader(respBody))
	var wire wireResponse
	if decErr := dec.Decode(&wire); decErr != nil {
		return Response{}, 0, fmt.Errorf("jevmeasure: decode response: %v", decErr)
	}

	answers := make(map[string]Answer, len(wire.Answers))
	for id, a := range wire.Answers {
		answers[id] = Answer{
			Type:          a.Type,
			Choice:        a.Choice,
			Confidence:    a.Confidence,
			Probabilities: a.Probabilities,
			Noul:          a.Noul,
		}
	}
	return Response{Answers: answers, Usage: wire.Usage}, 0, nil
}
