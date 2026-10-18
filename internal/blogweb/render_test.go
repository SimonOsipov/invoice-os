package blogweb

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

const (
	origin  = "https://blog.example"
	gateway = "https://api.example/"
)

func realShellHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../frontend/blog/index.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shell(t *testing.T) Shell {
	t.Helper()
	s, err := ParseShell(realShellHTML(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func str(s string) *string { return &s }

func has(t *testing.T, page, want string) {
	t.Helper()
	if !strings.Contains(page, want) {
		t.Errorf("missing %q in:\n%s", want, page)
	}
}

func lacks(t *testing.T, page, bad string) {
	t.Helper()
	if strings.Contains(page, bad) {
		t.Errorf("unexpected %q in:\n%s", bad, page)
	}
}

func TestRenderPost_HeadCarriesTitleMetaCanonicalAndOpenGraph(t *testing.T) {
	p := Post{ID: "p", Title: "T", Summary: "S", Cover: str("/blog/covers/p/17")}
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, p))
	if n := strings.Count(page, "<title>"); n != 1 {
		t.Errorf("title count %d", n)
	}
	for _, w := range []string{
		`<title>T · ASComply blog</title>`,
		`<meta name="description" content="S">`,
		`<link rel="canonical" href="https://blog.example/p">`,
		`<meta property="og:title" content="T">`,
		`<meta property="og:description" content="S">`,
		`<meta property="og:type" content="article">`,
		`<meta property="og:image" content="https://api.example/blog/covers/p/17">`,
	} {
		has(t, page, w)
	}
}

func TestRenderPost_NoCoverMeansNoOgImage(t *testing.T) {
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, Post{ID: "p", Title: "T", Summary: "S"}))
	lacks(t, page, "og:image")
	for _, w := range []string{"og:title", "og:description", "og:type"} {
		has(t, page, w)
	}
}

func TestRenderPost_BlockShowsTitleAuthorAndDate(t *testing.T) {
	p := Post{ID: "p", Title: "T", AuthorName: "Ada Obi", PublishDate: "2026-10-09"}
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, p))
	has(t, page, "<h1>T</h1>")
	has(t, page, `<time datetime="2026-10-09">9 October 2026</time>`)
	if strings.Index(page, "Ada Obi") > strings.Index(page, "<time") {
		t.Error("author must precede the date")
	}
}

func TestRenderPost_BodyBlocksInDocumentOrder(t *testing.T) {
	p := Post{ID: "p", Title: "T", Body: []Block{
		{Type: "p", Text: "AAA"}, {Type: "h", Text: "BBB"}, {Type: "ul", Items: []string{"CCC", "DDD"}},
		{Type: "note", Title: "EEE", Text: "FFF"}, {Type: "quote", Text: "GGG", Who: "HHH"},
		{Type: "demo", Demo: "import"}, {Type: "p", Text: "III"},
	}}
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, p))
	block := page[strings.Index(page, "<div data-crawl>"):]
	last := -1
	for _, w := range []string{"AAA", "BBB", "CCC", "DDD", "EEE", "FFF", "GGG", "HHH", "III"} {
		i := strings.Index(block, w)
		if i <= last {
			t.Fatalf("%s out of order (%d <= %d)", w, i, last)
		}
		last = i
	}
	for _, w := range []string{"<h2>BBB</h2>", "<li>CCC</li>", "<li>DDD</li>", "<blockquote>"} {
		has(t, block, w)
	}
	lacks(t, block, "import")
}

func TestRenderPost_ScriptInTitleRendersAsText(t *testing.T) {
	p := Post{ID: "p", Title: "<script>alert(1)</script> & co", Summary: "S"}
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, p))
	esc := "&lt;script&gt;alert(1)&lt;/script&gt; &amp; co"
	has(t, page, "<title>"+esc+" · ASComply blog</title>")
	has(t, page, `<meta property="og:title" content="`+esc+`">`)
	has(t, page, "<h1>"+esc+"</h1>")
	if n := strings.Count(page, "<script>alert"); n != 0 {
		t.Errorf("raw script occurs %d times", n)
	}
}

func TestRender_EveryDatabaseFieldIsEscaped(t *testing.T) {
	evil := `"><img src=x onerror=1>`
	cat := Category{ID: "c", Name: evil, Headline: evil, Intro: evil}
	p := Post{ID: "p", CategoryID: "c", PublishDate: "2026-10-09", Title: evil, Summary: evil, AuthorName: evil,
		Body: []Block{{Type: "p", Text: evil}, {Type: "h", Text: evil}, {Type: "ul", Items: []string{evil}},
			{Type: "note", Title: evil, Text: evil}, {Type: "quote", Text: evil, Who: evil}}}
	snap := &Snapshot{Categories: []Category{cat}, Posts: []Post{p}}
	sh := shell(t)
	for name, page := range map[string]string{
		"post":     string(sh.Post(origin, gateway, snap, p)),
		"category": string(sh.Category(origin, snap, cat)),
		"home":     string(sh.Home(origin, snap)),
	} {
		lacks(t, page, "<img src=x")
		has(t, page, "&lt;img")
		if t.Failed() {
			t.Fatal(name)
		}
	}
}

var twoCats = &Snapshot{
	Categories: []Category{{ID: "a", Name: "a-name", Intro: "I"}, {ID: "b", Name: "b-name"}},
	Posts: []Post{
		{ID: "p1", CategoryID: "a", Title: "One", PublishDate: "2026-10-05"},
		{ID: "p2", CategoryID: "a", Title: "Two", PublishDate: "2026-10-01"},
	},
}

func TestRenderHome_LinksEveryPostAndEachCategoryWithPosts(t *testing.T) {
	page := string(shell(t).Home(origin, twoCats))
	has(t, page, "<title>News from the invoice compliance desk · ASComply blog</title>")
	has(t, page, `content="`+homeDescription+`"`)
	has(t, page, "<h1>"+homeHeading+"</h1>")
	for _, w := range []string{`href="/p1"`, `href="/p2"`, `href="/category/a"`} {
		has(t, page, w)
	}
	lacks(t, page, `href="/category/b"`)
}

func TestRenderCategory_OwnTitleIntroAndItsPostsOnly(t *testing.T) {
	snap := &Snapshot{Categories: twoCats.Categories, Posts: []Post{
		{ID: "p1", CategoryID: "a"}, {ID: "p2", CategoryID: "b"}}}
	page := string(shell(t).Category(origin, snap, snap.Categories[0]))
	has(t, page, "<title>a-name · ASComply blog</title>")
	has(t, page, `<meta name="description" content="I">`)
	has(t, page, `href="https://blog.example/category/a"`)
	has(t, page, `href="/p1"`)
	lacks(t, page, `href="/p2"`)
}

func TestRenderCategory_DescriptionFallsBackToHeadlineThenHome(t *testing.T) {
	sh := shell(t)
	page := string(sh.Category(origin, &Snapshot{}, Category{ID: "c", Name: "N", Headline: "H"}))
	has(t, page, `<meta name="description" content="H">`)
	page = string(sh.Category(origin, &Snapshot{}, Category{ID: "c", Name: "N"}))
	has(t, page, `<meta name="description" content="`+homeDescription+`">`)
}

func TestRenderNotFound_IsNoindex(t *testing.T) {
	page := string(shell(t).NotFound(origin, "/x"))
	has(t, page, "<title>Not found · ASComply blog</title>")
	has(t, page, `<meta name="robots" content="noindex">`)
	has(t, page, "<h1>Not found</h1>")
	has(t, page, `href="/"`)
}

func TestRenderHome_ZeroPostsHasHeadingAndNoEmptyNav(t *testing.T) {
	page := string(shell(t).Home(origin, &Snapshot{Categories: twoCats.Categories}))
	has(t, page, "<title>News from the invoice compliance desk · ASComply blog</title>")
	has(t, page, `content="`+homeDescription+`"`)
	has(t, page, "<h1>"+homeHeading+"</h1>")
	for _, bad := range []string{"<nav", "<article", `href="/category/`} {
		lacks(t, page, bad)
	}
}

func TestRenderCategory_ZeroPostsKeepsItsOwnHead(t *testing.T) {
	c := Category{ID: "empty", Name: "empty-name", Intro: "I"}
	page := string(shell(t).Category(origin, &Snapshot{Categories: []Category{c}}, c))
	has(t, page, "<title>empty-name · ASComply blog</title>")
	has(t, page, `<meta name="description" content="I">`)
	has(t, page, "<h1>empty-name</h1>")
	lacks(t, page, "<article")
}

type parsedSitemap struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
}

func TestSitemap_IsAValidUrlsetWithLastmod(t *testing.T) {
	snap := &Snapshot{Categories: twoCats.Categories, Posts: []Post{
		{ID: "p-old", CategoryID: "a", PublishDate: "2026-10-01"},
		{ID: "p-new", CategoryID: "a", PublishDate: "2026-10-05"}}}
	var got parsedSitemap
	if err := xml.Unmarshal(Sitemap(origin, snap), &got); err != nil {
		t.Fatal(err)
	}
	if got.XMLName.Space != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Errorf("namespace %q", got.XMLName.Space)
	}
	wantLoc := []string{origin + "/", origin + "/category/a", origin + "/p-new", origin + "/p-old"}
	wantMod := []string{"2026-10-05", "2026-10-05", "2026-10-05", "2026-10-01"}
	if len(got.URLs) != 4 {
		t.Fatalf("urls %d", len(got.URLs))
	}
	for i, u := range got.URLs {
		if u.Loc != wantLoc[i] || u.LastMod != wantMod[i] {
			t.Errorf("url %d = %+v, want %s %s", i, u, wantLoc[i], wantMod[i])
		}
	}
}

func TestSitemap_EmptySnapshotListsOnlyHomeWithoutLastmod(t *testing.T) {
	out := Sitemap(origin, &Snapshot{})
	var got parsedSitemap
	if err := xml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.URLs) != 1 || got.URLs[0].Loc != origin+"/" {
		t.Errorf("urls %+v", got.URLs)
	}
	lacks(t, string(out), "lastmod")
}

func TestSitemap_EscapesTheOrigin(t *testing.T) {
	var got parsedSitemap
	if err := xml.Unmarshal(Sitemap("https://a&b", &Snapshot{}), &got); err != nil {
		t.Fatal(err)
	}
	if got.URLs[0].Loc != "https://a&b/" {
		t.Errorf("loc %q", got.URLs[0].Loc)
	}
}

func TestRobots_AllowsAllAndNamesTheSitemap(t *testing.T) {
	want := "User-agent: *\nAllow: /\n\nSitemap: https://blog.example/sitemap.xml\n"
	if got := string(Robots(origin)); got != want {
		t.Errorf("robots %q", got)
	}
}

func TestRender_CrawlerBlockIsInsideRootAndHiddenFromScriptClients(t *testing.T) {
	src := realShellHTML(t)
	page := string(shell(t).Post(origin, gateway, &Snapshot{}, Post{ID: "p", Title: "T"}))
	if n := strings.Count(page, `<div id="root"><div data-crawl>`); n != 1 {
		t.Errorf("crawler block opens %d times", n)
	}
	end := strings.Index(page, "</head>")
	for _, w := range []string{
		"<style>#root>[data-crawl]{display:none}</style>",
		"<noscript><style>#root>[data-crawl]{display:block}</style></noscript>",
	} {
		if i := strings.Index(page, w); i < 0 || i > end {
			t.Errorf("%q at %d, head ends %d", w, i, end)
		}
	}
	// Vite's own head tags survive byte for byte.
	head := src[strings.Index(src, "<head>"):strings.Index(src, "<title>")]
	has(t, page, head)
	has(t, page, `<script type="module" src="/src/main.tsx"></script>`)
}

func TestParseShell_RefusesAShellWithoutExactlyOneOfEachMarker(t *testing.T) {
	good := realShellHTML(t)
	title := "<title>ASComply Africa — Blog</title>"
	for name, bad := range map[string]string{
		"<title>":           strings.Replace(good, title, "", 1),
		"two <title>":       strings.Replace(good, title, title+title, 1),
		"no root":           strings.Replace(good, rootDiv, "", 1),
		"two root divs":     strings.Replace(good, rootDiv, rootDiv+rootDiv, 1),
		"root before title": strings.Replace(strings.Replace(good, rootDiv, "", 1), title, rootDiv+title, 1),
	} {
		if _, err := ParseShell(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ParseShell(good); err != nil {
		t.Errorf("real shell: %v", err)
	}
}

func TestParseShell_RefusesAShellThatAlreadyHoldsAServerTag(t *testing.T) {
	good := realShellHTML(t)
	for _, tag := range []string{
		`<meta name="description" content="x">`, `<link rel="canonical" href="/">`,
		`<meta name="robots" content="x">`, `<meta property="og:title" content="x">`,
	} {
		_, err := ParseShell(strings.Replace(good, "</head>", tag+"</head>", 1))
		if err == nil {
			t.Errorf("%s: no error", tag)
		}
	}
}
