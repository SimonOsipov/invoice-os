package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ceiling: in-process counts; a restart clears them and replicas do not share them. Revisit when the gateway runs more than one replica.
// ceiling: clients behind one NAT share the per-IP caps; an attacker can spend a victim address's 3 subscribes an hour. Revisit if abuse reports name a shared-IP block or a victim address.
const (
	BlogSubscribePerIP    = 10
	BlogSubscribePerEmail = 3
	BlogConfirmPerIP      = 30
	BlogSubscribeWindow   = time.Hour
	BlogSubscribeMaxKeys  = 10_000
)

const (
	maxBlogSubscribeBytes = 4096
	maxBlogConfirmBytes   = 1024
	blogUpstreamTimeout   = 5 * time.Second
	// Above the 10 s accountmail Resend timeout: notifications mails inline after it commits.
	blogSubscribeTimeout = 15 * time.Second
	maxBlogAnswerBytes   = 4096
)

var (
	blogCategoryIDRE   = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
	blogConfirmTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	blogFrequencies    = []string{"weekly", "every_post", "monthly"}
)

// BlogSubscribeRoutes holds the public newsletter handlers.
type BlogSubscribeRoutes struct{ Subscribe, Confirm http.Handler }

// BlogSubscribeHandlers serves POST /blog/subscriptions and its confirm by forwarding to notifications at base.
// Subscribe answers every valid request alike, no earlier than minResponse after it arrives.
// Neither the email nor the token reaches a log line.
func BlogSubscribeHandlers(base *url.URL, client *http.Client, gatewayToken string, minResponse time.Duration, perIP, perEmail, confirmPerIP *SignInThrottle, enforce bool, log *slog.Logger) BlogSubscribeRoutes {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := &blogSubscriber{base: base, client: &c, token: gatewayToken, minResponse: minResponse,
		perIP: perIP, perEmail: perEmail, confirmPerIP: confirmPerIP, enforce: enforce, log: log}
	return BlogSubscribeRoutes{Subscribe: http.HandlerFunc(s.subscribe), Confirm: http.HandlerFunc(s.confirm)}
}

type blogSubscriber struct {
	base                          *url.URL
	client                        *http.Client
	token                         string
	minResponse                   time.Duration
	perIP, perEmail, confirmPerIP *SignInThrottle
	enforce                       bool
	log                           *slog.Logger
}

type blogSubscribeBody struct {
	Email       string   `json:"email"`
	CategoryIDs []string `json:"category_ids"`
	Frequency   string   `json:"frequency"`
}

// blogPostOnly writes the 405 and reports false for any other method.
func blogPostOnly(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// validateBlogSubscribe normalises b and returns the 400 message, or "".
func validateBlogSubscribe(b *blogSubscribeBody) string {
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if n := len(b.Email); n < 3 || n > maxEmailBytes || strings.Count(b.Email, "@") != 1 ||
		strings.IndexFunc(b.Email, unicode.IsSpace) >= 0 || strings.ContainsRune(b.Email, 0) {
		return "email is invalid"
	}
	if n := len(b.CategoryIDs); n < 1 || n > 50 {
		return "category_ids must have 1 to 50 entries"
	}
	for _, id := range b.CategoryIDs {
		if !blogCategoryIDRE.MatchString(id) {
			return "category_ids is invalid"
		}
	}
	slices.Sort(b.CategoryIDs)
	b.CategoryIDs = slices.Compact(b.CategoryIDs)
	if !slices.Contains(blogFrequencies, b.Frequency) {
		return "frequency is invalid"
	}
	return ""
}

// post sends body to notifications and returns the status and the capped answer body; status 0 is a transport error.
func (s *blogSubscriber) post(r *http.Request, path string, timeout time.Duration, body any) (int, []byte) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base.JoinPath("internal", "blog", path).String(), bytes.NewReader(raw))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxBlogAnswerBytes))
	return resp.StatusCode, answer
}

func (s *blogSubscriber) subscribe(w http.ResponseWriter, r *http.Request) {
	if !blogPostOnly(w, r) {
		return
	}
	start := time.Now()
	var in blogSubscribeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlogSubscribeBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateBlogSubscribe(&in); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// IP first: a request refused for its IP spends no email count.
	key, source := clientKey(r)
	ipHeld := s.perIP.Reserve(key)
	emailHeld := false
	limited := func(limit string) bool {
		s.log.WarnContext(r.Context(), "blog-subscribe: limit reached",
			slog.String("limit", limit), slog.String("key_source", source), slog.Bool("enforced", s.enforce))
		return s.enforce
	}
	refused := false
	if !ipHeld {
		refused = limited("ip")
	} else if emailHeld = s.perEmail.Reserve(in.Email); !emailHeld {
		refused = limited("email")
	}

	status, answer := http.StatusAccepted, `{"status":"accepted"}`
	var upstream time.Duration
	if !refused {
		code, _ := s.post(r, "subscriptions", blogSubscribeTimeout, in)
		upstream = time.Since(start)
		switch {
		case code == http.StatusAccepted:
		case code == http.StatusBadRequest:
			// notifications mailed nothing, so both reservations come back.
			if ipHeld {
				s.perIP.Refund(key)
			}
			if emailHeld {
				s.perEmail.Refund(in.Email)
			}
			writeError(w, http.StatusBadRequest, "request is invalid")
			return
		case code == http.StatusServiceUnavailable:
			status, answer = http.StatusServiceUnavailable, `{"error":"subscriptions are unavailable"}`
		default:
			s.log.WarnContext(r.Context(), "blog-subscribe: upstream failed", slog.Int("upstream_status", code))
			status, answer = http.StatusBadGateway, `{"error":"subscribe is unavailable"}`
		}
	}

	if holdMinimum(r.Context(), s.log, "blog-subscribe: timing", start, upstream, s.minResponse) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer+"\n")
	}
}

func (s *blogSubscriber) confirm(w http.ResponseWriter, r *http.Request) {
	if !blogPostOnly(w, r) {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlogConfirmBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	gone := func() { writeError(w, http.StatusGone, "link expired or used") }
	if !blogConfirmTokenRE.MatchString(in.Token) {
		gone()
		return
	}
	key, source := clientKey(r)
	if !s.confirmPerIP.Reserve(key) {
		s.log.WarnContext(r.Context(), "blog-confirm: limit reached",
			slog.String("limit", "ip"), slog.String("key_source", source), slog.Bool("enforced", s.enforce))
		if s.enforce {
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
	}

	code, answer := s.post(r, "subscriptions/confirm", blogUpstreamTimeout, map[string]string{"token": in.Token})
	switch code {
	case http.StatusOK:
		// Only the two known fields travel on; anything else upstream sends stays here.
		var up struct {
			Everywhere bool `json:"unsubscribed_everywhere"`
		}
		_ = json.Unmarshal(answer, &up)
		writeJSON(w, http.StatusOK, map[string]any{"status": "confirmed", "unsubscribed_everywhere": up.Everywhere})
	case http.StatusGone:
		gone()
	case http.StatusServiceUnavailable:
		writeError(w, http.StatusServiceUnavailable, "subscriptions are unavailable")
	default:
		s.log.WarnContext(r.Context(), "blog-confirm: upstream failed", slog.Int("upstream_status", code))
		writeError(w, http.StatusBadGateway, "confirm is unavailable")
	}
}
