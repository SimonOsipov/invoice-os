// [M4-21-03] Test-first (RED) suite for db.Seed (task-127, Test Spec row 7 /
// AC-7). Pre-authored BEFORE bootstrap.go's real Seed body exists — the shipped
// stub always returns a non-nil "not implemented" error, so the one test below
// fails immediately on that assertion, never on a missing symbol.
//
// Design mirrors bootstrap_test.go's conventions in this package: env-gated skip
// on DATABASE_SUPERUSER_URL only (Seed runs exclusively as the superuser —
// tenants is FORCE RLS), reusing its requireSuperuserDSN / bootstrapSuperuserPool
// helpers rather than duplicating them. The "migrated schema" precondition is
// already satisfied by this package's shared dev/CI Postgres (every other test in
// this package depends on the same already-migrated schema), so this test does
// not re-migrate; it only exercises Seed itself.
package db_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedTenants mirrors db/seed.dev.sql's fixed tenant rows (id, name, kind)
// exactly, so a stale/incomplete embed or a half-applying Seed is caught by a
// mismatched id/name/kind, not just "no error".
var seedTenants = []struct {
	id, name, kind string
}{
	{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "Tenant A (dev)", "firm"},
	{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "Tenant B (dev)", "firm"},
	{"11111111-1111-1111-1111-111111111111", "Okafor & Partners", "firm"},
	{"22222222-2222-2222-2222-222222222222", "Honeywell Group", "in_house"},
	{"a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0", "ASComply (dev)", "firm"},
}

// seedMemberships mirrors db/seed.dev.sql's fixed membership rows exactly.
var seedMemberships = []struct {
	tenantID, userID, role string
}{
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000001", "admin"},
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000003", "preparer"},
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000004", "reviewer"},
	{"22222222-2222-2222-2222-222222222222", "c0000000-0000-0000-0000-000000000002", "admin"},
}

// TestSeedFromEmbeddedIsIdempotent: Test Spec row 7 / AC-7. Calls db.Seed twice
// against dbsql.FS (the embedded copy of db/seed.dev.sql, never the on-disk
// file); neither call may error, and each seeded tenant + membership
// must appear EXACTLY ONCE with the expected kind/role — proving both idempotency
// (ON CONFLICT DO UPDATE / DO NOTHING actually holds across a repeated call) and
// that the embedded file matches the on-disk fixture data, not a stale copy.
func TestSeedFromEmbeddedIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}

	for _, tc := range seedTenants {
		var count int
		var kind string
		err := pool.QueryRow(ctx,
			`SELECT count(*), max(kind) FROM tenants WHERE id = $1 AND name = $2 GROUP BY kind`,
			tc.id, tc.name,
		).Scan(&count, &kind)
		if err != nil {
			t.Fatalf("query tenant %s (%s): %v (does it exist at all after Seed?)", tc.id, tc.name, err)
		}
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after two Seed calls, want exactly 1", tc.id, tc.name, count)
		}
		if kind != tc.kind {
			t.Errorf("tenant %s (%s): kind = %q, want %q", tc.id, tc.name, kind, tc.kind)
		}
	}

	for _, tc := range seedMemberships {
		var count int
		var role string
		err := pool.QueryRow(ctx,
			`SELECT count(*), max(role) FROM memberships WHERE tenant_id = $1 AND user_id = $2 GROUP BY role`,
			tc.tenantID, tc.userID,
		).Scan(&count, &role)
		if err != nil {
			t.Fatalf("query membership %s/%s: %v (does it exist at all after Seed?)", tc.tenantID, tc.userID, err)
		}
		if count != 1 {
			t.Errorf("membership %s/%s: found %d rows after two Seed calls, want exactly 1", tc.tenantID, tc.userID, count)
		}
		if role != tc.role {
			t.Errorf("membership %s/%s: role = %q, want %q", tc.tenantID, tc.userID, role, tc.role)
		}
	}
}

// ---- QA adversarial coverage (post-implementation, Mode B, task-127) ------------

// TestSeedConcurrentInvocationIsSafeWithoutALock: adversarial coverage for AC-7.
// db.Seed deliberately takes NO advisory lock of its own (unlike Bootstrap;
// Provision serializes the reset/purge/seed tail one layer up, for the purge's
// reverse lock order rather than for Seed) — the
// implementation's rationale is that seed.dev.sql's inserts are already
// idempotent via ON CONFLICT, so concurrent invocation should converge cleanly
// rather than racing the way unprotected concurrent bootstrap.sql execution does
// (SQLSTATE XX000, see TestBootstrapSQLConcurrentInvocationConverges in
// bootstrap_test.go). This proves that rationale empirically instead of trusting
// the comment: N goroutines all call Seed concurrently, and every one of them
// must return nil.
func TestSeedConcurrentInvocationIsSafeWithoutALock(t *testing.T) {
	superDSN := requireSuperuserDSN(t)

	const n = 6
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Seed(context.Background(), superDSN, dbsql.FS)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Seed call returned an error — expected ON CONFLICT to make this always safe with no lock: %v", err)
		}
	}
}

type seedPostState struct {
	id, status         string
	authorID, authorNm *string
	updatedAt          time.Time
}

const seedHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

func readSeedPosts(t *testing.T, pool *pgxpool.Pool) map[string]seedPostState {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id, status, author_id::text, author_name, updated_at FROM public.blog_posts WHERE id = ANY($1)`, blogSeedIDs())
	if err != nil {
		t.Fatalf("read seeded posts: %v", err)
	}
	defer rows.Close()
	got := map[string]seedPostState{}
	for rows.Next() {
		var p seedPostState
		if err := rows.Scan(&p.id, &p.status, &p.authorID, &p.authorNm, &p.updatedAt); err != nil {
			t.Fatalf("scan seeded post: %v", err)
		}
		got[p.id] = p
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read seeded posts: %v", err)
	}
	return got
}

// guardBlogSeedRows snapshots the 20 seeded posts and the seed author's membership, and restores them at cleanup.
func guardBlogSeedRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	before := readSeedPosts(t, pool)
	if len(before) != len(blogSeedPosts) {
		t.Fatalf("seeded posts found = %d, want %d: the seed migration is not applied", len(before), len(blogSeedPosts))
	}
	var role, name, email, status string
	err := pool.QueryRow(ctx, `SELECT role, coalesce(display_name, ''), coalesce(email, ''), status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		seedHouseTenant, blogSeedUser).Scan(&role, &name, &email, &status)
	hadMember := err == nil
	if err != nil && err != pgx.ErrNoRows {
		t.Fatalf("read the seed author membership: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range before {
			if _, err := pool.Exec(ctx, `UPDATE public.blog_posts SET status = $2, author_id = $3::uuid, author_name = $4, updated_at = $5 WHERE id = $1`,
				p.id, p.status, p.authorID, p.authorNm, p.updatedAt); err != nil {
				t.Errorf("restore post %s: %v", p.id, err)
			}
		}
		if !hadMember {
			if _, err := pool.Exec(ctx, `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, seedHouseTenant, blogSeedUser); err != nil {
				t.Errorf("remove the seed author membership: %v", err)
			}
			return
		}
		if _, err := pool.Exec(ctx, `UPDATE memberships SET role = $3, display_name = $4, email = $5, status = $6 WHERE tenant_id = $1 AND user_id = $2`,
			seedHouseTenant, blogSeedUser, role, name, email, status); err != nil {
			t.Errorf("restore the seed author membership: %v", err)
		}
	})
}

func TestSeedBlogPostsArePublishedWithTheSeedAuthor(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	guardBlogSeedRows(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	posts := readSeedPosts(t, pool)
	if len(posts) != len(blogSeedPosts) {
		t.Fatalf("seeded posts found = %d, want %d", len(posts), len(blogSeedPosts))
	}
	for id, p := range posts {
		if p.status != "published" || p.authorID == nil || *p.authorID != blogSeedUser || p.authorNm == nil || *p.authorNm != blogSeedName {
			t.Errorf("post %s after Seed: status %q author_id %v author_name %v, want published %s %q", id, p.status, p.authorID, p.authorNm, blogSeedUser, blogSeedName)
		}
	}

	var role, name, email, status string
	if err := pool.QueryRow(ctx, `SELECT role, display_name, email, status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		seedHouseTenant, blogSeedUser).Scan(&role, &name, &email, &status); err != nil {
		t.Fatalf("read the seed author membership: %v", err)
	}
	if role != "reviewer" || name != blogSeedName || email != "blog-seed@ascomply.invalid" || status != "active" {
		t.Errorf("seed author membership = %s %q %q %s, want reviewer %q blog-seed@ascomply.invalid active", role, name, email, status, blogSeedName)
	}
	var house string
	if err := pool.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house); err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	if house != seedHouseTenant {
		t.Logf("house tenant is %s, not the dev tenant: is_house_member not asserted", house)
		return
	}
	var isHouse bool
	if err := pool.QueryRow(ctx, `SELECT public.is_house_member($1::uuid)`, blogSeedUser).Scan(&isHouse); err != nil {
		t.Fatalf("call is_house_member: %v", err)
	}
	if !isHouse {
		t.Error("is_house_member(seed author) = false, want true")
	}
}

func TestSeedBlogKeepsAStaffUnpublishedPost(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	guardBlogSeedRows(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	var title string
	if err := pool.QueryRow(ctx, `SELECT title FROM public.blog_posts WHERE id = 'what-changes'`).Scan(&title); err != nil {
		t.Fatalf("read the what-changes title: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE public.blog_posts SET title = $1 WHERE id = 'what-changes'`, title)
	})
	if _, err := pool.Exec(ctx, `UPDATE public.blog_posts SET status = 'draft', title = 'Staff edited title' WHERE id = 'what-changes'`); err != nil {
		t.Fatalf("set what-changes back to draft: %v", err)
	}
	before := readSeedPosts(t, pool)
	published := 0
	for id, p := range before {
		if id != "what-changes" && p.status == "published" {
			published++
		}
	}
	if published != len(blogSeedPosts)-1 {
		t.Fatalf("published seeded posts after the first Seed = %d, want %d", published, len(blogSeedPosts)-1)
	}
	if wc := before["what-changes"]; wc.status != "draft" || wc.authorID == nil || *wc.authorID != blogSeedUser {
		t.Fatalf("what-changes before the second Seed = %+v, want a draft that keeps its author", wc)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	after := readSeedPosts(t, pool)
	for id, want := range before {
		got := after[id]
		if got.status != want.status || !got.updatedAt.Equal(want.updatedAt) || (got.authorID == nil) != (want.authorID == nil) ||
			(got.authorID != nil && *got.authorID != *want.authorID) {
			t.Errorf("post %s changed on the second Seed: before %+v, after %+v", id, want, got)
		}
	}
	if after["what-changes"].status != "draft" {
		t.Errorf("what-changes after the second Seed = %q, want draft", after["what-changes"].status)
	}
	var kept string
	if err := pool.QueryRow(ctx, `SELECT title FROM public.blog_posts WHERE id = 'what-changes'`).Scan(&kept); err != nil || kept != "Staff edited title" {
		t.Errorf("what-changes title after the second Seed = %q (err %v), want the staff edit", kept, err)
	}
}

// A complete authorless draft that is not one of the 20 seed ids stays a draft: the publish UPDATE is keyed on the seed ids.
func TestSeedBlogLeavesAStaffMadeDraftAlone(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	guardBlogSeedRows(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.blog_posts (id, category_id, publish_date, title, summary, body)
		VALUES ('qa-staff-draft', 'product', '2026-10-01', 'QA staff draft', 'Complete enough to publish.', '[{"type":"p","text":"x"}]')`); err != nil {
		t.Fatalf("plant a staff draft: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.blog_posts WHERE id = 'qa-staff-draft'`) })

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	var status string
	var author *string
	if err := pool.QueryRow(ctx, `SELECT status, author_id::text FROM public.blog_posts WHERE id = 'qa-staff-draft'`).Scan(&status, &author); err != nil {
		t.Fatalf("read the staff draft: %v", err)
	}
	if status != "draft" || author != nil {
		t.Errorf("staff draft after Seed: status %q author %v, want draft with no author", status, author)
	}
	if n := len(readSeedPosts(t, pool)); n != len(blogSeedPosts) {
		t.Errorf("seeded posts after Seed = %d, want %d", n, len(blogSeedPosts))
	}
}
