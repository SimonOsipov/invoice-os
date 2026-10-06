package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

type spiedRequest struct{ Method, URL, Auth string }

// spyTransport records every request and answers 200.
type spyTransport struct {
	mu   sync.Mutex
	reqs []spiedRequest
}

func (s *spyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, spiedRequest{r.Method, r.URL.String(), r.Header.Get("Authorization")})
	s.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func (s *spyTransport) seen() []spiedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spiedRequest(nil), s.reqs...)
}

func getenvOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func textLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

var oneMail = []accountmail.Message{{To: "b@x.test", Subject: "s", HTML: "<p>h</p>"}}

func assertModeLine(t *testing.T, logs, mode string) {
	t.Helper()
	if !strings.Contains(logs, "tenancy: invite mail mode") || !strings.Contains(logs, "mode="+mode) || !strings.Contains(logs, "level=INFO") {
		t.Errorf("log = %q, want an INFO line %q with mode=%s", logs, "tenancy: invite mail mode", mode)
	}
}

func TestInviteSender_PreviewCapturesAndDiscardsTheKey(t *testing.T) {
	for _, env := range []string{"pr-123", "dev-pr-7"} {
		t.Run(env, func(t *testing.T) {
			spy := &spyTransport{}
			var logs bytes.Buffer
			env := getenvOf(map[string]string{"RAILWAY_ENVIRONMENT_NAME": env, "RESEND_SENDING_KEY": "k_live"})

			s := inviteSender(env, spy, textLogger(&logs))

			c, ok := s.(*accountmail.Capture)
			if !ok {
				t.Fatalf("sender = %T, want *accountmail.Capture", s)
			}
			if err := s.Send(context.Background(), oneMail); err != nil {
				t.Errorf("Send err = %v, want nil", err)
			}
			if got := spy.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) %+v, want none", len(got), got)
			}
			if got := c.Messages(); len(got) != 1 || got[0].To != "b@x.test" {
				t.Errorf("captured = %+v, want the one mail", got)
			}
			assertModeLine(t, logs.String(), "capture")
			if strings.Contains(logs.String(), "k_live") {
				t.Errorf("log holds the key: %s", logs.String())
			}
		})
	}
}

func TestInviteSender_ProductionWithKeySendsThroughTheTransport(t *testing.T) {
	spy := &spyTransport{}
	var logs bytes.Buffer
	env := getenvOf(map[string]string{"RAILWAY_ENVIRONMENT_NAME": "production", "RESEND_SENDING_KEY": "k_live"})

	s := inviteSender(env, spy, textLogger(&logs))

	if _, ok := s.(*accountmail.Resend); !ok {
		t.Fatalf("sender = %T, want *accountmail.Resend", s)
	}
	if err := s.Send(context.Background(), oneMail); err != nil {
		t.Fatalf("Send err = %v, want nil", err)
	}
	got := spy.seen()
	if len(got) != 1 {
		t.Fatalf("the transport saw %d request(s) %+v, want 1", len(got), got)
	}
	if got[0].Method != http.MethodPost || got[0].URL != "https://api.resend.com/emails/batch" || got[0].Auth != "Bearer k_live" {
		t.Errorf("request = %+v, want POST https://api.resend.com/emails/batch with Authorization: Bearer k_live", got[0])
	}
	assertModeLine(t, logs.String(), "real")
	if strings.Contains(logs.String(), "k_live") {
		t.Errorf("log holds the key: %s", logs.String())
	}
}

func TestInviteSender_ProductionWithoutKeyIsOff(t *testing.T) {
	spy := &spyTransport{}
	var logs bytes.Buffer
	env := getenvOf(map[string]string{"RAILWAY_ENVIRONMENT_NAME": "production"})

	s := inviteSender(env, spy, textLogger(&logs))

	if _, ok := s.(accountmail.Off); !ok {
		t.Fatalf("sender = %T, want accountmail.Off", s)
	}
	if err := s.Send(context.Background(), oneMail); !errors.Is(err, accountmail.ErrNotConfigured) {
		t.Errorf("Send err = %v, want ErrNotConfigured", err)
	}
	if got := spy.seen(); len(got) != 0 {
		t.Errorf("the transport saw %d request(s), want none", len(got))
	}
	assertModeLine(t, logs.String(), "off")
}
