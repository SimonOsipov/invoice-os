package accountmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	From          = "ASComply <no-reply@ascomply.com>"
	ResendBaseURL = "https://api.resend.com"

	// Resend's batch endpoint rejects more than 100 emails.
	maxBatch = 100
)

type Message struct{ To, Subject, HTML string }

type Sender interface {
	Send(ctx context.Context, msgs []Message) error
}

// SendError carries only the status: a Resend body or a *url.Error would leak the address, key or URL.
type SendError struct{ Status int }

func (e *SendError) Error() string {
	return fmt.Sprintf("accountmail: send failed: status %d", e.Status)
}

var ErrNotConfigured = errors.New("accountmail: sending is not configured")

type Resend struct {
	baseURL, key string
	client       *http.Client
}

func NewResend(baseURL, key string, rt http.RoundTripper) *Resend {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Resend{baseURL: baseURL, key: key, client: &http.Client{
		Transport:     rt,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type batchItem struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

func (r *Resend) Send(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if len(msgs) > maxBatch {
		return fmt.Errorf("accountmail: batch of %d exceeds %d", len(msgs), maxBatch)
	}
	items := make([]batchItem, len(msgs))
	for i, m := range msgs {
		items[i] = batchItem{From: From, To: []string{m.To}, Subject: m.Subject, HTML: m.HTML}
	}
	body, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("accountmail: encode batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/emails/batch", bytes.NewReader(body))
	if err != nil {
		return &SendError{}
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-batch-validation", "strict")
	resp, err := r.client.Do(req)
	if err != nil {
		return &SendError{}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &SendError{Status: resp.StatusCode}
	}
	return nil
}

// Capture keeps the last 100 messages in memory for preview environments.
type Capture struct {
	mu   sync.Mutex
	msgs []Message
}

func (c *Capture) Send(_ context.Context, msgs []Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msgs...)
	if n := len(c.msgs) - maxBatch; n > 0 {
		c.msgs = append([]Message(nil), c.msgs[n:]...)
	}
	return nil
}

func (c *Capture) Messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.msgs...)
}

type Off struct{}

func (Off) Send(context.Context, []Message) error { return ErrNotConfigured }

type Mode string

const (
	ModeReal    Mode = "real"
	ModeCapture Mode = "capture"
	ModeOff     Mode = "off"
)

// ModeFromEnv reads only RESEND_SENDING_KEY; RESEND_API_KEY is the contacts key and never picks real.
func ModeFromEnv(getenv func(string) string, preview bool) (Mode, string) {
	if preview {
		return ModeCapture, ""
	}
	if key := getenv("RESEND_SENDING_KEY"); key != "" {
		return ModeReal, key
	}
	return ModeOff, ""
}

func NewSender(mode Mode, key string, rt http.RoundTripper) Sender {
	switch mode {
	case ModeReal:
		return NewResend(ResendBaseURL, key, rt)
	case ModeCapture:
		return &Capture{}
	}
	return Off{}
}
