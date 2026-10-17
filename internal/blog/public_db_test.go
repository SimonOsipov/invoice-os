package blog

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pubDay is the store's "today" in these tests; far from any real post date.
var pubDay = time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)

func day(offset int) string { return pubDay.AddDate(0, 0, offset).Format(time.DateOnly) }

type pubFx struct {
	super *pgxpool.Pool
	s     *Store
	r     string
	cat   string
}

// newPubFx seeds one own category with the superuser; the unique tag cleans up every row.
func newPubFx(t *testing.T) pubFx {
	t.Helper()
	super, app := dbPools(t)
	r := uniq(t, super)
	cat := "c-" + r
	seedCategory(t, super, cat, "Cat "+r, maxPosition(t, super)+1)
	return pubFx{super: super, s: NewStore(app), r: r, cat: cat}
}

func maxPosition(t *testing.T, super *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := super.QueryRow(bg, `SELECT coalesce(max(position), 0) FROM public.blog_categories`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedCategory(t *testing.T, super *pgxpool.Pool, id, name string, position int) {
	t.Helper()
	if _, err := super.Exec(bg, `INSERT INTO public.blog_categories (id, name, headline, intro, icon, tone, position)
		VALUES ($1, $2, $2 || '.', 'intro', 'package', 1, $3)`, id, name, position); err != nil {
		t.Fatalf("seed category %s: %v", id, err)
	}
}

type seedSpec struct {
	id, status, date string
	body             []Block
}

// seedPost inserts a post as the superuser; a published post gets author, summary and a body.
func (f pubFx) seedPost(t *testing.T, sp seedSpec) {
	t.Helper()
	if sp.body == nil {
		sp.body = []Block{{Type: "p", Text: "x"}}
	}
	raw, err := json.Marshal(sp.body)
	if err != nil {
		t.Fatal(err)
	}
	id := sp.id
	if !strings.Contains(id, f.r) {
		t.Fatalf("seed id %q must contain the tag %q", id, f.r)
	}
	if _, err := f.super.Exec(bg, `INSERT INTO public.blog_posts (id, category_id, publish_date, title, summary, author_id, author_name, status, body)
		VALUES ($1, $2, $3, 'Title '||$1, 'Summary '||$1, $4, 'Author '||$1, $5, $6::jsonb)`,
		id, f.cat, sp.date, uuid.NewString(), sp.status, string(raw)); err != nil {
		t.Fatalf("seed post %s: %v", id, err)
	}
}

func (f pubFx) seedCover(t *testing.T, id string, b []byte) {
	t.Helper()
	if _, err := f.super.Exec(bg, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, 'image/png', $2)`, id, b); err != nil {
		t.Fatalf("seed cover %s: %v", id, err)
	}
}

// coverVersion reads the stored version the design defines: updated_at as Unix microseconds.
func (f pubFx) coverVersion(t *testing.T, id string) int64 {
	t.Helper()
	var v int64
	if err := f.super.QueryRow(bg, `SELECT (extract(epoch FROM updated_at) * 1000000)::bigint FROM public.blog_post_covers WHERE post_id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read cover version of %s: %v", id, err)
	}
	return v
}

func (f pubFx) posts(t *testing.T, today string) map[string]int {
	t.Helper()
	got, err := f.s.PublicPosts(bg, today)
	if err != nil {
		t.Fatalf("PublicPosts: %v", err)
	}
	idx := map[string]int{}
	for i, p := range got {
		idx[p.ID] = i
	}
	return idx
}

func (f pubFx) listed(t *testing.T, today, id string) PublicPost {
	t.Helper()
	got, err := f.s.PublicPosts(bg, today)
	if err != nil {
		t.Fatalf("PublicPosts: %v", err)
	}
	for _, p := range got {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("PublicPosts(%s) does not list %s", today, id)
	return PublicPost{}
}

func TestPublic_PostsListOnlyVisiblePosts(t *testing.T) {
	f := newPubFx(t)
	yesterday, onDay, tomorrow, draft := "y-"+f.r, "d-"+f.r, "f-"+f.r, "g-"+f.r
	f.seedPost(t, seedSpec{id: yesterday, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: onDay, status: "published", date: day(0)})
	f.seedPost(t, seedSpec{id: tomorrow, status: "published", date: day(1)})
	f.seedPost(t, seedSpec{id: draft, status: "draft", date: day(-1)})
	idx := f.posts(t, day(0))
	for _, id := range []string{yesterday, onDay} {
		if _, ok := idx[id]; !ok {
			t.Errorf("visible post %s is not listed", id)
		}
	}
	for _, id := range []string{tomorrow, draft} {
		if _, ok := idx[id]; ok {
			t.Errorf("hidden post %s is listed", id)
		}
	}
}

func TestPublic_PostOnItsDateIsVisibleAndADayEarlierIsNot(t *testing.T) {
	f := newPubFx(t)
	id := "p-" + f.r
	f.seedPost(t, seedSpec{id: id, status: "published", date: day(0)})
	if p, err := f.s.PublicPost(bg, id, day(0)); err != nil || p.ID != id {
		t.Fatalf("PublicPost on its date = %+v, %v; want the post", p, err)
	}
	if _, err := f.s.PublicPost(bg, id, day(-1)); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("PublicPost a day before its date: err = %v, want ErrPostNotFound", err)
	}
}

func TestPublic_DraftFutureAndUnknownGiveTheSameError(t *testing.T) {
	f := newPubFx(t)
	draft, future := "g-"+f.r, "f-"+f.r
	f.seedPost(t, seedSpec{id: draft, status: "draft", date: day(-1)})
	f.seedPost(t, seedSpec{id: future, status: "published", date: day(1)})
	vis := "v-" + f.r
	f.seedPost(t, seedSpec{id: vis, status: "published", date: day(0)})
	if _, err := f.s.PublicPost(bg, vis, day(0)); err != nil {
		t.Fatalf("a visible post must read: %v", err)
	}
	_, e1 := f.s.PublicPost(bg, draft, day(0))
	_, e2 := f.s.PublicPost(bg, future, day(0))
	_, e3 := f.s.PublicPost(bg, "none-"+f.r, day(0))
	for name, err := range map[string]error{"draft": e1, "future": e2, "unknown": e3} {
		if !errors.Is(err, ErrPostNotFound) {
			t.Errorf("%s: err = %v, want ErrPostNotFound", name, err)
		}
	}
	if e1 != e2 || e2 != e3 {
		t.Errorf("errors differ: %v, %v, %v", e1, e2, e3)
	}
}

func TestPublic_PostsAreNewestFirst(t *testing.T) {
	f := newPubFx(t)
	a, b, c := "a-"+f.r, "b-"+f.r, "c-"+f.r
	f.seedPost(t, seedSpec{id: a, status: "published", date: day(-3)})
	f.seedPost(t, seedSpec{id: b, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: c, status: "published", date: day(-2)})
	idx := f.posts(t, day(0))
	for _, id := range []string{a, b, c} {
		if _, ok := idx[id]; !ok {
			t.Fatalf("post %s is not listed", id)
		}
	}
	if !(idx[b] < idx[c] && idx[c] < idx[a]) {
		t.Errorf("positions D-1=%d D-2=%d D-3=%d, want ascending", idx[b], idx[c], idx[a])
	}
}

func TestPublic_SameDateOrdersById(t *testing.T) {
	f := newPubFx(t)
	b, a := f.r+"-b", f.r+"-a"
	f.seedPost(t, seedSpec{id: b, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: a, status: "published", date: day(-1)})
	idx := f.posts(t, day(0))
	ia, okA := idx[a]
	ib, okB := idx[b]
	if !okA || !okB {
		t.Fatalf("both posts must be listed: a=%v b=%v", okA, okB)
	}
	if ia >= ib {
		t.Errorf("-a at %d, -b at %d; want -a first", ia, ib)
	}
}

func TestPublic_PostCarriesTheWireFields(t *testing.T) {
	f := newPubFx(t)
	id := "w-" + f.r
	body := []Block{
		{Type: "p", Text: "Hello"},
		{Type: "ul", Items: []string{"one", "two"}},
		{Type: "note", Kind: "deadline", Title: "Heads up", Text: "Soon"},
	}
	f.seedPost(t, seedSpec{id: id, status: "published", date: day(-1), body: body})
	got, err := f.s.PublicPost(bg, id, day(0))
	if err != nil {
		t.Fatal(err)
	}
	want := PublicPost{ID: id, CategoryID: f.cat, PublishDate: day(-1), Title: "Title " + id, Summary: "Summary " + id,
		AuthorName: "Author " + id, Featured: false, Cover: nil, Body: body}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PublicPost = %+v\nwant %+v", got, want)
	}
	if listed := f.listed(t, day(0), id); !reflect.DeepEqual(listed, want) {
		t.Errorf("PublicPosts element = %+v\nwant %+v", listed, want)
	}
}

func TestPublic_CategoryCountsOnlyVisiblePosts(t *testing.T) {
	f := newPubFx(t)
	f.seedPost(t, seedSpec{id: "v-" + f.r, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: "d-" + f.r, status: "draft", date: day(-1)})
	f.seedPost(t, seedSpec{id: "f-" + f.r, status: "published", date: day(1)})
	cats, err := f.s.PublicCategories(bg, day(0))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cats, func(c Category) bool { return c.ID == f.cat })
	if i < 0 {
		t.Fatalf("own category %s is not listed", f.cat)
	}
	if cats[i].PostCount != 1 {
		t.Errorf("post_count = %d, want 1 (one visible, one draft, one future)", cats[i].PostCount)
	}
}

func TestPublic_EmptyCategoryIsListedWithZero(t *testing.T) {
	f := newPubFx(t)
	cats, err := f.s.PublicCategories(bg, day(0))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cats, func(c Category) bool { return c.ID == f.cat })
	if i < 0 {
		t.Fatalf("empty own category %s is not listed", f.cat)
	}
	if cats[i].PostCount != 0 {
		t.Errorf("post_count = %d, want 0", cats[i].PostCount)
	}
	if cats[i].Name != "Cat "+f.r || cats[i].Icon != "package" || cats[i].Tone != 1 {
		t.Errorf("category fields = %+v", cats[i])
	}
}

func TestPublic_CategoriesFollowPosition(t *testing.T) {
	f := newPubFx(t)
	hi, lo := "hi-"+f.r, "lo-"+f.r
	super := f.super
	base := maxPosition(t, super)
	seedCategory(t, super, hi, "Hi "+f.r, base+2)
	seedCategory(t, super, lo, "Lo "+f.r, base+1)
	cats, err := f.s.PublicCategories(bg, day(0))
	if err != nil {
		t.Fatal(err)
	}
	ih := slices.IndexFunc(cats, func(c Category) bool { return c.ID == hi })
	il := slices.IndexFunc(cats, func(c Category) bool { return c.ID == lo })
	if ih < 0 || il < 0 {
		t.Fatalf("both categories must be listed: hi=%d lo=%d", ih, il)
	}
	if il >= ih {
		t.Errorf("lower position at %d, higher at %d; want lower first", il, ih)
	}
}

func TestPublic_APinnedDraftIsNotListed(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPubFx(t)
	vis, draft := "v-"+f.r, "g-"+f.r
	f.seedPost(t, seedSpec{id: vis, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: draft, status: "draft", date: day(-1)})
	if _, err := f.super.Exec(bg, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, draft); err != nil {
		t.Fatal(err)
	}
	idx := f.posts(t, day(0))
	if _, ok := idx[vis]; !ok {
		t.Fatalf("visible post %s is not listed", vis)
	}
	if _, ok := idx[draft]; ok {
		t.Errorf("pinned draft %s is listed", draft)
	}
}

func TestPublic_APinnedVisiblePostReportsFeatured(t *testing.T) {
	super, _ := dbPools(t)
	pinState(t, super)
	f := newPubFx(t)
	pinned, other := "p-"+f.r, "o-"+f.r
	f.seedPost(t, seedSpec{id: pinned, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: other, status: "published", date: day(-2)})
	if _, err := f.super.Exec(bg, `UPDATE public.blog_posts SET featured = true WHERE id = $1`, pinned); err != nil {
		t.Fatal(err)
	}
	all, err := f.s.PublicPosts(bg, day(0))
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, p := range all {
		switch {
		case p.ID == pinned:
			seen = true
			if !p.Featured {
				t.Errorf("pinned visible post is not Featured")
			}
		case p.Featured:
			t.Errorf("post %s is Featured but is not the pin", p.ID)
		}
	}
	if !seen {
		t.Fatalf("pinned post %s is not listed", pinned)
	}
}

// versionOf parses the version off a cover path, requiring the gateway path shape.
func versionOf(t *testing.T, cover *string, id string) int64 {
	t.Helper()
	if cover == nil {
		t.Fatal("cover is nil, want a path")
	}
	rest, ok := strings.CutPrefix(*cover, PublicCoverPrefix+id+"/")
	if !ok {
		t.Fatalf("cover %q does not start with %q", *cover, PublicCoverPrefix+id+"/")
	}
	v, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		t.Fatalf("cover version %q is not a decimal integer: %v", rest, err)
	}
	return v
}

func TestPublic_CoverIsServedAtTheListedVersion(t *testing.T) {
	f := newPubFx(t)
	id := "c-" + f.r
	png := []byte("\x89PNG\r\n\x1a\nfirst")
	f.seedPost(t, seedSpec{id: id, status: "published", date: day(-1)})
	f.seedCover(t, id, png)
	p := f.listed(t, day(0), id)
	v := versionOf(t, p.Cover, id)
	if want := f.coverVersion(t, id); v != want {
		t.Errorf("listed version = %d, want updated_at micros %d", v, want)
	}
	ct, b, err := f.s.PublicCover(bg, id, v, day(0))
	if err != nil {
		t.Fatal(err)
	}
	if ct != "image/png" || !bytes.Equal(b, png) {
		t.Errorf("PublicCover = %q, %q; want image/png and the stored bytes", ct, b)
	}
}

func TestPublic_AReplacedCoverMovesToANewVersion(t *testing.T) {
	f := newPubFx(t)
	id := "c-" + f.r
	f.seedPost(t, seedSpec{id: id, status: "published", date: day(-1)})
	f.seedCover(t, id, []byte("old-bytes"))
	oldPath := f.listed(t, day(0), id).Cover
	oldV := versionOf(t, oldPath, id)
	if _, _, err := f.s.PublicCover(bg, id, oldV, day(0)); err != nil {
		t.Fatalf("the current version must read before the replace: %v", err)
	}
	if _, err := f.super.Exec(bg, `UPDATE public.blog_post_covers SET bytes = $2, updated_at = now() WHERE post_id = $1`, id, []byte("new-bytes")); err != nil {
		t.Fatal(err)
	}
	newPath := f.listed(t, day(0), id).Cover
	newV := versionOf(t, newPath, id)
	if *newPath == *oldPath || newV == oldV {
		t.Fatalf("replaced cover kept its path: old %s, new %s", *oldPath, *newPath)
	}
	if _, _, err := f.s.PublicCover(bg, id, oldV, day(0)); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("old version: err = %v, want ErrPostNotFound", err)
	}
	if _, b, err := f.s.PublicCover(bg, id, newV, day(0)); err != nil || string(b) != "new-bytes" {
		t.Errorf("new version = %q, %v; want new-bytes", b, err)
	}
}

func TestPublic_CoverOfAHiddenPostIsNotFound(t *testing.T) {
	f := newPubFx(t)
	vis, draft, future := "v-"+f.r, "g-"+f.r, "f-"+f.r
	f.seedPost(t, seedSpec{id: vis, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: draft, status: "draft", date: day(-1)})
	f.seedPost(t, seedSpec{id: future, status: "published", date: day(1)})
	for _, id := range []string{vis, draft, future} {
		f.seedCover(t, id, []byte("img-"+id))
	}
	if _, _, err := f.s.PublicCover(bg, vis, f.coverVersion(t, vis), day(0)); err != nil {
		t.Fatalf("a visible post's current cover must read: %v", err)
	}
	for name, id := range map[string]string{"draft": draft, "future": future} {
		if _, _, err := f.s.PublicCover(bg, id, f.coverVersion(t, id), day(0)); !errors.Is(err, ErrPostNotFound) {
			t.Errorf("%s: err = %v, want ErrPostNotFound", name, err)
		}
	}
	if _, _, err := f.s.PublicCover(bg, "none-"+f.r, 1, day(0)); !errors.Is(err, ErrPostNotFound) {
		t.Errorf("unknown post: err = %v, want ErrPostNotFound", err)
	}
}

func TestPublic_PostWithoutCoverHasNilCover(t *testing.T) {
	f := newPubFx(t)
	bare, covered := "b-"+f.r, "c-"+f.r
	f.seedPost(t, seedSpec{id: bare, status: "published", date: day(-1)})
	f.seedPost(t, seedSpec{id: covered, status: "published", date: day(-1)})
	f.seedCover(t, covered, []byte("img"))
	if p, err := f.s.PublicPost(bg, covered, day(0)); err != nil || p.Cover == nil {
		t.Fatalf("covered post = %+v, %v; want a cover path", p, err)
	}
	p, err := f.s.PublicPost(bg, bare, day(0))
	if err != nil {
		t.Fatal(err)
	}
	if p.Cover != nil {
		t.Errorf("Cover = %q, want nil", *p.Cover)
	}
}

func TestPublic_ReadPublicRefusesAWrite(t *testing.T) {
	f := newPubFx(t)
	if err := f.s.readPublic(bg, func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(bg, `SELECT 1`).Scan(&n)
	}); err != nil {
		t.Fatalf("a read in readPublic must succeed: %v", err)
	}
	err := f.s.readPublic(bg, func(tx pgx.Tx) error {
		_, err := tx.Exec(bg, `UPDATE public.blog_posts SET title = title WHERE false`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Errorf("write in readPublic: err = %v, want SQLSTATE 25006 (read_only_sql_transaction)", err)
	}
}
