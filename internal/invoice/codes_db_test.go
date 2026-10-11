package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// codeRow is one nrs_codes row a test inserts; entries is the JSON array stored.
type codeRow struct {
	list, code, entries string
}

// codePrefix is unique per test, so searches on it ignore any real rows of the list.
func codePrefix() string { return "ZZT" + uuid.NewString() }

func seedCodes(t *testing.T, super *pgxpool.Pool, rows ...codeRow) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		if _, err := super.Exec(ctx, `INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, $3::jsonb)`,
			r.list, r.code, r.entries); err != nil {
			t.Fatalf("seed nrs_codes %s/%s: %v", r.list, r.code, err)
		}
	}
	t.Cleanup(func() {
		for _, r := range rows {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = $1 AND code = $2`, r.list, r.code)
		}
	})
}

func codeCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(),
		auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
}

func codesOf(got []CodeEntry) []string {
	out := make([]string, 0, len(got))
	for _, e := range got {
		out = append(out, e.Code)
	}
	return out
}

// searchAs runs the real store the way a request does and fails on an error.
func searchAs(t *testing.T, app *pgxpool.Pool, tenantID, list, q string, limit int) []CodeEntry {
	t.Helper()
	got, err := NewStore(app).SearchCodes(codeCtx(tenantID), list, q, limit)
	if err != nil {
		t.Fatalf("SearchCodes(%s, %q, %d): %v", list, q, limit, err)
	}
	return got
}

func assertCodes(t *testing.T, what string, got []CodeEntry, want ...string) {
	t.Helper()
	if !slices.Equal(codesOf(got), want) {
		t.Errorf("%s: codes = %v, want %v", what, codesOf(got), want)
	}
}

func TestSearchCodes_OrdersExactThenPrefixThenLabel(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes order")
	p := codePrefix()
	// Plain code order would put the label matches (0a, 0b) first.
	seedCodes(t, super,
		codeRow{"states", p + "0b", `[{"name":"town of ` + p + `a1 b"}]`},
		codeRow{"states", p + "a11", `[{"name":"x"}]`},
		codeRow{"states", p + "a1", `[{"name":"x"}]`},
		codeRow{"states", p + "0a", `[{"name":"town of ` + p + `a1 a"}]`},
		codeRow{"states", p + "a10", `[{"name":"x"}]`},
		codeRow{"states", p + "zz", `[{"name":"unrelated"}]`},
	)

	got := searchAs(t, app, tenant, "states", p+"a1", 50)
	assertCodes(t, "exact, prefix, label", got, p+"a1", p+"a10", p+"a11", p+"0a", p+"0b")
}

func TestSearchCodes_MatchesCaseInsensitively(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes case")
	p := codePrefix()
	seedCodes(t, super, codeRow{"states", p + "Lagos", `[{"name":"` + p + ` Lagos State"}]`})
	lower := strings.ToLower(p)

	assertCodes(t, "code arm", searchAs(t, app, tenant, "states", lower+"lag", 50), p+"Lagos")
	assertCodes(t, "label arm", searchAs(t, app, tenant, "states", lower+" lagos st", 50), p+"Lagos")
}

func TestSearchCodes_HonoursTheLimit(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes limit")
	p := codePrefix()
	var rows []codeRow
	for _, c := range []string{"5", "3", "1", "4", "2"} {
		rows = append(rows, codeRow{"states", p + c, `[{"name":"x"}]`})
	}
	seedCodes(t, super, rows...)

	assertCodes(t, "limit 2", searchAs(t, app, tenant, "states", p, 2), p+"1", p+"2")
	assertCodes(t, "limit 10 control", searchAs(t, app, tenant, "states", p, 10), p+"1", p+"2", p+"3", p+"4", p+"5")
}

func TestSearchCodes_LabelKeyOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes label")
	p := codePrefix()
	seedCodes(t, super,
		codeRow{"lgas", p + "1", `[{"name":"N1","value":"V1","description":"D1"}]`},
		codeRow{"lgas", p + "2", `[{"value":"V2","description":"D2"}]`},
		codeRow{"lgas", p + "3", `[{"description":"D3"}]`},
		codeRow{"lgas", p + "4", `[{"name":"","description":"d4"}]`},
		codeRow{"lgas", p + "5", `[{"name":"","value":"v5","description":"D5"}]`},
		codeRow{"lgas", p + "6", `[{"name":"first"},{"name":"second"}]`},
		codeRow{"lgas", p + "7", `[{"name":null,"value":null}]`},
		codeRow{"lgas", p + "8", `[{"value":"","description":"D8"}]`},
	)

	got := searchAs(t, app, tenant, "lgas", p, 50)
	assertCodes(t, "rows", got, p+"1", p+"2", p+"3", p+"4", p+"5", p+"6", p+"7", p+"8")
	for i, want := range []string{"N1", "V2", "D3", "d4", "v5", "first", "", "D8"} {
		if i < len(got) && got[i].Label != want {
			t.Errorf("label of %s = %q, want %q", got[i].Code, got[i].Label, want)
		}
	}
}

func TestSearchCodes_PercentOnlyForNumericTaxCategories(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes percent")
	p := codePrefix()
	seedCodes(t, super,
		codeRow{"tax-categories", p + "1", `[{"value":"STANDARD_VAT","percent":"7.5"}]`},
		codeRow{"tax-categories", p + "2", `[{"value":"OTHER","percent":"Not Available"}]`},
		codeRow{"tax-categories", p + "3", `[{"value":"BLANK","percent":""}]`},
		codeRow{"tax-categories", p + "4", `[{"value":"ZERO_VAT","percent":"0.0"}]`},
		codeRow{"tax-categories", p + "5", `[{"value":"NOKEY"}]`},
		codeRow{"states", p + "6", `[{"name":"Elsewhere","percent":"5"}]`},
	)

	got := searchAs(t, app, tenant, "tax-categories", p, 50)
	assertCodes(t, "tax-categories", got, p+"1", p+"2", p+"3", p+"4", p+"5")
	want := []*string{strPtr("7.5"), nil, nil, strPtr("0.0"), nil}
	for i, w := range want {
		if i >= len(got) {
			break
		}
		g := got[i].Percent
		if (g == nil) != (w == nil) || (g != nil && *g != *w) {
			t.Errorf("percent of %s = %v, want %v", got[i].Code, deref(g), deref(w))
		}
	}

	st := searchAs(t, app, tenant, "states", p, 50)
	assertCodes(t, "states", st, p+"6")
	if len(st) == 1 && st[0].Percent != nil {
		t.Errorf("a states entry's percent = %q, want null", *st[0].Percent)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func TestSearchCodes_BlankQueryListsTheFirstCodes(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes blank")
	p := codePrefix()
	seedCodes(t, super,
		codeRow{"invoice-quantity-codes", p + "c", `[{"name":"x"}]`},
		codeRow{"invoice-quantity-codes", p + "a", `[{"name":"x"}]`},
		codeRow{"invoice-quantity-codes", p + "b", `[{"name":"x"}]`},
	)

	rows, err := super.Query(context.Background(),
		`SELECT code FROM nrs_codes WHERE list = 'invoice-quantity-codes' ORDER BY code LIMIT 50`)
	if err != nil {
		t.Fatalf("read the list's first 50 codes: %v", err)
	}
	var want []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want = append(want, c)
	}
	rows.Close()
	if len(want) < 3 {
		t.Fatalf("the list holds %d codes, want at least the 3 seeded", len(want))
	}

	got := searchAs(t, app, tenant, "invoice-quantity-codes", "", 50)
	if !slices.Equal(codesOf(got), want) {
		t.Errorf("blank q, limit 50: codes = %v, want the list's first 50 by code %v", codesOf(got), want)
	}
	assertCodes(t, "blank q, limit 2", searchAs(t, app, tenant, "invoice-quantity-codes", "", 2), want[:2]...)
}

func TestSearchCodes_WildcardsMatchLiterally(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes wildcards")
	p := codePrefix()
	seedCodes(t, super,
		codeRow{"states", p + "_%x", `[{"name":"x"}]`},
		codeRow{"states", p + "ax", `[{"name":"x"}]`},
		codeRow{"states", p + `b\x`, `[{"name":"x"}]`},
		codeRow{"states", p + "bcx", `[{"name":"x"}]`},
		codeRow{"states", p + "L1", `[{"name":"` + p + `w_z"}]`},
		codeRow{"states", p + "L2", `[{"name":"` + p + `wxz"}]`},
		codeRow{"states", p + "_Zx", `[{"name":"x"}]`},
		codeRow{"states", p + "aY%x", `[{"name":"x"}]`},
	)

	assertCodes(t, "underscore and percent", searchAs(t, app, tenant, "states", p+"_%", 50), p+"_%x")
	assertCodes(t, "percent mid-query", searchAs(t, app, tenant, "states", p+"_%x", 50), p+"_%x")
	assertCodes(t, "underscore mid-query", searchAs(t, app, tenant, "states", p+"aY%", 50), p+"aY%x")
	assertCodes(t, "backslash", searchAs(t, app, tenant, "states", p+`b\`, 50), p+`b\x`)
	assertCodes(t, "underscore in the label arm", searchAs(t, app, tenant, "states", p+"w_z", 50), p+"L1")
}

// codeSearchServe runs the real handler over the real store for ctx.
func codeSearchServe(t *testing.T, app *pgxpool.Pool, ctx context.Context, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /v1/code-lists/{list}", CodeSearchHandler(NewStore(app).SearchCodes, nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	return rec
}

type codeSearchBody struct {
	Codes []CodeEntry `json:"codes"`
}

func decodeCodeSearch(t *testing.T, rec *httptest.ResponseRecorder) codeSearchBody {
	t.Helper()
	var b codeSearchBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return b
}

func TestCodeSearch_NoTenantIs401(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes 401")
	p := codePrefix()
	seedCodes(t, super, codeRow{"states", p + "1", `[{"name":"x"}]`})
	target := "/v1/code-lists/states?q=" + p

	// Control: an active member reads the row.
	rec := codeSearchServe(t, app, codeCtx(tenant), target)
	if rec.Code != http.StatusOK || len(decodeCodeSearch(t, rec).Codes) != 1 {
		t.Errorf("active member control: status %d body %s, want 200 with the seeded row", rec.Code, rec.Body.String())
	}

	for name, ctx := range map[string]context.Context{
		"no identity":        context.Background(),
		"identity no tenant": codeCtx(""),
	} {
		rec := codeSearchServe(t, app, ctx, target)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (body=%s)", name, rec.Code, rec.Body.String())
			continue
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error != "unauthorized" {
			t.Errorf("%s: error = %q, want unauthorized", name, e.Error)
		}
	}
}

func TestCodeSearch_InactiveMemberIs403(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "codes 403")
	const suspended = "d4a10005-0000-4000-8000-0000000000c1"
	const noRow = "d4a10005-0000-4000-8000-0000000000c2"
	seedMembershipWithStatus(t, super, tenant, suspended, "preparer", "suspended")
	p := codePrefix()
	seedCodes(t, super, codeRow{"states", p + "1", `[{"name":"x"}]`})
	target := "/v1/code-lists/states?q=" + p

	rec := codeSearchServe(t, app, codeCtx(tenant), target)
	if rec.Code != http.StatusOK || len(decodeCodeSearch(t, rec).Codes) != 1 {
		t.Errorf("active member control: status %d body %s, want 200 with the seeded row", rec.Code, rec.Body.String())
	}

	for name, subject := range map[string]string{"suspended member": suspended, "no membership row": noRow} {
		t.Run(name, func(t *testing.T) {
			ctx := auth.WithIdentity(context.Background(),
				auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenant})
			assertNotActiveMember403(t, codeSearchServe(t, app, ctx, target))
		})
	}
}

func TestCodeSearch_BothTenantsReadTheSameCodes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "codes tenant A")
	tenantB := seedTenant(t, super, "codes tenant B")
	p := codePrefix()
	seedCodes(t, super,
		codeRow{"states", p + "1", `[{"name":"one"}]`},
		codeRow{"states", p + "2", `[{"name":"two"}]`},
	)

	a := searchAs(t, app, tenantA, "states", p, 50)
	b := searchAs(t, app, tenantB, "states", p, 50)
	assertCodes(t, "tenant A", a, p+"1", p+"2")
	assertCodes(t, "tenant B", b, p+"1", p+"2")
	if !slices.EqualFunc(a, b, func(x, y CodeEntry) bool { return x.Code == y.Code && x.Label == y.Label }) {
		t.Errorf("tenant A %+v and tenant B %+v read different entries", a, b)
	}
}
