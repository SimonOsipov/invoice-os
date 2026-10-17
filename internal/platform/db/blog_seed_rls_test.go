// The seed migration (*_seed_blog.sql) holds the prototype blog content as authorless drafts.
// Tests read committed rows through the superuser pool and the embedded migration text.
package db_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/blog"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	blogSeedUser = "b1000000-0000-0000-0000-000000000001"
	blogSeedName = "ASComply Editorial"
)

type blogSeedPost struct {
	id, category, date string
	blocks             int
}

// blogSeedPosts is the 20 prototype posts in blog-data.js order (decision D-35).
var blogSeedPosts = []blogSeedPost{
	{"fifty-more-pilot", "company", "2026-10-14", 7},
	{"what-changes", "regulation", "2026-10-09", 10},
	{"recognition-learns", "product", "2026-10-02", 7},
	{"sage-connector", "partners", "2026-09-24", 6},
	{"phased-rollout", "regulation", "2026-09-18", 7},
	{"approval-thresholds", "product", "2026-09-11", 6},
	{"sealed-rules", "engineering", "2026-09-04", 6},
	{"lagos-office", "company", "2026-08-27", 4},
	{"tin-errors", "regulation", "2026-08-20", 6},
	{"submission-retries", "engineering", "2026-08-13", 5},
	{"firm-portfolio", "customers", "2026-08-06", 6},
	{"csv-import", "product", "2026-07-30", 6},
	{"si-app-partners", "partners", "2026-07-23", 5},
	{"lagos-freight", "customers", "2026-07-16", 6},
	{"evidence-bundles", "product", "2026-06-25", 4},
	{"readiness-assessment", "company", "2026-06-18", 5},
	{"records-retention", "regulation", "2026-05-28", 6},
	{"audit-log", "engineering", "2026-05-14", 5},
	{"consultant-programme", "partners", "2026-04-30", 4},
	{"sahara-foods", "customers", "2026-04-16", 6},
}

func blogSeedIDs() []string {
	ids := make([]string, len(blogSeedPosts))
	for i, p := range blogSeedPosts {
		ids[i] = p.id
	}
	return ids
}

// blogSeedMigration returns the text of the one *_seed_blog.sql in migrations.FS.
func blogSeedMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, "*_seed_blog.sql")
	if err != nil {
		t.Fatalf("glob *_seed_blog.sql in migrations.FS: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("seed migration not found: migrations.FS holds %d files matching *_seed_blog.sql (%v), want exactly 1", len(matches), matches)
	}
	b, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(b)
}

// stripSQLComments drops -- and /* */ comments and leaves quoted strings (with doubled-quote escapes) intact.
func stripSQLComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(s))
			out.WriteString(s[i:end])
			i = end
		case strings.HasPrefix(s[i:], "--"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func TestRLS_BlogSeedHasTheSixCategories(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	type cat struct {
		id, name, icon string
		tone, position int
	}
	want := []cat{
		{"product", "Product updates", "package", 0, 1},
		{"regulation", "Regulation & FIRS news", "landmark", 1, 2},
		{"customers", "Customer stories", "building-2", 2, 3},
		{"company", "Company news", "megaphone", 3, 4},
		{"partners", "Partner announcements", "handshake", 4, 5},
		{"engineering", "Engineering", "code-xml", 5, 6},
	}
	ids := make([]string, len(want))
	for i, c := range want {
		ids[i] = c.id
	}
	rows, err := h.super.Query(context.Background(),
		`SELECT id, name, icon, tone::int, position FROM public.blog_categories WHERE id = ANY($1) ORDER BY position`, ids)
	if err != nil {
		t.Fatalf("read seeded categories: %v", err)
	}
	defer rows.Close()
	var got []cat
	for rows.Next() {
		var c cat
		if err := rows.Scan(&c.id, &c.name, &c.icon, &c.tone, &c.position); err != nil {
			t.Fatalf("scan category: %v", err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read categories: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("seeded categories found = %d, want %d: the seed migration is not applied", len(got), len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seeded categories = %+v, want %+v", got, want)
	}
	var headline, intro string
	if err := h.super.QueryRow(context.Background(),
		`SELECT headline, intro FROM public.blog_categories WHERE id = 'regulation'`).Scan(&headline, &intro); err != nil {
		t.Fatalf("read regulation copy: %v", err)
	}
	if headline != "The rules, explained plainly." {
		t.Errorf("regulation headline = %q", headline)
	}
	if intro != "What Nigeria's e-invoicing rollout asks of your business, and when." {
		t.Errorf("regulation intro = %q", intro)
	}
}

func TestRLS_BlogSeedHasTheTwentyPosts(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	rows, err := h.super.Query(context.Background(),
		`SELECT id, category_id, publish_date::text, jsonb_array_length(body) FROM public.blog_posts WHERE id = ANY($1)`, blogSeedIDs())
	if err != nil {
		t.Fatalf("read seeded posts: %v", err)
	}
	defer rows.Close()
	got := map[string]blogSeedPost{}
	for rows.Next() {
		var p blogSeedPost
		if err := rows.Scan(&p.id, &p.category, &p.date, &p.blocks); err != nil {
			t.Fatalf("scan post: %v", err)
		}
		got[p.id] = p
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read posts: %v", err)
	}
	if len(got) != len(blogSeedPosts) {
		t.Fatalf("seeded posts found = %d, want %d: the seed migration is not applied", len(got), len(blogSeedPosts))
	}
	for _, want := range blogSeedPosts {
		if got[want.id] != want {
			t.Errorf("post %s = %+v, want %+v", want.id, got[want.id], want)
		}
	}
}

var blogInsertInto = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(?:public\.)?(blog_[a-z_]+)\b`)

func TestRLS_BlogSeedPostsAreAuthorlessDrafts(t *testing.T) {
	sql := stripSQLComments(blogSeedMigration(t))
	if strings.TrimSpace(sql) == "" {
		t.Fatal("seed migration holds no SQL")
	}
	if strings.Contains(strings.ToLower(sql), "blog_post_covers") {
		t.Error("seed migration names blog_post_covers: the seed posts have no cover")
	}
	all := blogInsertInto.FindAllStringSubmatch(sql, -1)
	var posts int
	for _, m := range all {
		if strings.EqualFold(m[1], "blog_posts") {
			posts++
		}
	}
	if posts == 0 {
		t.Fatal("seed migration holds no INSERT INTO blog_posts")
	}
	listed := regexp.MustCompile(`(?is)INSERT\s+INTO\s+(?:public\.)?blog_posts\s*\(([^)]*)\)`).FindAllStringSubmatch(sql, -1)
	if len(listed) != posts {
		t.Errorf("%d of %d blog_posts INSERTs name no column list", posts-len(listed), posts)
	}
	for _, m := range listed {
		if cols := strings.Join(strings.Fields(m[1]), " "); cols != "id, category_id, publish_date, title, summary, body" {
			t.Errorf("blog_posts INSERT columns = (%s), want (id, category_id, publish_date, title, summary, body)", cols)
		}
	}

	requireHarness(t)
	requireBlogTables(t)
	var found, featured, covers int
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE featured) FROM public.blog_posts WHERE id = ANY($1)`, blogSeedIDs()).Scan(&found, &featured); err != nil {
		t.Fatalf("read seeded posts: %v", err)
	}
	if found != len(blogSeedPosts) {
		t.Fatalf("seeded posts found = %d, want %d: the seed migration is not applied", found, len(blogSeedPosts))
	}
	covers = blogCount(t, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = ANY($1)`, blogSeedIDs())
	if featured != 0 || covers != 0 {
		t.Errorf("seeded posts featured = %d, with a cover = %d, want 0 and 0", featured, covers)
	}
}

func TestRLS_BlogSeedMigrationHoldsNoUUID(t *testing.T) {
	uuidRe := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-`)
	if !uuidRe.MatchString("x " + blogSeedUser + " y") {
		t.Fatal("control: the UUID regexp does not match a planted UUID")
	}
	sql := stripSQLComments(blogSeedMigration(t))
	inserts := map[string]bool{}
	for _, m := range blogInsertInto.FindAllStringSubmatch(sql, -1) {
		inserts[strings.ToLower(m[1])] = true
	}
	if !inserts["blog_categories"] || !inserts["blog_posts"] {
		t.Fatalf("seed migration INSERTs into %v, want blog_categories and blog_posts", inserts)
	}
	if loc := uuidRe.FindStringIndex(sql); loc != nil {
		t.Errorf("seed migration holds a UUID near %q", sql[loc[0]:min(loc[1]+20, len(sql))])
	}
}

type blogSeedBody struct {
	id   string
	raw  []byte
	body []blog.Block
}

func blogSeedBodies(t *testing.T) []blogSeedBody {
	t.Helper()
	requireHarness(t)
	requireBlogTables(t)
	rows, err := h.super.Query(context.Background(), `SELECT id, body::text FROM public.blog_posts WHERE id = ANY($1) ORDER BY id`, blogSeedIDs())
	if err != nil {
		t.Fatalf("read seeded bodies: %v", err)
	}
	defer rows.Close()
	var out []blogSeedBody
	for rows.Next() {
		var b blogSeedBody
		var text string
		if err := rows.Scan(&b.id, &text); err != nil {
			t.Fatalf("scan body: %v", err)
		}
		b.raw = []byte(text)
		if err := json.Unmarshal(b.raw, &b.body); err != nil {
			t.Fatalf("decode body of %s: %v", b.id, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read bodies: %v", err)
	}
	if len(out) != len(blogSeedPosts) {
		t.Fatalf("seeded posts found = %d, want %d: the seed migration is not applied", len(out), len(blogSeedPosts))
	}
	return out
}

func TestRLS_BlogSeedBodiesAreValidAndClean(t *testing.T) {
	totals := map[string]int{}
	for _, p := range blogSeedBodies(t) {
		if err := blog.ValidateBlocks(p.body); err != nil {
			t.Errorf("%s: ValidateBlocks: %v", p.id, err)
		}
		clean, err := json.Marshal(blog.CleanBody(p.body))
		if err != nil {
			t.Fatalf("%s: marshal cleaned body: %v", p.id, err)
		}
		var stored, cleaned any
		if err := json.Unmarshal(p.raw, &stored); err != nil {
			t.Fatalf("%s: decode stored body: %v", p.id, err)
		}
		if err := json.Unmarshal(clean, &cleaned); err != nil {
			t.Fatalf("%s: decode cleaned body: %v", p.id, err)
		}
		if !reflect.DeepEqual(stored, cleaned) {
			t.Errorf("%s: CleanBody changes the stored body\nstored  %s\ncleaned %s", p.id, p.raw, clean)
		}
		for _, b := range p.body {
			k := b.Type
			if b.Type == "note" {
				k += ":" + b.Kind
			}
			if b.Type == "demo" {
				k += ":" + b.Demo
			}
			totals[k]++
		}
	}
	want := map[string]int{
		"p": 50, "h": 38, "ul": 17, "quote": 3,
		"note:regulation": 3, "note:deadline": 2, "note:product": 1,
		"demo:import": 1, "demo:approval": 1, "demo:clearance": 1,
	}
	if !reflect.DeepEqual(totals, want) {
		t.Errorf("block totals = %v, want %v", totals, want)
	}
}

func TestRLS_BlogSeedKeepsApostrophesAndCallouts(t *testing.T) {
	bodies := map[string]blogSeedBody{}
	for _, p := range blogSeedBodies(t) {
		bodies[p.id] = p
	}
	tin := bodies["tin-errors"]
	if len(tin.body) == 0 {
		t.Fatal("tin-errors has no blocks")
	}
	last := tin.body[len(tin.body)-1]
	wantLast := blog.Block{Type: "note", Kind: "regulation", Title: "VAT rate",
		Text: "The standard VAT rate used in these checks is 7.5%. Exempt and zero-rated lines are handled by their own rules."}
	if !reflect.DeepEqual(last, wantLast) {
		t.Errorf("tin-errors last block = %+v, want %+v", last, wantLast)
	}
	firm := bodies["firm-portfolio"]
	if len(firm.body) == 0 {
		t.Fatal("firm-portfolio has no blocks")
	}
	if first := firm.body[0]; !strings.Contains(first.Text, "each client's invoice checks") {
		t.Errorf("firm-portfolio first block text = %q, want it to contain %q", first.Text, "each client's invoice checks")
	}
}

// Down removes only the seed ids and Up restores them as authorless drafts; a staff-made category and post survive.
// Runs in a rolled-back tx, so the seeded rows are untouched.
func TestRLS_BlogSeedMigrationDownKeepsStaffRowsAndUpRestoresTheSeed(t *testing.T) {
	requireHarness(t)
	requireBlogTables(t)
	ctx := context.Background()
	up, down, ok := strings.Cut(blogSeedMigration(t), "-- +goose Down")
	if !ok || !strings.Contains(up, "-- +goose Up") {
		t.Fatal("seed migration has no Up and Down sections")
	}
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`INSERT INTO public.blog_categories (id, name, headline, intro, icon, tone, position) VALUES ('qa-staff-cat', 'QA staff category', '', '', 'package', 0, 99)`,
		`INSERT INTO public.blog_posts (id, category_id, title) VALUES ('qa-staff-post', 'qa-staff-cat', 'QA staff post')`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("plant a staff row: %v", err)
		}
	}
	count := func() (seedPosts, seedCats, staffPosts, staffCats, authored int) {
		t.Helper()
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM public.blog_posts WHERE id = ANY($1)),
			(SELECT count(*) FROM public.blog_categories WHERE id = ANY($2)),
			(SELECT count(*) FROM public.blog_posts WHERE id = 'qa-staff-post'),
			(SELECT count(*) FROM public.blog_categories WHERE id = 'qa-staff-cat'),
			(SELECT count(*) FROM public.blog_posts WHERE id = ANY($1) AND (author_id IS NOT NULL OR status <> 'draft' OR featured))`,
			blogSeedIDs(), []string{"product", "regulation", "customers", "company", "partners", "engineering"}).
			Scan(&seedPosts, &seedCats, &staffPosts, &staffCats, &authored); err != nil {
			t.Fatalf("count: %v", err)
		}
		return
	}
	if sp, sc, stp, stc, _ := count(); sp != 20 || sc != 6 || stp != 1 || stc != 1 {
		t.Fatalf("before Down: seed posts %d, seed categories %d, staff post %d, staff category %d, want 20 6 1 1", sp, sc, stp, stc)
	}
	// The staff post sits in the staff category, so Down's category delete never meets a foreign key.
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("run the Down: %v", err)
	}
	if sp, sc, stp, stc, _ := count(); sp != 0 || sc != 0 || stp != 1 || stc != 1 {
		t.Errorf("after Down: seed posts %d, seed categories %d, staff post %d, staff category %d, want 0 0 1 1", sp, sc, stp, stc)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("run the Up again: %v", err)
	}
	if sp, sc, stp, stc, authored := count(); sp != 20 || sc != 6 || stp != 1 || stc != 1 || authored != 0 {
		t.Errorf("after Up: seed posts %d, seed categories %d, staff post %d, staff category %d, authored/published/featured %d, want 20 6 1 1 0", sp, sc, stp, stc, authored)
	}
}
