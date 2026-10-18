package notifications

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const (
	blogTopic   = "topic-blog"
	blogSegment = "seg-blog"
)

func blogResendAt(v *fakeVendor) *BlogResend {
	return NewBlogResend(v.srv.URL, "rs-key", BlogKeys{TopicID: blogTopic, SegmentID: blogSegment}, nil)
}

func blogSub(email string) BlogSubscriber {
	return BlogSubscriber{Email: email, CategoryIDs: []string{"a", "b"}, Frequency: "weekly"}
}

// blogReplies answers GET with the given status and body and every write with 200.
func blogReplies(get int, getBody string) func(seenReq) (int, string) {
	return func(s seenReq) (int, string) {
		if s.Method == http.MethodGet {
			return get, getBody
		}
		return 200, `{}`
	}
}

func decodeBody(t *testing.T, s seenReq) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Body, &m); err != nil {
		t.Fatalf("%s body %q is not a JSON object: %v", s.label(), s.Body, err)
	}
	return m
}

func wantTopicsBody(t *testing.T, s seenReq) {
	t.Helper()
	var got []map[string]string
	if err := json.Unmarshal(s.Body, &got); err != nil {
		t.Fatalf("topics body %q: %v", s.Body, err)
	}
	want := []map[string]string{{"id": blogTopic, "subscription": "opt_in"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topics body = %v, want %v", got, want)
	}
}

func TestBlogResend_NewContactIsCreatedWithProperties(t *testing.T) {
	v := newVendor(t, blogReplies(404, `{}`))
	if err := blogResendAt(v).SyncBlog(t.Context(), blogSub(adaEmail)); err != nil {
		t.Fatalf("SyncBlog() err = %v", err)
	}
	want := []string{
		"GET /contacts/" + adaEmail, "POST /contacts",
		"POST /contacts/" + adaEmail + "/segments/" + blogSegment, "PATCH /contacts/" + adaEmail + "/topics",
	}
	if got := v.labels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	calls := v.calls()
	create := decodeBody(t, calls[1])
	wantProps := map[string]any{"blog_categories": ",a,b,", "blog_frequency": "weekly"}
	if create["email"] != adaEmail || !reflect.DeepEqual(create["properties"], wantProps) {
		t.Errorf("create body = %v, want email %s and properties %v", create, adaEmail, wantProps)
	}
	for _, c := range calls {
		if c.Auth != "Bearer rs-key" {
			t.Errorf("%s Authorization = %q", c.label(), c.Auth)
		}
	}
	wantTopicsBody(t, calls[3])
}

func TestBlogResend_ExistingContactIsPatched(t *testing.T) {
	v := newVendor(t, blogReplies(200, `{"unsubscribed":false}`))
	if err := blogResendAt(v).SyncBlog(t.Context(), blogSub(adaEmail)); err != nil {
		t.Fatalf("SyncBlog() err = %v", err)
	}
	want := []string{
		"GET /contacts/" + adaEmail, "PATCH /contacts/" + adaEmail,
		"POST /contacts/" + adaEmail + "/segments/" + blogSegment, "PATCH /contacts/" + adaEmail + "/topics",
	}
	if got := v.labels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	calls := v.calls()
	patch := decodeBody(t, calls[1])
	if _, ok := patch["properties"]; !ok {
		t.Errorf("patch body %v has no properties", patch)
	}
	if _, ok := patch["unsubscribed"]; ok {
		t.Errorf("patch body %v sends unsubscribed", patch)
	}
	wantTopicsBody(t, calls[3])
}

func TestBlogResend_GloballyUnsubscribedStaysUnsubscribed(t *testing.T) {
	v := newVendor(t, blogReplies(200, `{"unsubscribed":true}`))
	if err := blogResendAt(v).SyncBlog(t.Context(), blogSub(adaEmail)); err != nil {
		t.Fatalf("SyncBlog() err = %v", err)
	}
	want := []string{
		"GET /contacts/" + adaEmail, "PATCH /contacts/" + adaEmail,
		"POST /contacts/" + adaEmail + "/segments/" + blogSegment, "PATCH /contacts/" + adaEmail + "/topics",
	}
	if got := v.labels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	calls := v.calls()
	patch := decodeBody(t, calls[1])
	if _, ok := patch["properties"]; !ok {
		t.Errorf("patch body %v has no properties", patch)
	}
	if _, ok := patch["unsubscribed"]; ok {
		t.Errorf("patch body %v sends unsubscribed", patch)
	}
	wantTopicsBody(t, calls[3])
}

func TestBlogResend_StopsAtFirstFailure(t *testing.T) {
	v := newVendor(t, func(s seenReq) (int, string) {
		switch s.Method {
		case http.MethodGet:
			return 200, `{"unsubscribed":false}`
		case http.MethodPatch:
			return 500, `{}`
		}
		return 200, `{}`
	})
	err := blogResendAt(v).SyncBlog(t.Context(), blogSub(adaEmail))
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 500 {
		t.Fatalf("err = %v, want *DeliveryError{Status:500}", err)
	}
	if got, want := v.labels(), []string{"GET /contacts/" + adaEmail, "PATCH /contacts/" + adaEmail}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestBlogResend_NeverSendsUnsubscribedOptOutOrSuppression(t *testing.T) {
	for _, c := range []struct {
		name string
		get  int
		body string
	}{
		{"create", 404, `{}`},
		{"update subscribed", 200, `{"unsubscribed":false}`},
		{"update unsubscribed", 200, `{"unsubscribed":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := newVendor(t, blogReplies(c.get, c.body))
			if err := blogResendAt(v).SyncBlog(t.Context(), blogSub(adaEmail)); err != nil {
				t.Fatalf("SyncBlog() err = %v", err)
			}
			for _, s := range v.calls() {
				if strings.Contains(string(s.Body), "opt_out") || strings.Contains(string(s.Body), "unsubscribed") {
					t.Errorf("%s body %q holds opt_out or unsubscribed", s.label(), s.Body)
				}
				if strings.Contains(s.path(), "/suppressions") {
					t.Errorf("request %s touches suppressions", s.label())
				}
			}
		})
	}
}

func TestBlogResend_ContactUnsubscribedReadsTheFlag(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr int
	}{
		{"unsubscribed", 200, `{"unsubscribed":true}`, true, 0},
		{"subscribed", 200, `{"unsubscribed":false}`, false, 0},
		{"missing", 404, `{}`, false, 0},
		{"outage", 500, `{}`, false, 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newVendor(t, blogReplies(c.status, c.body))
			got, err := blogResendAt(v).ContactUnsubscribed(t.Context(), adaEmail)
			if c.wantErr != 0 {
				var de *DeliveryError
				if !errors.As(err, &de) || de.Status != c.wantErr {
					t.Fatalf("err = %v, want *DeliveryError{Status:%d}", err, c.wantErr)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
			if want := []string{"GET /contacts/" + url.PathEscape(adaEmail)}; !reflect.DeepEqual(v.labels(), want) {
				t.Errorf("requests = %v, want %v", v.labels(), want)
			}
		})
	}
}

func TestBlogResend_EmailPathSegmentIsPathEscaped(t *testing.T) {
	const email = "a/b?c+d@e.co"
	esc := url.PathEscape(email)
	if esc != "a%2Fb%3Fc+d@e.co" {
		t.Fatalf("PathEscape = %q, the premise of this test changed", esc)
	}
	v := newVendor(t, blogReplies(200, `{"unsubscribed":false}`))
	if err := blogResendAt(v).SyncBlog(t.Context(), blogSub(email)); err != nil {
		t.Fatalf("SyncBlog() err = %v", err)
	}
	if _, err := blogResendAt(v).ContactUnsubscribed(t.Context(), email); err != nil {
		t.Fatalf("ContactUnsubscribed() err = %v", err)
	}
	calls := v.calls()
	if len(calls) != 5 {
		t.Fatalf("requests = %v, want 5", v.labels())
	}
	for _, s := range calls {
		u, err := url.ParseRequestURI(s.URI)
		if err != nil {
			t.Fatalf("parse %q: %v", s.URI, err)
		}
		seg := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/contacts/"), "/")[0]
		if seg != esc {
			t.Errorf("%s segment = %q, want %q", s.label(), seg, esc)
		}
		if !strings.HasPrefix(u.Path, "/contacts/"+email) {
			t.Errorf("%s decoded path = %q, want it to decode back to the email", s.label(), u.Path)
		}
	}
}
