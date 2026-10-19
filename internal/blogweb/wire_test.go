package blogweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/blog"
)

func TestWire_DecodesTheGatewayPublicPost(t *testing.T) {
	cover := blog.CoverPath("p", 17)
	src := blog.PublicPost{
		ID: "p", CategoryID: "c", PublishDate: "2026-10-09", Title: "T", Summary: "S", AuthorName: "Ada",
		Featured: true, Cover: &cover,
		Body: []blog.Block{
			{Type: "p", Text: "para"}, {Type: "h", Text: "head"}, {Type: "ul", Items: []string{"x", "y"}},
			{Type: "note", Kind: "tip", Title: "nt", Text: "nx"}, {Type: "quote", Text: "q", Who: "w"},
			{Type: "demo", Demo: "import"},
		},
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var got Post
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := Post{ID: "p", CategoryID: "c", PublishDate: "2026-10-09", Title: "T", Summary: "S", AuthorName: "Ada",
		Featured: true, Cover: &cover,
		Body: []Block{
			{Type: "p", Text: "para"}, {Type: "h", Text: "head"}, {Type: "ul", Items: []string{"x", "y"}},
			{Type: "note", Kind: "tip", Title: "nt", Text: "nx"}, {Type: "quote", Text: "q", Who: "w"},
			{Type: "demo", Demo: "import"},
		}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestWire_DecodesTheGatewayCategory(t *testing.T) {
	src := blog.Category{ID: "c", CategoryInput: blog.CategoryInput{Name: "N", Headline: "H", Intro: "I"}, PostCount: 3}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var got Category
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if want := (Category{ID: "c", Name: "N", Headline: "H", Intro: "I", PostCount: 3}); got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestWire_DecodesTheRealHandlerEnvelopes(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/categories", blog.PublicCategoriesHandler(func(context.Context, string) ([]blog.Category, error) {
		return []blog.Category{{ID: "c1"}}, nil
	}, nil))
	mux.Handle("/posts", blog.PublicPostsHandler(func(context.Context, string) ([]blog.PublicPost, error) {
		return []blog.PublicPost{{ID: "p1"}}, nil
	}, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var cats categoriesEnvelope
	getJSON(t, srv.URL+"/categories", &cats)
	if len(cats.Categories) != 1 || cats.Categories[0].ID != "c1" {
		t.Errorf("categories %+v", cats)
	}
	var posts postsEnvelope
	getJSON(t, srv.URL+"/posts", &posts)
	if len(posts.Posts) != 1 || posts.Posts[0].ID != "p1" {
		t.Errorf("posts %+v", posts)
	}
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}
