package blog

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestSlug_PortsThePrototype(t *testing.T) {
	long := strings.Repeat("abcdefghi ", 7)
	for _, c := range []struct{ in, want string }{
		{"Import from CSV or Excel", "import-from-csv-or-excel"},
		{"R&D", "r-and-d"},
		{"  --Hello--  ", "hello"},
		{"!!!", "post"},
		{"Ägypten 2026", "gypten-2026"},
		{long, strings.ReplaceAll(strings.TrimSpace(long), " ", "-")[:MaxSlugLen]},
	} {
		if got := Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlug_CutsAfterTrimLikeThePrototype(t *testing.T) {
	got := Slug(strings.Repeat("a", MaxSlugLen-1) + " b")
	if len(got) != MaxSlugLen || !strings.HasSuffix(got, "-") {
		t.Errorf("got %q (len %d), want length 64 ending in -", got, len(got))
	}
}

func TestUniqueID_AppendsTwoThenThree(t *testing.T) {
	set := func(ids ...string) func(string) bool {
		return func(s string) bool {
			for _, id := range ids {
				if id == s {
					return true
				}
			}
			return false
		}
	}
	for _, c := range []struct {
		taken []string
		want  string
	}{{nil, "a"}, {[]string{"a"}, "a-2"}, {[]string{"a", "a-2"}, "a-3"}} {
		if got := UniqueID("a", set(c.taken...)); got != c.want {
			t.Errorf("taken %v: got %q, want %q", c.taken, got, c.want)
		}
	}
}

func TestUniqueID_ReservedPostIDsAreTaken(t *testing.T) {
	none := func(string) bool { return false }
	for title, want := range map[string]string{"Subscribe": "subscribe-2", "Category": "category-2", "Other": "other"} {
		if got := PostID(title, none); got != want {
			t.Errorf("PostID(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestCleanBody_EmptyIsANonNilSlice(t *testing.T) {
	for name, in := range map[string][]Block{"nil": nil, "empty": {}, "blank": {{Type: "p", Text: " "}}} {
		got := CleanBody(in)
		if got == nil || len(got) != 0 {
			t.Errorf("%s: got %#v", name, got)
		}
		if b, _ := json.Marshal(got); string(b) != "[]" {
			t.Errorf("%s: marshals to %s", name, b)
		}
	}
}

func TestCleanBody_PortsBClean(t *testing.T) {
	got := CleanBody([]Block{
		{Type: "p", Text: "  "},
		{Type: "p", Text: " x "},
		{Type: "ul", Items: []string{"", " a "}},
		{Type: "ul", Items: []string{" "}},
		{Type: "note", Title: "only a title"},
		{Type: "quote", Who: "someone"},
		{Type: "demo", Demo: "import"},
		{Type: "h", Text: ""},
	})
	want := []Block{
		{Type: "p", Text: "x"},
		{Type: "ul", Items: []string{"a"}},
		{Type: "note", Title: "only a title"},
		{Type: "demo", Demo: "import"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestCleanBody_KeepsOnlyTheTypesFields(t *testing.T) {
	got := CleanBody([]Block{{Type: "p", Text: "hi", Items: []string{"z"}, Who: "w", Title: "t"}})
	b, _ := json.Marshal(got)
	if string(b) != `[{"type":"p","text":"hi"}]` {
		t.Errorf("got %s", b)
	}
}

func TestValidateBlocks_RefusesUnknownShapes(t *testing.T) {
	err := ValidateBlocks([]Block{{Type: "p"}, {Type: "h"}, {Type: "video"}})
	if err == nil || !strings.Contains(err.Error(), "block 2") {
		t.Errorf("video: %v", err)
	}
	for _, b := range []Block{
		{Type: "note", Kind: "warning"},
		{Type: "note", Kind: ""},
		{Type: "demo", Demo: "video"},
	} {
		if err := ValidateBlocks([]Block{b}); err == nil || !strings.Contains(err.Error(), "block 0") {
			t.Errorf("%+v: %v", b, err)
		}
	}
	valid := []Block{
		{Type: "p"}, {Type: "h"}, {Type: "ul"}, {Type: "note", Kind: "deadline"},
		{Type: "quote"}, {Type: "demo", Demo: "clearance"},
	}
	if err := ValidateBlocks(valid); err != nil {
		t.Errorf("valid shapes: %v", err)
	}
}

func TestCheckPost_TrimsTitleAndSummary(t *testing.T) {
	got, fe := CheckPost(PostInput{Status: "draft", Title: "  Hello ", Summary: " Dek "})
	if fe != nil || got.Title != "Hello" || got.Summary != "Dek" {
		t.Errorf("got %+v, %v", got, fe)
	}
}

func TestCheckPost_DraftNeedsATitle(t *testing.T) {
	_, fe := CheckPost(PostInput{Status: "draft", Title: "  "})
	want := map[string]string{"title": MsgTitle}
	if fe == nil || !reflect.DeepEqual(fe.Fields, want) || fe.Summary != MsgTitleToSave {
		t.Errorf("got %+v", fe)
	}
	if _, fe := CheckPost(PostInput{Status: "draft", Title: "T"}); fe != nil {
		t.Errorf("title only: %+v", fe)
	}
}

func TestCheckPost_PublishNeedsEveryField(t *testing.T) {
	_, fe := CheckPost(PostInput{Status: "published"})
	want := map[string]string{
		"title": MsgTitle, "summary": MsgSummary, "category_id": MsgCategory,
		"publish_date": MsgDate, "author_id": MsgAuthor, "body": MsgBody,
	}
	if fe == nil || !reflect.DeepEqual(fe.Fields, want) || fe.Summary != MsgFixToPublish {
		t.Errorf("got %+v", fe)
	}
}

func TestCheckPost_PublishWithOnlyBlankBlocksFailsBody(t *testing.T) {
	_, fe := CheckPost(PostInput{
		Status: "published", Title: "T", Summary: "S", CategoryID: sp("c"), PublishDate: sp("2026-01-02"),
		AuthorID: sp("u"), Body: []Block{{Type: "p", Text: " "}, {Type: "p"}},
	})
	if fe == nil || !reflect.DeepEqual(fe.Fields, map[string]string{"body": MsgBody}) {
		t.Errorf("got %+v", fe)
	}
}

func TestCheckCategory_NameIconToneHeadline(t *testing.T) {
	ok := CategoryInput{Name: "N", Icon: "package", Tone: 0}
	cases := []struct {
		name string
		mod  func(*CategoryInput)
		want map[string]string
	}{
		{"blank name", func(c *CategoryInput) { c.Name = " " }, map[string]string{"name": MsgCategoryName}},
		{"bad icon", func(c *CategoryInput) { c.Icon = "rocket" }, map[string]string{"icon": MsgIcon}},
		{"tone 6", func(c *CategoryInput) { c.Tone = 6 }, map[string]string{"tone": MsgTone}},
		{"tone -1", func(c *CategoryInput) { c.Tone = -1 }, map[string]string{"tone": MsgTone}},
	}
	for _, c := range cases {
		in := ok
		c.mod(&in)
		if _, fe := CheckCategory(in); fe == nil || !reflect.DeepEqual(fe.Fields, c.want) {
			t.Errorf("%s: got %+v", c.name, fe)
		}
	}
	got, fe := CheckCategory(CategoryInput{Name: " Product ", Headline: " ", Icon: "package", Tone: 5})
	if fe != nil || got.Name != "Product" || got.Headline != "Product." {
		t.Errorf("got %+v, %v", got, fe)
	}
}

func TestCheckCategoryDelete_Refusals(t *testing.T) {
	id, other := "a", "b"
	missing := &FieldErrors{Fields: map[string]string{"move_to": MsgMoveTo}, Summary: MsgMoveTo}
	for _, c := range []struct {
		name        string
		cats, posts int
		moveTo      *string
		exists      bool
		want        error
	}{
		{"only, no posts", 1, 0, nil, false, ErrOnlyCategory},
		{"only, posts", 1, 3, &other, true, ErrOnlyCategory},
		{"nil move", 2, 3, nil, false, missing},
		{"self move", 2, 3, &id, true, missing},
		{"unknown move", 2, 3, &other, false, missing},
		{"no posts", 2, 0, nil, false, nil},
		{"valid move", 2, 3, &other, true, nil},
	} {
		got := CheckCategoryDelete(c.cats, c.posts, c.moveTo, c.exists, id)
		var fe *FieldErrors
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %v", c.name, got)
		case c.want == ErrOnlyCategory && !errors.Is(got, ErrOnlyCategory):
			t.Errorf("%s: got %v", c.name, got)
		case c.want == missing && (!errors.As(got, &fe) || !reflect.DeepEqual(fe.Fields, missing.Fields)):
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestSaveEvent_ByTransition(t *testing.T) {
	for _, c := range []struct{ was, now, want string }{
		{"", "draft", EventPostCreated},
		{"", "published", EventPostCreated},
		{"draft", "published", EventPostPublished},
		{"published", "draft", EventPostUnpublished},
		{"draft", "draft", EventPostUpdated},
		{"published", "published", EventPostUpdated},
	} {
		if got := SaveEvent(c.was, c.now); got != c.want {
			t.Errorf("%q->%q: got %q, want %q", c.was, c.now, got, c.want)
		}
	}
}
