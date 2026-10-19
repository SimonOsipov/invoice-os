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
		{"", "post"},
		{"日本語", "post"},
		{"Café Ünïcode", "caf-n-code"},
		{"a & b", "a-and-b"},
		{"UPPER_case.Mix", "upper-case-mix"},
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

func TestSlug_OutputIsAlwaysASCIIAtMostTheCap(t *testing.T) {
	got := Slug(strings.Repeat("é", 100) + strings.Repeat("a", 80))
	if got == "" || len(got) != MaxSlugLen || strings.Trim(got, "a") != "" {
		t.Errorf("got %q", got)
	}
	if got := Slug(strings.Repeat("a", MaxSlugLen+1)); len(got) != MaxSlugLen {
		t.Errorf("65 bytes: len %d", len(got))
	}
	if got := Slug(strings.Repeat("a", MaxSlugLen)); len(got) != MaxSlugLen {
		t.Errorf("64 bytes: len %d", len(got))
	}
	for _, in := range []string{"İstanbul", "Kelvin K sign", "日本語 2026 日本語"} {
		for _, r := range Slug(in) {
			if r > 127 {
				t.Errorf("Slug(%q) = %q holds a non-ASCII rune", in, Slug(in))
			}
		}
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
	for title, want := range map[string]string{"Subscribe": "subscribe-2", "Category": "category-2", "Health": "health-2", "Other": "other"} {
		if got := PostID(title, none); got != want {
			t.Errorf("PostID(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestPostID_ReservedIDsCollideWithTakenToo(t *testing.T) {
	taken := func(id string) bool { return id == "subscribe-2" || id == "category-2" || id == "x" }
	for title, want := range map[string]string{"Subscribe": "subscribe-3", "CATEGORY": "category-3", "x": "x-2", "!!!": "post"} {
		if got := PostID(title, taken); got != want {
			t.Errorf("PostID(%q) = %q, want %q", title, got, want)
		}
	}
	if UniqueID("subscribe", func(string) bool { return false }) != "subscribe" {
		t.Error("UniqueID alone must not know the reserved ids")
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
	raw, _ := json.Marshal(Block{Type: "h", Text: "hi", Items: []string{"z"}, Who: "w", Title: "t", Kind: "k", Demo: "d"})
	if string(raw) != `{"type":"h","text":"hi"}` {
		t.Errorf("marshal of an unclean block: %s", raw)
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

func TestCheckPost_OnlyPublishedStatusPublishes(t *testing.T) {
	if _, fe := CheckPost(PostInput{Status: "", Title: "T"}); fe != nil {
		t.Errorf("status \"\" must not demand publish fields: %+v", fe)
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

func TestCleanBody_EdgeBlocks(t *testing.T) {
	got := CleanBody([]Block{
		{Type: "p", Title: "title only"},
		{Type: "h", Text: "\t\n "},
		{Type: "quote", Text: " q ", Who: " w "},
		{Type: "note", Kind: "deadline", Title: " n ", Text: " t "},
		{Type: "note", Kind: "deadline"},
		{Type: "demo"},
		{Type: "video", Text: " v "},
		{Type: "ul"},
	})
	want := []Block{
		{Type: "quote", Text: "q", Who: "w"},
		{Type: "note", Kind: "deadline", Title: "n", Text: "t"},
		{Type: "demo"},
		{Type: "video", Text: "v"},
	}
	if len(got) == 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestBlock_JSONRoundTripKeepsEachShape(t *testing.T) {
	in := `[{"type":"p","text":"a"},{"type":"h","text":"b"},{"type":"ul","items":["c"]},` +
		`{"type":"note","text":"e","kind":"product","title":"d"},{"type":"quote","text":"f","who":"g"},{"type":"demo","demo":"approval"}]`
	var body []Block
	if err := json.Unmarshal([]byte(in), &body); err != nil || len(body) != 6 {
		t.Fatalf("unmarshal: %v %d", err, len(body))
	}
	if out, _ := json.Marshal(body); string(out) != in {
		t.Errorf("got %s", out)
	}
	if out, _ := json.Marshal(Block{Type: "ul"}); string(out) != `{"type":"ul","items":[]}` {
		t.Errorf("empty ul: %s", out)
	}
}

func TestValidateBlocks_UnknownShapeEdges(t *testing.T) {
	if err := ValidateBlocks(nil); err != nil {
		t.Errorf("nil body: %v", err)
	}
	for _, b := range []Block{{Type: ""}, {Type: "P"}, {Type: "note", Kind: "Deadline"}, {Type: "demo", Demo: ""}} {
		if err := ValidateBlocks([]Block{{Type: "p"}, b}); err == nil || !strings.Contains(err.Error(), "block 1") {
			t.Errorf("%+v: %v", b, err)
		}
	}
	for _, k := range NoteKinds {
		if err := ValidateBlocks([]Block{{Type: "note", Kind: k}}); err != nil {
			t.Errorf("kind %s: %v", k, err)
		}
	}
	for _, d := range Demos {
		if err := ValidateBlocks([]Block{{Type: "demo", Demo: d}}); err != nil {
			t.Errorf("demo %s: %v", d, err)
		}
	}
}

func TestCheckPost_WhitespaceAndNilFieldsOnPublish(t *testing.T) {
	full := PostInput{
		Status: "published", Title: "T", Summary: "S", CategoryID: sp("c"), PublishDate: sp("2026-01-02"),
		AuthorID: sp("u"), Body: []Block{{Type: "p", Text: "x"}},
	}
	if got, fe := CheckPost(full); fe != nil || len(got.Body) != 1 {
		t.Fatalf("complete post: %+v %v", got, fe)
	}
	for field, mod := range map[string]func(*PostInput){
		"title":        func(p *PostInput) { p.Title = " \t" },
		"summary":      func(p *PostInput) { p.Summary = "  " },
		"category_id":  func(p *PostInput) { p.CategoryID = sp("  ") },
		"publish_date": func(p *PostInput) { p.PublishDate = nil },
		"author_id":    func(p *PostInput) { p.AuthorID = sp(" ") },
		"body":         func(p *PostInput) { p.Body = nil },
	} {
		in := full
		mod(&in)
		_, fe := CheckPost(in)
		if fe == nil || len(fe.Fields) != 1 || fe.Fields[field] == "" || fe.Summary != MsgFixToPublish {
			t.Errorf("%s: %+v", field, fe)
		}
	}
}

func TestCheckPost_DraftIgnoresPublishFieldsAndCleansBody(t *testing.T) {
	got, fe := CheckPost(PostInput{Status: "draft", Title: "T", Body: []Block{{Type: "p", Text: " "}, {Type: "p", Text: " k "}}})
	if fe != nil || !reflect.DeepEqual(got.Body, []Block{{Type: "p", Text: "k"}}) {
		t.Errorf("got %+v %v", got, fe)
	}
	got, _ = CheckPost(PostInput{Status: "draft", Title: "T"})
	if got.Body == nil {
		t.Error("body must be a non-nil slice")
	}
}

func TestCheckCategory_EdgesAndSummary(t *testing.T) {
	ok := CategoryInput{Name: "N", Icon: "package", Tone: 0}
	for _, tone := range []int{0, 5} {
		in := ok
		in.Tone = tone
		if _, fe := CheckCategory(in); fe != nil {
			t.Errorf("tone %d: %+v", tone, fe)
		}
	}
	for _, ic := range Icons {
		in := ok
		in.Icon = " " + ic + " "
		if got, fe := CheckCategory(in); fe != nil || got.Icon != ic {
			t.Errorf("icon %q: %+v %v", ic, got, fe)
		}
	}
	if len(Icons) != 16 {
		t.Fatalf("icons: %d", len(Icons))
	}
	_, fe := CheckCategory(CategoryInput{Name: "  ", Icon: "x", Tone: 9})
	want := map[string]string{"name": MsgCategoryName, "icon": MsgIcon, "tone": MsgTone}
	if fe == nil || !reflect.DeepEqual(fe.Fields, want) || fe.Summary != MsgCategoryName {
		t.Errorf("all bad: %+v", fe)
	}
	got, _ := CheckCategory(CategoryInput{Name: "N", Headline: " H ", Intro: " i ", Icon: "clock", Tone: 1})
	if got.Headline != "H" || got.Intro != "i" {
		t.Errorf("trim: %+v", got)
	}
}

func TestFieldErrors_ErrorIsTheSummary(t *testing.T) {
	var err error = &FieldErrors{Fields: map[string]string{"a": "b"}, Summary: "sum"}
	if err.Error() != "sum" {
		t.Errorf("got %q", err.Error())
	}
}

func TestCheckCategoryDelete_OnlyCategoryWinsAndNoPostsNeedNoMove(t *testing.T) {
	b := "b"
	if err := CheckCategoryDelete(2, 0, &b, false, "a"); err != nil {
		t.Errorf("no posts, unknown move_to: %v", err)
	}
	if err := CheckCategoryDelete(2, 0, nil, false, "a"); err != nil {
		t.Errorf("no posts: %v", err)
	}
	if err := CheckCategoryDelete(1, 0, nil, false, "a"); !errors.Is(err, ErrOnlyCategory) {
		t.Errorf("only: %v", err)
	}
}

func TestDesignValues_ConstantsMessagesEvents(t *testing.T) {
	if MaxSlugLen != 64 || MaxCoverBytes != 2097152 || maxPostBodyBytes != 262144 || maxSmallBodyBytes != 8192 ||
		ToneCount != 6 || TeamAuthorName != "ASComply team" {
		t.Error("numeric or byline constant drifted from the Design")
	}
	lists := map[string][2][]string{
		"icons": {Icons, {"package", "landmark", "building-2", "megaphone", "handshake", "code-xml", "globe", "sparkles",
			"shield-check", "file-check", "chart-column", "workflow", "plug", "archive", "key-round", "clock"}},
		"kinds": {NoteKinds, {"regulation", "deadline", "product"}},
		"demos": {Demos, {"import", "approval", "clearance"}},
		"rsvd":  {reservedPostIDs, {"subscribe", "category", "health"}},
	}
	for n, l := range lists {
		if !reflect.DeepEqual(l[0], l[1]) {
			t.Errorf("%s: %v", n, l[0])
		}
	}
	for got, want := range map[string]string{
		MsgTitle: "Add a title.", MsgSummary: "Add a summary. It shows on cards and under the title.",
		MsgCategory: "Choose a category.", MsgDate: "Choose a publish date.",
		MsgBody: "Add at least one block with content.", MsgAuthor: "Choose an author.",
		MsgAuthorNotStaff: "Choose an ASComply staff member as the author.",
		MsgFixToPublish:   "Fix the highlighted fields to publish", MsgTitleToSave: "Add a title to save the draft",
		MsgCategoryName: "Add a name.", MsgCategoryNameTaken: "A category with this name already exists.",
		MsgOnlyCategory: "This is the only category. Add another before deleting it.",
		MsgMoveTo:       "Choose a category to move its posts to.", MsgIcon: "Choose an icon.",
		MsgTone: "Choose a cover tone.", MsgCoverType: "Choose a JPG or PNG image",
		MsgCoverTooLarge: "Choose an image of 2 MB or less",
		EventPostCreated: "blog.post.created", EventPostUpdated: "blog.post.updated",
		EventPostPublished: "blog.post.published", EventPostUnpublished: "blog.post.unpublished",
		EventPostDeleted: "blog.post.deleted", EventPostPinned: "blog.post.pinned",
		EventPostUnpinned: "blog.post.unpinned", EventPostCoverSet: "blog.post.cover_set",
		EventPostCoverRemoved: "blog.post.cover_removed", EventCategoryAdded: "blog.category.added",
		EventCategoryUpdated: "blog.category.updated", EventCategoryMoved: "blog.category.moved",
		EventCategoryDeleted: "blog.category.deleted",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if ErrCategoryNameTaken.Error() != MsgCategoryNameTaken || ErrOnlyCategory.Error() != MsgOnlyCategory {
		t.Error("error text must be the message")
	}
}
