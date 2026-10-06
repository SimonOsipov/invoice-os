package accountmail

// RED STUB (RESEND-05-04, compile-only). The executor deletes this file and
// implements these symbols in sender.go per the subtask.

import (
	"context"
	"errors"
	"net/http"
)

const (
	From          = "ASComply <no-reply@ascomply.com>"
	ResendBaseURL = "https://api.resend.com"
)

type Mode string

type Message struct{ To, Subject, HTML string }

type Sender interface {
	Send(ctx context.Context, msgs []Message) error
}

type SendError struct{ Status int }

func (e *SendError) Error() string { return "" }

var ErrNotConfigured = errors.New("stub")

type Resend struct{}

func NewResend(baseURL, key string, rt http.RoundTripper) *Resend { return &Resend{} }

func (*Resend) Send(ctx context.Context, msgs []Message) error { return nil }

type Capture struct{}

func (*Capture) Send(ctx context.Context, msgs []Message) error { return nil }
func (*Capture) Messages() []Message                            { return nil }

type Off struct{}

func (Off) Send(ctx context.Context, msgs []Message) error { return nil }

func ModeFromEnv(getenv func(string) string, preview bool) (Mode, string) { return "", "" }

func NewSender(mode Mode, key string, rt http.RoundTripper) Sender { return nil }
