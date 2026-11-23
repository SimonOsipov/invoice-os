package erp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures ---------------------------------------------------------------------------

const (
	tinOne = "12345678-0001"
	tinTwo = "87654321-0002"
)

// mapFx is one firm with an admin, a preparer, a preparer limited to client x, and clients x and y.
type mapFx struct {
	super                    *pgxpool.Pool
	store                    *Store
	tenant                   string
	admin, preparer, limited context.Context
	x, y                     string
}

// newMapFx registers cs, or the fake and secondSystem when none is given.
func newMapFx(t *testing.T, cs ...Connector) *mapFx {
	t.Helper()
	super, app := storePools(t)
	if len(cs) == 0 {
		cs = []Connector{NewFake(testRedirect), secondSystem{NewFake(testRedirect)}}
	}
	f := &mapFx{super: super, store: storeOver(t, app, nil, cs...)}
	f.tenant = seedTenant(t, super, "firm")
	f.admin = seedMember(t, super, f.tenant, "admin", "active", true)
	f.preparer = seedMember(t, super, f.tenant, "preparer", "active", true)
	f.x = seedClient(t, super, f.tenant, "Client X")
	f.y = seedClient(t, super, f.tenant, "Client Y")
	f.limited = seedMember(t, super, f.tenant, "preparer", "active", false, f.x)
	return f
}

// withDefault is the fake answering "fake" with default mapping m.
func withDefault(m Mapping) Connector {
	base := NewFake(testRedirect)
	return renamedConnector{Connector: base, name: base.System(), own: true, m: m}
}

func defaultWithout(keys ...string) Mapping {
	m := NewFake(testRedirect).DefaultMapping()
	for _, k := range keys {
		delete(m, k)
	}
	return m
}

func strp(s string) *string { return &s }

func chg(target, path string) FieldChange { return FieldChange{Target: target, SourcePath: &path} }
func chgNull(target string) FieldChange   { return FieldChange{Target: target} }

func validFields() []FieldChange { return []FieldChange{chg("line_description", "lines[].item_id")} }

func validLookups() []Lookup {
	return []Lookup{{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne)}}
}

func (f *mapFx) override(t *testing.T, entity, system, target, path string) {
	t.Helper()
	mustExec(t, f.super,
		"INSERT INTO erp_field_mappings (tenant_id, entity_id, system, target, source_path) VALUES ($1, $2, $3, $4, $5)",
		f.tenant, entity, system, target, path)
}

func (f *mapFx) lookup(t *testing.T, entity, system, company string, l Lookup) {
	t.Helper()
	mustExec(t, f.super,
		"INSERT INTO erp_lookups (tenant_id, entity_id, system, external_key, kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga)"+
			" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)",
		f.tenant, entity, system, company, l.Kind, l.ERPKey, l.TaxCategory, l.ExemptionCode, l.HSNCode, l.ServiceCode, l.TIN, l.State, l.LGA)
}

// overrides is the saved override rows of one client and system, target -> path.
func (f *mapFx) overrides(t *testing.T, entity, system string) Mapping {
	t.Helper()
	rows, err := f.super.Query(context.Background(),
		"SELECT target, source_path FROM erp_field_mappings WHERE entity_id = $1 AND system = $2", entity, system)
	if err != nil {
		t.Fatalf("read overrides: %v", err)
	}
	defer rows.Close()
	out := Mapping{}
	for rows.Next() {
		var k, p string
		if err := rows.Scan(&k, &p); err != nil {
			t.Fatalf("scan overrides: %v", err)
		}
		out[k] = p
	}
	return out
}

// lookupRow is the one stored entry of a client, company, kind and key.
func (f *mapFx) lookupRow(t *testing.T, entity, company, kind, key string) (Lookup, bool) {
	t.Helper()
	var l Lookup
	err := f.super.QueryRow(context.Background(),
		"SELECT kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga FROM erp_lookups"+
			" WHERE entity_id = $1 AND system = 'fake' AND external_key = $2 AND kind = $3 AND erp_key = $4",
		entity, company, kind, key).
		Scan(&l.Kind, &l.ERPKey, &l.TaxCategory, &l.ExemptionCode, &l.HSNCode, &l.ServiceCode, &l.TIN, &l.State, &l.LGA)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lookup{}, false
	}
	if err != nil {
		t.Fatalf("read lookup row: %v", err)
	}
	return l, true
}

// stored is every override and lookup row of the tenant, so "nothing written" also catches an update that
// leaves the counts alone; the audit count rides along.
func (f *mapFx) stored(t *testing.T) string {
	t.Helper()
	var maps, looks string
	mustScan(t, f.super,
		"SELECT coalesce((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM erp_field_mappings m WHERE m.tenant_id = $1), '[]')::text,"+
			" coalesce((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM erp_lookups l WHERE l.tenant_id = $1), '[]')::text",
		[]any{f.tenant}, &maps, &looks)
	return fmt.Sprintf("mappings=%s lookups=%s changed=%d", maps, looks, f.changed(t))
}

func (f *mapFx) changed(t *testing.T) int {
	return eventCount(t, f.super, f.tenant, "erp.mapping.changed")
}

func (f *mapFx) rowCount(t *testing.T) int {
	t.Helper()
	return countOf(t, f.super, "SELECT (SELECT count(*) FROM erp_field_mappings WHERE tenant_id = $1) + (SELECT count(*) FROM erp_lookups WHERE tenant_id = $1)", f.tenant)
}

func fieldOf(t *testing.T, v MappingView, key string) FieldView {
	t.Helper()
	for _, fv := range v.Fields {
		if fv.Target == key {
			return fv
		}
	}
	t.Fatalf("view has no field %q", key)
	return FieldView{}
}

// clientTargets are the targets a view shows as the client's own, sorted.
func clientTargets(v MappingView) []string {
	var out []string
	for _, fv := range v.Fields {
		if fv.Origin == "client" {
			out = append(out, fv.Target)
		}
	}
	slices.Sort(out)
	return out
}

func lineTargetKeys() []string {
	var out []string
	for _, tg := range Targets() {
		if tg.Line {
			out = append(out, tg.Key)
		}
	}
	return out
}

// wantInvalid proves err is want (ErrInvalidMapping or ErrInvalidLookup) with each token in its message and
// none of the secrets.
func wantInvalid(t *testing.T, label string, err, want error, tokens []string, secrets ...string) {
	t.Helper()
	wantErr(t, label, err, want)
	if err == nil {
		return
	}
	for _, tok := range tokens {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("%s: message %q does not name %q", label, err.Error(), tok)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("%s: message %q carries the submitted value %q", label, err.Error(), s)
		}
	}
}

// nrsFixtureCodes is one code per list the lookups check against.
var nrsFixtureCodes = []struct{ list, code string }{
	{"tax-categories", "STANDARD_VAT"}, {"vat-exemptions", "2915.3100"}, {"hs-codes", "0101.21"},
	{"services-codes", "0111"}, {"states", "NG-AB"}, {"lgas", "NG-AB-ANO"},
}

// seedNRSCodes inserts the fixture codes as the superuser and removes only the ones it inserted.
func seedNRSCodes(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	for _, c := range nrsFixtureCodes {
		tag, err := super.Exec(context.Background(),
			"INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]'::jsonb) ON CONFLICT DO NOTHING", c.list, c.code)
		if err != nil {
			t.Fatalf("seed nrs_codes %s/%s: %v", c.list, c.code, err)
		}
		if tag.RowsAffected() == 1 {
			t.Cleanup(func() {
				_, _ = super.Exec(context.Background(), "DELETE FROM nrs_codes WHERE list = $1 AND code = $2", c.list, c.code)
			})
		}
	}
}

func manyCustomers(n int) []Lookup {
	out := make([]Lookup, n)
	for i := range out {
		out[i] = Lookup{Kind: "customer", ERPKey: fmt.Sprintf("k%04d", i), TIN: strp(tinOne)}
	}
	return out
}

// --- AC-1: reads --------------------------------------------------------------------------

func TestStore_MappingShowsClientDefaultAndUnmappedTargets(t *testing.T) {
	def := defaultWithout("line_base_quantity")
	f := newMapFx(t, withDefault(def))
	f.override(t, f.x, "fake", "line_description", "lines[].item_id")

	v, err := f.store.Mapping(f.admin, f.x, "fake")
	if err != nil {
		t.Fatalf("Mapping: %v", err)
	}
	targets := Targets()
	if len(targets) == 0 || len(v.Fields) != len(targets) {
		t.Fatalf("Mapping has %d fields, want len(Targets()) = %d", len(v.Fields), len(targets))
	}
	if v.System != "fake" {
		t.Errorf("System = %q, want fake", v.System)
	}
	for i, tg := range targets {
		got := v.Fields[i]
		if got.Target != tg.Key || got.Label != tg.Label || got.Line != tg.Line {
			t.Errorf("field %d = {%s %q line=%v}, want {%s %q line=%v}", i, got.Target, got.Label, got.Line, tg.Key, tg.Label, tg.Line)
		}
		if tg.Key == "line_description" || tg.Key == "line_base_quantity" {
			continue
		}
		if got.Origin != "default" || deref(got.SourcePath) != def[tg.Key] || deref(got.DefaultPath) != def[tg.Key] {
			t.Errorf("%s = origin %q source %s default %s, want the default %q", tg.Key, got.Origin, deref(got.SourcePath), deref(got.DefaultPath), def[tg.Key])
		}
	}
	d := fieldOf(t, v, "line_description")
	if d.Origin != "client" || deref(d.SourcePath) != "lines[].item_id" || deref(d.DefaultPath) != "lines[].description" {
		t.Errorf("line_description = origin %q source %s default %s, want client lines[].item_id over default lines[].description", d.Origin, deref(d.SourcePath), deref(d.DefaultPath))
	}
	q := fieldOf(t, v, "line_base_quantity")
	if q.Origin != "none" || q.SourcePath != nil || q.DefaultPath != nil {
		t.Errorf("line_base_quantity = origin %q source %s default %s, want none with both paths null", q.Origin, deref(q.SourcePath), deref(q.DefaultPath))
	}
	n := fieldOf(t, v, "invoice_number")
	if n.Origin != "default" || deref(n.SourcePath) != "number" || deref(n.DefaultPath) != "number" {
		t.Errorf("invoice_number = origin %q source %s default %s, want default number", n.Origin, deref(n.SourcePath), deref(n.DefaultPath))
	}

	// The wire names are the API contract; an absent path is null, not missing.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		System string
		Fields []map[string]any
	}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.Fields) != len(targets) {
		t.Fatalf("wire = %s, %v", raw, err)
	}
	keys := make([]string, 0, 6)
	for k := range wire.Fields[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"default_path", "label", "line", "origin", "source_path", "target"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("a field's JSON keys = %v, want %v", keys, want)
	}
	for _, wf := range wire.Fields {
		if wf["target"] == "line_base_quantity" && (wf["source_path"] != nil || wf["default_path"] != nil) {
			t.Errorf("unmapped target on the wire = %v, want null paths", wf)
		}
	}
}

func TestStore_MappingIsPerClientAndSystem(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_description", "lines[].item_id")
	f.override(t, f.x, "second", "line_unit_price", "lines[].price")

	y, err := f.store.Mapping(f.admin, f.y, "fake")
	if err != nil || len(y.Fields) != len(Targets()) || len(clientTargets(y)) != 0 {
		t.Errorf("Mapping(Y, fake): %d fields, client targets %v, err %v, want every target default", len(y.Fields), clientTargets(y), err)
	}
	xf, err := f.store.Mapping(f.admin, f.x, "fake")
	if err != nil || !reflect.DeepEqual(clientTargets(xf), []string{"line_description"}) {
		t.Errorf("Mapping(X, fake) client targets = %v, %v, want only line_description", clientTargets(xf), err)
	}
	xs, err := f.store.Mapping(f.admin, f.x, "second")
	if err != nil || !reflect.DeepEqual(clientTargets(xs), []string{"line_unit_price"}) {
		t.Errorf("Mapping(X, second) client targets = %v, %v, want only line_unit_price", clientTargets(xs), err)
	}
	if err == nil && xs.System != "second" {
		t.Errorf("Mapping(X, second).System = %q", xs.System)
	}
}

func TestStore_EveryActiveMemberReadsMappingAndLookups(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_description", "lines[].item_id")
	f.lookup(t, f.x, "fake", "co-1", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne)})
	f.lookup(t, f.x, "fake", "co-1", Lookup{Kind: "item", ERPKey: "i1", HSNCode: strp("0101.21")})

	var wantM MappingView
	var wantL LookupsView
	for i, c := range []struct {
		name string
		ctx  context.Context
	}{{"admin", f.admin}, {"preparer", f.preparer}, {"client-limited member", f.limited}} {
		m, err := f.store.Mapping(c.ctx, f.x, "fake")
		if err != nil {
			t.Errorf("%s Mapping: %v", c.name, err)
			continue
		}
		l, err := f.store.Lookups(c.ctx, f.x, "fake", "co-1")
		if err != nil {
			t.Errorf("%s Lookups: %v", c.name, err)
			continue
		}
		if i == 0 {
			wantM, wantL = m, l
			if !reflect.DeepEqual(clientTargets(m), []string{"line_description"}) || len(l.Lookups) != 2 {
				t.Fatalf("control: admin sees client targets %v and %d lookups, want line_description and 2", clientTargets(m), len(l.Lookups))
			}
			continue
		}
		if !reflect.DeepEqual(m, wantM) || !reflect.DeepEqual(l, wantL) {
			t.Errorf("%s reads differ from the admin's:\n%+v\n%+v", c.name, m, wantM)
		}
	}
}

func TestStore_ReadsOutsideTheCallersScopeAreNotFound(t *testing.T) {
	f := newMapFx(t)
	other := seedTenant(t, f.super, "firm")
	foreign := seedClient(t, f.super, other, "Foreign Client")
	if _, err := f.store.Mapping(f.limited, f.x, "fake"); err != nil {
		t.Fatalf("control: the limited member's own client Mapping = %v, want nil", err)
	}
	for _, c := range []struct{ name, entity string }{
		{"a client outside the member's scope", f.y},
		{"an unknown client", uuid.NewString()},
		{"a non-UUID id", "not-a-uuid"},
	} {
		_, err := f.store.Mapping(f.limited, c.entity, "fake")
		wantErr(t, "limited Mapping "+c.name, err, ErrNoSuchRecord)
		_, err = f.store.Lookups(f.limited, c.entity, "fake", "co-1")
		wantErr(t, "limited Lookups "+c.name, err, ErrNoSuchRecord)
	}
	_, err := f.store.Mapping(f.admin, foreign, "fake")
	wantErr(t, "admin Mapping of another tenant's client", err, ErrNoSuchRecord)
	_, err = f.store.Lookups(f.admin, foreign, "fake", "co-1")
	wantErr(t, "admin Lookups of another tenant's client", err, ErrNoSuchRecord)
}

func TestStore_InactiveMemberReadsAreRefusedBySeam(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_description", "lines[].item_id")
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{
		{"suspended preparer", seedMember(t, f.super, f.tenant, "preparer", "suspended", true)},
		{"inactive admin", seedMember(t, f.super, f.tenant, "admin", "invited", true)},
	} {
		_, err := f.store.Mapping(c.ctx, f.x, "fake")
		wantErr(t, c.name+" Mapping", err, db.ErrNotActiveMember)
		_, err = f.store.Lookups(c.ctx, f.x, "fake", "co-1")
		wantErr(t, c.name+" Lookups", err, db.ErrNotActiveMember)
	}
	if _, err := f.store.Mapping(f.preparer, f.x, "fake"); err != nil {
		t.Errorf("control: an active preparer's Mapping = %v, want nil", err)
	}
}

func TestStore_MappingOfAnUnregisteredSystemIsRefused(t *testing.T) {
	f := newMapFx(t)
	before := f.stored(t)
	_, err := f.store.Mapping(f.admin, f.x, "quickbooks")
	wantErr(t, "Mapping", err, ErrUnknownSystem)
	_, err = f.store.Lookups(f.admin, f.x, "quickbooks", "co-1")
	wantErr(t, "Lookups", err, ErrUnknownSystem)
	_, err = f.store.SaveMapping(f.admin, f.x, "quickbooks", validFields())
	wantErr(t, "SaveMapping", err, ErrUnknownSystem)
	_, err = f.store.SaveLookups(f.admin, f.x, "quickbooks", "co-1", validLookups())
	wantErr(t, "SaveLookups", err, ErrUnknownSystem)
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
	// The same bodies save on a registered system, so the refusals above were the system's.
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields()); err != nil {
		t.Errorf("control: SaveMapping on fake = %v, want nil", err)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", validLookups()); err != nil {
		t.Errorf("control: SaveLookups on fake = %v, want nil", err)
	}
}

// --- AC-2: SaveMapping ----------------------------------------------------------------------

func TestStore_SaveMappingUpsertsAndNullReverts(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_unit_price", "lines[].price") // a target the saves never name
	rows := func() int {
		return countOf(t, f.super, "SELECT count(*) FROM erp_field_mappings WHERE entity_id = $1 AND system = 'fake' AND target = 'line_description'", f.x)
	}

	v, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields())
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if got := f.overrides(t, f.x, "fake")["line_description"]; rows() != 1 || got != "lines[].item_id" {
		t.Errorf("after the first save: %d rows, path %q, want 1 and lines[].item_id", rows(), got)
	}
	if d := fieldOf(t, v, "line_description"); d.Origin != "client" || deref(d.SourcePath) != "lines[].item_id" {
		t.Errorf("returned view line_description = %q %s, want client lines[].item_id", d.Origin, deref(d.SourcePath))
	}

	v, err = f.store.SaveMapping(f.admin, f.x, "fake", []FieldChange{chg("line_description", "lines[].description")})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if got := f.overrides(t, f.x, "fake")["line_description"]; rows() != 1 || got != "lines[].description" {
		t.Errorf("after the second save: %d rows, path %q, want 1 and lines[].description", rows(), got)
	}
	if d := fieldOf(t, v, "line_description"); deref(d.SourcePath) != "lines[].description" {
		t.Errorf("returned view line_description = %s, want lines[].description", deref(d.SourcePath))
	}

	v, err = f.store.SaveMapping(f.admin, f.x, "fake", []FieldChange{chgNull("line_description"), chgNull("line_quantity")})
	if err != nil {
		t.Fatalf("null save: %v", err)
	}
	if rows() != 0 {
		t.Errorf("after the null save: %d rows, want 0", rows())
	}
	if d := fieldOf(t, v, "line_description"); d.Origin != "default" || deref(d.SourcePath) != deref(d.DefaultPath) {
		t.Errorf("returned view line_description = %q %s, want the default", d.Origin, deref(d.SourcePath))
	}
	if p := fieldOf(t, v, "line_unit_price"); p.Origin != "client" || deref(p.SourcePath) != "lines[].price" {
		t.Errorf("an unnamed target = %q %s, want its override kept", p.Origin, deref(p.SourcePath))
	}
	if got := f.overrides(t, f.x, "fake"); !reflect.DeepEqual(got, Mapping{"line_unit_price": "lines[].price"}) {
		t.Errorf("rows after the saves = %v, want only line_unit_price", got)
	}
}

func TestStore_SaveMappingRefusesEachInvalidChange(t *testing.T) {
	f := newMapFx(t)
	noNumber := storeOver(t, f.store.pool, nil, withDefault(defaultWithout("invoice_number")))
	f.override(t, f.x, "fake", "invoice_number", "num") // so the null below deletes something

	all := make([]FieldChange, 0, len(Targets())+1)
	for _, tg := range Targets() {
		all = append(all, chg(tg.Key, NewFake(testRedirect).DefaultMapping()[tg.Key]))
	}
	good := chg("line_unit_price", "lines[].price") // valid; the refused clause must take it down too
	cases := []struct {
		name   string
		store  *Store
		fields []FieldChange
		tokens []string
	}{
		{"unknown target", f.store, []FieldChange{good, chg("no_such_target", "lines[].x")}, []string{"no_such_target", reasonUnknown}},
		{"the same target twice", f.store, []FieldChange{chg("line_description", "lines[].item_id"), chg("line_description", "lines[].description")}, []string{"line_description"}},
		{"no rows", f.store, nil, []string{"fields"}},
		{"an empty list", f.store, []FieldChange{}, []string{"fields"}},
		// One past the limit repeats a target, so only the row count says "fields".
		{"one row more than the targets", f.store, append(slices.Clone(all), chg(all[0].Target, "number")), []string{"fields"}},
		{"a header target reading an array", f.store, []FieldChange{good, chg("issue_date", "lines[].d")}, []string{"issue_date", reasonHeaderArr}},
		{"a line target on another array", f.store, []FieldChange{good, chg("line_quantity", "Line[].Qty")}, []string{"line_quantity", reasonOneArray}},
		{"a malformed path", f.store, []FieldChange{good, chg("line_total", "lines[].a b")}, []string{"line_total", reasonPath}},
		{"@party on a line target", f.store, []FieldChange{good, chg("line_description", "@party.name")}, []string{"line_description", reasonParty}},
		{"invoice_number reverted where the default lacks it", noNumber, []FieldChange{good, chgNull("invoice_number")}, []string{"invoice_number", reasonRequired}},
	}
	before := f.stored(t)
	for _, c := range cases {
		_, err := c.store.SaveMapping(f.admin, f.x, "fake", c.fields)
		wantInvalid(t, c.name, err, ErrInvalidMapping, c.tokens)
		if got := f.stored(t); got != before {
			t.Errorf("%s: rows or audit changed:\nbefore %s\nafter  %s", c.name, before, got)
		}
	}
	// A body that names every target once is accepted, so the refusals above were each clause's own.
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", all); err != nil {
		t.Errorf("control: every target at its default path = %v, want nil", err)
	}
}

func TestStore_SaveMappingWaitsForTheClientSystemLock(t *testing.T) {
	f := newMapFx(t)
	ctx := context.Background()
	holder, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('erp_mapping:' || $1::text || ':fake', 0))", f.x); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	done, doneUpper := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := f.store.SaveMapping(bounded(t, f.admin), f.x, "fake", validFields())
		done <- err
	}()
	// The same client spelled in capitals is the same client and the same lock.
	go func() {
		_, err := f.store.SaveMapping(bounded(t, f.admin), strings.ToUpper(f.x), "fake", validFields())
		doneUpper <- err
	}()
	select {
	case err := <-done:
		t.Errorf("SaveMapping(X, fake) returned %v while the lock was held, want it to wait", err)
		return
	case err := <-doneUpper:
		t.Errorf("SaveMapping(X in capitals, fake) returned %v while the lock was held, want it to wait", err)
		return
	case <-time.After(300 * time.Millisecond):
	}
	// The lock is per client and system: neither another client nor another system waits.
	for _, other := range []struct{ entity, system string }{{f.y, "fake"}, {f.x, "second"}} {
		got := make(chan error, 1)
		go func() {
			_, err := f.store.SaveMapping(bounded(t, f.admin), other.entity, other.system, validFields())
			got <- err
		}()
		select {
		case err := <-got:
			if err != nil {
				t.Errorf("SaveMapping(%s, %s) = %v, want nil", other.entity, other.system, err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("SaveMapping(%s, %s) waited on a lock it does not share", other.entity, other.system)
		}
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SaveMapping(X, fake) after the release = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("SaveMapping(X, fake) did not return after the lock was released")
	}
	select {
	case err := <-doneUpper:
		if err != nil {
			t.Errorf("SaveMapping(X in capitals, fake) after the release = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("SaveMapping(X in capitals, fake) did not return after the lock was released")
	}
}

// With no line target in the default, each save alone reads one array and is valid; the pair reads two.
func TestStore_ConcurrentSavesCannotCommitAnInvalidMapping(t *testing.T) {
	def := Mapping{"invoice_number": "number"}
	f := newMapFx(t, withDefault(def))
	const rounds = 20
	for i := range rounds {
		e := seedClient(t, f.super, f.tenant, fmt.Sprintf("Race %d", i))
		ctx := bounded(t, f.admin)
		errs := race(
			func() error {
				_, err := f.store.SaveMapping(ctx, e, "fake", []FieldChange{chg("line_description", "a[].x")})
				return err
			},
			func() error {
				_, err := f.store.SaveMapping(ctx, e, "fake", []FieldChange{chg("line_quantity", "b[].y")})
				return err
			},
		)
		ok, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInvalidMapping):
				refused++
			default:
				t.Fatalf("round %d: save = %v, want nil or ErrInvalidMapping", i, err)
			}
		}
		if ok != 1 || refused != 1 {
			t.Fatalf("round %d: %d saves succeeded and %d were refused, want exactly one each", i, ok, refused)
		}
		got := f.overrides(t, e, "fake")
		if len(got) != 1 {
			t.Fatalf("round %d: committed overrides = %v, want only the winner's", i, got)
		}
		if err := ValidateMapping(def.Merge(got)); err != nil {
			t.Fatalf("round %d: committed effective mapping fails ValidateMapping: %v", i, err)
		}
	}
	if n := f.changed(t); n != rounds {
		t.Errorf("%d erp.mapping.changed rows, want one per winning save (%d)", n, rounds)
	}
}

func TestStore_SaveMappingMovesAllLineTargetsTogether(t *testing.T) {
	f := newMapFx(t)
	lines := lineTargetKeys()
	if len(lines) < 3 || !slices.Contains(lines, TargetTaxCode) || !slices.Contains(lines, TargetItemID) {
		t.Fatalf("line targets = %v, want the import line keys plus erp_tax_code and erp_item_id", lines)
	}
	var body []FieldChange
	for _, k := range lines {
		body = append(body, chg(k, "Line[]."+k))
	}
	v, err := f.store.SaveMapping(f.admin, f.x, "fake", body)
	if err != nil {
		t.Fatalf("SaveMapping of every line target under Line[]: %v", err)
	}
	for _, k := range lines {
		if fv := fieldOf(t, v, k); fv.Origin != "client" || deref(fv.SourcePath) != "Line[]."+k {
			t.Errorf("%s = %q %s, want client Line[].%s", k, fv.Origin, deref(fv.SourcePath), k)
		}
	}
	if got := clientTargets(v); len(got) != len(lines) {
		t.Errorf("client targets = %v, want exactly the %d line targets", got, len(lines))
	}
}

// --- AC-3: lookups --------------------------------------------------------------------------

func TestStore_LookupsListOnlyTheClientsSystemCompanyEntries(t *testing.T) {
	f := newMapFx(t)
	// Keys sort differently from kinds, and insertion order matches neither.
	tax := Lookup{Kind: "tax_code", ERPKey: "atax", TaxCategory: strp("STANDARD_VAT"), ExemptionCode: strp("2915.3100")}
	c1 := Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne), State: strp("NG-AB")}
	c2 := Lookup{Kind: "customer", ERPKey: "c2", LGA: strp("NG-AB-ANO")}
	item := Lookup{Kind: "item", ERPKey: "bitem", HSNCode: strp("0101.21")}
	f.lookup(t, f.x, "fake", "co-1", tax)
	f.lookup(t, f.x, "fake", "co-1", c2)
	f.lookup(t, f.x, "fake", "co-2", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinTwo)})
	f.lookup(t, f.x, "second", "co-1", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinTwo)})
	f.lookup(t, f.y, "fake", "co-1", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinTwo)})
	f.lookup(t, f.x, "fake", "co-1", item)
	f.lookup(t, f.x, "fake", "co-1", c1)

	v, err := f.store.Lookups(f.admin, f.x, "fake", "co-1")
	if err != nil {
		t.Fatalf("Lookups: %v", err)
	}
	if want := []Lookup{c1, c2, item, tax}; !reflect.DeepEqual(v.Lookups, want) {
		t.Errorf("Lookups = %+v, want [customer c1, customer c2, item bitem, tax_code atax] with their values", v.Lookups)
	}
	if v.System != "fake" || v.ExternalKey != "co-1" {
		t.Errorf("view = %s/%s, want fake/co-1", v.System, v.ExternalKey)
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct{ Lookups []map[string]any }
	var top map[string]any
	if json.Unmarshal(raw, &top) != nil || json.Unmarshal(raw, &wire) != nil || len(wire.Lookups) != 4 {
		t.Fatalf("wire = %s", raw)
	}
	topKeys := make([]string, 0, 3)
	for k := range top {
		topKeys = append(topKeys, k)
	}
	slices.Sort(topKeys)
	if want := []string{"external_key", "lookups", "system"}; !reflect.DeepEqual(topKeys, want) {
		t.Errorf("view JSON keys = %v, want %v", topKeys, want)
	}
	keys := make([]string, 0, 9)
	for k := range wire.Lookups[1] { // customer c2: only lga is set
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"erp_key", "exemption_code", "hsn_code", "kind", "lga", "service_code", "state", "tax_category", "tin"}
	if !reflect.DeepEqual(keys, want) || wire.Lookups[1]["tin"] != nil || wire.Lookups[1]["lga"] != "NG-AB-ANO" {
		t.Errorf("an entry's JSON = %v, want the nine keys with null for unset", wire.Lookups[1])
	}
}

func TestStore_SaveLookupsWritesUnderItsCompanyOnly(t *testing.T) {
	f := newMapFx(t)
	f.lookup(t, f.x, "fake", "co-2", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinTwo)})
	tinAt := func(company string) string {
		l, ok := f.lookupRow(t, f.x, company, "customer", "c1")
		if !ok {
			return "<no row>"
		}
		return deref(l.TIN)
	}

	v, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne)}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if tinAt("co-1") != tinOne || tinAt("co-2") != tinTwo {
		t.Errorf("after the save: co-1 %s, co-2 %s, want %s and %s", tinAt("co-1"), tinAt("co-2"), tinOne, tinTwo)
	}
	if len(v.Lookups) != 1 || v.ExternalKey != "co-1" || deref(v.Lookups[0].TIN) != tinOne {
		t.Errorf("returned view = %+v, want only co-1's entry", v)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{{Kind: "customer", ERPKey: "c1"}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if tinAt("co-1") != "<no row>" || tinAt("co-2") != tinTwo {
		t.Errorf("after the delete: co-1 %s, co-2 %s, want no row and %s", tinAt("co-1"), tinAt("co-2"), tinTwo)
	}
}

// --- AC-4: SaveLookups ----------------------------------------------------------------------

func TestStore_SaveLookupsUpsertsReplacesAndDeletes(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	f.lookup(t, f.x, "fake", "co-1", Lookup{Kind: "customer", ERPKey: "c2", TIN: strp(tinTwo)}) // a sibling the saves never name
	save := func(l Lookup) LookupsView {
		t.Helper()
		v, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{l})
		if err != nil {
			t.Fatalf("save %+v: %v", l, err)
		}
		if read, err := f.store.Lookups(f.admin, f.x, "fake", "co-1"); err != nil || !reflect.DeepEqual(v, read) {
			t.Errorf("returned view %+v differs from Lookups %+v (%v)", v, read, err)
		}
		return v
	}
	row := func(kind, key string) (Lookup, bool) { return f.lookupRow(t, f.x, "co-1", kind, key) }

	save(Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne), State: strp("NG-AB")})
	if l, ok := row("customer", "c1"); !ok || deref(l.TIN) != tinOne || deref(l.State) != "NG-AB" || l.LGA != nil {
		t.Errorf("after the first save: %+v %v, want tin and state, no lga", l, ok)
	}
	save(Lookup{Kind: "customer", ERPKey: "c1", LGA: strp("NG-AB-ANO")})
	if l, ok := row("customer", "c1"); !ok || deref(l.LGA) != "NG-AB-ANO" || l.TIN != nil || l.State != nil {
		t.Errorf("after the replace: %+v %v, want only lga", l, ok)
	}
	v := save(Lookup{Kind: "customer", ERPKey: "c1"})
	if _, ok := row("customer", "c1"); ok {
		t.Error("an entry with every value null left its row")
	}
	for _, l := range v.Lookups {
		if l.ERPKey == "c1" {
			t.Errorf("returned view still lists the deleted entry: %+v", l)
		}
	}
	if l, ok := row("customer", "c2"); !ok || deref(l.TIN) != tinTwo {
		t.Errorf("an entry the saves never named = %+v %v, want it untouched", l, ok)
	}

	save(Lookup{Kind: "item", ERPKey: "i1", HSNCode: strp("0101.21")})
	save(Lookup{Kind: "item", ERPKey: "i1", ServiceCode: strp("0111")})
	if l, ok := row("item", "i1"); !ok || deref(l.ServiceCode) != "0111" || l.HSNCode != nil {
		t.Errorf("item after hsn then service = %+v %v, want only service", l, ok)
	}
}

func TestStore_SaveLookupsChecksEachCodeAgainstItsOwnList(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	companion := Lookup{Kind: "customer", ERPKey: "companion", TIN: strp(tinOne)}

	own := []Lookup{
		{Kind: "customer", ERPKey: "s", State: strp("NG-AB")},
		{Kind: "customer", ERPKey: "l", LGA: strp("NG-AB-ANO")},
		{Kind: "item", ERPKey: "h", HSNCode: strp("0101.21")},
		{Kind: "item", ERPKey: "v", ServiceCode: strp("0111")},
		{Kind: "tax_code", ERPKey: "t", TaxCategory: strp("STANDARD_VAT"), ExemptionCode: strp("2915.3100")},
	}
	v, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", own)
	if err != nil || len(v.Lookups) != len(own) {
		t.Fatalf("each field with its own list's code: %d entries, %v, want %d and nil", len(v.Lookups), err, len(own))
	}
	if l, ok := f.lookupRow(t, f.x, "co-1", "tax_code", "t"); !ok || deref(l.ExemptionCode) != "2915.3100" {
		t.Errorf("tax_code row = %+v %v, want the exemption code stored", l, ok)
	}

	before := f.stored(t)
	wrong := []struct {
		name         string
		entry        Lookup
		field, list  string
		secret       string
		exactMessage string
	}{
		{"state from the lgas list", Lookup{Kind: "customer", ERPKey: "w1", State: strp("NG-AB-ANO")}, "state", "states", "NG-AB-ANO", "customer state: not on the NRS states list"},
		{"lga from the states list", Lookup{Kind: "customer", ERPKey: "w2", LGA: strp("NG-AB")}, "lga", "lgas", "NG-AB", ""},
		{"hsn from the services list", Lookup{Kind: "item", ERPKey: "w3", HSNCode: strp("0111")}, "hsn_code", "hs-codes", "0111", ""},
		{"service from the hs list", Lookup{Kind: "item", ERPKey: "w4", ServiceCode: strp("0101.21")}, "service_code", "services-codes", "0101.21", ""},
		{"tax category from the states list", Lookup{Kind: "tax_code", ERPKey: "w5", TaxCategory: strp("NG-AB")}, "tax_category", "tax-categories", "NG-AB", ""},
		{"exemption from the hs list", Lookup{Kind: "tax_code", ERPKey: "w6", TaxCategory: strp("STANDARD_VAT"), ExemptionCode: strp("0101.21")}, "exemption_code", "vat-exemptions", "0101.21", ""},
		{"a state on no list", Lookup{Kind: "customer", ERPKey: "w7", State: strp("NG-ZZ")}, "state", "states", "NG-ZZ", "customer state: not on the NRS states list"},
	}
	for _, c := range wrong {
		_, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{companion, c.entry})
		wantInvalid(t, c.name, err, ErrInvalidLookup, []string{c.entry.Kind, c.field, c.list}, c.secret)
		if c.exactMessage != "" && err != nil && err.Error() != c.exactMessage {
			t.Errorf("%s: message = %q, want %q", c.name, err.Error(), c.exactMessage)
		}
		if got := f.stored(t); got != before {
			t.Errorf("%s: rows or audit changed:\nbefore %s\nafter  %s", c.name, before, got)
		}
	}
}

func TestStore_SaveLookupsNamesTheFirstUnknownCodeInRequestOrder(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	entries := []Lookup{
		{Kind: "customer", ERPKey: "a", TIN: strp(tinOne), State: strp("NG-AB"), LGA: strp("NG-ZZ-NOPE")},
		{Kind: "item", ERPKey: "b", HSNCode: strp("9999.99")},
		{Kind: "customer", ERPKey: "c", State: strp("NG-ZZ")},
	}
	const want = "customer lga: not on the NRS lgas list"
	for i := range 20 {
		if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", entries); err == nil || err.Error() != want {
			t.Fatalf("call %d: err = %v, want %q", i, err, want)
		}
	}
}

func TestStore_SaveLookupsRefusesEachBadEntry(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	ok := func(k string) Lookup { return Lookup{Kind: "customer", ERPKey: k, TIN: strp(tinOne)} }
	longKey := strings.Repeat("q", 257)
	cases := []struct {
		name    string
		company string
		entries []Lookup
		tokens  []string
		secret  string
	}{
		{"external_key empty", "", []Lookup{ok("c1")}, []string{"external_key: required"}, ""},
		{"external_key blank", "  ", []Lookup{ok("c1")}, []string{"external_key: required"}, ""},
		{"external_key of 257", strings.Repeat("e", 257), []Lookup{ok("c1")}, []string{"external_key: required"}, strings.Repeat("e", 257)},
		{"unknown kind", "co-1", []Lookup{ok("c1"), {Kind: "vendor", ERPKey: "v1", TIN: strp(tinOne)}}, []string{"kind"}, "vendor"},
		{"empty key", "co-1", []Lookup{ok("c1"), ok("")}, []string{"erp_key"}, ""},
		{"blank key", "co-1", []Lookup{ok("c1"), ok("  ")}, []string{"erp_key"}, ""},
		{"key of 257", "co-1", []Lookup{ok("c1"), ok(longKey)}, []string{"erp_key"}, longKey},
		{"tax_code with a tin", "co-1", []Lookup{ok("c1"), {Kind: "tax_code", ERPKey: "t1", TaxCategory: strp("STANDARD_VAT"), TIN: strp(tinOne)}}, []string{"tax_code", "tin"}, tinOne},
		{"tax_code without a tax_category", "co-1", []Lookup{ok("c1"), {Kind: "tax_code", ERPKey: "t1", ExemptionCode: strp("2915.3100")}}, []string{"tax_code", "tax_category"}, ""},
		{"item with hsn and service", "co-1", []Lookup{ok("c1"), {Kind: "item", ERPKey: "i1", HSNCode: strp("0101.21"), ServiceCode: strp("0111")}}, []string{"item"}, ""},
		{"item with a field it does not take", "co-1", []Lookup{ok("c1"), {Kind: "item", ERPKey: "i1", HSNCode: strp("0101.21"), TaxCategory: strp("STANDARD_VAT")}}, []string{"item", "tax_category"}, ""},
		{"customer with an hsn", "co-1", []Lookup{ok("c1"), {Kind: "customer", ERPKey: "c2", TIN: strp(tinOne), HSNCode: strp("0101.21")}}, []string{"customer", "hsn_code"}, ""},
		{"duplicate entry", "co-1", []Lookup{ok("c1"), {Kind: "customer", ERPKey: "c1", TIN: strp(tinTwo)}}, nil, ""},
		{"duplicate after trimming", "co-1", []Lookup{ok("c1"), ok(" c1 ")}, nil, ""},
		{"no entries", "co-1", nil, nil, ""},
		{"501 entries", "co-1", manyCustomers(501), nil, ""},
		{"a tin that is not fileable", "co-1", []Lookup{ok("c1"), {Kind: "customer", ERPKey: "c2", TIN: strp("1234567897")}}, []string{"tin"}, "1234567897"},
	}
	before := f.stored(t)
	for _, c := range cases {
		_, err := f.store.SaveLookups(f.admin, f.x, "fake", c.company, c.entries)
		wantInvalid(t, c.name, err, ErrInvalidLookup, c.tokens, c.secret)
		if got := f.stored(t); got != before {
			t.Errorf("%s: rows or audit changed:\nbefore %s\nafter  %s", c.name, before, got)
		}
	}

	// The boundaries and the delete-only entry are accepted, so the refusals above were each clause's own.
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{{Kind: "customer", ERPKey: "nothing-set"}}); err != nil {
		t.Errorf("control: a customer with no value = %v, want nil (a delete)", err)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", strings.Repeat("e", 256), []Lookup{ok(strings.Repeat("k", 256))}); err != nil {
		t.Errorf("control: external_key and key of 256 = %v, want nil", err)
	}
	if v, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-5", manyCustomers(500)); err != nil || len(v.Lookups) != 500 {
		t.Errorf("control: 500 entries = %d, %v, want 500 and nil", len(v.Lookups), err)
	}
}

func TestStore_SaveLookupsIsAllOrNothing(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	f.lookup(t, f.x, "fake", "co-1", Lookup{Kind: "customer", ERPKey: "gone", TIN: strp(tinTwo)})
	before := f.stored(t)
	_, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{
		{Kind: "customer", ERPKey: "gone"},                          // a delete
		{Kind: "customer", ERPKey: "new", TIN: strp(tinOne)},        // an insert
		{Kind: "customer", ERPKey: "bad", State: strp("NG-AB-ANO")}, // off its list
	})
	wantInvalid(t, "save with a bad third entry", err, ErrInvalidLookup, []string{"state"})
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
	if _, ok := f.lookupRow(t, f.x, "co-1", "customer", "gone"); !ok {
		t.Error("the delete of the first entry was kept")
	}
}

func TestStore_SaveLookupsTrimsAndBlankIsNull(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	v, err := f.store.SaveLookups(f.admin, f.x, "fake", " co-1 ", []Lookup{
		{Kind: "customer", ERPKey: " c1 ", TIN: strp(" " + tinOne + " "), State: strp("  ")},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	l, found := f.lookupRow(t, f.x, "co-1", "customer", "c1")
	if !found || deref(l.TIN) != tinOne || l.State != nil {
		t.Errorf("row = %+v %v, want key c1 under co-1, tin trimmed, state null", l, found)
	}
	if v.ExternalKey != "co-1" || len(v.Lookups) != 1 || v.Lookups[0].ERPKey != "c1" {
		t.Errorf("returned view = %+v, want the trimmed company and key", v)
	}
}

// --- AC-5: who may save ---------------------------------------------------------------------

func TestStore_NonAdminSaveRefusedWhateverTheInput(t *testing.T) {
	f := newMapFx(t)
	other := seedTenant(t, f.super, "firm")
	foreign := seedClient(t, f.super, other, "Foreign Client")
	badFields := []FieldChange{chg("issue_date", "lines[].d")}
	badLookups := []Lookup{{Kind: "vendor", ERPKey: "v1", TIN: strp(tinOne)}}
	inputs := []struct {
		name, entity, system    string
		fields                  []FieldChange
		lookups                 []Lookup
		downFields, downLookups error // the admin's answer; nil: the admin's save succeeds
	}{
		{"a valid body", f.x, "fake", validFields(), validLookups(), nil, nil},
		{"an invalid body", f.x, "fake", badFields, badLookups, ErrInvalidMapping, ErrInvalidLookup},
		{"an empty body", f.x, "fake", nil, nil, ErrInvalidMapping, ErrInvalidLookup},
		{"an unregistered system", f.x, "quickbooks", validFields(), validLookups(), ErrUnknownSystem, ErrUnknownSystem},
		{"another tenant's client", foreign, "fake", validFields(), validLookups(), ErrNoSuchRecord, ErrNoSuchRecord},
		{"a non-UUID client", "not-a-uuid", "fake", validFields(), validLookups(), ErrNoSuchRecord, ErrNoSuchRecord},
		{"a client outside the member's scope", f.y, "fake", validFields(), validLookups(), nil, nil},
	}
	before := f.stored(t)
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{{"preparer", f.preparer}, {"client-limited member", f.limited}} {
		for _, in := range inputs {
			v, err := f.store.SaveMapping(c.ctx, in.entity, in.system, in.fields)
			wantErr(t, c.name+" SaveMapping, "+in.name, err, ErrNotPermitted)
			if len(v.Fields) != 0 {
				t.Errorf("%s SaveMapping, %s: returned %d fields on a refusal", c.name, in.name, len(v.Fields))
			}
			lv, err := f.store.SaveLookups(c.ctx, in.entity, in.system, "co-1", in.lookups)
			wantErr(t, c.name+" SaveLookups, "+in.name, err, ErrNotPermitted)
			if len(lv.Lookups) != 0 {
				t.Errorf("%s SaveLookups, %s: returned %d lookups on a refusal", c.name, in.name, len(lv.Lookups))
			}
		}
	}
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed after refused saves:\nbefore %s\nafter  %s", before, got)
	}
	// The admin reaches each input's own error, or saves it, so only the role refused above.
	for _, in := range inputs {
		_, err := f.store.SaveMapping(f.admin, in.entity, in.system, in.fields)
		if in.downFields == nil {
			if err != nil {
				t.Errorf("control: admin SaveMapping, %s = %v, want nil", in.name, err)
			}
		} else {
			wantErr(t, "control: admin SaveMapping, "+in.name, err, in.downFields)
		}
		_, err = f.store.SaveLookups(f.admin, in.entity, in.system, "co-1", in.lookups)
		if in.downLookups == nil {
			if err != nil {
				t.Errorf("control: admin SaveLookups, %s = %v, want nil", in.name, err)
			}
		} else {
			wantErr(t, "control: admin SaveLookups, "+in.name, err, in.downLookups)
		}
	}
}

func TestStore_AdminSaveOutsideItsReachIsNotFound(t *testing.T) {
	f := newMapFx(t)
	other := seedTenant(t, f.super, "firm")
	foreign := seedClient(t, f.super, other, "Foreign Client")
	count := func() int {
		return f.rowCount(t) + countOf(t, f.super, "SELECT count(*) FROM erp_field_mappings WHERE tenant_id = $1", other)
	}

	for _, c := range []struct {
		name   string
		ctx    context.Context
		entity string
	}{
		{"admin of another tenant", f.admin, foreign},
	} {
		_, err := f.store.SaveMapping(c.ctx, c.entity, "fake", validFields())
		wantErr(t, c.name+" SaveMapping", err, ErrNoSuchRecord)
		_, err = f.store.SaveLookups(c.ctx, c.entity, "fake", "co-1", validLookups())
		wantErr(t, c.name+" SaveLookups", err, ErrNoSuchRecord)
	}
	if n := count(); n != 0 {
		t.Errorf("%d rows after refused saves, want 0", n)
	}
	if n := f.changed(t) + eventCount(t, f.super, other, "erp.mapping.changed"); n != 0 {
		t.Errorf("%d erp.mapping.changed rows after refused saves, want 0", n)
	}
	// The same bodies save for the admin inside its own tenant.
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields()); err != nil {
		t.Errorf("control: the admin's SaveMapping on X = %v, want nil", err)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", validLookups()); err != nil {
		t.Errorf("control: the admin's SaveLookups on X = %v, want nil", err)
	}
}

// A firm admin's scope is every client, so only a non-admin member can be out of reach.
func TestStore_LimitedMemberSaveOutsideItsScopeIsRefusedAndReadIsNotFound(t *testing.T) {
	f := newMapFx(t)
	before := f.stored(t)
	_, err := f.store.SaveMapping(f.limited, f.y, "fake", validFields())
	wantErr(t, "limited SaveMapping on Y", err, ErrNotPermitted)
	_, err = f.store.SaveLookups(f.limited, f.y, "fake", "co-1", validLookups())
	wantErr(t, "limited SaveLookups on Y", err, ErrNotPermitted)
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed after refused saves:\nbefore %s\nafter  %s", before, got)
	}
	_, err = f.store.Mapping(f.limited, f.y, "fake")
	wantErr(t, "limited Mapping of Y", err, ErrNoSuchRecord)
	_, err = f.store.Lookups(f.limited, f.y, "fake", "co-1")
	wantErr(t, "limited Lookups of Y", err, ErrNoSuchRecord)
}

func TestReadOverrides_FiltersByClientAndSystem(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_description", "lines[].item_id")
	f.override(t, f.x, "second", "line_unit_price", "lines[].price")
	f.override(t, f.y, "fake", "line_quantity", "lines[].qty")
	ctx := context.Background()
	tx, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, c := range []struct {
		entity, system string
		want           Mapping
	}{
		{f.x, "fake", Mapping{"line_description": "lines[].item_id"}},
		{f.x, "second", Mapping{"line_unit_price": "lines[].price"}},
		{f.y, "fake", Mapping{"line_quantity": "lines[].qty"}},
		{f.y, "second", Mapping{}},
	} {
		got, err := readOverrides(ctx, tx, c.entity, c.system)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("readOverrides(%s, %s) = %v, %v, want %v", c.entity, c.system, got, err, c.want)
		}
	}
}

func TestStore_InactiveCallerSaveRefusedBySeam(t *testing.T) {
	f := newMapFx(t)
	before := f.stored(t)
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{
		{"suspended admin", seedMember(t, f.super, f.tenant, "admin", "suspended", true)},
		{"invited admin", seedMember(t, f.super, f.tenant, "admin", "invited", true)},
		{"no membership", auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: f.tenant})},
	} {
		_, err := f.store.SaveMapping(c.ctx, f.x, "fake", validFields())
		wantErr(t, c.name+" SaveMapping", err, db.ErrNotActiveMember)
		_, err = f.store.SaveLookups(c.ctx, f.x, "fake", "co-1", validLookups())
		wantErr(t, c.name+" SaveLookups", err, db.ErrNotActiveMember)
	}
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields()); err != nil {
		t.Errorf("control: an active admin's SaveMapping = %v, want nil", err)
	}
}

func TestStore_LockedWorkspaceRefusesSavesAndServesReads(t *testing.T) {
	f := newMapFx(t)
	f.override(t, f.x, "fake", "line_unit_price", "lines[].price")
	f.lookup(t, f.x, "fake", "co-1", Lookup{Kind: "customer", ERPKey: "c1", TIN: strp(tinOne)})
	lockWorkspace(t, f.super, f.tenant, subjectOf(f.admin))
	before := f.stored(t)

	_, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields())
	wantErr(t, "SaveMapping", err, db.ErrWorkspaceLocked)
	_, err = f.store.SaveLookups(f.admin, f.x, "fake", "co-1", validLookups())
	wantErr(t, "SaveLookups", err, db.ErrWorkspaceLocked)
	if got := f.stored(t); got != before {
		t.Errorf("rows or audit changed in a locked workspace:\nbefore %s\nafter  %s", before, got)
	}
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{{"admin", f.admin}, {"preparer", f.preparer}} {
		m, err := f.store.Mapping(c.ctx, f.x, "fake")
		if err != nil || !reflect.DeepEqual(clientTargets(m), []string{"line_unit_price"}) {
			t.Errorf("%s Mapping in a locked workspace = client targets %v, %v, want line_unit_price", c.name, clientTargets(m), err)
		}
		l, err := f.store.Lookups(c.ctx, f.x, "fake", "co-1")
		if err != nil || len(l.Lookups) != 1 {
			t.Errorf("%s Lookups in a locked workspace = %d entries, %v, want 1", c.name, len(l.Lookups), err)
		}
	}
}

// --- AC-6: audit ----------------------------------------------------------------------------

func TestStore_EachSaveRecordsOneMappingChanged(t *testing.T) {
	f := newMapFx(t)
	seedNRSCodes(t, f.super)
	secrets := []string{"secretpath", "cust-secret-77", tinOne, "NG-AB"}
	// Several rows in each body: one event per save, not per row.
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", []FieldChange{
		chg("line_description", "lines[].secretpath"), chg("line_unit_price", "lines[].secretpath"), chgNull("line_quantity"),
	}); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{
		{Kind: "customer", ERPKey: "cust-secret-77", TIN: strp(tinOne), State: strp("NG-AB")},
		{Kind: "customer", ERPKey: "c2", TIN: strp(tinOne)},
		{Kind: "item", ERPKey: "i1", HSNCode: strp("0101.21")},
	}); err != nil {
		t.Fatalf("SaveLookups: %v", err)
	}

	rows, err := f.super.Query(context.Background(),
		"SELECT actor, payload::text, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = 'erp.mapping.changed' ORDER BY created_at, id", f.tenant)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var actor, payload string
		var entity *string
		if err := rows.Scan(&actor, &payload, &entity); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if actor != subjectOf(f.admin) {
			t.Errorf("actor = %q, want the admin's subject", actor)
		}
		if entity != nil {
			t.Errorf("entity_id = %s, want NULL (workspace-level)", *entity)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !reflect.DeepEqual(keys, []string{"part", "system"}) || p["system"] != "fake" {
			t.Errorf("payload = %s, want exactly system=fake and part", payload)
		}
		for _, s := range secrets {
			if strings.Contains(payload, s) {
				t.Errorf("payload %s carries %q", payload, s)
			}
		}
		parts = append(parts, fmt.Sprint(p["part"]))
	}
	if !reflect.DeepEqual(parts, []string{"fields", "lookups"}) {
		t.Errorf("erp.mapping.changed parts = %v, want [fields lookups]: one per save", parts)
	}
}

func TestStore_RefusedSaveRecordsNothing(t *testing.T) {
	f := newMapFx(t)
	_, err := f.store.SaveMapping(f.admin, f.x, "fake", []FieldChange{chg("issue_date", "lines[].d")})
	wantErr(t, "invalid mapping", err, ErrInvalidMapping)
	_, err = f.store.SaveLookups(f.admin, f.x, "fake", "co-1", []Lookup{{Kind: "vendor", ERPKey: "v1", TIN: strp(tinOne)}})
	wantErr(t, "invalid lookup", err, ErrInvalidLookup)
	_, err = f.store.SaveMapping(f.preparer, f.x, "fake", validFields())
	wantErr(t, "non-admin mapping", err, ErrNotPermitted)
	_, err = f.store.SaveLookups(f.preparer, f.x, "fake", "co-1", validLookups())
	wantErr(t, "non-admin lookups", err, ErrNotPermitted)
	if n := f.changed(t); n != 0 {
		t.Fatalf("%d erp.mapping.changed rows after refused saves, want 0", n)
	}
	// The same admin's valid saves record, so the zero above is the refusals'.
	if _, err := f.store.SaveMapping(f.admin, f.x, "fake", validFields()); err != nil {
		t.Fatalf("control SaveMapping: %v", err)
	}
	if _, err := f.store.SaveLookups(f.admin, f.x, "fake", "co-1", validLookups()); err != nil {
		t.Fatalf("control SaveLookups: %v", err)
	}
	if n := f.changed(t); n != 2 {
		t.Errorf("%d erp.mapping.changed rows after two valid saves, want 2", n)
	}
}
