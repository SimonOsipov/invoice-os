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

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var discard = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

func serve(h http.HandlerFunc, method, id, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func wantJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	got := strings.TrimSpace(rec.Body.String())
	if rec.Code != status {
		t.Fatalf("status = %d (%s), want %d", rec.Code, got, status)
	}
	var a, b any
	if json.Unmarshal([]byte(got), &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !reflect.DeepEqual(a, b) {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func errBody(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

type postFake struct {
	calls int
	got   PostInput
	id    string
	post  Post
	err   error
}

func (f *postFake) create(_ context.Context, in PostInput) (Post, error) {
	f.calls++
	f.got = in
	return f.post, f.err
}

func (f *postFake) update(_ context.Context, id string, in PostInput) (Post, error) {
	f.calls++
	f.id, f.got = id, in
	return f.post, f.err
}

func TestPostHandlers_RefuseMalformedBodies(t *testing.T) {
	bodies := map[string]string{
		"truncated json":     `{`,
		"unknown key":        `{"title":"T","status":"draft","dek":"x"}`,
		"trailing data":      `{"title":"T","status":"draft"}{}`,
		"status archived":    `{"title":"T","status":"archived"}`,
		"status missing":     `{"title":"T"}`,
		"status empty":       `{"title":"T","status":""}`,
		"publish_date month": `{"title":"T","status":"draft","publish_date":"2026-13-01"}`,
		"publish_date shape": `{"title":"T","status":"draft","publish_date":"1 Jan 2026"}`,
		"block video":        `{"title":"T","status":"draft","body":[{"type":"video","text":"x"}]}`,
		"empty body":         ``,
		"status Draft":       `{"title":"T","status":"Draft"}`,
		"publish_date feb30": `{"title":"T","status":"draft","publish_date":"2026-02-30"}`,
		"publish_date short": `{"title":"T","status":"draft","publish_date":"2026-3-1"}`,
	}
	for name, body := range bodies {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(name+" "+method, func(t *testing.T) {
				f := &postFake{}
				h := CreatePostHandler(f.create, discard)
				if method == http.MethodPut {
					h = UpdatePostHandler(f.update, discard)
				}
				rec := serve(h, method, "p", "/v1/staff/blog/posts", body)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("status = %d (%s), want 400", rec.Code, rec.Body.String())
				}
				if f.calls != 0 {
					t.Error("store was called")
				}
			})
		}
	}
}

func TestPostHandlers_CapTheBody(t *testing.T) {
	f := &postFake{}
	over := `{"title":"T","status":"draft","summary":"` + strings.Repeat("a", maxPostBodyBytes) + `"}`
	rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", over)
	wantJSON(t, rec, http.StatusRequestEntityTooLarge, errBody("request body too large"))
	if f.calls != 0 {
		t.Error("store was called")
	}
	under := `{"title":"T","status":"draft","summary":"` + strings.Repeat("a", maxPostBodyBytes-1000) + `"}`
	if rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", under); rec.Code != http.StatusCreated {
		t.Errorf("under cap = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	head, tail := `{"title":"T","status":"draft","summary":"`, `"}`
	exact := head + strings.Repeat("a", maxPostBodyBytes-len(head)-len(tail)) + tail
	if len(exact) != maxPostBodyBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(exact), maxPostBodyBytes)
	}
	if rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", exact); rec.Code != http.StatusCreated {
		t.Errorf("exactly at cap = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	if rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", exact+" "); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("cap+1 = %d, want 413", rec.Code)
	}
}

func TestPostHandlers_DraftWithoutTitleIs422(t *testing.T) {
	f := &postFake{}
	rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", `{"title":"  ","status":"draft"}`)
	wantJSON(t, rec, http.StatusUnprocessableEntity,
		`{"error":"`+MsgTitleToSave+`","fields":{"title":"`+MsgTitle+`"}}`)
	if f.calls != 0 {
		t.Error("store was called")
	}
}

func TestPostHandlers_PublishMissingFieldsIs422(t *testing.T) {
	f := &postFake{}
	rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", `{"status":"published"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want 422", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range body.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"author_id", "body", "category_id", "publish_date", "summary", "title"}
	if body.Error != MsgFixToPublish || !reflect.DeepEqual(keys, want) {
		t.Errorf("error %q fields %v, want %q and %v", body.Error, keys, MsgFixToPublish, want)
	}
	if f.calls != 0 {
		t.Error("store was called")
	}
}

func TestPostHandlers_StoreFieldErrorsAre422(t *testing.T) {
	f := &postFake{err: &FieldErrors{Fields: map[string]string{"author_id": MsgAuthorNotStaff}, Summary: MsgAuthorNotStaff}}
	rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", `{"title":"T","status":"draft"}`)
	wantJSON(t, rec, http.StatusUnprocessableEntity,
		`{"error":"`+MsgAuthorNotStaff+`","fields":{"author_id":"`+MsgAuthorNotStaff+`"}}`)
}

func TestPostHandlers_CreateUpdateDelete(t *testing.T) {
	f := &postFake{post: Post{ID: "hello"}}
	f.post.Title = "T"
	rec := serve(CreatePostHandler(f.create, discard), "POST", "", "/p", `{"title":" T ","status":"draft","publish_date":"2026-03-01"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":"hello"`) {
		t.Errorf("create = %d %s", rec.Code, rec.Body.String())
	}
	if f.got.Title != "T" {
		t.Errorf("store got title %q, want the trimmed title", f.got.Title)
	}
	rec = serve(UpdatePostHandler(f.update, discard), "PUT", "hello", "/p/hello", `{"title":"T","status":"draft"}`)
	if rec.Code != http.StatusOK || f.id != "hello" {
		t.Errorf("update = %d id %q", rec.Code, f.id)
	}
	f.err = ErrPostNotFound
	rec = serve(UpdatePostHandler(f.update, discard), "PUT", "nope", "/p/nope", `{"title":"T","status":"draft"}`)
	wantJSON(t, rec, http.StatusNotFound, errBody("no such post"))

	del := func(context.Context, string) error { return nil }
	rec = serve(DeletePostHandler(del, discard), "DELETE", "hello", "/p/hello", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("delete = %d %q, want 204 and an empty body", rec.Code, rec.Body.String())
	}
	del = func(context.Context, string) error { return ErrPostNotFound }
	wantJSON(t, serve(DeletePostHandler(del, discard), "DELETE", "x", "/p/x", ""), http.StatusNotFound, errBody("no such post"))
}

func TestFeaturedHandler_NeedsTheFlag(t *testing.T) {
	var gotID string
	var gotFlag bool
	var err error
	h := SetFeaturedHandler(func(_ context.Context, id string, f bool) (FeaturedResult, error) {
		gotID, gotFlag = id, f
		return FeaturedResult{ID: id, Featured: f, Unpinned: ptr("old")}, err
	}, discard)
	wantJSON(t, serve(h, "PUT", "p", "/f", `{}`), http.StatusBadRequest, errBody("featured is required"))
	wantJSON(t, serve(h, "PUT", "p", "/f", `{"featured":true}`), http.StatusOK, `{"id":"p","featured":true,"unpinned":"old"}`)
	if gotID != "p" || !gotFlag {
		t.Errorf("store got %q %v", gotID, gotFlag)
	}
	err = ErrPostNotFound
	wantJSON(t, serve(h, "PUT", "p", "/f", `{"featured":false}`), http.StatusNotFound, errBody("no such post"))
}

type coverFake struct {
	calls int
	ct    string
	size  int
	err   error
}

func (f *coverFake) set(_ context.Context, _, ct string, b []byte) (Cover, error) {
	f.calls++
	f.ct, f.size = ct, len(b)
	return Cover{ContentType: ct, SizeBytes: len(b)}, f.err
}

func jpegBytes(n int) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, n-11)...)
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D}

func TestGetCoverHandler_ServesTheStoredBytes(t *testing.T) {
	img := jpegBytes(32)
	var gotID string
	h := GetCoverHandler(func(_ context.Context, id string) (string, []byte, error) {
		gotID = id
		return "image/jpeg", img, nil
	}, discard)
	rec := serve(h, "GET", "post-1", "/c", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]string{"Content-Type": "image/jpeg", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), img) || len(img) != 32 {
		t.Errorf("body = %d bytes, want the %d stored bytes", rec.Body.Len(), len(img))
	}
	if gotID != "post-1" {
		t.Errorf("store got id %q, want post-1", gotID)
	}
}

func TestGetCoverHandler_ServesPNGWithItsType(t *testing.T) {
	h := GetCoverHandler(func(context.Context, string) (string, []byte, error) { return "image/png", pngMagic, nil }, discard)
	rec := serve(h, "GET", "p", "/c", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("= %d type %q, want 200 image/png", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestGetCoverHandler_NoCoverIs404(t *testing.T) {
	h := GetCoverHandler(func(context.Context, string) (string, []byte, error) { return "", nil, ErrCoverNotFound }, discard)
	wantJSON(t, serve(h, "GET", "p", "/c", ""), http.StatusNotFound, errBody("no such cover"))
}

func TestGetCoverHandler_UnknownPostIs404(t *testing.T) {
	h := GetCoverHandler(func(context.Context, string) (string, []byte, error) { return "", nil, ErrPostNotFound }, discard)
	wantJSON(t, serve(h, "GET", "p", "/c", ""), http.StatusNotFound, errBody("no such post"))
}

func TestGetCoverHandler_StoreFailureIs500(t *testing.T) {
	h := GetCoverHandler(func(context.Context, string) (string, []byte, error) { return "", nil, errors.New("boom") }, discard)
	wantJSON(t, serve(h, "GET", "p", "/c", ""), http.StatusInternalServerError, errBody("internal error"))
}

func TestCoverHandler_AcceptsJPEGAndPNG(t *testing.T) {
	f := &coverFake{}
	h := SetCoverHandler(f.set, discard)
	if rec := serve(h, "PUT", "p", "/c", string(jpegBytes(64))); rec.Code != http.StatusOK || f.ct != "image/jpeg" {
		t.Errorf("jpeg = %d type %q", rec.Code, f.ct)
	}
	req := httptest.NewRequest("PUT", "/c", bytes.NewReader(pngMagic))
	req.Header.Set("Content-Type", "image/gif")
	req.SetPathValue("id", "p")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || f.ct != "image/png" {
		t.Errorf("png with a gif header = %d type %q, want 200 image/png", rec.Code, f.ct)
	}
}

func TestCoverHandler_RefusesOtherBytes(t *testing.T) {
	f := &coverFake{}
	h := SetCoverHandler(f.set, discard)
	wantJSON(t, serve(h, "PUT", "p", "/c", "GIF89a\x01\x00\x01\x00"), http.StatusUnsupportedMediaType, errBody(MsgCoverType))
	wantJSON(t, serve(h, "PUT", "p", "/c", ""), http.StatusUnsupportedMediaType, errBody(MsgCoverType))
	if f.calls != 0 {
		t.Error("store was called")
	}
}

func TestCoverHandler_CapIsMaxCoverBytes(t *testing.T) {
	f := &coverFake{}
	h := SetCoverHandler(f.set, discard)
	if rec := serve(h, "PUT", "p", "/c", string(jpegBytes(MaxCoverBytes))); rec.Code != http.StatusOK || f.size != MaxCoverBytes {
		t.Errorf("at the cap = %d size %d, want 200", rec.Code, f.size)
	}
	f.calls = 0
	wantJSON(t, serve(h, "PUT", "p", "/c", string(jpegBytes(MaxCoverBytes+1))), http.StatusRequestEntityTooLarge, errBody(MsgCoverTooLarge))
	if f.calls != 0 {
		t.Error("over-cap body reached the store")
	}
}

func TestCoverHandler_Remove(t *testing.T) {
	var err error
	h := RemoveCoverHandler(func(context.Context, string) error { return err }, discard)
	if rec := serve(h, "DELETE", "p", "/c", ""); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("remove = %d %q", rec.Code, rec.Body.String())
	}
	err = ErrPostNotFound
	wantJSON(t, serve(h, "DELETE", "p", "/c", ""), http.StatusNotFound, errBody("no such post"))
}

func TestCategoryHandlers_AddAndUpdate(t *testing.T) {
	var err error
	calls := 0
	add := AddCategoryHandler(func(_ context.Context, in CategoryInput) (Category, error) {
		calls++
		return Category{ID: "x", CategoryInput: in}, err
	}, discard)
	good := `{"name":"News","icon":"globe","tone":2}`
	wantJSON(t, serve(add, "POST", "", "/c", `{"name":" ","icon":"globe","tone":2}`), http.StatusUnprocessableEntity,
		`{"error":"`+MsgCategoryName+`","fields":{"name":"`+MsgCategoryName+`"}}`)
	if calls != 0 {
		t.Error("store was called on a blank name")
	}
	err = ErrCategoryNameTaken
	wantJSON(t, serve(add, "POST", "", "/c", good), http.StatusConflict, errBody(MsgCategoryNameTaken))
	err = nil
	rec := serve(add, "POST", "", "/c", good)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"headline":"News."`) {
		t.Errorf("add = %d %s", rec.Code, rec.Body.String())
	}
	wantJSON(t, serve(add, "POST", "", "/c", `{"name":"N","icon":"globe","tone":2,"x":1}`), http.StatusBadRequest, errBody("invalid request body"))

	upErr := ErrCategoryNotFound
	up := UpdateCategoryHandler(func(context.Context, string, CategoryInput) (Category, error) { return Category{}, upErr }, discard)
	wantJSON(t, serve(up, "PUT", "x", "/c/x", good), http.StatusNotFound, errBody("no such category"))
	upErr = nil
	if rec := serve(up, "PUT", "x", "/c/x", good); rec.Code != http.StatusOK {
		t.Errorf("update = %d", rec.Code)
	}
	over := `{"name":"N","icon":"globe","tone":2,"intro":"` + strings.Repeat("a", maxSmallBodyBytes) + `"}`
	if rec := serve(up, "PUT", "x", "/c/x", over); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("over small cap = %d, want 413", rec.Code)
	}
}

func TestCategoryHandlers_MoveAndDelete(t *testing.T) {
	var moveErr error
	moveCalls := 0
	move := MoveCategoryHandler(func(_ context.Context, _, dir string) (bool, []Category, error) {
		moveCalls++
		return true, []Category{{ID: "a"}}, moveErr
	}, discard)
	wantJSON(t, serve(move, "POST", "a", "/m", `{"direction":"left"}`), http.StatusBadRequest, errBody("direction must be up or down"))
	if moveCalls != 0 {
		t.Error("store was called for a bad direction")
	}
	if rec := serve(move, "POST", "a", "/m", `{"direction":"up"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"moved":true`) {
		t.Errorf("move = %d %s", rec.Code, rec.Body.String())
	}
	moveErr = ErrCategoryNotFound
	wantJSON(t, serve(move, "POST", "a", "/m", `{"direction":"down"}`), http.StatusNotFound, errBody("no such category"))
	moveErr = ErrDirection
	wantJSON(t, serve(move, "POST", "a", "/m", `{"direction":"down"}`), http.StatusBadRequest, errBody("direction must be up or down"))

	var gotMove *string
	var delErr error
	del := DeleteCategoryHandler(func(_ context.Context, _ string, mv *string) (DeleteResult, error) {
		gotMove = mv
		return DeleteResult{MovedPosts: 2, MoveTo: mv}, delErr
	}, discard)
	wantJSON(t, serve(del, "DELETE", "a", "/c/a?move_to=b", ""), http.StatusOK, `{"moved_posts":2,"move_to":"b"}`)
	if gotMove == nil || *gotMove != "b" {
		t.Errorf("store got move_to %v, want b", gotMove)
	}
	serve(del, "DELETE", "a", "/c/a", "")
	if gotMove != nil {
		t.Errorf("store got move_to %q with no query, want nil", *gotMove)
	}
	delErr = ErrOnlyCategory
	wantJSON(t, serve(del, "DELETE", "a", "/c/a", ""), http.StatusConflict, errBody(MsgOnlyCategory))
	delErr = &FieldErrors{Fields: map[string]string{"move_to": MsgMoveTo}, Summary: MsgMoveTo}
	wantJSON(t, serve(del, "DELETE", "a", "/c/a", ""), http.StatusUnprocessableEntity,
		`{"error":"`+MsgMoveTo+`","fields":{"move_to":"`+MsgMoveTo+`"}}`)
}

func TestAuthorsHandler_EncodesANullNameWithTheEmail(t *testing.T) {
	h := AuthorsHandler(func(context.Context) ([]Author, error) {
		return []Author{{UserID: "u-1", Email: ptr("e@ascomply.invalid")}}, nil
	}, discard)
	wantJSON(t, serve(h, "GET", "", "/a", ""), http.StatusOK,
		`{"authors":[{"user_id":"u-1","display_name":null,"email":"e@ascomply.invalid"}]}`)
}

func TestListHandlers_EncodeEmptyListsAsArrays(t *testing.T) {
	wantJSON(t, serve(ListPostsHandler(func(context.Context) ([]Post, error) { return nil, nil }, discard), "GET", "", "/p", ""), 200, `{"posts":[]}`)
	wantJSON(t, serve(ListCategoriesHandler(func(context.Context) ([]Category, error) { return nil, nil }, discard), "GET", "", "/c", ""), 200, `{"categories":[]}`)
	wantJSON(t, serve(AuthorsHandler(func(context.Context) ([]Author, error) { return nil, nil }, discard), "GET", "", "/a", ""), 200, `{"authors":[]}`)
}

type handlerCase struct {
	name   string
	method string
	body   string
	target string
	build  func(err error, log *slog.Logger) http.HandlerFunc
}

// allHandlers lists every constructor with a request that passes its own validation.
func allHandlers() []handlerCase {
	post := `{"title":"T","status":"draft"}`
	cat := `{"name":"N","icon":"globe","tone":1}`
	return []handlerCase{
		{"ListCategories", "GET", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return ListCategoriesHandler(func(context.Context) ([]Category, error) { return nil, e }, l)
		}},
		{"AddCategory", "POST", cat, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return AddCategoryHandler(func(context.Context, CategoryInput) (Category, error) { return Category{}, e }, l)
		}},
		{"UpdateCategory", "PUT", cat, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return UpdateCategoryHandler(func(context.Context, string, CategoryInput) (Category, error) { return Category{}, e }, l)
		}},
		{"MoveCategory", "POST", `{"direction":"up"}`, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return MoveCategoryHandler(func(context.Context, string, string) (bool, []Category, error) { return false, nil, e }, l)
		}},
		{"DeleteCategory", "DELETE", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return DeleteCategoryHandler(func(context.Context, string, *string) (DeleteResult, error) { return DeleteResult{}, e }, l)
		}},
		{"Authors", "GET", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return AuthorsHandler(func(context.Context) ([]Author, error) { return nil, e }, l)
		}},
		{"ListPosts", "GET", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return ListPostsHandler(func(context.Context) ([]Post, error) { return nil, e }, l)
		}},
		{"CreatePost", "POST", post, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return CreatePostHandler(func(context.Context, PostInput) (Post, error) { return Post{}, e }, l)
		}},
		{"UpdatePost", "PUT", post, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return UpdatePostHandler(func(context.Context, string, PostInput) (Post, error) { return Post{}, e }, l)
		}},
		{"DeletePost", "DELETE", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return DeletePostHandler(func(context.Context, string) error { return e }, l)
		}},
		{"SetFeatured", "PUT", `{"featured":true}`, "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return SetFeaturedHandler(func(context.Context, string, bool) (FeaturedResult, error) { return FeaturedResult{}, e }, l)
		}},
		{"SetCover", "PUT", string(jpegBytes(32)), "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return SetCoverHandler(func(context.Context, string, string, []byte) (Cover, error) { return Cover{}, e }, l)
		}},
		{"RemoveCover", "DELETE", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return RemoveCoverHandler(func(context.Context, string) error { return e }, l)
		}},
		{"GetCover", "GET", "", "/x", func(e error, l *slog.Logger) http.HandlerFunc {
			return GetCoverHandler(func(context.Context, string) (string, []byte, error) { return "image/png", pngMagic, e }, l)
		}},
	}
}

func TestHandlers_MapNotStaffAndUnknownErrors(t *testing.T) {
	if n := len(allHandlers()); n != 14 {
		t.Fatalf("%d handler cases, want 14", n)
	}
	for _, c := range allHandlers() {
		t.Run(c.name, func(t *testing.T) {
			wantJSON(t, serve(c.build(db.ErrNotStaff, discard), c.method, "i", c.target, c.body), http.StatusForbidden, errBody("forbidden"))
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			wantJSON(t, serve(c.build(errors.New("boom"), log), c.method, "i", c.target, c.body), http.StatusInternalServerError, errBody("internal error"))
			if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), "boom") {
				t.Errorf("log = %q, want one error line naming the cause", logs.String())
			}
		})
	}
}

func TestSmallBodyHandlers_RefuseMalformedJSON(t *testing.T) {
	bodies := []string{``, `{`, `null1`, `{"direction":"up","x":1}`, `{"direction":"up"}{}`, `[]`}
	cases := map[string]func(*int) http.HandlerFunc{
		"AddCategory": func(n *int) http.HandlerFunc {
			return AddCategoryHandler(func(context.Context, CategoryInput) (Category, error) { *n++; return Category{}, nil }, discard)
		},
		"UpdateCategory": func(n *int) http.HandlerFunc {
			return UpdateCategoryHandler(func(context.Context, string, CategoryInput) (Category, error) { *n++; return Category{}, nil }, discard)
		},
		"MoveCategory": func(n *int) http.HandlerFunc {
			return MoveCategoryHandler(func(context.Context, string, string) (bool, []Category, error) { *n++; return false, nil, nil }, discard)
		},
		"SetFeatured": func(n *int) http.HandlerFunc {
			return SetFeaturedHandler(func(context.Context, string, bool) (FeaturedResult, error) { *n++; return FeaturedResult{}, nil }, discard)
		},
	}
	if len(cases) != 4 {
		t.Fatalf("%d handlers, want 4", len(cases))
	}
	for name, build := range cases {
		for _, body := range bodies {
			n := 0
			rec := serve(build(&n), "POST", "i", "/x", body)
			if rec.Code != http.StatusBadRequest || n != 0 {
				t.Errorf("%s %q = %d, store calls %d; want 400 and 0", name, body, rec.Code, n)
			}
		}
	}
	// exact and cap+1 for the small cap
	n := 0
	h := cases["SetFeatured"](&n)
	pad := maxSmallBodyBytes - len(`{"featured":true}`)
	exact := `{"featured":true}` + strings.Repeat(" ", pad)
	if rec := serve(h, "PUT", "i", "/x", exact); rec.Code != http.StatusOK {
		t.Errorf("exactly at small cap = %d, want 200", rec.Code)
	}
	if rec := serve(h, "PUT", "i", "/x", exact+" "); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("small cap+1 = %d, want 413", rec.Code)
	}
}

func TestFeaturedHandler_RefusesNonBooleans(t *testing.T) {
	n, flag := 0, true
	h := SetFeaturedHandler(func(_ context.Context, _ string, f bool) (FeaturedResult, error) {
		n++
		flag = f
		return FeaturedResult{Featured: f}, nil
	}, discard)
	for _, body := range []string{`{"featured":null}`, `{"featured":"yes"}`, `{"featured":1}`} {
		if rec := serve(h, "PUT", "p", "/f", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, rec.Code)
		}
	}
	if n != 0 {
		t.Errorf("store called %d times", n)
	}
	wantJSON(t, serve(h, "PUT", "p", "/f", `{"featured":false}`), http.StatusOK, `{"id":"","featured":false,"unpinned":null}`)
	if n != 1 || flag {
		t.Errorf("featured false must reach the store once as false, got %d calls, flag %v", n, flag)
	}
}

func TestHandlers_PassTheIDAndQueryVerbatim(t *testing.T) {
	ids := []string{"über-ñ", "a/b", "x y", "", "../p"}
	var got string
	rec := func(id string) { got = id }
	build := map[string]struct {
		h      func() http.HandlerFunc
		method string
		body   string
	}{
		"UpdateCategory": {func() http.HandlerFunc {
			return UpdateCategoryHandler(func(_ context.Context, id string, _ CategoryInput) (Category, error) { rec(id); return Category{}, nil }, discard)
		}, "PUT", `{"name":"N","icon":"globe","tone":1}`},
		"MoveCategory": {func() http.HandlerFunc {
			return MoveCategoryHandler(func(_ context.Context, id, _ string) (bool, []Category, error) { rec(id); return false, nil, nil }, discard)
		}, "POST", `{"direction":"up"}`},
		"DeleteCategory": {func() http.HandlerFunc {
			return DeleteCategoryHandler(func(_ context.Context, id string, _ *string) (DeleteResult, error) {
				rec(id)
				return DeleteResult{}, nil
			}, discard)
		}, "DELETE", ``},
		"UpdatePost": {func() http.HandlerFunc {
			return UpdatePostHandler(func(_ context.Context, id string, _ PostInput) (Post, error) { rec(id); return Post{}, nil }, discard)
		}, "PUT", `{"title":"T","status":"draft"}`},
		"DeletePost": {func() http.HandlerFunc {
			return DeletePostHandler(func(_ context.Context, id string) error { rec(id); return nil }, discard)
		}, "DELETE", ``},
		"SetFeatured": {func() http.HandlerFunc {
			return SetFeaturedHandler(func(_ context.Context, id string, _ bool) (FeaturedResult, error) {
				rec(id)
				return FeaturedResult{}, nil
			}, discard)
		}, "PUT", `{"featured":true}`},
		"SetCover": {func() http.HandlerFunc {
			return SetCoverHandler(func(_ context.Context, id, _ string, _ []byte) (Cover, error) { rec(id); return Cover{}, nil }, discard)
		}, "PUT", string(jpegBytes(16))},
		"RemoveCover": {func() http.HandlerFunc {
			return RemoveCoverHandler(func(_ context.Context, id string) error { rec(id); return nil }, discard)
		}, "DELETE", ``},
	}
	if len(build) != 8 {
		t.Fatalf("%d id-taking handlers, want 8", len(build))
	}
	for name, c := range build {
		for _, id := range ids {
			got = "unset"
			if r := serve(c.h(), c.method, id, "/x", c.body); r.Code >= 400 {
				t.Errorf("%s id %q = %d", name, id, r.Code)
			}
			if got != id {
				t.Errorf("%s: store got id %q, want %q", name, got, id)
			}
		}
	}
	var gotMove *string
	del := DeleteCategoryHandler(func(_ context.Context, _ string, mv *string) (DeleteResult, error) {
		gotMove = mv
		return DeleteResult{}, nil
	}, discard)
	serve(del, "DELETE", "a", "/c/a?move_to=", "")
	if gotMove != nil {
		t.Errorf("empty move_to reached the store as %q, want nil", *gotMove)
	}
	serve(del, "DELETE", "a", "/c/a?move_to=%C3%BCber", "")
	if gotMove == nil || *gotMove != "über" {
		t.Errorf("move_to = %v, want über", gotMove)
	}
}

func TestHandlers_AnswerJSON(t *testing.T) {
	for _, c := range allHandlers() {
		for _, err := range []error{nil, db.ErrNotStaff} {
			r := serve(c.build(err, discard), c.method, "i", c.target, c.body)
			if r.Body.Len() == 0 {
				continue // 204
			}
			if c.name == "GetCover" && err == nil {
				continue // the image itself
			}
			if ct := r.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s err=%v: Content-Type %q, want application/json", c.name, err, ct)
			}
		}
	}
}

func TestListPostsHandler_WireCarriesNoAuthorEmail(t *testing.T) {
	h := ListPostsHandler(func(context.Context) ([]Post, error) {
		return []Post{{ID: "p", AuthorName: ptr("Ada"), PostInput: PostInput{Title: "T", AuthorID: ptr("u-1")}}}, nil
	}, discard)
	r := serve(h, "GET", "", "/p", "")
	var body struct {
		Posts []map[string]any `json:"posts"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || len(body.Posts) != 1 {
		t.Fatalf("body %s err %v", r.Body.String(), err)
	}
	if _, has := body.Posts[0]["author_name"]; !has {
		t.Error("post wire lacks author_name")
	}
	if strings.Contains(strings.ToLower(r.Body.String()), "email") {
		t.Errorf("post wire mentions email: %s", r.Body.String())
	}
}

func TestAuthorsHandler_NamedAuthorKeepsTheName(t *testing.T) {
	h := AuthorsHandler(func(context.Context) ([]Author, error) {
		return []Author{{UserID: "u-1", DisplayName: ptr("Ada"), Email: ptr("a@x.invalid")}, {UserID: "u-2"}}, nil
	}, discard)
	wantJSON(t, serve(h, "GET", "", "/a", ""), http.StatusOK,
		`{"authors":[{"user_id":"u-1","display_name":"Ada","email":"a@x.invalid"},{"user_id":"u-2","display_name":null,"email":null}]}`)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("reset") }

func TestCoverHandler_RefusesAndMapsEdges(t *testing.T) {
	f := &coverFake{}
	h := SetCoverHandler(f.set, discard)
	req := httptest.NewRequest("PUT", "/c", bytes.NewReader(pngMagic))
	req.Header.Set("Content-Type", "image/jpeg")
	req.SetPathValue("id", "p")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || f.ct != "image/png" {
		t.Errorf("png body with a jpeg header = %d type %q, want 200 image/png", rec.Code, f.ct)
	}
	f.calls = 0
	for name, body := range map[string]string{"html": "<html><body>x</body></html>", "text": "hello", "webp": "RIFF\x00\x00\x00\x00WEBPVP8 ", "pdf": "%PDF-1.4"} {
		if r := serve(h, "PUT", "p", "/c", body); r.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s = %d, want 415", name, r.Code)
		}
	}
	big := "GIF89a" + strings.Repeat("x", MaxCoverBytes)
	wantJSON(t, serve(h, "PUT", "p", "/c", big), http.StatusRequestEntityTooLarge, errBody(MsgCoverTooLarge))
	if f.calls != 0 {
		t.Error("store was called")
	}
	f.err = ErrPostNotFound
	wantJSON(t, serve(h, "PUT", "p", "/c", string(jpegBytes(16))), http.StatusNotFound, errBody("no such post"))
	f.err = nil
	rq := httptest.NewRequest("PUT", "/c", failingReader{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, rq)
	wantJSON(t, rr, http.StatusBadRequest, errBody("invalid request body"))
	if f.calls != 1 {
		t.Errorf("store calls %d after a read error, want the earlier 404 only", f.calls)
	}
}

func TestCheck_IsIdempotentOnItsOutput(t *testing.T) {
	in := PostInput{Title: "  T  ", Summary: " s ", Status: "draft", Body: []Block{{Type: "p", Text: " x "}}}
	once, fe := CheckPost(in)
	if fe != nil {
		t.Fatal(fe)
	}
	twice, fe := CheckPost(once)
	if fe != nil || !reflect.DeepEqual(once, twice) {
		t.Errorf("CheckPost is not idempotent: %+v then %+v (%v)", once, twice, fe)
	}
	c, fe := CheckCategory(CategoryInput{Name: " N ", Icon: "globe", Tone: 1})
	c2, fe2 := CheckCategory(c)
	if fe != nil || fe2 != nil || c != c2 {
		t.Errorf("CheckCategory is not idempotent: %+v then %+v", c, c2)
	}
}
