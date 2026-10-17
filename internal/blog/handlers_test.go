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
	}
}

func TestHandlers_MapNotStaffAndUnknownErrors(t *testing.T) {
	if n := len(allHandlers()); n != 13 {
		t.Fatalf("%d handler cases, want 13", n)
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
