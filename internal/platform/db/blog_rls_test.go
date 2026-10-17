// The blog tables are global: no tenant, no RLS. The guarantees here are columns, grants,
// CHECKs, indexes and the staff_read_blog_authors definer function.
// Rows are written through the superuser pool; each refused statement runs on its own implicit tx.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	blogAuthorsSig = "public.staff_read_blog_authors(uuid)"
	blogMaxCover   = 2097152
)

var blogTables = []string{"blog_categories", "blog_posts", "blog_post_covers"}

// blogIcons is the prototype ICONS list (blog-data.js).
var blogIcons = []string{
	"package", "landmark", "building-2", "megaphone", "handshake", "code-xml", "globe", "sparkles",
	"shield-check", "file-check", "chart-column", "workflow", "plug", "archive", "key-round", "clock",
}

func requireBlogTables(t *testing.T) {
	t.Helper()
	for _, tbl := range blogTables {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+tbl).Scan(&reg); err != nil {
			t.Fatalf("read to_regclass %s: %v", tbl, err)
		}
		if reg == nil {
			t.Fatalf("table public.%s not found: the blog migration is not applied", tbl)
		}
	}
}

func requireBlogAuthors(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, blogAuthorsSig).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure: %v", err)
	}
	if reg == nil {
		t.Fatalf("function %s not found: the blog migration is not applied", blogAuthorsSig)
	}
}

// blogScope returns an id prefix unique to the test. Cleanup deletes its posts, then its categories.
func blogScope(t *testing.T) string {
	t.Helper()
	requireHarness(t)
	requireBlogTables(t)
	prefix := "qa-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.blog_posts WHERE id LIKE $1`, prefix+"%")
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.blog_categories WHERE id LIKE $1`, prefix+"%")
	})
	return prefix
}

func blogInsertCategory(id, name, icon string, tone int) error {
	_, err := h.super.Exec(context.Background(),
		`INSERT INTO public.blog_categories (id, name, headline, icon, tone, position)
		 VALUES ($1, $2, $2, $3, $4, (SELECT coalesce(max(position), 0) + 1 FROM public.blog_categories))`,
		id, name, icon, tone)
	return err
}

func blogMustCategory(t *testing.T, id, name string) {
	t.Helper()
	if err := blogInsertCategory(id, name, "package", 0); err != nil {
		t.Fatalf("seed category %s: %v", id, err)
	}
}

// blogPostRow holds one blog_posts insert. nil category, date, authorID and authorName insert NULL.
type blogPostRow struct {
	id                                   string
	category, date, authorID, authorName any
	title, summary, status, body         string
	featured                             bool
}

// blogComplete is a post that satisfies blog_posts_published_complete.
func blogComplete(id, category string) blogPostRow {
	return blogPostRow{
		id: id, category: category, date: "2026-01-02", authorID: uuid.NewString(), authorName: "Author",
		title: "Title", summary: "Summary", status: "published", body: `[{"type":"p","text":"x"}]`,
	}
}

func (r blogPostRow) insert() error {
	_, err := h.super.Exec(context.Background(),
		`INSERT INTO public.blog_posts (id, category_id, publish_date, title, summary, author_id, author_name, status, featured, body)
		 VALUES ($1, $2::text, $3::date, $4, $5, $6::uuid, $7::text, $8, $9, $10::jsonb)`,
		r.id, r.category, r.date, r.title, r.summary, r.authorID, r.authorName, r.status, r.featured, r.body)
	return err
}

func blogMustPost(t *testing.T, r blogPostRow) {
	t.Helper()
	if err := r.insert(); err != nil {
		t.Fatalf("seed post %s: %v", r.id, err)
	}
}

// blogWantRefused asserts err is a PgError with code and, when constraint is not empty, that constraint.
func blogWantRefused(t *testing.T, what string, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: want SQLSTATE %s %s, got %v", what, code, constraint, err)
		return
	}
	if pgErr.Code != code || (constraint != "" && pgErr.ConstraintName != constraint) {
		t.Errorf("%s: want SQLSTATE %s on %q, got %s on %q (%s)", what, code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func blogCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	return mustCount(t, h.super, sql, args...)
}

func TestRLS_BlogTablesHaveTheirColumns(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	want := map[string][]string{
		"blog_categories": {
			"id text NO", "name text NO", "headline text NO", "intro text NO", "icon text NO",
			"tone smallint NO", "position integer NO",
		},
		"blog_posts": {
			"id text NO", "category_id text YES", "publish_date date YES", "title text NO", "summary text NO",
			"author_id uuid YES", "author_name text YES", "status text NO", "featured boolean NO", "body jsonb NO",
			"created_at timestamp with time zone NO", "updated_at timestamp with time zone NO",
		},
		"blog_post_covers": {
			"post_id text NO", "content_type text NO", "bytes bytea NO", "updated_at timestamp with time zone NO",
		},
	}
	for tbl, cols := range want {
		rows, err := h.super.Query(context.Background(),
			`SELECT column_name || ' ' || data_type || ' ' || is_nullable FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = $1`, tbl)
		if err != nil {
			t.Fatalf("read columns of %s: %v", tbl, err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("scan columns of %s: %v", tbl, err)
		}
		sort.Strings(got)
		sort.Strings(cols)
		if !reflect.DeepEqual(got, cols) {
			t.Errorf("%s columns (name type nullable) = %q, want %q", tbl, got, cols)
		}
	}
}

func TestRLS_BlogGrantsAreTheFourVerbs(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	wantPrivs := map[string]bool{
		"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
		"TRUNCATE": false, "REFERENCES": false, "TRIGGER": false,
	}
	for _, tbl := range blogTables {
		for priv, want := range wantPrivs {
			var got bool
			if err := h.super.QueryRow(context.Background(),
				`SELECT has_table_privilege('invoice_app', $1, $2)`, "public."+tbl, priv).Scan(&got); err != nil {
				t.Fatalf("has_table_privilege %s %s: %v", tbl, priv, err)
			}
			if got != want {
				t.Errorf("invoice_app %s on %s = %t, want %t", priv, tbl, got, want)
			}
		}
	}
}

func TestRLS_BlogCategoryIconIsOneOfSixteen(t *testing.T) {
	prefix := blogScope(t)
	if len(blogIcons) != 16 {
		t.Fatalf("test list holds %d icons, want 16", len(blogIcons))
	}
	for i, icon := range blogIcons {
		id := prefix + "-i" + icon
		if err := blogInsertCategory(id, "Icon "+icon+" "+prefix, icon, i%6); err != nil {
			t.Errorf("insert category with icon %q: %v", icon, err)
		}
	}
	if n := blogCount(t, `SELECT count(*) FROM public.blog_categories WHERE id LIKE $1`, prefix+"%"); n != 16 {
		t.Errorf("categories stored = %d, want 16 (one per icon)", n)
	}
	err := blogInsertCategory(prefix+"-rocket", "Icon rocket "+prefix, "rocket", 0)
	blogWantRefused(t, "icon rocket", err, "23514", "")
}

func TestRLS_BlogCategoryToneIsZeroToFive(t *testing.T) {
	prefix := blogScope(t)
	for _, tone := range []int{0, 5} {
		if err := blogInsertCategory(prefix+"-ok"+strconv.Itoa(tone), "Tone ok "+strconv.Itoa(tone)+" "+prefix, "package", tone); err != nil {
			t.Errorf("insert tone %d: %v", tone, err)
		}
	}
	if n := blogCount(t, `SELECT count(*) FROM public.blog_categories WHERE id LIKE $1`, prefix+"%"); n != 2 {
		t.Errorf("categories stored = %d, want 2 (tone 0 and 5)", n)
	}
	for _, tone := range []int{6, -1} {
		err := blogInsertCategory(prefix+"-bad", "Tone bad "+prefix, "package", tone)
		blogWantRefused(t, "tone "+strconv.Itoa(tone), err, "23514", "")
	}
}

func TestRLS_BlogCategoryNameIsUniqueIgnoringCase(t *testing.T) {
	prefix := blogScope(t)
	name := "Blog Test " + prefix
	blogMustCategory(t, prefix+"-a", name)
	if err := blogInsertCategory(prefix+"-b", name+" other", "package", 0); err != nil {
		t.Errorf("insert a different name: %v", err)
	}
	err := blogInsertCategory(prefix+"-c", strings.ToLower(name), "package", 0)
	blogWantRefused(t, "name differing only by case", err, "23505", "blog_categories_name_key")
}

func TestRLS_BlogCategoryPositionsSwapInOneUpdate(t *testing.T) {
	prefix := blogScope(t)
	a, b := prefix+"-a", prefix+"-b"
	blogMustCategory(t, a, "Swap A "+prefix)
	blogMustCategory(t, b, "Swap B "+prefix)
	pos := func(id string) int {
		return blogCount(t, `SELECT position FROM public.blog_categories WHERE id = $1`, id)
	}
	pa, pb := pos(a), pos(b)
	if pa == pb {
		t.Fatalf("seed positions are equal: %d", pa)
	}
	tag, err := h.super.Exec(context.Background(),
		`UPDATE public.blog_categories SET position = CASE id WHEN $1 THEN $4::int WHEN $2 THEN $3::int END WHERE id IN ($1, $2)`,
		a, b, pa, pb)
	if err != nil || tag.RowsAffected() != 2 {
		t.Fatalf("swap positions in one UPDATE: rows = %d, err = %v", tag.RowsAffected(), err)
	}
	if got := []int{pos(a), pos(b)}; got[0] != pb || got[1] != pa {
		t.Errorf("positions after the swap = %v, want [%d %d]", got, pb, pa)
	}
	_, err = h.super.Exec(context.Background(), `UPDATE public.blog_categories SET position = $3 WHERE id IN ($1, $2)`, a, b, 900000+pa)
	blogWantRefused(t, "two categories at one position", err, "23505", "blog_categories_position_key")
}

func TestRLS_BlogCategoryWithPostsCannotBeDeleted(t *testing.T) {
	prefix := blogScope(t)
	cat := prefix + "-c"
	blogMustCategory(t, cat, "Held "+prefix)
	post := blogComplete(prefix+"-p", cat)
	post.status = "draft"
	blogMustPost(t, post)
	_, err := h.super.Exec(context.Background(), `DELETE FROM public.blog_categories WHERE id = $1`, cat)
	blogWantRefused(t, "delete a category that has a post", err, "23503", "")
	if n := blogCount(t, `SELECT count(*) FROM public.blog_categories WHERE id = $1`, cat); n != 1 {
		t.Errorf("category rows after the refused delete = %d, want 1", n)
	}
	if _, err := h.super.Exec(context.Background(), `DELETE FROM public.blog_posts WHERE id = $1`, post.id); err != nil {
		t.Fatalf("delete the post: %v", err)
	}
	if _, err := h.super.Exec(context.Background(), `DELETE FROM public.blog_categories WHERE id = $1`, cat); err != nil {
		t.Errorf("delete the emptied category: %v", err)
	}
}

func TestRLS_BlogOnlyOnePostIsFeatured(t *testing.T) {
	prefix := blogScope(t)
	ctx := context.Background()
	rows, err := h.super.Query(ctx, `SELECT id FROM public.blog_posts WHERE featured`)
	if err != nil {
		t.Fatalf("read featured posts: %v", err)
	}
	prior, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("scan featured posts: %v", err)
	}
	// Registered after blogScope's cleanup, so it runs first and restores the pin after the probe rows are gone.
	t.Cleanup(func() {
		_, _ = h.super.Exec(ctx, `DELETE FROM public.blog_posts WHERE id LIKE $1`, prefix+"%")
		_, _ = h.super.Exec(ctx, `UPDATE public.blog_posts SET featured = true WHERE id = ANY($1)`, prior)
	})
	if _, err := h.super.Exec(ctx, `UPDATE public.blog_posts SET featured = false WHERE featured`); err != nil {
		t.Fatalf("clear the existing pin: %v", err)
	}

	for _, id := range []string{"-n1", "-n2"} {
		if err := (blogPostRow{id: prefix + id, title: "Plain", status: "draft", body: "[]"}).insert(); err != nil {
			t.Errorf("insert non-featured post %s: %v", id, err)
		}
	}
	first := blogPostRow{id: prefix + "-f1", title: "Pinned", status: "draft", body: "[]", featured: true}
	blogMustPost(t, first)

	second := blogPostRow{id: prefix + "-f2", title: "Pinned too", status: "draft", body: "[]", featured: true}
	blogWantRefused(t, "insert a second featured post", second.insert(), "23505", "blog_posts_one_featured")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, prefix+"-n1")
	blogWantRefused(t, "pin a second post", err, "23505", "blog_posts_one_featured")
	if n := blogCount(t, `SELECT count(*) FROM public.blog_posts WHERE featured AND id LIKE $1`, prefix+"%"); n != 1 {
		t.Errorf("featured probe posts = %d, want 1", n)
	}
}

func TestRLS_BlogPublishedPostIsComplete(t *testing.T) {
	prefix := blogScope(t)
	cat := prefix + "-c"
	blogMustCategory(t, cat, "Complete "+prefix)
	blogMustPost(t, blogComplete(prefix+"-ok", cat))

	blank := blogPostRow{id: prefix + "-draft", title: "Draft", status: "draft", body: "[]"}
	if err := blank.insert(); err != nil {
		t.Errorf("insert a draft with summary, category, date, author and body empty: %v", err)
	}

	cases := []struct {
		name string
		edit func(r *blogPostRow)
	}{
		{"summary blank", func(r *blogPostRow) { r.summary = "  " }},
		{"category NULL", func(r *blogPostRow) { r.category = nil }},
		{"date NULL", func(r *blogPostRow) { r.date = nil }},
		{"author NULL", func(r *blogPostRow) { r.authorID, r.authorName = nil, nil }},
		{"body empty", func(r *blogPostRow) { r.body = "[]" }},
	}
	for i, c := range cases {
		r := blogComplete(prefix+"-bad"+string(rune('0'+i)), cat)
		c.edit(&r)
		blogWantRefused(t, "publish with "+c.name, r.insert(), "23514", "blog_posts_published_complete")
	}

	ctx := context.Background()
	_, err := h.super.Exec(ctx, `UPDATE public.blog_posts SET status = 'published' WHERE id = $1`, blank.id)
	blogWantRefused(t, "publish an empty draft", err, "23514", "blog_posts_published_complete")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_posts SET summary = '' WHERE id = $1`, prefix+"-ok")
	blogWantRefused(t, "blank the summary of a published post", err, "23514", "blog_posts_published_complete")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_posts SET status = 'draft', summary = '' WHERE id = $1`, prefix+"-ok")
	if err != nil {
		t.Errorf("unpublish and blank the summary in one UPDATE: %v", err)
	}
}

func TestRLS_BlogPostRefusesBlankTitleAndUnknownStatus(t *testing.T) {
	prefix := blogScope(t)
	ok := blogPostRow{id: prefix + "-ok", title: "Fine", status: "draft", body: "[]"}
	if err := ok.insert(); err != nil {
		t.Fatalf("insert a plain draft: %v", err)
	}
	blank := ok
	blank.id, blank.title = prefix+"-blank", "  "
	blogWantRefused(t, "blank title", blank.insert(), "23514", "")

	status := ok
	status.id, status.status = prefix+"-status", "archived"
	blogWantRefused(t, "status archived", status.insert(), "23514", "")

	idOnly := ok
	idOnly.id, idOnly.authorID = prefix+"-pair", uuid.NewString()
	blogWantRefused(t, "author_id without author_name", idOnly.insert(), "23514", "blog_posts_author_pair")

	nameOnly := ok
	nameOnly.id, nameOnly.authorName = prefix+"-pair2", "Someone"
	blogWantRefused(t, "author_name without author_id", nameOnly.insert(), "23514", "blog_posts_author_pair")
}

func TestRLS_BlogCoverTypeAndCap(t *testing.T) {
	prefix := blogScope(t)
	post := blogPostRow{id: prefix + "-p", title: "Cover", status: "draft", body: "[]"}
	blogMustPost(t, post)
	cover := func(contentType string, n int) error {
		_, err := h.super.Exec(context.Background(),
			`INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, $2, $3)`,
			post.id, contentType, make([]byte, n))
		return err
	}

	blogWantRefused(t, "image/gif", cover("image/gif", 10), "23514", "")
	blogWantRefused(t, "0 bytes", cover("image/jpeg", 0), "23514", "")
	blogWantRefused(t, "one byte over the cap", cover("image/jpeg", blogMaxCover+1), "23514", "")
	if n := blogCount(t, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1`, post.id); n != 0 {
		t.Fatalf("cover rows after three refusals = %d, want 0", n)
	}
	if err := cover("image/jpeg", blogMaxCover); err != nil {
		t.Fatalf("insert a cover of exactly %d bytes: %v", blogMaxCover, err)
	}
	if _, err := h.super.Exec(context.Background(), `DELETE FROM public.blog_posts WHERE id = $1`, post.id); err != nil {
		t.Fatalf("delete the post: %v", err)
	}
	if n := blogCount(t, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1`, post.id); n != 0 {
		t.Errorf("cover rows after the post was deleted = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

func TestRLS_BlogCoverIsOnePerPostAndEditsStayInBounds(t *testing.T) {
	prefix := blogScope(t)
	ctx := context.Background()
	post := blogPostRow{id: prefix + "-p", title: "Cover", status: "draft", body: "[]"}
	blogMustPost(t, post)
	_, err := h.super.Exec(ctx, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/png', '\x01')`, prefix+"-missing")
	blogWantRefused(t, "cover of an unknown post", err, "23503", "")
	if _, err := h.super.Exec(ctx, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/png', '\x01')`, post.id); err != nil {
		t.Fatalf("insert a 1-byte png cover: %v", err)
	}
	_, err = h.super.Exec(ctx, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/jpeg', '\x01')`, post.id)
	blogWantRefused(t, "a second cover for one post", err, "23505", "blog_post_covers_pkey")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_post_covers SET content_type = 'image/webp' WHERE post_id = $1`, post.id)
	blogWantRefused(t, "update to image/webp", err, "23514", "")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_post_covers SET bytes = ''::bytea WHERE post_id = $1`, post.id)
	blogWantRefused(t, "update to 0 bytes", err, "23514", "")
	_, err = h.super.Exec(ctx, `UPDATE public.blog_post_covers SET bytes = $2 WHERE post_id = $1`, post.id, make([]byte, blogMaxCover+1))
	blogWantRefused(t, "update to one byte over the cap", err, "23514", "")
	if _, err := h.super.Exec(ctx, `UPDATE public.blog_post_covers SET content_type = 'image/jpeg', bytes = $2 WHERE post_id = $1`, post.id, make([]byte, blogMaxCover)); err != nil {
		t.Errorf("replace the cover with a jpeg of exactly %d bytes: %v", blogMaxCover, err)
	}
}

type blogAuthor struct {
	id, email string
	name      *string
}

func blogCallAuthors(actor any) ([]blogAuthor, error) {
	rows, err := h.app.Query(context.Background(),
		`SELECT user_id::text, display_name, coalesce(email, '') FROM public.staff_read_blog_authors($1::uuid)`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []blogAuthor
	for rows.Next() {
		var a blogAuthor
		if err := rows.Scan(&a.id, &a.name, &a.email); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// blogSeedMember commits an active or suspended membership and sets its display name and email.
func blogSeedMember(t *testing.T, tenant, status string, name *string, email string) string {
	t.Helper()
	user := seedCommittedMembership(t, tenant, "reviewer", status)
	if _, err := h.super.Exec(context.Background(),
		`UPDATE memberships SET display_name = $2, email = $3 WHERE user_id = $1 AND tenant_id = $4`, user, name, email, tenant); err != nil {
		t.Fatalf("name the seeded member: %v", err)
	}
	return user
}

func blogStr(s string) *string { return &s }

func TestRLS_BlogAuthorsRefuseANonStaffActor(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	staff := seedCommittedMembership(t, house, "reviewer", "active")
	if rows, err := blogCallAuthors(staff); err != nil || len(rows) == 0 {
		t.Fatalf("active house actor: rows = %d, err = %v, want at least the actor", len(rows), err)
	}
	_, err := blogCallAuthors(uuid.NewString())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || !strings.HasSuffix(pgErr.Message, ": actor is not staff") {
		t.Fatalf("random actor: got %v, want SQLSTATE 42501 ending \": actor is not staff\"", err)
	}
}

func TestRLS_BlogAuthorsAreActiveHouseMembers(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	r := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	actor := seedCommittedMembership(t, house, "reviewer", "active")
	zed := blogSeedMember(t, house, "active", blogStr("Zed "+r), "zed-"+r+"@ascomply.invalid")
	amy := blogSeedMember(t, house, "active", blogStr("Amy "+r), "amy-"+r+"@ascomply.invalid")
	nameless := blogSeedMember(t, house, "active", blogStr("  "), "blank-"+r+"@ascomply.invalid")
	suspended := blogSeedMember(t, house, "suspended", blogStr("Sus "+r), "sus-"+r+"@ascomply.invalid")
	invited := blogSeedMember(t, house, "invited", blogStr("Inv "+r), "inv-"+r+"@ascomply.invalid")
	other := blogSeedMember(t, h.tenantA, "active", blogStr("Other "+r), "other-"+r+"@ascomply.invalid")

	got, err := blogCallAuthors(actor)
	if err != nil {
		t.Fatalf("call as an active house actor: %v", err)
	}
	var order []string
	byID := map[string]blogAuthor{}
	for _, a := range got {
		byID[a.id] = a
		if slices.Contains([]string{zed, amy, nameless}, a.id) {
			order = append(order, a.id)
		}
		if a.id == suspended || a.id == invited || a.id == other {
			t.Errorf("author list holds %s, a suspended, invited or other-tenant member", a.id)
		}
	}
	if want := []string{amy, zed, nameless}; !slices.Equal(order, want) {
		t.Fatalf("seeded authors in list order = %v, want Amy, Zed, nameless %v", order, want)
	}
	for id, want := range map[string][2]string{amy: {"Amy " + r, "amy-" + r + "@ascomply.invalid"}, zed: {"Zed " + r, "zed-" + r + "@ascomply.invalid"}} {
		a := byID[id]
		if a.name == nil || *a.name != want[0] || a.email != want[1] {
			t.Errorf("author %s = %v / %q, want %q / %q", id, a.name, a.email, want[0], want[1])
		}
	}
	if n := byID[nameless]; n.name != nil || n.email != "blank-"+r+"@ascomply.invalid" {
		t.Errorf("blank-name author = %v / %q, want NULL / its email", n.name, n.email)
	}
	seenNameless := false
	for _, a := range got {
		if a.name == nil {
			seenNameless = true
		} else if seenNameless {
			t.Errorf("named author %q listed after a nameless one", *a.name)
		}
	}
}

func TestRLS_BlogAuthorsListANamelessMemberWithANullName(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	r := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	actor := seedCommittedMembership(t, house, "reviewer", "active")
	isNull := blogSeedMember(t, house, "active", nil, "a-null-"+r+"@ascomply.invalid")
	blank := blogSeedMember(t, house, "active", blogStr("  "), "b-blank-"+r+"@ascomply.invalid")

	got, err := blogCallAuthors(actor)
	if err != nil {
		t.Fatalf("call as an active house actor: %v", err)
	}
	want := map[string]string{isNull: "a-null-" + r + "@ascomply.invalid", blank: "b-blank-" + r + "@ascomply.invalid"}
	found := 0
	for _, a := range got {
		email, ok := want[a.id]
		if !ok {
			continue
		}
		found++
		if a.name != nil || a.email != email {
			t.Errorf("member %s = %v / %q, want NULL / %q", a.id, a.name, a.email, email)
		}
	}
	if found != 2 {
		t.Errorf("nameless members listed = %d, want 2", found)
	}
}

func TestRLS_BlogAuthorsFunctionIsAStaffReader(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	ctx := context.Background()
	var definer bool
	var owner string
	var config []string
	if err := h.super.QueryRow(ctx,
		`SELECT prosecdef, pg_get_userbyid(proowner), coalesce(proconfig, '{}') FROM pg_proc WHERE oid = to_regprocedure($1)`,
		blogAuthorsSig).Scan(&definer, &owner, &config); err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}
	if !definer || owner != "staff_reader" || !slices.Equal(config, []string{`search_path=""`}) {
		t.Errorf("security definer = %t, owner = %q, proconfig = %q, want true, staff_reader, [search_path=\"\"]", definer, owner, config)
	}
	var app bool
	if err := h.super.QueryRow(ctx, `SELECT has_function_privilege('invoice_app', $1, 'EXECUTE')`, blogAuthorsSig).Scan(&app); err != nil {
		t.Fatalf("has_function_privilege: %v", err)
	}
	if !app {
		t.Error("invoice_app holds no EXECUTE, want it")
	}
	public := blogCount(t,
		`SELECT count(*) FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
		  WHERE p.oid = to_regprocedure($1) AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'`, blogAuthorsSig)
	if public != 0 {
		t.Errorf("PUBLIC holds EXECUTE on %s, want none", blogAuthorsSig)
	}
	rows, err := h.super.Query(ctx,
		`SELECT pg_get_userbyid(a.grantee) FROM pg_proc p, aclexplode(p.proacl) a WHERE p.oid = to_regprocedure($1) ORDER BY 1`, blogAuthorsSig)
	if err != nil {
		t.Fatalf("read the function ACL: %v", err)
	}
	grantees, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("scan the function ACL: %v", err)
	}
	if want := []string{"invoice_app", "staff_reader"}; !slices.Equal(grantees, want) {
		t.Errorf("EXECUTE grantees = %q, want %q", grantees, want)
	}
}

func TestRLS_BlogAuthorsRefuseEveryActorThatIsNotAnActiveHouseMember(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	if rows, err := blogCallAuthors(seedCommittedMembership(t, house, "reviewer", "active")); err != nil || len(rows) == 0 {
		t.Fatalf("active house actor: rows = %d, err = %v, want at least the actor", len(rows), err)
	}
	actors := map[string]any{
		"active member of another tenant": seedCommittedMembership(t, h.tenantA, "reviewer", "active"),
		"suspended house member":          seedCommittedMembership(t, house, "reviewer", "suspended"),
		"invited house member":            seedCommittedMembership(t, house, "reviewer", "invited"),
		"NULL actor":                      nil,
	}
	for what, actor := range actors {
		_, err := blogCallAuthors(actor)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: got %v, want SQLSTATE 42501", what, err)
		}
	}
}

func TestRLS_BlogAuthorsAreOrderedByNameThenNamelessByEmail(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	r := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	mail := func(p string) string { return p + "-" + r + "@ascomply.invalid" }
	actor := seedCommittedMembership(t, house, "reviewer", "active")
	// Nameless: NULL, empty and blank names, emails out of seeding order. Named: one padded name, two with the same name.
	nlZ := blogSeedMember(t, house, "active", nil, mail("z"))
	nlA := blogSeedMember(t, house, "active", blogStr(""), mail("a"))
	nlM := blogSeedMember(t, house, "active", blogStr("  "), mail("m"))
	sameZ := blogSeedMember(t, house, "active", blogStr("Same "+r), mail("zz"))
	sameB := blogSeedMember(t, house, "active", blogStr("Same "+r), mail("bb"))
	padded := blogSeedMember(t, house, "active", blogStr("  Aaa "+r+"  "), mail("pad"))

	got, err := blogCallAuthors(actor)
	if err != nil {
		t.Fatalf("call as an active house actor: %v", err)
	}
	seeded := []string{padded, sameB, sameZ, nlA, nlM, nlZ}
	var order []string
	byID := map[string]blogAuthor{}
	for _, a := range got {
		byID[a.id] = a
		if slices.Contains(seeded, a.id) {
			order = append(order, a.id)
		}
	}
	if !slices.Equal(order, seeded) {
		t.Fatalf("seeded authors in list order = %v, want %v (named by name then email, then nameless by email)", order, seeded)
	}
	if n := byID[padded]; n.name == nil || *n.name != "Aaa "+r {
		t.Errorf("padded name came back as %v, want %q (trimmed)", n.name, "Aaa "+r)
	}
	for _, id := range []string{nlA, nlM, nlZ} {
		if byID[id].name != nil {
			t.Errorf("nameless member %s has name %q, want NULL", id, *byID[id].name)
		}
	}
}

func TestRLS_BlogAuthorsListEachPersonOnceFromTheirOldestActiveMembership(t *testing.T) {
	requireHarness(t)
	requireBlogAuthors(t)
	house := adoptHouseTenant(t)
	r := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	actor := seedCommittedMembership(t, house, "reviewer", "active")
	// Also in tenant A, older and active: the older row names the person (D-26).
	both := blogSeedMember(t, house, "active", blogStr("Newer "+r), "newer-"+r+"@ascomply.invalid")
	// Also in tenant A, older but suspended: the active house row names the person.
	oldSuspended := blogSeedMember(t, house, "active", blogStr("House "+r), "house-"+r+"@ascomply.invalid")
	older := func(user, status, name string) {
		t.Helper()
		id := uuid.NewString()
		if _, err := h.super.Exec(context.Background(),
			`INSERT INTO memberships (id, tenant_id, user_id, role, status, display_name, email, created_at)
			 VALUES ($1, $2, $3, 'reviewer', $4, $5, $6, now() - interval '30 days')`,
			id, h.tenantA, user, status, name, strings.ToLower(name)+"@ascomply.invalid"); err != nil {
			t.Fatalf("seed an older %s membership: %v", status, err)
		}
		t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id) })
	}
	older(both, "active", "Older"+r)
	older(oldSuspended, "suspended", "Gone"+r)

	got, err := blogCallAuthors(actor)
	if err != nil {
		t.Fatalf("call as an active house actor: %v", err)
	}
	rows := map[string][]blogAuthor{}
	for _, a := range got {
		rows[a.id] = append(rows[a.id], a)
	}
	if len(rows[both]) != 1 || rows[both][0].name == nil || *rows[both][0].name != "Older"+r {
		t.Errorf("person with two active memberships = %+v, want one row named %q", rows[both], "Older"+r)
	}
	if len(rows[oldSuspended]) != 1 || rows[oldSuspended][0].name == nil || *rows[oldSuspended][0].name != "House "+r {
		t.Errorf("person with an older suspended membership = %+v, want one row named %q", rows[oldSuspended], "House "+r)
	}
}

func TestRLS_BlogAppRoleReadsAndWritesAllThreeTables(t *testing.T) {
	prefix := blogScope(t)
	ctx := context.Background()
	cat, post := prefix+"-c", prefix+"-p"
	steps := []struct {
		what, sql string
		args      []any
	}{
		{"insert category", `INSERT INTO public.blog_categories (id, name, headline, icon, tone, position)
			VALUES ($1, 'App ' || $1, 'h', 'package', 1, (SELECT coalesce(max(position), 0) + 1 FROM public.blog_categories))`, []any{cat}},
		{"insert post", `INSERT INTO public.blog_posts (id, title, category_id) VALUES ($1, 't', $2)`, []any{post, cat}},
		{"insert cover", `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/png', '\x01')`, []any{post}},
		{"update category", `UPDATE public.blog_categories SET tone = 2 WHERE id = $1`, []any{cat}},
		{"update post", `UPDATE public.blog_posts SET title = 'u', updated_at = now() WHERE id = $1`, []any{post}},
		{"update cover", `UPDATE public.blog_post_covers SET content_type = 'image/jpeg' WHERE post_id = $1`, []any{post}},
		{"delete cover", `DELETE FROM public.blog_post_covers WHERE post_id = $1`, []any{post}},
		{"delete post", `DELETE FROM public.blog_posts WHERE id = $1`, []any{post}},
		{"delete category", `DELETE FROM public.blog_categories WHERE id = $1`, []any{cat}},
	}
	for i, s := range steps {
		if i == 6 {
			for tbl, id := range map[string]string{"blog_categories": cat, "blog_posts": post, "blog_post_covers": post} {
				col := map[string]string{"blog_post_covers": "post_id"}[tbl]
				if col == "" {
					col = "id"
				}
				var n int
				if err := h.app.QueryRow(ctx, `SELECT count(*) FROM public.`+tbl+` WHERE `+col+` = $1`, id).Scan(&n); err != nil || n != 1 {
					t.Errorf("invoice_app read-back of %s = %d, err = %v, want 1 (no RLS)", tbl, n, err)
				}
			}
		}
		tag, err := h.app.Exec(ctx, s.sql, s.args...)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("invoice_app %s: rows = %d, err = %v", s.what, tag.RowsAffected(), err)
		}
	}
}

func TestRLS_BlogRowShapeCHECKsAndKeys(t *testing.T) {
	prefix := blogScope(t)
	ctx := context.Background()
	blogMustCategory(t, prefix+"-c", "Shape "+prefix)

	slugOK := strings.Repeat("a", 80)
	for _, id := range []string{"a", "a-1-b", slugOK} {
		if err := blogInsertCategory(id, "Slug "+id, "package", 0); err != nil {
			t.Errorf("category id %q refused: %v", id, err)
		}
		_, _ = h.super.Exec(ctx, `DELETE FROM public.blog_categories WHERE id = $1`, id)
	}
	for _, id := range []string{"", "Upper", "under_score", "sp ace", "a/b", "ab\n", slugOK + "a"} {
		blogWantRefused(t, "category id "+strconv.Quote(id), blogInsertCategory(id, "Slug bad", "package", 0), "23514", "blog_categories_id_check")
		bad := blogPostRow{id: id, title: "t", status: "draft", body: "[]"}
		blogWantRefused(t, "post id "+strconv.Quote(id), bad.insert(), "23514", "blog_posts_id_check")
	}
	if err := (blogPostRow{id: slugOK, title: "t", status: "draft", body: "[]"}).insert(); err != nil {
		t.Errorf("post id of 80 chars refused: %v", err)
	}
	_, _ = h.super.Exec(ctx, `DELETE FROM public.blog_posts WHERE id = $1`, slugOK)

	blogWantRefused(t, "blank category name", blogInsertCategory(prefix+"-blank", "  ", "package", 0), "23514", "blog_categories_name_check")
	blogWantRefused(t, "duplicate category id", blogInsertCategory(prefix+"-c", "Shape other "+prefix, "package", 0), "23505", "blog_categories_pkey")
	_, err := h.super.Exec(ctx, `INSERT INTO public.blog_categories (id, name, headline, icon, tone, position)
		SELECT $1, 'Same position ' || $1, 'h', 'package', 0, position FROM public.blog_categories WHERE id = $2`, prefix+"-dup", prefix+"-c")
	blogWantRefused(t, "insert at a taken position", err, "23505", "blog_categories_position_key")

	for _, body := range []string{`{}`, `"text"`, `null`, `1`} {
		r := blogPostRow{id: prefix + "-b", title: "t", status: "draft", body: body}
		blogWantRefused(t, "body "+body, r.insert(), "23514", "blog_posts_body_check")
	}
	unknown := blogPostRow{id: prefix + "-u", category: prefix + "-nope", title: "t", status: "draft", body: "[]"}
	blogWantRefused(t, "post in an unknown category", unknown.insert(), "23503", "blog_posts_category_id_fkey")
	dup := blogPostRow{id: prefix + "-d", title: "t", status: "draft", body: "[]"}
	blogMustPost(t, dup)
	blogWantRefused(t, "duplicate post id", dup.insert(), "23505", "blog_posts_pkey")

	var idx string
	if err := h.super.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'blog_posts_category_idx'`).Scan(&idx); err != nil {
		t.Fatalf("read blog_posts_category_idx: %v", err)
	}
	if !strings.Contains(idx, "(category_id)") {
		t.Errorf("blog_posts_category_idx = %q, want an index on (category_id)", idx)
	}
}

// Runs the real Down then Up of the blog migration in a rolled-back tx, so no seeded blog row is lost.
func TestRLS_BlogMigrationDownLeavesNoObjectAndUpRestoresThem(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	requireBlogAuthors(t)
	ctx := context.Background()
	matches, err := fs.Glob(migrations.FS, "*_blog.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("blog migration files = %v, err = %v, want exactly 1", matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	up, down, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok || !strings.Contains(up, "-- +goose Up") {
		t.Fatalf("%s has no Up and Down sections", matches[0])
	}
	objects := func(tx pgx.Tx) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relname LIKE 'blog\_%')
			+ (SELECT count(*) FROM pg_proc WHERE pronamespace = 'public'::regnamespace AND proname = 'staff_read_blog_authors')
			+ (SELECT count(*) FROM pg_type WHERE typnamespace = 'public'::regnamespace AND typname LIKE 'blog\_%' AND typtype <> 'c' AND typelem = 0)`).Scan(&n); err != nil {
			t.Fatalf("count blog objects: %v", err)
		}
		return n
	}
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if n := objects(tx); n == 0 {
		t.Fatal("no blog object before the Down: the count query matches nothing")
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("run the Down: %v", err)
	}
	if n := objects(tx); n != 0 {
		t.Errorf("blog objects after the Down = %d, want 0", n)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("run the Up again after the Down: %v", err)
	}
	if n := objects(tx); n == 0 {
		t.Error("no blog object after Down then Up")
	}
}
