package blogweb

import (
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"
)

const (
	siteSuffix      = " · ASComply blog"
	homeHeading     = "News from the invoice compliance desk."
	homeDescription = "News, regulation and product notes on e-invoicing compliance in Nigeria, from ASComply Africa."

	titleOpen  = "<title>"
	titleClose = "</title>"
	rootDiv    = `<div id="root"></div>`
)

//go:embed pages.html
var pagesHTML string

var pages = template.Must(template.New("pages").Funcs(template.FuncMap{
	"date": longDate,
}).Parse(pagesHTML))

// serverTags are the head tags the server writes; a shell holding one would duplicate it.
var serverTags = []string{`name="description"`, `rel="canonical"`, `name="robots"`, `property="og:`}

// Shell is the SPA's index.html split around its title and root div.
type Shell struct {
	beforeTitle, betweenTitleAndRoot, afterRoot string
}

func ParseShell(index string) (Shell, error) {
	for _, m := range []string{titleOpen, titleClose, rootDiv} {
		if n := strings.Count(index, m); n != 1 {
			return Shell{}, fmt.Errorf("blogweb: shell holds %d of %q, want 1", n, m)
		}
	}
	for _, t := range serverTags {
		if strings.Contains(index, t) {
			return Shell{}, fmt.Errorf("blogweb: shell already holds a server-written tag %q", t)
		}
	}
	ti, te := strings.Index(index, titleOpen), strings.Index(index, titleClose)+len(titleClose)
	ri := strings.Index(index, rootDiv)
	if te > ri {
		return Shell{}, fmt.Errorf("blogweb: shell has its root div before its title")
	}
	return Shell{index[:ti], index[te:ri], index[ri+len(rootDiv):]}, nil
}

type head struct {
	Title, Description, Canonical, Image string
	NoIndex                              bool
	Post                                 *Post // set for the OG article tags
}

func (s Shell) render(h head, body string, data any) []byte {
	h.Title += siteSuffix
	var b bytes.Buffer
	b.WriteString(s.beforeTitle)
	exec(&b, "head", h)
	b.WriteString(s.betweenTitleAndRoot)
	b.WriteString(`<div id="root"><div data-crawl>`)
	exec(&b, body, data)
	b.WriteString(`</div></div>`)
	b.WriteString(s.afterRoot)
	return b.Bytes()
}

// exec panics: the data is built in-process, so a failure is a template bug a test catches.
func exec(b *bytes.Buffer, name string, data any) {
	if err := pages.ExecuteTemplate(b, name, data); err != nil {
		panic("blogweb: render " + name + ": " + err.Error())
	}
}

func longDate(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return t.Format("2 January 2006")
}

func (s Shell) Home(origin string, snap *Snapshot) []byte {
	var cats []Category
	for _, c := range snap.Categories {
		if len(postsOf(snap, c.ID)) > 0 {
			cats = append(cats, c)
		}
	}
	return s.render(
		head{Title: strings.TrimSuffix(homeHeading, "."), Description: homeDescription, Canonical: origin + "/"},
		"home", struct {
			Heading    string
			Categories []Category
			Posts      []Post
		}{homeHeading, cats, newestFirst(snap.Posts)})
}

func (s Shell) Category(origin string, snap *Snapshot, c Category) []byte {
	desc := c.Intro
	if desc == "" {
		desc = c.Headline
	}
	if desc == "" {
		desc = homeDescription
	}
	return s.render(
		head{Title: c.Name, Description: desc, Canonical: origin + "/category/" + c.ID},
		"category", struct {
			Category Category
			Posts    []Post
		}{c, newestFirst(postsOf(snap, c.ID))})
}

// Post renders one post; gateway is the origin the cover path is relative to.
func (s Shell) Post(origin, gateway string, snap *Snapshot, p Post) []byte {
	h := head{Title: p.Title, Description: p.Summary, Canonical: origin + "/" + p.ID, Post: &p}
	if p.Cover != nil {
		h.Image = strings.TrimSuffix(gateway, "/") + *p.Cover
	}
	var cat *Category
	for i := range snap.Categories {
		if snap.Categories[i].ID == p.CategoryID {
			cat = &snap.Categories[i]
		}
	}
	return s.render(h, "post", struct {
		Post     Post
		Category *Category
	}{p, cat})
}

func (s Shell) Subscribe(origin string) []byte {
	return s.render(head{Title: "Subscribe", Description: homeDescription, Canonical: origin + "/subscribe"}, "subscribe", nil)
}

// SubscribeConfirm is the subscribe page at /subscribe/confirm, kept out of the index.
func (s Shell) SubscribeConfirm(origin string) []byte {
	return s.render(
		head{Title: "Subscribe", Description: homeDescription, Canonical: origin + "/subscribe/confirm", NoIndex: true},
		"subscribe", nil)
}

func (s Shell) NotFound(origin, path string) []byte {
	return s.render(
		head{Title: "Not found", Description: homeDescription, Canonical: origin + path, NoIndex: true},
		"notfound", nil)
}

func postsOf(snap *Snapshot, categoryID string) []Post {
	var out []Post
	for _, p := range snap.Posts {
		if p.CategoryID == categoryID {
			out = append(out, p)
		}
	}
	return out
}

func newestFirst(posts []Post) []Post {
	out := append([]Post(nil), posts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].PublishDate > out[j].PublishDate })
	return out
}

type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

// Sitemap lists home, each category with posts and each post; counts come from the posts, not post_count.
func Sitemap(origin string, s *Snapshot) []byte {
	posts := newestFirst(s.Posts)
	home := urlEntry{Loc: origin + "/"}
	if len(posts) > 0 {
		home.LastMod = posts[0].PublishDate
	}
	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: []urlEntry{home}}
	for _, c := range s.Categories {
		if cp := newestFirst(postsOf(s, c.ID)); len(cp) > 0 {
			set.URLs = append(set.URLs, urlEntry{origin + "/category/" + c.ID, cp[0].PublishDate})
		}
	}
	for _, p := range posts {
		set.URLs = append(set.URLs, urlEntry{origin + "/" + p.ID, p.PublishDate})
	}
	out, err := xml.Marshal(set)
	if err != nil {
		panic("blogweb: sitemap: " + err.Error()) // plain strings cannot fail to marshal
	}
	return append([]byte(xml.Header), out...)
}

func Robots(origin string) []byte {
	return []byte("User-agent: *\nAllow: /\n\nSitemap: " + origin + "/sitemap.xml\n")
}
