package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const notFoundBody = `{"error":"not found"}` + "\n"

// publicFake records every store call; one fake serves all four public store methods.
type publicFake struct {
	calls   int
	todays  []string
	id      string
	version int64

	cats  []Category
	posts []PublicPost
	post  PublicPost
	ctype string
	bytes []byte
	err   error
}

func (f *publicFake) categories(_ context.Context, today string) ([]Category, error) {
	f.calls++
	f.todays = append(f.todays, today)
	return f.cats, f.err
}

func (f *publicFake) listPosts(_ context.Context, today string) ([]PublicPost, error) {
	f.calls++
	f.todays = append(f.todays, today)
	return f.posts, f.err
}

func (f *publicFake) getPost(_ context.Context, id, today string) (PublicPost, error) {
	f.calls++
	f.id = id
	f.todays = append(f.todays, today)
	return f.post, f.err
}

func (f *publicFake) cover(_ context.Context, id string, version int64, today string) (string, []byte, error) {
	f.calls++
	f.id, f.version = id, version
	f.todays = append(f.todays, today)
	return f.ctype, f.bytes, f.err
}

type publicCase struct {
	name   string
	target string
	build  func(f *publicFake, log *slog.Logger) http.HandlerFunc
	// pattern carries the wildcards, so PathValue works without a mux in front.
	pattern string
}

func publicCases() []publicCase {
	return []publicCase{
		{"PublicCategories", "/internal/blog/categories", func(f *publicFake, l *slog.Logger) http.HandlerFunc {
			return PublicCategoriesHandler(f.categories, l)
		}, "GET /internal/blog/categories"},
		{"PublicPosts", "/internal/blog/posts", func(f *publicFake, l *slog.Logger) http.HandlerFunc {
			return PublicPostsHandler(f.listPosts, l)
		}, "GET /internal/blog/posts"},
		{"PublicPost", "/internal/blog/posts/p1", func(f *publicFake, l *slog.Logger) http.HandlerFunc {
			return PublicPostHandler(f.getPost, l)
		}, "GET /internal/blog/posts/{id}"},
		{"PublicCover", "/internal/blog/covers/p1/1700000000000000", func(f *publicFake, l *slog.Logger) http.HandlerFunc {
			return PublicCoverHandler(f.cover, l)
		}, "GET /internal/blog/covers/{id}/{version}"},
	}
}

// serveMux routes through a real mux so the {id} and {version} path values are set.
func serveMux(c publicCase, h http.HandlerFunc, mutate func(*http.Request)) *httptest.ResponseRecorder {
	m := http.NewServeMux()
	m.HandleFunc(c.pattern, h)
	req := httptest.NewRequest("GET", c.target, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	return rec
}

func TestPublicCategoriesHandler_ListsCategories(t *testing.T) {
	f := &publicFake{cats: []Category{
		{ID: "c1", CategoryInput: CategoryInput{Name: "One", Headline: "H1", Intro: "I1", Icon: "globe", Tone: 1}, PostCount: 3},
		{ID: "c2", CategoryInput: CategoryInput{Name: "Two", Headline: "H2", Intro: "I2", Icon: "book", Tone: 2}, PostCount: 0},
	}}
	c := publicCases()[0]
	rec := serveMux(c, c.build(f, discard), nil)
	if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusOK || ct != "application/json" {
		t.Fatalf("status %d content-type %q, want 200 application/json (%s)", rec.Code, ct, rec.Body.String())
	}
	var got struct {
		Categories []map[string]any `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("%d categories, want 2 (%s)", len(got.Categories), rec.Body.String())
	}
	if got.Categories[0]["post_count"] != float64(3) || got.Categories[1]["post_count"] != float64(0) || got.Categories[0]["id"] != "c1" {
		t.Errorf("categories = %v, want c1 post_count 3 then c2 post_count 0", got.Categories)
	}
	if f.calls != 1 {
		t.Errorf("store called %d times, want 1", f.calls)
	}
}

func TestPublicPostsHandler_WireKeysAreExactlyTheNine(t *testing.T) {
	cover := "/blog/covers/p1/42"
	f := &publicFake{posts: []PublicPost{{
		ID: "p1", CategoryID: "c1", PublishDate: "2026-01-02", Title: "T", Summary: "S", AuthorName: "A",
		Featured: true, Cover: &cover,
		Body: []Block{{Type: "note", Kind: "tip", Title: "Hi", Text: "x"}},
	}}}
	c := publicCases()[1]
	rec := serveMux(c, c.build(f, discard), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Posts []map[string]json.RawMessage `json:"posts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Posts) != 1 {
		t.Fatalf("%d posts, want 1 (%s)", len(got.Posts), rec.Body.String())
	}
	var keys []string
	for k := range got.Posts[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"author_name", "body", "category_id", "cover", "featured", "id", "publish_date", "summary", "title"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if string(got.Posts[0]["cover"]) != `"/blog/covers/p1/42"` {
		t.Errorf("cover = %s, want the path", got.Posts[0]["cover"])
	}
	if !strings.Contains(string(got.Posts[0]["body"]), `"type":"note"`) {
		t.Errorf("body = %s, want the note block", got.Posts[0]["body"])
	}
}

func TestPublicPostsHandler_EmptyListIsAnArray(t *testing.T) {
	c := publicCases()[1]
	rec := serveMux(c, c.build(&publicFake{}, discard), nil)
	wantJSON(t, rec, http.StatusOK, `{"posts":[]}`)
}

func TestPublicCategoriesHandler_EmptyListIsAnArray(t *testing.T) {
	c := publicCases()[0]
	rec := serveMux(c, c.build(&publicFake{}, discard), nil)
	wantJSON(t, rec, http.StatusOK, `{"categories":[]}`)
}

func TestPublicPostsHandler_NoCoverIsNull(t *testing.T) {
	f := &publicFake{posts: []PublicPost{{ID: "p1", Body: []Block{{Type: "p", Text: "x"}}}}}
	c := publicCases()[1]
	rec := serveMux(c, c.build(f, discard), nil)
	var got struct {
		Posts []map[string]json.RawMessage `json:"posts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Posts) != 1 {
		t.Fatalf("status %d body %s err %v, want one post", rec.Code, rec.Body.String(), err)
	}
	if v, ok := got.Posts[0]["cover"]; !ok || string(v) != "null" {
		t.Errorf("cover = %q present=%v, want present null", v, ok)
	}
}

func TestPublicPostHandler_ServesOnePostAndPassesTheID(t *testing.T) {
	f := &publicFake{post: PublicPost{ID: "p1", Title: "T", Body: []Block{{Type: "p", Text: "x"}}}}
	c := publicCases()[2]
	rec := serveMux(c, c.build(f, discard), nil)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s err %v, want 200 post", rec.Code, rec.Body.String(), err)
	}
	if string(got["id"]) != `"p1"` || string(got["cover"]) != "null" {
		t.Errorf("post = %s, want id p1 and cover null", rec.Body.String())
	}
	if f.id != "p1" {
		t.Errorf("store id = %q, want p1", f.id)
	}
}

func TestPublicHandlers_PassTodayInLagos(t *testing.T) {
	for _, c := range publicCases() {
		t.Run(c.name, func(t *testing.T) {
			f := &publicFake{ctype: "image/png", bytes: []byte("x")}
			h := c.build(f, discard)
			before := LagosDate(time.Now())
			// A client date in a query or header must not reach the store.
			rec := serveMux(c, h, func(r *http.Request) {
				r.URL.RawQuery = "today=2000-01-01"
				r.Header.Set("X-Today", "2000-01-01")
			})
			after := LagosDate(time.Now())
			if rec.Code != http.StatusOK || f.calls != 1 {
				t.Fatalf("status %d, store calls %d; want 200 and 1", rec.Code, f.calls)
			}
			if got := f.todays[0]; got != before && got != after {
				t.Errorf("today = %q, want LagosDate(now) (%q or %q)", got, before, after)
			}
		})
	}
}

func TestPublicPostHandler_NotFoundIsUniform(t *testing.T) {
	c := publicCases()[2]
	for _, err := range []error{ErrPostNotFound, errors.Join(errors.New("ctx"), ErrPostNotFound)} {
		rec := serveMux(c, c.build(&publicFake{err: err}, discard), nil)
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("err %v = %d %q, want 404 %q", err, rec.Code, rec.Body.String(), notFoundBody)
		}
	}
}

func TestPublicCoverHandler_NotFoundIsUniform(t *testing.T) {
	c := publicCases()[3]
	rec := serveMux(c, c.build(&publicFake{err: ErrPostNotFound}, discard), nil)
	if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
		t.Errorf("= %d %q, want 404 %q", rec.Code, rec.Body.String(), notFoundBody)
	}
}

func TestPublicCoverHandler_BadVersionIs404WithoutAStoreCall(t *testing.T) {
	c := publicCases()[3]
	stale := serveMux(c, c.build(&publicFake{err: ErrPostNotFound}, discard), nil)
	if stale.Code != http.StatusNotFound {
		t.Fatalf("control: ErrPostNotFound = %d, want 404", stale.Code)
	}
	for _, v := range []string{"abc", "-1", "99999999999999999999"} {
		f := &publicFake{ctype: "image/png", bytes: []byte("x")}
		bad := c
		bad.target = "/internal/blog/covers/p1/" + v
		rec := serveMux(bad, c.build(f, discard), nil)
		if rec.Code != http.StatusNotFound || rec.Body.String() != stale.Body.String() || rec.Header().Get("Content-Type") != stale.Header().Get("Content-Type") {
			t.Errorf("version %q = %d %q, want the ErrPostNotFound 404 (%q)", v, rec.Code, rec.Body.String(), stale.Body.String())
		}
		if f.calls != 0 {
			t.Errorf("version %q called the store %d times, want 0", v, f.calls)
		}
	}
}

func TestPublicCoverHandler_ServesTheBytesWithTheirType(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	f := &publicFake{ctype: "image/png", bytes: png}
	c := publicCases()[3]
	rec := serveMux(c, c.build(f, discard), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Errorf("body = %v, want %v", rec.Body.Bytes(), png)
	}
	if f.id != "p1" || f.version != 1700000000000000 {
		t.Errorf("store got id %q version %d, want p1 and 1700000000000000", f.id, f.version)
	}
}

func TestPublicHandlers_ProxiedRequestIs404(t *testing.T) {
	for _, c := range publicCases() {
		t.Run(c.name, func(t *testing.T) {
			f := &publicFake{}
			rec := serveMux(c, c.build(f, discard), func(r *http.Request) { r.Header["X-User-ID"] = []string{""} })
			if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
				t.Errorf("= %d %q, want 404 %q", rec.Code, rec.Body.String(), notFoundBody)
			}
			if f.calls != 0 {
				t.Errorf("store called %d times, want 0", f.calls)
			}
			ok := serveMux(c, c.build(&publicFake{ctype: "image/png", bytes: []byte("x")}, discard), nil)
			if ok.Code != http.StatusOK {
				t.Errorf("control without X-User-ID = %d, want 200", ok.Code)
			}
		})
	}
}

func TestPublicHandlers_StoreFailureIs500(t *testing.T) {
	for _, c := range publicCases() {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			rec := serveMux(c, c.build(&publicFake{err: errors.New("boom")}, log), nil)
			if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"internal error"}` {
				t.Errorf("= %d %q, want 500 internal error", rec.Code, rec.Body.String())
			}
			if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), "boom") || !strings.Contains(logs.String(), "blog: public") {
				t.Errorf("log = %q, want one ERROR line naming boom and \"blog: public\"", logs.String())
			}
		})
	}
}

func TestPublicHandlers_TodayIsTheLagosDateNotUTC(t *testing.T) {
	// 23:30 UTC on the 17th is already the 18th in Lagos (UTC+1).
	orig := now
	now = func() time.Time { return time.Date(2026, 10, 17, 23, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { now = orig })
	for _, c := range publicCases() {
		t.Run(c.name, func(t *testing.T) {
			f := &publicFake{ctype: "image/png", bytes: []byte("x")}
			serveMux(c, c.build(f, discard), nil)
			if len(f.todays) != 1 || f.todays[0] != "2026-10-18" {
				t.Errorf("today = %v, want [2026-10-18]", f.todays)
			}
		})
	}
}
