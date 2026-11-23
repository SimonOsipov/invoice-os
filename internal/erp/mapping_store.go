package erp

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ErrInvalidLookup is a lookup body the save refuses; its message names the entry's kind, field and list, never the value.
var ErrInvalidLookup = errors.New("erp: invalid lookup")

// lookupError matches ErrInvalidLookup and reads like "customer state: not on the NRS states list".
type lookupError struct{ msg string }

func (e *lookupError) Error() string        { return e.msg }
func (e *lookupError) Is(target error) bool { return target == ErrInvalidLookup }

func invalidLookup(msg string) error { return &lookupError{msg} }

type FieldView struct {
	Target      string  `json:"target"`
	Label       string  `json:"label"`
	Line        bool    `json:"line"`
	SourcePath  *string `json:"source_path"`
	DefaultPath *string `json:"default_path"`
	Origin      string  `json:"origin"` // client, default or none
}

type MappingView struct {
	System string      `json:"system"`
	Fields []FieldView `json:"fields"`
}

// FieldChange names one target; a nil SourcePath reverts it to the connector default.
type FieldChange struct {
	Target     string  `json:"target"`
	SourcePath *string `json:"source_path"`
}

type LookupsView struct {
	System      string   `json:"system"`
	ExternalKey string   `json:"external_key"`
	Lookups     []Lookup `json:"lookups"`
}

const maxLookupEntries = 500

// Mapping has no role check: any active member who sees the client reads it.
func (s *Store) Mapping(ctx context.Context, entityID, system string) (MappingView, error) {
	var view MappingView
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		c, ok := s.reg[system]
		if !ok {
			return ErrUnknownSystem
		}
		over, err := readOverrides(ctx, tx, entityID, system)
		if err != nil {
			return err
		}
		view = mappingView(system, c.DefaultMapping(), over)
		return nil
	})
	if err != nil {
		return MappingView{}, err
	}
	return view, nil
}

func mappingView(system string, def, over Mapping) MappingView {
	v := MappingView{System: system, Fields: []FieldView{}}
	for _, t := range Targets() {
		f := FieldView{Target: t.Key, Label: t.Label, Line: t.Line, Origin: "none"}
		if p, ok := def[t.Key]; ok {
			f.DefaultPath, f.SourcePath, f.Origin = ptr(p), ptr(p), "default"
		}
		if p, ok := over[t.Key]; ok {
			f.SourcePath, f.Origin = ptr(p), "client"
		}
		v.Fields = append(v.Fields, f)
	}
	return v
}

// SaveMapping upserts each named override, or deletes it for a nil path, when the effective mapping stays valid.
func (s *Store) SaveMapping(ctx context.Context, entityID, system string, changes []FieldChange) (MappingView, error) {
	var view MappingView
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		c, ok := s.reg[system]
		if !ok {
			return ErrUnknownSystem
		}
		id, _ := uuid.Parse(entityID) // requireClient parsed it; the lock key needs the one canonical spelling
		entityID = id.String()
		// ValidateMapping reads across rows; the lock keeps two saves from each passing against the other's old state.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_mapping:' || $1::text || ':' || $2::text, 0))`, entityID, system); err != nil {
			return err
		}
		if n := len(Targets()); len(changes) == 0 || len(changes) > n {
			return invalidMapping("fields", "name 1 to "+strconv.Itoa(n)+" targets")
		}
		over, err := readOverrides(ctx, tx, entityID, system)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, ch := range changes {
			if _, ok := targetOf(ch.Target); !ok {
				return invalidMapping(ch.Target, reasonUnknown)
			}
			if seen[ch.Target] {
				return invalidMapping(ch.Target, "named twice")
			}
			seen[ch.Target] = true
			if ch.SourcePath == nil {
				delete(over, ch.Target)
			} else {
				over[ch.Target] = *ch.SourcePath
			}
		}
		def := c.DefaultMapping()
		if err := ValidateMapping(def.Merge(over)); err != nil {
			return err
		}
		for _, ch := range changes {
			if ch.SourcePath == nil {
				_, err = tx.Exec(ctx, `DELETE FROM erp_field_mappings WHERE entity_id = $1 AND system = $2 AND target = $3`, entityID, system, ch.Target)
			} else {
				_, err = tx.Exec(ctx,
					`INSERT INTO erp_field_mappings (tenant_id, entity_id, system, target, source_path) VALUES ($1, $2, $3, $4, $5)
					 ON CONFLICT (tenant_id, entity_id, system, target) DO UPDATE SET source_path = EXCLUDED.source_path, updated_at = now()`,
					caller.TenantID, entityID, system, ch.Target, *ch.SourcePath)
			}
			if err != nil {
				return err
			}
		}
		view = mappingView(system, def, over)
		return audit.Record(ctx, tx, caller.Subject, "erp.mapping.changed", map[string]any{"system": system, "part": "fields"})
	})
	if err != nil {
		return MappingView{}, err
	}
	return view, nil
}

// Lookups has no role check; it lists the client's entries of one system and ERP company only.
func (s *Store) Lookups(ctx context.Context, entityID, system, externalKey string) (LookupsView, error) {
	externalKey = strings.TrimSpace(externalKey)
	var view LookupsView
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		if _, ok := s.reg[system]; !ok {
			return ErrUnknownSystem
		}
		if !validCompanyKey(externalKey) {
			return invalidLookup("external_key: required")
		}
		var err error
		view, err = readLookupsView(ctx, tx, entityID, system, externalKey)
		return err
	})
	if err != nil {
		return LookupsView{}, err
	}
	return view, nil
}

func validCompanyKey(k string) bool { return k != "" && len(k) <= 256 }

func readLookupsView(ctx context.Context, tx pgx.Tx, entityID, system, externalKey string) (LookupsView, error) {
	v := LookupsView{System: system, ExternalKey: externalKey, Lookups: []Lookup{}}
	rows, err := tx.Query(ctx,
		`SELECT kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga
		 FROM erp_lookups WHERE entity_id = $1 AND system = $2 AND external_key = $3
		 ORDER BY kind COLLATE "C", erp_key COLLATE "C"`, entityID, system, externalKey)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Lookup
		if err := rows.Scan(&l.Kind, &l.ERPKey, &l.TaxCategory, &l.ExemptionCode, &l.HSNCode, &l.ServiceCode, &l.TIN, &l.State, &l.LGA); err != nil {
			return v, err
		}
		v.Lookups = append(v.Lookups, l)
	}
	return v, rows.Err()
}

// lookupField is one value of an entry: its wire name, the list that bounds it ("" for none) and its slot.
type lookupField struct {
	name, list string
	val        **string
}

func lookupFields(l *Lookup) []lookupField {
	return []lookupField{
		{"tax_category", "tax-categories", &l.TaxCategory}, {"exemption_code", "vat-exemptions", &l.ExemptionCode},
		{"hsn_code", "hs-codes", &l.HSNCode}, {"service_code", "services-codes", &l.ServiceCode},
		{"tin", "", &l.TIN}, {"state", "states", &l.State}, {"lga", "lgas", &l.LGA},
	}
}

// kindFields are the fields each kind takes, as the erp_lookups_values_check CHECK.
var kindFields = map[string][]string{
	"tax_code": {"tax_category", "exemption_code"},
	"item":     {"hsn_code", "service_code"},
	"customer": {"tin", "state", "lga"},
}

// fileableTIN matches the customer package's pattern and the erp_lookups_tin_check CHECK.
var fileableTIN = regexp.MustCompile(`^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$`)

// normalizeLookup trims the key and each value (blank is nil) and refuses an entry the table's CHECKs would.
// An entry with no value is a delete: del is true.
func normalizeLookup(l Lookup) (out Lookup, del bool, err error) {
	taken, ok := kindFields[l.Kind]
	if !ok {
		return l, false, invalidLookup("kind: must be tax_code, item or customer")
	}
	l.ERPKey = strings.TrimSpace(l.ERPKey)
	if l.ERPKey == "" || len(l.ERPKey) > 256 {
		return l, false, invalidLookup(l.Kind + " erp_key: required")
	}
	set := 0
	for _, f := range lookupFields(&l) {
		if *f.val != nil {
			t := strings.TrimSpace(**f.val)
			if t == "" {
				*f.val = nil
			} else {
				*f.val = &t
			}
		}
		if *f.val == nil {
			continue
		}
		set++
		if !contains(taken, f.name) {
			return l, false, invalidLookup(l.Kind + " " + f.name + ": not used by this kind")
		}
	}
	switch {
	case set == 0:
		return l, true, nil
	case l.Kind == "tax_code" && l.TaxCategory == nil:
		return l, false, invalidLookup("tax_code tax_category: required")
	case l.Kind == "item" && set != 1:
		return l, false, invalidLookup("item: exactly one of hsn_code or service_code")
	case l.TIN != nil && !fileableTIN.MatchString(*l.TIN):
		return l, false, invalidLookup("customer tin: not a fileable TIN")
	}
	return l, false, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// checkCodes refuses the first value that is not on its NRS list, one query per list.
func checkCodes(ctx context.Context, tx pgx.Tx, entries []Lookup) error {
	unknown := map[string]map[string]bool{} // list -> codes asked, then those not found
	for i := range entries {
		for _, f := range lookupFields(&entries[i]) {
			if f.list == "" || *f.val == nil {
				continue
			}
			if unknown[f.list] == nil {
				unknown[f.list] = map[string]bool{}
			}
			unknown[f.list][**f.val] = true
		}
	}
	for list, byCode := range unknown {
		asked := make([]string, 0, len(byCode))
		for c := range byCode {
			asked = append(asked, c)
		}
		rows, err := tx.Query(ctx, `SELECT code FROM nrs_codes WHERE list = $1 AND code = ANY($2::text[])`, list, asked)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			delete(byCode, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for i := range entries {
		for _, f := range lookupFields(&entries[i]) {
			if f.list != "" && *f.val != nil && unknown[f.list][**f.val] {
				return invalidLookup(entries[i].Kind + " " + f.name + ": not on the NRS " + f.list + " list")
			}
		}
	}
	return nil
}

// SaveLookups replaces each named entry's values under one ERP company and deletes an entry with none.
func (s *Store) SaveLookups(ctx context.Context, entityID, system, externalKey string, entries []Lookup) (LookupsView, error) {
	var view LookupsView
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		if _, ok := s.reg[system]; !ok {
			return ErrUnknownSystem
		}
		externalKey = strings.TrimSpace(externalKey)
		if !validCompanyKey(externalKey) {
			return invalidLookup("external_key: required")
		}
		if len(entries) == 0 || len(entries) > maxLookupEntries {
			return invalidLookup("lookups: 1 to " + strconv.Itoa(maxLookupEntries) + " entries")
		}
		norm := make([]Lookup, len(entries))
		dels := make([]bool, len(entries))
		seen := map[[2]string]bool{}
		for i, e := range entries {
			var err error
			if norm[i], dels[i], err = normalizeLookup(e); err != nil {
				return err
			}
			k := [2]string{norm[i].Kind, norm[i].ERPKey}
			if seen[k] {
				return invalidLookup(norm[i].Kind + " erp_key: named twice")
			}
			seen[k] = true
		}
		if err := checkCodes(ctx, tx, norm); err != nil {
			return err
		}
		for i, l := range norm {
			var err error
			if dels[i] {
				_, err = tx.Exec(ctx,
					`DELETE FROM erp_lookups WHERE entity_id = $1 AND system = $2 AND external_key = $3 AND kind = $4 AND erp_key = $5`,
					entityID, system, externalKey, l.Kind, l.ERPKey)
			} else {
				_, err = tx.Exec(ctx,
					`INSERT INTO erp_lookups (tenant_id, entity_id, system, external_key, kind, erp_key,
					        tax_category, exemption_code, hsn_code, service_code, tin, state, lga)
					 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
					 ON CONFLICT (tenant_id, entity_id, system, external_key, kind, erp_key) DO UPDATE SET
					        tax_category = EXCLUDED.tax_category, exemption_code = EXCLUDED.exemption_code,
					        hsn_code = EXCLUDED.hsn_code, service_code = EXCLUDED.service_code,
					        tin = EXCLUDED.tin, state = EXCLUDED.state, lga = EXCLUDED.lga, updated_at = now()`,
					caller.TenantID, entityID, system, externalKey, l.Kind, l.ERPKey,
					l.TaxCategory, l.ExemptionCode, l.HSNCode, l.ServiceCode, l.TIN, l.State, l.LGA)
			}
			if err != nil {
				return err
			}
		}
		var err error
		if view, err = readLookupsView(ctx, tx, entityID, system, externalKey); err != nil {
			return err
		}
		return audit.Record(ctx, tx, caller.Subject, "erp.mapping.changed", map[string]any{"system": system, "part": "lookups"})
	})
	if err != nil {
		return LookupsView{}, err
	}
	return view, nil
}

// readOverrides reads the client's saved overrides of one system.
func readOverrides(ctx context.Context, tx pgx.Tx, entityID, system string) (Mapping, error) {
	rows, err := tx.Query(ctx, `SELECT target, source_path FROM erp_field_mappings WHERE entity_id = $1 AND system = $2`, entityID, system)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Mapping{}
	for rows.Next() {
		var k, p string
		if err := rows.Scan(&k, &p); err != nil {
			return nil, err
		}
		out[k] = p
	}
	return out, rows.Err()
}
