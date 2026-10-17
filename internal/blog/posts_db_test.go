package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var bg = context.Background()

// postFx is a store, a category to post into, and a unique suffix; a throwaway actor adds the category.
type postFx struct {
	super *pgxpool.Pool
	s     *Store
	r     string
	cat   string
}

func newPostFx(t *testing.T) postFx {
	t.Helper()
	super, app := dbPools(t)
	r := uniq(t, super)
	s := NewStore(app)
	_, other := staffActor(t, super)
	return postFx{super: super, s: s, r: r, cat: mustAdd(t, s, other, "Cat "+r).ID}
}

func draftIn(title string) PostInput { return PostInput{Title: title, Status: "draft"} }

func (f postFx) pubIn(title, author string) PostInput {
	return PostInput{Title: title, Summary: "s", CategoryID: &f.cat, PublishDate: ptr("2026-01-02"), AuthorID: &author,
		Status: "published", Body: []Block{{Type: "p", Text: "x"}}}
}

func ptr[T any](v T) *T { return &v }

func mustCreate(t *testing.T, s *Store, ctx context.Context, in PostInput) Post {
	t.Helper()
	p, err := s.CreatePost(ctx, in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Title, err)
	}
	return p
}

// houseMember seeds a membership in the given tenant and returns its user id.
func houseMember(t *testing.T, super *pgxpool.Pool, tenant, status string, name *string, email string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(bg, `INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, 'admin', $3, $4, $5)`,
		tenant, id, status, name, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, id) })
	return id
}

// pinState records the featured post, clears the pin, and restores the pin at cleanup. Call it before uniq's cleanup is registered.
func pinState(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	var was *string
	if err := super.QueryRow(bg, `SELECT id FROM public.blog_posts WHERE featured`).Scan(&was); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := super.Exec(bg, `UPDATE public.blog_posts SET featured = false WHERE featured`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(bg, `UPDATE public.blog_posts SET featured = false WHERE featured`)
		if was != nil {
			_, _ = super.Exec(bg, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, *was)
		}
	})
}

func featuredIDs(t *testing.T, super *pgxpool.Pool) []string {
	t.Helper()
	rows, err := super.Query(bg, `SELECT id FROM public.blog_posts WHERE featured`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func storedBody(t *testing.T, super *pgxpool.Pool, id string) any {
	t.Helper()
	var raw string
	if err := super.QueryRow(bg, `SELECT body::text FROM public.blog_posts WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read body of %s: %v", id, err)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func count(t *testing.T, super *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := super.QueryRow(bg, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func events(rows []auditRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Event
	}
	return out
}

func wantRow(t *testing.T, r auditRow, event string, payload map[string]any) {
	t.Helper()
	if r.Event != event {
		t.Errorf("event = %s, want %s", r.Event, event)
		return
	}
	for k, want := range payload {
		if got, ok := r.Payload[k]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s payload[%s] = %v (present %v), want %v", event, k, got, ok, want)
		}
	}
}

func fieldErrs(t *testing.T, err error) map[string]string {
	t.Helper()
	var fe *FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FieldErrors", err)
	}
	return fe.Fields
}

func failAudit(t *testing.T) error {
	t.Helper()
	orig := recordStaff
	boom := errors.New("audit down")
	recordStaff = func(context.Context, pgx.Tx, audit.StaffEvent) error { return boom }
	t.Cleanup(func() { recordStaff = orig })
	return boom
}

func TestPosts_CreateSlugsTheTitleOnce(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	p := mustCreate(t, f.s, ctx, draftIn("Blog test "+f.r))
	if p.ID != "blog-test-"+f.r || p.Status != "draft" || p.AuthorID != nil || p.AuthorName != nil {
		t.Errorf("post = %+v, want id blog-test-%s, draft, no author", p, f.r)
	}
	got, err := f.s.UpdatePost(ctx, p.ID, draftIn("Other"))
	if err != nil || got.ID != p.ID || got.Title != "Other" {
		t.Errorf("update = %+v, %v; want id %s kept and title Other", got, err, p.ID)
	}
}

func TestPosts_CreateCollisionAppendsAndSkipsReserved(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "dup-"+f.r, f.cat, "draft")
	if p := mustCreate(t, f.s, ctx, draftIn("Dup "+f.r)); p.ID != "dup-"+f.r+"-2" {
		t.Errorf("id = %q, want dup-%s-2", p.ID, f.r)
	}
	t.Cleanup(func() { _, _ = f.super.Exec(bg, `DELETE FROM public.blog_posts WHERE id LIKE 'subscribe-%'`) })
	if p := mustCreate(t, f.s, ctx, draftIn("Subscribe")); p.ID == "subscribe" {
		t.Errorf("id = %q, a reserved id", p.ID)
	}
}

func TestPosts_SaveStoresTheCleanBody(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	in := draftIn("Body " + f.r)
	in.Body = []Block{{Type: "p", Text: ` x `}, {Type: "p", Text: ""}, {Type: "ul", Items: []string{"", "a"}}}
	p := mustCreate(t, f.s, ctx, in)
	var want any
	_ = json.Unmarshal([]byte(`[{"type":"p","text":"x"},{"type":"ul","items":["a"]}]`), &want)
	if got := storedBody(t, f.super, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("stored body = %v, want %v", got, want)
	}
}

func TestPosts_DraftWithOnlyATitleStoresAnEmptyArray(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	var fromNull PostInput
	if err := json.Unmarshal([]byte(`{"title":"Null `+f.r+`","status":"draft","body":null}`), &fromNull); err != nil {
		t.Fatal(err)
	}
	blankBlock := draftIn("Blank " + f.r)
	blankBlock.Body = []Block{{Type: "p", Text: " "}}
	for _, in := range []PostInput{draftIn("Omitted " + f.r), fromNull, blankBlock} {
		p, err := f.s.CreatePost(ctx, in)
		if err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
		var typ string
		var n int
		if err := f.super.QueryRow(bg, `SELECT jsonb_typeof(body), jsonb_array_length(body) FROM public.blog_posts WHERE id = $1`, p.ID).Scan(&typ, &n); err != nil {
			t.Fatal(err)
		}
		if typ != "array" || n != 0 {
			t.Errorf("%q stored body = %s of length %d, want an empty array", in.Title, typ, n)
		}
	}
}

func TestPosts_SaveTrimsTitleAndSummary(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	in := draftIn("  Hello " + f.r + " ")
	in.Summary = " Dek "
	p := mustCreate(t, f.s, ctx, in)
	if p.ID != "hello-"+f.r || p.Title != "Hello "+f.r || p.Summary != "Dek" {
		t.Errorf("post = %+v, want trimmed title and summary, id hello-%s", p, f.r)
	}
	got, err := f.s.UpdatePost(ctx, p.ID, draftIn(" Other "))
	if err != nil || got.Title != "Other" {
		t.Errorf("update = %+v, %v; want title Other", got, err)
	}
}

func TestPosts_UpdateUnknownIsNotFound(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	seedPost(t, f.super, "cur-"+f.r, f.cat, "draft")
	f.super.Exec(bg, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, "cur-"+f.r)
	actor, ctx := staffActor(t, f.super)
	in := draftIn("Nope")
	in.Featured = true
	if _, err := f.s.UpdatePost(ctx, "nope-"+f.r, in); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("err = %v, want ErrPostNotFound", err)
	}
	if ids := featuredIDs(t, f.super); !slices.Equal(ids, []string{"cur-" + f.r}) {
		t.Errorf("featured = %v, want the pin unchanged", ids)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_posts WHERE id = $1`, "nope-"+f.r); n != 0 {
		t.Errorf("%d rows for the unknown id", n)
	}
	if rows := auditRowsOf(t, f.super, actor); len(rows) != 0 {
		t.Errorf("audit rows = %v, want none", events(rows))
	}
}

func TestPosts_ConcurrentCreatesGetDistinctIDs(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	ids := make([]string, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.s.CreatePost(ctx, draftIn("Race "+f.r))
			if err != nil {
				t.Errorf("create: %v", err)
			}
			ids[i] = p.ID
		}()
	}
	wg.Wait()
	slices.Sort(ids)
	if want := []string{"race-" + f.r, "race-" + f.r + "-2"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestPosts_UnknownCategoryIsAFieldError(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	in := draftIn("Nocat " + f.r)
	in.CategoryID = ptr("nope-" + f.r)
	_, err := f.s.CreatePost(ctx, in)
	if got := fieldErrs(t, err); !reflect.DeepEqual(got, map[string]string{"category_id": MsgCategory}) {
		t.Errorf("fields = %v", got)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_posts WHERE title = $1`, in.Title); n != 0 {
		t.Errorf("%d rows stored", n)
	}
}

func TestPosts_NonStaffAuthorIsAFieldError(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	house := adoptHouseTenant(t, f.super)
	other := uuid.NewString()
	if _, err := f.super.Exec(bg, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, other, "Other "+f.r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.super.Exec(bg, `DELETE FROM memberships WHERE tenant_id = $1`, other)
		_, _ = f.super.Exec(bg, `DELETE FROM tenants WHERE id = $1`, other)
	})
	authors := map[string]string{
		"random":    uuid.NewString(),
		"suspended": houseMember(t, f.super, house, "suspended", ptr("Gone "+f.r), "gone-"+f.r+"@ascomply.invalid"),
		"other":     houseMember(t, f.super, other, "active", ptr("Else "+f.r), "else-"+f.r+"@example.invalid"),
		"not-uuid":  "x-" + f.r,
	}
	for name, a := range authors {
		_, err := f.s.CreatePost(ctx, f.pubIn("Pub "+name+" "+f.r, a))
		if got := fieldErrs(t, err); !reflect.DeepEqual(got, map[string]string{"author_id": MsgAuthorNotStaff}) {
			t.Errorf("%s: fields = %v", name, got)
		}
	}
}

func TestPosts_StaffAuthorStoresTheDisplayName(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	a := houseMember(t, f.super, adoptHouseTenant(t, f.super), "active", ptr("Writer "+f.r), "writer-"+f.r+"@ascomply.invalid")
	p := mustCreate(t, f.s, ctx, f.pubIn("Named "+f.r, a))
	if p.AuthorID == nil || *p.AuthorID != a || p.AuthorName == nil || *p.AuthorName != "Writer "+f.r {
		t.Errorf("author = %v / %v, want %s / Writer %s", p.AuthorID, p.AuthorName, a, f.r)
	}
}

func TestPosts_AuthorsListsHouseMembers(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	a := houseMember(t, f.super, adoptHouseTenant(t, f.super), "active", ptr("Writer "+f.r), "writer-"+f.r+"@ascomply.invalid")
	list, err := f.s.Authors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(list, func(x Author) bool { return x.UserID == a && x.DisplayName != nil && *x.DisplayName == "Writer "+f.r }) {
		t.Errorf("authors %v lack %s", list, a)
	}
	if _, err := f.s.Authors(staffCtx(uuid.NewString())); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("non-house actor err = %v, want db.ErrNotStaff", err)
	}
}

func TestPosts_AuthorsListsANamelessHouseMember(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	email := "blank-" + f.r + "@ascomply.invalid"
	a := houseMember(t, f.super, adoptHouseTenant(t, f.super), "active", nil, email)
	list, err := f.s.Authors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(list, func(x Author) bool {
		return x.UserID == a && x.DisplayName == nil && x.Email != nil && *x.Email == email
	}) {
		t.Errorf("authors %v lack the nameless member %s", list, a)
	}
}

func TestPosts_NamelessAuthorStoresTheTeamByline(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	a := houseMember(t, f.super, adoptHouseTenant(t, f.super), "active", ptr("  "), "blank-"+f.r+"@ascomply.invalid")
	p := mustCreate(t, f.s, ctx, f.pubIn("Team "+f.r, a))
	check := func(label string, got Post) {
		if got.AuthorID == nil || *got.AuthorID != a || got.AuthorName == nil || *got.AuthorName != TeamAuthorName {
			t.Errorf("%s: author = %v / %v, want %s / %s", label, got.AuthorID, got.AuthorName, a, TeamAuthorName)
		}
	}
	check("create", p)
	up, err := f.s.UpdatePost(ctx, p.ID, f.pubIn("Team "+f.r, a))
	if err != nil {
		t.Fatal(err)
	}
	check("update", up)
}

func TestPosts_AuthorEmailIsNeverStoredOnThePost(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	house := adoptHouseTenant(t, f.super)
	nameless := "nameless-" + f.r + "@ascomply.invalid"
	named := "named-" + f.r + "@ascomply.invalid"
	for i, m := range []struct {
		name  *string
		email string
	}{{nil, nameless}, {ptr("Writer " + f.r), named}} {
		a := houseMember(t, f.super, house, "active", m.name, m.email)
		p := mustCreate(t, f.s, ctx, f.pubIn("Mail "+string(rune('a'+i))+" "+f.r, a))
		var row string
		if err := f.super.QueryRow(bg, `SELECT row_to_json(p)::text FROM public.blog_posts p WHERE id = $1`, p.ID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(p)
		for _, s := range []string{row, string(wire)} {
			if strings.Contains(s, m.email) || strings.Contains(s, "@ascomply.invalid") {
				t.Errorf("post %s carries an email: %s", p.ID, s)
			}
		}
	}
}

func TestPosts_StatusTransitionsPickTheEvent(t *testing.T) {
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	a := houseMember(t, f.super, adoptHouseTenant(t, f.super), "active", ptr("Writer "+f.r), "w-"+f.r+"@ascomply.invalid")
	in := f.pubIn("Flow "+f.r, a)
	in.Status = "draft"
	p := mustCreate(t, f.s, ctx, in)
	in.Status = "published"
	for range 2 {
		if _, err := f.s.UpdatePost(ctx, p.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	in.Status = "draft"
	if _, err := f.s.UpdatePost(ctx, p.ID, in); err != nil {
		t.Fatal(err)
	}
	want := []string{EventPostCreated, EventPostPublished, EventPostUpdated, EventPostUnpublished}
	if got := events(auditRowsOf(t, f.super, actor)); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestPosts_SavingFeaturedUnpinsTheOther(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	_, first := staffActor(t, f.super)
	actor, ctx := staffActor(t, f.super)
	in := draftIn("Pin P " + f.r)
	in.Featured = true
	p := mustCreate(t, f.s, first, in)
	in.Title = "Pin Q " + f.r
	q := mustCreate(t, f.s, ctx, in)
	if ids := featuredIDs(t, f.super); !slices.Equal(ids, []string{q.ID}) {
		t.Fatalf("featured = %v, want only %s", ids, q.ID)
	}
	rows := auditRowsOf(t, f.super, actor)
	if len(rows) != 3 {
		t.Fatalf("events = %v, want created, unpinned, pinned", events(rows))
	}
	wantRow(t, rows[0], EventPostCreated, map[string]any{"id": q.ID})
	wantRow(t, rows[1], EventPostUnpinned, map[string]any{"id": p.ID, "displaced_by": q.ID})
	wantRow(t, rows[2], EventPostPinned, map[string]any{"id": q.ID})
}

func TestPosts_SavingUnfeaturedRecordsTheUnpin(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	_, first := staffActor(t, f.super)
	actor, ctx := staffActor(t, f.super)
	in := draftIn("Unpin " + f.r)
	in.Featured = true
	q := mustCreate(t, f.s, first, in)
	in.Featured = false
	if _, err := f.s.UpdatePost(ctx, q.ID, in); err != nil {
		t.Fatal(err)
	}
	if ids := featuredIDs(t, f.super); len(ids) != 0 {
		t.Errorf("featured = %v, want none", ids)
	}
	rows := auditRowsOf(t, f.super, actor)
	if len(rows) != 2 {
		t.Fatalf("events = %v, want updated, unpinned", events(rows))
	}
	wantRow(t, rows[0], EventPostUpdated, map[string]any{"id": q.ID})
	wantRow(t, rows[1], EventPostUnpinned, map[string]any{"id": q.ID, "displaced_by": nil})
	if _, err := f.s.UpdatePost(ctx, q.ID, in); err != nil {
		t.Fatal(err)
	}
	if got := events(auditRowsOf(t, f.super, actor)); !slices.Equal(got, []string{EventPostUpdated, EventPostUnpinned, EventPostUpdated}) {
		t.Errorf("events after an unchanged update = %v, want one more updated only", got)
	}
}

func TestPosts_SetFeaturedPinsAndUnpins(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	seedPost(t, f.super, "q-"+f.r, f.cat, "draft")
	p, q := "p-"+f.r, "q-"+f.r

	res, err := f.s.SetFeatured(ctx, p, true)
	if err != nil || !res.Featured || res.Unpinned != nil || res.ID != p {
		t.Fatalf("pin P = %+v, %v", res, err)
	}
	rows := auditRowsOf(t, f.super, actor)
	if len(rows) != 1 {
		t.Fatalf("events = %v, want pinned", events(rows))
	}
	wantRow(t, rows[0], EventPostPinned, map[string]any{"id": p})

	res, err = f.s.SetFeatured(ctx, q, true)
	if err != nil || res.Unpinned == nil || *res.Unpinned != p {
		t.Fatalf("pin Q = %+v, %v; want unpinned %s", res, err, p)
	}
	if ids := featuredIDs(t, f.super); !slices.Equal(ids, []string{q}) {
		t.Errorf("featured = %v, want only Q", ids)
	}
	rows = auditRowsOf(t, f.super, actor)[1:]
	if len(rows) != 2 {
		t.Fatalf("events = %v, want unpinned, pinned", events(rows))
	}
	wantRow(t, rows[0], EventPostUnpinned, map[string]any{"id": p, "displaced_by": q})
	wantRow(t, rows[1], EventPostPinned, map[string]any{"id": q})

	res, err = f.s.SetFeatured(ctx, q, false)
	if err != nil || res.Featured || res.Unpinned != nil {
		t.Fatalf("unpin Q = %+v, %v", res, err)
	}
	if ids := featuredIDs(t, f.super); len(ids) != 0 {
		t.Errorf("featured = %v, want none", ids)
	}
	rows = auditRowsOf(t, f.super, actor)[3:]
	if len(rows) != 1 {
		t.Fatalf("events = %v, want unpinned", events(rows))
	}
	wantRow(t, rows[0], EventPostUnpinned, map[string]any{"id": q, "displaced_by": nil})
}

func TestPosts_SetFeaturedSameStateWritesNothing(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	res, err := f.s.SetFeatured(ctx, "p-"+f.r, false)
	if err != nil || res.Featured || res.Unpinned != nil {
		t.Errorf("result = %+v, %v", res, err)
	}
	if rows := auditRowsOf(t, f.super, actor); len(rows) != 0 {
		t.Errorf("audit rows = %v, want none", events(rows))
	}
}

func TestPosts_SetFeaturedUnknownIDKeepsThePin(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	if _, err := f.super.Exec(bg, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, "p-"+f.r); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		if _, err := f.s.SetFeatured(ctx, "nope-"+f.r, on); !errors.Is(err, ErrPostNotFound) {
			t.Errorf("featured=%v err = %v, want ErrPostNotFound", on, err)
		}
	}
	if ids := featuredIDs(t, f.super); !slices.Equal(ids, []string{"p-" + f.r}) {
		t.Errorf("featured = %v, want P kept", ids)
	}
	if rows := auditRowsOf(t, f.super, actor); len(rows) != 0 {
		t.Errorf("audit rows = %v, want none", events(rows))
	}
}

func TestPosts_ConcurrentPinsLeaveOne(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	seedPost(t, f.super, "q-"+f.r, f.cat, "draft")
	var wg sync.WaitGroup
	for _, id := range []string{"p-" + f.r, "q-" + f.r} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.s.SetFeatured(ctx, id, true); err != nil {
				t.Errorf("pin %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if ids := featuredIDs(t, f.super); len(ids) != 1 {
		t.Errorf("featured = %v, want exactly one", ids)
	}
}

func TestPosts_CoverSetReplaceRemove(t *testing.T) {
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	id := "p-" + f.r

	c1, err := f.s.SetCover(ctx, id, "image/jpeg", bytes.Repeat([]byte{1}, 10))
	if err != nil || c1.ContentType != "image/jpeg" || c1.SizeBytes != 10 {
		t.Fatalf("set = %+v, %v", c1, err)
	}
	c2, err := f.s.SetCover(ctx, id, "image/png", bytes.Repeat([]byte{2}, 20))
	if err != nil || c2.ContentType != "image/png" || c2.SizeBytes != 20 || !c2.UpdatedAt.After(c1.UpdatedAt) {
		t.Fatalf("replace = %+v, %v; first %+v", c2, err, c1)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1`, id); n != 1 {
		t.Errorf("%d cover rows, want 1", n)
	}
	if err := f.s.RemoveCover(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1`, id); n != 0 {
		t.Errorf("%d cover rows after remove, want 0", n)
	}
	rows := auditRowsOf(t, f.super, actor)
	if got := events(rows); !slices.Equal(got, []string{EventPostCoverSet, EventPostCoverSet, EventPostCoverRemoved}) {
		t.Fatalf("events = %v", got)
	}
	wantRow(t, rows[0], EventPostCoverSet, map[string]any{"id": id, "content_type": "image/jpeg", "size_bytes": float64(10)})
	if err := f.s.RemoveCover(ctx, id); err != nil {
		t.Errorf("second remove = %v, want nil", err)
	}
	if n := len(auditRowsOf(t, f.super, actor)); n != 3 {
		t.Errorf("%d audit rows after a no-op remove, want 3", n)
	}
}

func TestPosts_CoverCapMatchesTheDBCheck(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	if MaxCoverBytes != 2<<20 {
		t.Fatalf("MaxCoverBytes = %d, want %d", MaxCoverBytes, 2<<20)
	}
	img := make([]byte, MaxCoverBytes)
	copy(img, []byte{0xFF, 0xD8, 0xFF})
	if c, err := f.s.SetCover(ctx, "p-"+f.r, "image/jpeg", img); err != nil || c.SizeBytes != MaxCoverBytes {
		t.Fatalf("cap-sized cover = %+v, %v", c, err)
	}
	tx, err := f.super.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(bg)
	_, err = tx.Exec(bg, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/jpeg', $2)
		ON CONFLICT (post_id) DO UPDATE SET bytes = EXCLUDED.bytes`, "p-"+f.r, make([]byte, MaxCoverBytes+1))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "blog_post_covers_bytes_check" {
		t.Errorf("over-cap insert err = %v, want 23514 blog_post_covers_bytes_check", err)
	}
}

func TestPosts_CoverOnUnknownPostIsNotFound(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	if _, err := f.s.SetCover(ctx, "nope-"+f.r, "image/png", []byte{1}); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("set err = %v", err)
	}
	if err := f.s.RemoveCover(ctx, "nope-"+f.r); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("remove err = %v", err)
	}
}

func TestPosts_DeleteRemovesPostAndCover(t *testing.T) {
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	id := "p-" + f.r
	if _, err := f.s.SetCover(ctx, id, "image/png", []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeletePost(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.super, `SELECT (SELECT count(*) FROM public.blog_posts WHERE id = $1) + (SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1)`, id); n != 0 {
		t.Errorf("%d rows left", n)
	}
	rows := auditRowsOf(t, f.super, actor)
	wantRow(t, rows[len(rows)-1], EventPostDeleted, map[string]any{"id": id, "title": id, "status": "draft"})
	if err := f.s.DeletePost(ctx, id); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("second delete = %v, want ErrPostNotFound", err)
	}
}

func TestPosts_ListCarriesDraftsBodiesAndCoverMeta(t *testing.T) {
	f := newPostFx(t)
	_, ctx := staffActor(t, f.super)
	in := draftIn("Draft " + f.r)
	in.Body = []Block{{Type: "p", Text: "x"}}
	d := mustCreate(t, f.s, ctx, in)
	seedPost(t, f.super, "p-"+f.r, f.cat, "published")
	if _, err := f.s.SetCover(ctx, d.ID, "image/jpeg", make([]byte, 7)); err != nil {
		t.Fatal(err)
	}
	list, err := f.s.ListPosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var gotD, gotP *Post
	for i := range list {
		switch list[i].ID {
		case d.ID:
			gotD = &list[i]
		case "p-" + f.r:
			gotP = &list[i]
		}
	}
	if gotD == nil || gotP == nil {
		t.Fatalf("list lacks the draft (%v) or the published post (%v)", gotD != nil, gotP != nil)
	}
	if len(gotD.Body) != 1 || len(gotP.Body) != 1 || gotD.Cover == nil || gotD.Cover.SizeBytes != 7 || gotP.Cover != nil {
		t.Errorf("draft = %+v, published = %+v", gotD, gotP)
	}
	var wire map[string]any
	raw, _ := json.Marshal(gotD)
	_ = json.Unmarshal(raw, &wire)
	if _, ok := wire["cover"].(map[string]any)["bytes"]; ok {
		t.Error("cover JSON carries bytes")
	}
}

func TestPosts_AuditFailureRollsBack(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	seedPost(t, f.super, "p-"+f.r, f.cat, "draft")
	boom := failAudit(t)

	if _, err := f.s.CreatePost(ctx, draftIn("Fail "+f.r)); !errors.Is(err, boom) {
		t.Errorf("create err = %v", err)
	}
	if _, err := f.s.SetFeatured(ctx, "p-"+f.r, true); !errors.Is(err, boom) {
		t.Errorf("pin err = %v", err)
	}
	if _, err := f.s.SetCover(ctx, "p-"+f.r, "image/png", []byte{1}); !errors.Is(err, boom) {
		t.Errorf("cover err = %v", err)
	}
	if err := f.s.DeletePost(ctx, "p-"+f.r); !errors.Is(err, boom) {
		t.Errorf("delete err = %v", err)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_posts WHERE title = $1`, "Fail "+f.r); n != 0 {
		t.Errorf("created post kept: %d rows", n)
	}
	if ids := featuredIDs(t, f.super); len(ids) != 0 {
		t.Errorf("featured = %v, want none", ids)
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_post_covers WHERE post_id = $1`, "p-"+f.r); n != 0 {
		t.Errorf("cover kept")
	}
	if n := count(t, f.super, `SELECT count(*) FROM public.blog_posts WHERE id = $1`, "p-"+f.r); n != 1 {
		t.Errorf("deleted post is gone")
	}
	if rows := auditRowsOf(t, f.super, actor); len(rows) != 0 {
		t.Errorf("audit rows = %v, want none", events(rows))
	}
}

func TestPosts_AuditRowsAreGlobal(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPostFx(t)
	actor, ctx := staffActor(t, f.super)
	in := draftIn("Global " + f.r)
	in.Featured = true
	p := mustCreate(t, f.s, ctx, in)
	if _, err := f.s.SetCover(ctx, p.ID, "image/png", []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RemoveCover(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeletePost(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	rows := auditRowsOf(t, f.super, actor)
	if len(rows) != 5 {
		t.Fatalf("events = %v, want 5 rows", events(rows))
	}
	for i, r := range rows {
		if r.Tenant != nil || r.Subject != nil {
			t.Errorf("row %d tenant = %v subject = %v, want NULL", i, r.Tenant, r.Subject)
		}
	}
	if n := count(t, f.super, `SELECT count(*) FROM audit_log a JOIN staff_audit_log s ON a.event = s.event AND a.created_at = s.created_at WHERE s.actor = $1`, actor); n != 0 {
		t.Errorf("%d audit_log echoes, want 0", n)
	}
}
