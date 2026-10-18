// Package blogweb serves crawler-readable HTML for the blog SPA. It is a client of the
// gateway's public blog JSON and imports no other internal package.
package blogweb

type Block struct {
	Type  string   `json:"type"`
	Text  string   `json:"text"`
	Items []string `json:"items"`
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Who   string   `json:"who"`
	Demo  string   `json:"demo"`
}

type Post struct {
	ID          string  `json:"id"`
	CategoryID  string  `json:"category_id"`
	PublishDate string  `json:"publish_date"` // YYYY-MM-DD
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	AuthorName  string  `json:"author_name"`
	Featured    bool    `json:"featured"`
	Cover       *string `json:"cover"` // "/blog/covers/<id>/<version>" on the gateway origin
	Body        []Block `json:"body"`
}

type Category struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Headline  string `json:"headline"`
	Intro     string `json:"intro"`
	PostCount int    `json:"post_count"`
}

type Snapshot struct {
	Categories []Category // gateway order = position order
	Posts      []Post     // gateway order = publish_date DESC, id
}

type categoriesEnvelope struct {
	Categories []Category `json:"categories"`
}

type postsEnvelope struct {
	Posts []Post `json:"posts"`
}
