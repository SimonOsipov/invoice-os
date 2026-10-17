package blog

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func catIn(name string) CategoryInput {
	return CategoryInput{Name: name, Icon: "package", Tone: 1}
}

func mustAdd(t *testing.T, s *Store, ctx context.Context, name string) Category {
	t.Helper()
	c, err := s.AddCategory(ctx, catIn(name))
	if err != nil {
		t.Fatalf("add %q: %v", name, err)
	}
	return c
}

func categoryIDs(t *testing.T, s *Store, ctx context.Context) []string {
	t.Helper()
	cats, err := s.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(cats))
	for i, c := range cats {
		ids[i] = c.ID
	}
	return ids
}

func seedPost(t *testing.T, super *pgxpool.Pool, id, category, status string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO public.blog_posts (id, category_id, title, status, summary, publish_date, author_id, author_name, body)
		 VALUES ($1, $2, $1, $3, 's', current_date, gen_random_uuid(), 'n', '[{"type":"p","text":"x"}]')`, id, category, status); err != nil {
		t.Fatalf("seed post %s: %v", id, err)
	}
}

func postCategory(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var c string
	if err := super.QueryRow(context.Background(), `SELECT category_id FROM public.blog_posts WHERE id = $1`, id).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func categoryPosition(t *testing.T, super *pgxpool.Pool, id string) int {
	t.Helper()
	var p int
	if err := super.QueryRow(context.Background(), `SELECT position FROM public.blog_categories WHERE id = $1`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCategories_AddSlugsTheNameAndAppends(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	var maxPos int
	if err := super.QueryRow(context.Background(), `SELECT coalesce(max(position), 0) FROM public.blog_categories`).Scan(&maxPos); err != nil {
		t.Fatal(err)
	}

	a := mustAdd(t, s, ctx, "R&D "+r)
	if a.ID != "r-and-d-"+r || a.Headline != "R&D "+r+"." {
		t.Errorf("category = %+v, want id r-and-d-%s and headline %q", a, r, "R&D "+r+".")
	}
	if p := categoryPosition(t, super, a.ID); p != maxPos+1 {
		t.Errorf("position = %d, want %d", p, maxPos+1)
	}
	if b := mustAdd(t, s, ctx, "R and D "+r); b.ID != "r-and-d-"+r+"-2" {
		t.Errorf("second id = %q, want r-and-d-%s-2", b.ID, r)
	}
	if c := mustAdd(t, s, ctx, "R & D "+r); c.ID != "r-and-d-"+r+"-3" {
		t.Errorf("third id = %q, want r-and-d-%s-3", c.ID, r)
	}
}

func TestCategories_DuplicateNameIgnoringCaseIsTaken(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	first := mustAdd(t, s, ctx, "Blog "+r)
	other := mustAdd(t, s, ctx, "Other "+r)

	if _, err := s.AddCategory(ctx, catIn("BLOG "+r)); !errors.Is(err, ErrCategoryNameTaken) {
		t.Errorf("add err = %v, want ErrCategoryNameTaken", err)
	}
	if _, err := s.UpdateCategory(ctx, other.ID, catIn("blog "+r)); !errors.Is(err, ErrCategoryNameTaken) {
		t.Errorf("update err = %v, want ErrCategoryNameTaken", err)
	}
	got, err := s.UpdateCategory(ctx, first.ID, catIn("Blog "+r+" "))
	if err != nil || got.ID != first.ID {
		t.Fatalf("same-row update = %+v, %v", got, err)
	}
	upper := catIn("Blog " + r)
	upper.Name = "BLOG " + r
	if got, err = s.UpdateCategory(ctx, first.ID, upper); err != nil || got.ID != first.ID || got.Name != "BLOG "+r {
		t.Errorf("recase update = %+v, %v", got, err)
	}
}

func TestCategories_UpdateKeepsTheID(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	c := mustAdd(t, s, ctx, "a "+r)

	got, err := s.UpdateCategory(ctx, c.ID, CategoryInput{Name: "  Renamed " + r + " ", Headline: " Head ", Intro: " Intro ", Icon: "globe", Tone: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := Category{ID: c.ID, CategoryInput: CategoryInput{Name: "Renamed " + r, Headline: "Head", Intro: "Intro", Icon: "globe", Tone: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := s.UpdateCategory(ctx, "no-such-"+r, catIn("x "+r)); !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("unknown id err = %v, want ErrCategoryNotFound", err)
	}
}

func TestCategories_MoveSwapsWithTheNeighbour(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	mustAdd(t, s, ctx, "W "+r)
	x, y, z := mustAdd(t, s, ctx, "X "+r), mustAdd(t, s, ctx, "Y "+r), mustAdd(t, s, ctx, "Z "+r)
	tail := func() []string { ids := categoryIDs(t, s, ctx); return ids[len(ids)-3:] }

	moved, cats, err := s.MoveCategory(ctx, y.ID, "up")
	if err != nil || !moved {
		t.Fatalf("move up = %v, %v", moved, err)
	}
	if got := tail(); !reflect.DeepEqual(got, []string{y.ID, x.ID, z.ID}) {
		t.Errorf("after up = %v", got)
	}
	if len(cats) != len(categoryIDs(t, s, ctx)) {
		t.Errorf("move returned %d categories", len(cats))
	}
	if moved, _, err = s.MoveCategory(ctx, y.ID, "down"); err != nil || !moved {
		t.Fatalf("move down = %v, %v", moved, err)
	}
	if got := tail(); !reflect.DeepEqual(got, []string{x.ID, y.ID, z.ID}) {
		t.Errorf("after down = %v", got)
	}
	rows := auditRowsOf(t, super, actor)
	if n := len(rows); n != 6 || rows[4].Event != EventCategoryMoved || rows[5].Event != EventCategoryMoved {
		t.Fatalf("audit = %+v", rows)
	}
	if rows[4].Payload["swapped_with"] != x.ID || rows[4].Payload["direction"] != "up" || rows[5].Payload["swapped_with"] != x.ID {
		t.Errorf("move payloads = %v, %v", rows[4].Payload, rows[5].Payload)
	}
}

func TestCategories_MoveAtTheEdgeIsANoOp(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	last := mustAdd(t, s, ctx, "Last "+r)
	before := categoryIDs(t, s, ctx)
	nAudit := len(auditRowsOf(t, super, actor))

	moved, _, err := s.MoveCategory(ctx, last.ID, "down")
	if err != nil || moved {
		t.Fatalf("move down = %v, %v, want false", moved, err)
	}
	if !slices.Equal(before, categoryIDs(t, s, ctx)) {
		t.Error("order changed")
	}
	if n := len(auditRowsOf(t, super, actor)); n != nAudit {
		t.Errorf("%d audit rows, want %d", n, nAudit)
	}
	if _, _, err := s.MoveCategory(ctx, last.ID, "sideways"); !errors.Is(err, ErrDirection) {
		t.Errorf("bad direction err = %v", err)
	}
	first := categoryIDs(t, s, ctx)[0]
	posBefore := categoryPosition(t, super, first)
	moved, cats, err := s.MoveCategory(ctx, first, "up")
	if err != nil || moved {
		t.Fatalf("move first up = %v, %v, want false", moved, err)
	}
	if len(cats) == 0 || cats[0].ID != first || categoryPosition(t, super, first) != posBefore {
		t.Errorf("top moved: %v", cats)
	}
	if n := len(auditRowsOf(t, super, actor)); n != nAudit {
		t.Errorf("%d audit rows after top no-op, want %d", n, nAudit)
	}
}

func TestCategories_DeleteMovesItsPostsInOneTx(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	a, b := mustAdd(t, s, ctx, "A "+r), mustAdd(t, s, ctx, "B "+r)
	seedPost(t, super, "p1-"+r, a.ID, "draft")
	seedPost(t, super, "p2-"+r, a.ID, "draft")

	res, err := s.DeleteCategory(ctx, a.ID, &b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.MovedPosts != 2 || res.MoveTo == nil || *res.MoveTo != b.ID {
		t.Errorf("result = %+v", res)
	}
	for _, p := range []string{"p1-" + r, "p2-" + r} {
		if c := postCategory(t, super, p); c != b.ID {
			t.Errorf("post %s category = %q, want %q", p, c, b.ID)
		}
	}
	if slices.Contains(categoryIDs(t, s, ctx), a.ID) {
		t.Error("category A still listed")
	}
	rows := auditRowsOf(t, super, actor)
	last := rows[len(rows)-1]
	if last.Event != EventCategoryDeleted || last.Payload["moved_posts"] != float64(2) || last.Payload["move_to"] != b.ID || last.Payload["id"] != a.ID {
		t.Errorf("audit = %+v", last)
	}
}

func TestCategories_DeleteRefusesAMissingTarget(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	a := mustAdd(t, s, ctx, "A "+r)
	mustAdd(t, s, ctx, "B "+r)
	seedPost(t, super, "p-"+r, a.ID, "draft")
	nope := "nope"

	for name, target := range map[string]*string{"nil": nil, "itself": &a.ID, "unknown": &nope} {
		_, err := s.DeleteCategory(ctx, a.ID, target)
		var fe *FieldErrors
		if !errors.As(err, &fe) || fe.Fields["move_to"] != MsgMoveTo {
			t.Errorf("%s: err = %v, want FieldErrors{move_to}", name, err)
		}
	}
	if c := postCategory(t, super, "p-"+r); c != a.ID {
		t.Errorf("post moved to %q", c)
	}
	if n := len(auditRowsOf(t, super, actor)); n != 2 {
		t.Errorf("%d audit rows, want only the two adds", n)
	}
}

func TestCategories_DeleteEmptyCategoryIgnoresTarget(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	c := mustAdd(t, s, ctx, "C "+r)
	mustAdd(t, s, ctx, "D "+r)

	res, err := s.DeleteCategory(ctx, c.ID, nil)
	if err != nil || res.MovedPosts != 0 || res.MoveTo != nil {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if slices.Contains(categoryIDs(t, s, ctx), c.ID) {
		t.Error("category C still listed")
	}
}

func TestCategories_DeleteUnknownIsNotFound(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	if _, err := NewStore(app).DeleteCategory(ctx, "no-such-"+r, nil); !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("err = %v, want ErrCategoryNotFound", err)
	}
}

func TestCategories_EveryWriteAuditsGlobally(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	c := mustAdd(t, s, ctx, "Audit "+r)
	if _, err := s.UpdateCategory(ctx, c.ID, catIn("Audit "+r+" v2")); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, ctx, "Neighbour "+r)
	if _, _, err := s.MoveCategory(ctx, c.ID, "down"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCategory(ctx, c.ID, nil); err != nil {
		t.Fatal(err)
	}

	want := []string{"blog.category.added", "blog.category.updated", "blog.category.added", "blog.category.moved", "blog.category.deleted"}
	rows := auditRowsOf(t, super, actor)
	if len(rows) != len(want) {
		t.Fatalf("%d audit rows, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row.Event != want[i] || row.Tenant != nil || row.Subject != nil || row.Payload["id"] == nil {
			t.Errorf("row %d = %+v, want global %s with an id", i, row, want[i])
		}
	}
}

func TestCategories_AuditFailureRollsBack(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	boom := errors.New("audit down")
	recordStaff = func(context.Context, pgx.Tx, audit.StaffEvent) error { return boom }

	if _, err := NewStore(app).AddCategory(ctx, catIn("Rollback "+r)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.blog_categories WHERE name = $1`, "Rollback "+r).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d categories named Rollback, err %v", n, err)
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

func TestCategories_NonStaffContextIsRefused(t *testing.T) {
	super, app := dbPools(t)
	r := uniq(t, super)
	if _, err := NewStore(app).AddCategory(context.Background(), catIn("Refused "+r)); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("err = %v, want db.ErrNotStaff", err)
	}
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.blog_categories WHERE name = $1`, "Refused "+r).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d categories written, err %v", n, err)
	}
	st := NewStore(app)
	bg := context.Background()
	if _, err := st.ListCategories(bg); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("list err = %v", err)
	}
	if _, err := st.UpdateCategory(bg, "x", catIn("x "+r)); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("update err = %v", err)
	}
	if _, _, err := st.MoveCategory(bg, "x", "up"); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("move err = %v", err)
	}
	if _, err := st.DeleteCategory(bg, "x", nil); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("delete err = %v", err)
	}
}

func TestCategories_ListIsOrderedWithPostCounts(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	a, b := mustAdd(t, s, ctx, "A "+r), mustAdd(t, s, ctx, "B "+r)
	seedPost(t, super, "p1-"+r, a.ID, "draft")
	seedPost(t, super, "p2-"+r, a.ID, "published")

	cats, err := s.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tail := cats[len(cats)-2:]
	if tail[0].ID != a.ID || tail[0].PostCount != 2 || tail[1].ID != b.ID || tail[1].PostCount != 0 {
		t.Errorf("tail = %+v", tail)
	}
}

func TestCategories_ConcurrentAddsGetDistinctPositions(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	var wg sync.WaitGroup
	got := make([]Category, 2)
	errs := make([]error, 2)
	for i, name := range []string{"One " + r, "Two " + r} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = s.AddCategory(ctx, catIn(name))
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if categoryPosition(t, super, got[0].ID) == categoryPosition(t, super, got[1].ID) {
		t.Error("both categories share a position")
	}
}

func TestCategories_MoveUnknownIsNotFound(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	before := categoryIDs(t, s, ctx)
	for _, dir := range []string{"up", "down"} {
		if _, _, err := s.MoveCategory(ctx, "no-such-"+r, dir); !errors.Is(err, ErrCategoryNotFound) {
			t.Errorf("%s err = %v, want ErrCategoryNotFound", dir, err)
		}
	}
	if !slices.Equal(before, categoryIDs(t, s, ctx)) {
		t.Error("order changed")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

func TestCategories_GoRulesMatchTheDBChecks(t *testing.T) {
	super, _ := dbPools(t)
	ctx := context.Background()
	if len(Icons) != 16 {
		t.Fatalf("len(Icons) = %d, want 16", len(Icons))
	}
	var def string
	if err := super.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'blog_categories_icon_check'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	var inDB []string
	for _, m := range regexp.MustCompile(`'([a-z0-9-]+)'`).FindAllStringSubmatch(def, -1) {
		inDB = append(inDB, m[1])
	}
	a, b := slices.Sorted(slices.Values(inDB)), slices.Sorted(slices.Values(Icons))
	if !slices.Equal(a, b) {
		t.Errorf("DB icons %v, Go icons %v", a, b)
	}
	for tone, ok := range map[int]bool{0: true, ToneCount - 1: true, ToneCount: false, -1: false} {
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.blog_categories (id, name, headline, icon, tone, position)
			VALUES ('tone-check', 'tone check', 'h', 'package', $1, (SELECT coalesce(max(position), 0) + 1 FROM public.blog_categories))`, tone)
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		switch {
		case ok && err != nil:
			t.Errorf("tone %d refused: %v", tone, err)
		case !ok && (!errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "blog_categories_tone_check"):
			t.Errorf("tone %d err = %v, want 23514 blog_categories_tone_check", tone, err)
		}
	}
}

func TestCategories_AuditFailureRollsBackEveryWrite(t *testing.T) {
	super, app := dbPools(t)
	actor, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	a, b := mustAdd(t, s, ctx, "A "+r), mustAdd(t, s, ctx, "B "+r)
	seedPost(t, super, "p-"+r, a.ID, "draft")
	orderBefore, nAudit := categoryIDs(t, s, ctx), len(auditRowsOf(t, super, actor))

	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	boom := errors.New("audit down")
	recordStaff = func(context.Context, pgx.Tx, audit.StaffEvent) error { return boom }

	if _, err := s.UpdateCategory(ctx, a.ID, catIn("Renamed "+r)); !errors.Is(err, boom) {
		t.Errorf("update err = %v", err)
	}
	if _, _, err := s.MoveCategory(ctx, a.ID, "down"); !errors.Is(err, boom) {
		t.Errorf("move err = %v", err)
	}
	if _, err := s.DeleteCategory(ctx, a.ID, &b.ID); !errors.Is(err, boom) {
		t.Errorf("delete err = %v", err)
	}
	recordStaff = orig

	if !slices.Equal(orderBefore, categoryIDs(t, s, ctx)) {
		t.Error("order or membership changed")
	}
	cats, _ := s.ListCategories(ctx)
	for _, c := range cats {
		if c.ID == a.ID && (c.Name != "A "+r || c.PostCount != 1) {
			t.Errorf("category A = %+v, want unchanged", c)
		}
	}
	if c := postCategory(t, super, "p-"+r); c != a.ID {
		t.Errorf("post moved to %q despite the rollback", c)
	}
	if n := len(auditRowsOf(t, super, actor)); n != nAudit {
		t.Errorf("%d audit rows, want %d", n, nAudit)
	}
}

func TestCategories_ConcurrentSameSlugAddsGetDistinctIDs(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	var wg sync.WaitGroup
	got := make([]Category, 2)
	errs := make([]error, 2)
	for i, name := range []string{"R&D " + r, "R and D " + r} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = s.AddCategory(ctx, catIn(name))
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	ids := []string{got[0].ID, got[1].ID}
	slices.Sort(ids)
	if ids[0] != "r-and-d-"+r || ids[1] != "r-and-d-"+r+"-2" {
		t.Errorf("ids = %v", ids)
	}
}

func TestCategories_ConcurrentSameNameAddsOneWins(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"Same " + r, "SAME " + r} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.AddCategory(ctx, catIn(name))
		}()
	}
	wg.Wait()
	ok, taken := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrCategoryNameTaken):
			taken++
		}
	}
	if ok != 1 || taken != 1 {
		t.Errorf("errs = %v, want one success and one ErrCategoryNameTaken", errs)
	}
}

func TestCategories_DeleteEmptyCategoryToItselfOrUnknownTargetSucceeds(t *testing.T) {
	super, app := dbPools(t)
	_, ctx := staffActor(t, super)
	r := uniq(t, super)
	s := NewStore(app)
	c, d := mustAdd(t, s, ctx, "C "+r), mustAdd(t, s, ctx, "D "+r)
	mustAdd(t, s, ctx, "E "+r)
	nope := "nope"
	if res, err := s.DeleteCategory(ctx, c.ID, &c.ID); err != nil || res.MovedPosts != 0 || res.MoveTo != nil {
		t.Errorf("itself: %+v, %v", res, err)
	}
	if res, err := s.DeleteCategory(ctx, d.ID, &nope); err != nil || res.MovedPosts != 0 || res.MoveTo != nil {
		t.Errorf("unknown: %+v, %v", res, err)
	}
}
