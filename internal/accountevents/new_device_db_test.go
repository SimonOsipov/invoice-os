package accountevents

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// failSender returns err from every Send.
type failSender struct{ err error }

func (f failSender) Send(context.Context, []accountmail.Message) error { return f.err }

func (e *env) ndWorker(s accountmail.Sender) (*NewDeviceWorker, func() string) {
	log, _, raw := capture()
	return &NewDeviceWorker{Pool: e.app, Sender: s, Logger: log}, raw
}

func ndJob(user, email, browser, os string) *river.Job[NewDeviceArgs] {
	return &river.Job[NewDeviceArgs]{Args: NewDeviceArgs{
		UserID: user, Email: email, Browser: browser, OS: os, IP: "203.0.113.7",
		OccurredAt: time.Date(2026, 10, 29, 13, 5, 0, 0, time.UTC).Format(time.RFC3339),
	}}
}

func mustWork(t *testing.T, w *NewDeviceWorker, j *river.Job[NewDeviceArgs]) {
	t.Helper()
	if err := w.Work(context.Background(), j); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func TestNewDevice_FirstPairIsSilent(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	if n := e.pairs(t, u); n != 1 {
		t.Errorf("pairs = %d, want 1", n)
	}
	if m := c.Messages(); len(m) != 0 {
		t.Errorf("%d message(s) sent for the first pair, want 0", len(m))
	}
}

func TestNewDevice_KnownPairIsSilent(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	if n := e.pairs(t, u); n != 1 {
		t.Errorf("pairs = %d, want 1", n)
	}
	if m := c.Messages(); len(m) != 0 {
		t.Errorf("%d message(s) sent for a known pair, want 0", len(m))
	}
}

func TestNewDevice_NewPairSendsOneMail(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	mustWork(t, w, ndJob(u, mail, "Firefox", "Windows"))
	if n := e.pairs(t, u); n != 2 {
		t.Errorf("pairs = %d, want 2", n)
	}
	m := c.Messages()
	if len(m) != 1 || m[0].To != mail || m[0].Subject != "New sign-in to your ASComply account" {
		t.Fatalf("messages = %+v, want one to %s with the fixed subject", m, mail)
	}
	for _, want := range []string{"Firefox on Windows", "(IP 203.0.113.7)", "29 October 2026, 14:05 WAT"} {
		if !strings.Contains(m[0].HTML, want) {
			t.Errorf("mail lacks %q", want)
		}
	}
}

func TestNewDevice_UnknownPairCountsAsAPair(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	mustWork(t, w, ndJob(u, mail, "", ""))
	if m := c.Messages(); len(m) != 1 || !strings.Contains(m[0].HTML, "Unknown device") {
		t.Fatalf("messages = %+v, want one naming Unknown device", m)
	}
	mustWork(t, w, ndJob(u, mail, "", ""))
	if m := c.Messages(); len(m) != 1 {
		t.Errorf("%d messages after a second unknown pair, want still 1", len(m))
	}
}

func TestNewDevice_SendFailureFailsTheOnlyAttempt(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	w, raw := e.ndWorker(&accountmail.Capture{})
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))

	w.Sender = failSender{&accountmail.SendError{Status: 500}}
	err := w.Work(context.Background(), ndJob(u, mail, "Firefox", "Windows"))
	var se *accountmail.SendError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("Work error = %v, want the SendError", err)
	}
	if n := e.pairs(t, u); n != 2 {
		t.Errorf("pairs = %d, want 2 (the pair stays recorded)", n)
	}
	if got := (NewDeviceArgs{}).InsertOpts().MaxAttempts; got != 1 {
		t.Errorf("MaxAttempts = %d, want 1", got)
	}
	if !strings.Contains(raw(), "send failed") {
		t.Errorf("the failure is not logged: %q", raw())
	}
	if strings.Contains(raw(), "@") || strings.Contains(raw(), mail) {
		t.Errorf("log holds an address: %s", raw())
	}
	if err == nil || strings.Contains(err.Error(), "@") {
		t.Errorf("error holds an address: %v", err)
	}
}

func TestNewDevice_OffSenderCountsAsAFailure(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	w, _ := e.ndWorker(accountmail.Off{})
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	err := w.Work(context.Background(), ndJob(u, mail, "Firefox", "Windows"))
	if !errors.Is(err, accountmail.ErrNotConfigured) {
		t.Fatalf("Work error = %v, want ErrNotConfigured", err)
	}
	if n := e.pairs(t, u); n != 2 {
		t.Errorf("pairs = %d, want 2", n)
	}
}

func TestNewDevice_AnotherAccountsPairsDoNotMakeThisOneKnown(t *testing.T) {
	e := newEnv(t)
	u, v, mail := e.user(), e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(v, e.email("v"), "Chrome", "macOS"))
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	if m := c.Messages(); len(m) != 0 {
		t.Errorf("%d message(s) for U's first pair while V holds the same pair, want 0", len(m))
	}
	if e.pairs(t, u) != 1 || e.pairs(t, v) != 1 {
		t.Errorf("pairs U=%d V=%d, want 1 and 1", e.pairs(t, u), e.pairs(t, v))
	}
}

func TestNewDevice_ParallelSignInsFromOneNewPairSendOneMail(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	const n = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- w.Work(context.Background(), ndJob(u, mail, "Firefox", "Windows"))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Work: %v", err)
		}
	}
	if m := c.Messages(); len(m) != 1 {
		t.Errorf("%d messages for %d parallel sign-ins from one new pair, want 1", len(m), n)
	}
	if got := e.pairs(t, u); got != 2 {
		t.Errorf("pairs = %d, want 2", got)
	}
}

func TestNewDevice_BadSignInTimeCancelsWithoutMail(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	j := ndJob(u, mail, "Firefox", "Windows")
	j.Args.OccurredAt = "not a time"
	var cancel *river.JobCancelError
	if err := w.Work(context.Background(), j); !errors.As(err, &cancel) {
		t.Fatalf("Work error = %v, want a JobCancel", err)
	}
	if m := c.Messages(); len(m) != 0 {
		t.Errorf("%d message(s) for an unreadable time, want 0", len(m))
	}
}

func TestNewDevice_EmptyIPRendersUnknownIP(t *testing.T) {
	e := newEnv(t)
	u, mail := e.user(), e.email("nd")
	c := &accountmail.Capture{}
	w, _ := e.ndWorker(c)
	mustWork(t, w, ndJob(u, mail, "Chrome", "macOS"))
	j := ndJob(u, mail, "Firefox", "Windows")
	j.Args.IP = ""
	mustWork(t, w, j)
	m := c.Messages()
	if len(m) != 1 || !strings.Contains(m[0].HTML, "(Unknown IP)") {
		t.Fatalf("messages = %+v, want one naming (Unknown IP)", m)
	}
}
