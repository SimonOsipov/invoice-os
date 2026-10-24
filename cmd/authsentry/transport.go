package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

const (
	bufferCap     = 100
	backoffBase   = time.Second
	backoffCap    = 5 * time.Minute
	rateLimitCap  = time.Hour
	attemptLimit  = 10 * time.Second
	flushPollStep = 10 * time.Millisecond
)

// bufferedTransport queues events in memory and posts them from one goroutine.
// SendEvent never blocks. A full buffer drops the oldest event.
//
// ceiling: delivery waits for the next auth request while CPU is throttled; revisit if an auth event arrives more than 10 min after its log line.
type bufferedTransport struct {
	client *http.Client
	now    func() time.Time
	sleep  func(time.Duration)

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*sentry.Event
	dsn    *sentry.Dsn
	closed bool
	done   chan struct{}
	kick   chan struct{}
}

func newBufferedTransport(client *http.Client, now func() time.Time, sleep func(time.Duration)) *bufferedTransport {
	t := &bufferedTransport{client: client, now: now, sleep: sleep, done: make(chan struct{}), kick: make(chan struct{}, 1)}
	t.cond = sync.NewCond(&t.mu)
	if sleep == nil {
		t.sleep = func(d time.Duration) {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-t.done:
			}
		}
	}
	go t.run()
	return t
}

func (t *bufferedTransport) Configure(options sentry.ClientOptions) {
	dsn, err := sentry.NewDsn(options.Dsn)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.dsn = dsn
	t.mu.Unlock()
}

func (t *bufferedTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if len(t.queue) >= bufferCap {
		t.queue = append(t.queue[:0], t.queue[1:]...)
	}
	t.queue = append(t.queue, event)
	t.cond.Signal()
}

func (t *bufferedTransport) Flush(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return t.FlushWithContext(ctx)
}

func (t *bufferedTransport) FlushWithContext(ctx context.Context) bool {
	tick := time.NewTicker(flushPollStep)
	defer tick.Stop()
	kicked := false
	for {
		t.mu.Lock()
		empty := len(t.queue) == 0
		t.mu.Unlock()
		if empty {
			return true
		}
		if !kicked {
			kicked = true
			select {
			case t.kick <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.done:
			return false
		case <-tick.C:
		}
	}
}

func (t *bufferedTransport) Close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.done)
		t.cond.Broadcast()
	}
	t.mu.Unlock()
}

type outcome int

const (
	delivered outcome = iota
	dropped
	retry
)

func (t *bufferedTransport) run() {
	failures := 0
	for {
		t.mu.Lock()
		for len(t.queue) == 0 && !t.closed {
			select {
			case <-t.kick:
			default:
			}
			t.cond.Wait()
		}
		if t.closed {
			t.mu.Unlock()
			return
		}
		head, dsn := t.queue[0], t.dsn
		t.mu.Unlock()

		res, wait := dropped, time.Duration(0)
		if dsn != nil {
			res, wait = t.deliver(head, dsn)
		}
		if res == retry {
			d := min(backoffBase<<min(failures, 20), backoffCap)
			failures++
			for t.wait(max(d, min(wait, rateLimitCap))) {
				t.flushPass()
			}
			continue
		}
		failures = 0
		t.mu.Lock()
		if len(t.queue) > 0 && t.queue[0] == head {
			t.queue = append(t.queue[:0], t.queue[1:]...)
		}
		t.mu.Unlock()
	}
}

// wait sleeps d and reports whether a Flush cut it short.
func (t *bufferedTransport) wait(d time.Duration) bool {
	over := make(chan struct{})
	go func() {
		defer close(over)
		t.sleep(d)
	}()
	select {
	case <-over:
	case <-t.kick:
		return true
	case <-t.done:
	}
	return false
}

// flushPass makes one attempt at each queued event, newest first, ignoring any wait.
// The exit event is the newest, and a Flush has only seconds to deliver it.
func (t *bufferedTransport) flushPass() {
	t.mu.Lock()
	snap, dsn := append([]*sentry.Event(nil), t.queue...), t.dsn
	t.mu.Unlock()
	if dsn == nil {
		return
	}
	for i := len(snap) - 1; i >= 0; i-- {
		select {
		case <-t.done:
			return
		default:
		}
		if res, _ := t.deliver(snap[i], dsn); res == retry {
			continue
		}
		t.mu.Lock()
		if j := slices.Index(t.queue, snap[i]); j >= 0 {
			t.queue = slices.Delete(t.queue, j, j+1)
		}
		t.mu.Unlock()
	}
}

// deliver posts one event. A panic drops that event only.
func (t *bufferedTransport) deliver(event *sentry.Event, dsn *sentry.Dsn) (res outcome, wait time.Duration) {
	defer func() {
		if recover() != nil {
			res, wait = dropped, 0
		}
	}()

	body, err := json.Marshal(event)
	if err != nil {
		return dropped, 0
	}
	var buf bytes.Buffer
	header, _ := json.Marshal(map[string]string{
		"event_id": string(event.EventID),
		"sent_at":  t.now().UTC().Format(time.RFC3339),
		"dsn":      dsn.String(),
	})
	buf.Write(header)
	fmt.Fprintf(&buf, "\n{\"type\":\"event\",\"length\":%d}\n", len(body))
	buf.Write(body)
	buf.WriteByte('\n')

	ctx, cancel := context.WithTimeout(context.Background(), attemptLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dsn.GetAPIURL().String(), &buf)
	if err != nil {
		return dropped, 0
	}
	for k, v := range dsn.RequestHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/x-sentry-envelope")

	resp, err := t.client.Do(req)
	if err != nil {
		return retry, 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return delivered, 0
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return retry, retryAfter(resp.Header)
	default:
		return dropped, 0
	}
}

// retryAfter reads Retry-After seconds and the X-Sentry-Rate-Limits retry-after values.
func retryAfter(h http.Header) time.Duration {
	var longest time.Duration
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
		longest = time.Duration(s) * time.Second
	}
	for _, group := range strings.Split(h.Get("X-Sentry-Rate-Limits"), ",") {
		secs, _, _ := strings.Cut(strings.TrimSpace(group), ":")
		if s, err := strconv.Atoi(secs); err == nil && s > 0 {
			longest = max(longest, time.Duration(s)*time.Second)
		}
	}
	return longest
}
