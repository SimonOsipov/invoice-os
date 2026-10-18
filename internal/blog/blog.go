// Package blog holds the blog content rules shared by the staff routes and the public read.
package blog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	MaxSlugLen        = 64
	MaxCoverBytes     = 2 << 20
	maxPostBodyBytes  = 256 << 10
	maxSmallBodyBytes = 8 << 10
	ToneCount         = 6
	TeamAuthorName    = "ASComply team"
)

const (
	MsgTitle             = "Add a title."
	MsgSummary           = "Add a summary. It shows on cards and under the title."
	MsgCategory          = "Choose a category."
	MsgDate              = "Choose a publish date."
	MsgBody              = "Add at least one block with content."
	MsgAuthor            = "Choose an author."
	MsgAuthorNotStaff    = "Choose an ASComply staff member as the author."
	MsgFixToPublish      = "Fix the highlighted fields to publish"
	MsgTitleToSave       = "Add a title to save the draft"
	MsgCategoryName      = "Add a name."
	MsgCategoryNameTaken = "A category with this name already exists."
	MsgOnlyCategory      = "This is the only category. Add another before deleting it."
	MsgMoveTo            = "Choose a category to move its posts to."
	MsgIcon              = "Choose an icon."
	MsgTone              = "Choose a cover tone."
	MsgCoverType         = "Choose a JPG or PNG image"
	MsgCoverTooLarge     = "Choose an image of 2 MB or less"
)

const (
	EventPostCreated      = "blog.post.created"
	EventPostUpdated      = "blog.post.updated"
	EventPostPublished    = "blog.post.published"
	EventPostUnpublished  = "blog.post.unpublished"
	EventPostDeleted      = "blog.post.deleted"
	EventPostPinned       = "blog.post.pinned"
	EventPostUnpinned     = "blog.post.unpinned"
	EventPostCoverSet     = "blog.post.cover_set"
	EventPostCoverRemoved = "blog.post.cover_removed"
	EventCategoryAdded    = "blog.category.added"
	EventCategoryUpdated  = "blog.category.updated"
	EventCategoryMoved    = "blog.category.moved"
	EventCategoryDeleted  = "blog.category.deleted"
)

// Icons keep the blog-data.js order.
var Icons = []string{
	"package", "landmark", "building-2", "megaphone", "handshake", "code-xml", "globe", "sparkles",
	"shield-check", "file-check", "chart-column", "workflow", "plug", "archive", "key-round", "clock",
}

var (
	NoteKinds       = []string{"regulation", "deadline", "product"}
	Demos           = []string{"import", "approval", "clearance"}
	reservedPostIDs = []string{"subscribe", "category"}
)

var (
	ErrPostNotFound      = errors.New("no such post")
	ErrCoverNotFound     = errors.New("no such cover")
	ErrCategoryNotFound  = errors.New("no such category")
	ErrCategoryNameTaken = errors.New(MsgCategoryNameTaken)
	ErrOnlyCategory      = errors.New(MsgOnlyCategory)
)

// Block is one body block; MarshalJSON emits only the fields of its type.
type Block struct {
	Type  string
	Text  string
	Items []string
	Kind  string
	Title string
	Who   string
	Demo  string
}

type blockWire struct {
	Type  string    `json:"type"`
	Text  *string   `json:"text,omitempty"`
	Items *[]string `json:"items,omitempty"`
	Kind  *string   `json:"kind,omitempty"`
	Title *string   `json:"title,omitempty"`
	Who   *string   `json:"who,omitempty"`
	Demo  *string   `json:"demo,omitempty"`
}

func (b Block) MarshalJSON() ([]byte, error) {
	w := blockWire{Type: b.Type}
	switch b.Type {
	case "ul":
		items := b.Items
		if items == nil {
			items = []string{}
		}
		w.Items = &items
	case "note":
		w.Kind, w.Title, w.Text = &b.Kind, &b.Title, &b.Text
	case "quote":
		w.Text, w.Who = &b.Text, &b.Who
	case "demo":
		w.Demo = &b.Demo
	default:
		w.Text = &b.Text
	}
	return json.Marshal(w)
}

func (b *Block) UnmarshalJSON(data []byte) error {
	var w struct {
		Type  string   `json:"type"`
		Text  string   `json:"text"`
		Items []string `json:"items"`
		Kind  string   `json:"kind"`
		Title string   `json:"title"`
		Who   string   `json:"who"`
		Demo  string   `json:"demo"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*b = Block{Type: w.Type, Text: w.Text, Items: w.Items, Kind: w.Kind, Title: w.Title, Who: w.Who, Demo: w.Demo}
	return nil
}

type PostInput struct {
	CategoryID  *string `json:"category_id"`
	PublishDate *string `json:"publish_date"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	AuthorID    *string `json:"author_id"`
	Status      string  `json:"status"`
	Featured    bool    `json:"featured"`
	Body        []Block `json:"body"`
}

type Post struct {
	PostInput
	ID         string    `json:"id"`
	AuthorName *string   `json:"author_name"`
	Cover      *Cover    `json:"cover"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Cover struct {
	ContentType string    `json:"content_type"`
	SizeBytes   int       `json:"size_bytes"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CategoryInput struct {
	Name     string `json:"name"`
	Headline string `json:"headline"`
	Intro    string `json:"intro"`
	Icon     string `json:"icon"`
	Tone     int    `json:"tone"`
}

type Category struct {
	ID string `json:"id"`
	CategoryInput
	PostCount int `json:"post_count"`
}

// Author: a nil DisplayName means a blank name.
type Author struct {
	UserID      string  `json:"user_id"`
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
}

// FieldErrors maps a wire field to its message; Summary is the 422 "error".
type FieldErrors struct {
	Fields  map[string]string
	Summary string
}

func (e *FieldErrors) Error() string { return e.Summary }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug ports the prototype bSlug; the cut runs after the trim (D-28).
func Slug(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
	if len(s) > MaxSlugLen {
		s = s[:MaxSlugLen]
	}
	if s == "" {
		return "post"
	}
	return s
}

// UniqueID returns base, else base-2, base-3, ...
func UniqueID(base string, taken func(string) bool) string {
	id := base
	for k := 2; taken(id); k++ {
		id = fmt.Sprintf("%s-%d", base, k)
	}
	return id
}

// PostID is the post id rule: reserved ids count as taken.
func PostID(title string, taken func(string) bool) string {
	return UniqueID(Slug(title), func(id string) bool {
		return slices.Contains(reservedPostIDs, id) || taken(id)
	})
}

// CleanBody ports bClean: each block keeps only its type's fields, then blank blocks drop.
// The result is never nil, so it marshals to [] (jsonb_typeof(body) = 'array').
func CleanBody(body []Block) []Block {
	out := []Block{}
	for _, b := range body {
		c := Block{Type: b.Type}
		switch b.Type {
		case "ul":
			for _, it := range b.Items {
				if it = strings.TrimSpace(it); it != "" {
					c.Items = append(c.Items, it)
				}
			}
		case "note":
			c.Kind = b.Kind
			c.Title = strings.TrimSpace(b.Title)
			c.Text = strings.TrimSpace(b.Text)
		case "quote":
			c.Text = strings.TrimSpace(b.Text)
			c.Who = strings.TrimSpace(b.Who)
		case "demo":
			c.Demo = b.Demo
		default:
			c.Text = strings.TrimSpace(b.Text)
		}
		if c.Type == "demo" || c.Text != "" || len(c.Items) > 0 || c.Title != "" {
			out = append(out, c)
		}
	}
	return out
}

// ValidateBlocks refuses an unknown type, note kind or demo, naming the block index.
func ValidateBlocks(body []Block) error {
	for i, b := range body {
		switch b.Type {
		case "p", "h", "ul", "quote":
		case "note":
			if !slices.Contains(NoteKinds, b.Kind) {
				return fmt.Errorf("block %d: unknown note kind %q", i, b.Kind)
			}
		case "demo":
			if !slices.Contains(Demos, b.Demo) {
				return fmt.Errorf("block %d: unknown demo %q", i, b.Demo)
			}
		default:
			return fmt.Errorf("block %d: unknown type %q", i, b.Type)
		}
	}
	return nil
}

func blank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }

// CheckPost ports bCheck plus the author rule and returns the input as it is stored.
func CheckPost(in PostInput) (PostInput, *FieldErrors) {
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = CleanBody(in.Body)
	publish := in.Status == "published"
	f := map[string]string{}
	if in.Title == "" {
		f["title"] = MsgTitle
	}
	if publish {
		if in.Summary == "" {
			f["summary"] = MsgSummary
		}
		if blank(in.CategoryID) {
			f["category_id"] = MsgCategory
		}
		if blank(in.PublishDate) {
			f["publish_date"] = MsgDate
		}
		if blank(in.AuthorID) {
			f["author_id"] = MsgAuthor
		}
		if len(in.Body) == 0 {
			f["body"] = MsgBody
		}
	}
	if len(f) == 0 {
		return in, nil
	}
	summary := MsgTitleToSave
	if publish {
		summary = MsgFixToPublish
	}
	return in, &FieldErrors{Fields: f, Summary: summary}
}

// CheckCategory trims, validates, and defaults a blank headline to name + ".".
func CheckCategory(in CategoryInput) (CategoryInput, *FieldErrors) {
	in.Name = strings.TrimSpace(in.Name)
	in.Headline = strings.TrimSpace(in.Headline)
	in.Intro = strings.TrimSpace(in.Intro)
	in.Icon = strings.TrimSpace(in.Icon)
	f := map[string]string{}
	summary := ""
	add := func(field, msg string) {
		f[field] = msg
		if summary == "" {
			summary = msg
		}
	}
	if in.Name == "" {
		add("name", MsgCategoryName)
	}
	if !slices.Contains(Icons, in.Icon) {
		add("icon", MsgIcon)
	}
	if in.Tone < 0 || in.Tone >= ToneCount {
		add("tone", MsgTone)
	}
	if len(f) > 0 {
		return in, &FieldErrors{Fields: f, Summary: summary}
	}
	if in.Headline == "" {
		in.Headline = in.Name + "."
	}
	return in, nil
}

// CheckCategoryDelete returns ErrOnlyCategory or a move_to FieldErrors.
func CheckCategoryDelete(categories, posts int, moveTo *string, moveToExists bool, id string) error {
	if categories == 1 {
		return ErrOnlyCategory
	}
	if posts > 0 && (moveTo == nil || *moveTo == id || !moveToExists) {
		return &FieldErrors{Fields: map[string]string{"move_to": MsgMoveTo}, Summary: MsgMoveTo}
	}
	return nil
}

// SaveEvent names the audit event of a status transition; was is "" for a new post.
func SaveEvent(was, now string) string {
	switch {
	case was == "":
		return EventPostCreated
	case was == "draft" && now == "published":
		return EventPostPublished
	case was == "published" && now == "draft":
		return EventPostUnpublished
	}
	return EventPostUpdated
}
