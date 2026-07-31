// supplier_tin.go: the ENTITY -> INVOICE supplier TIN wire-spelling
// restoration (INVCR-01-17, C7 fix), SHARED by this package's own
// Store.Create (the manual POST /v1/invoices path, below) and
// internal/importer's batch-import path (buildCreateInput / EntitySupplier)
// -- ONE owner, not a copy in each package, so the two entity->invoice
// boundaries cannot independently drift on what "restore the hyphen" means.
//
// Lives HERE, not in internal/importer (where it originated, as an
// unexported mbsSupplierTIN, before this fix): internal/importer imports
// internal/invoice, never the reverse, so this package is the only legal
// shared home for logic both need in PRODUCTION. internal/importer now calls
// MBSSupplierTIN directly instead of keeping its own copy.
package invoice

import "regexp"

// canonicalFIRSTIN matches the 12-digit canonical form portfolio's
// ValidateTIN (internal/portfolio/tin.go) produces for a FIRS TIN -- the
// hyphen-stripped spelling of NNNNNNNN-NNNN. A 10-digit JTB TIN is
// deliberately NOT matched (see MBSSupplierTIN).
var canonicalFIRSTIN = regexp.MustCompile(`^\d{12}$`)

// MBSSupplierTIN restores the MBS wire spelling of an ENTITY's TIN as it
// crosses the entity -> invoice boundary ([supplier-from-entity]).
//
// WHY THIS EXISTS: portfolio.ValidateTIN accepts a FIRS TIN in either
// spelling (bare 12-digit or hyphenated NNNNNNNN-NNNN) and CANONICALIZES it
// to 12 bare digits before persisting (tin.go:39, strings.Replace(trimmed,
// "-", "", 1)) -- its own doc: "both spellings of a FIRS TIN persist
// identically". The MBS rule supplier-tin-format
// (migrations/20260711121327_seed_mbs_v1.sql) demands ^[0-9]{8}-[0-9]{4}$.
// So an entity created through the REAL API path carries a TIN the wire rule
// rejects, and every invoice (imported OR manually created) for it reported a
// FALSE supplier-tin-format violation -- a firm's valid invoices rejected.
//
// WHY HERE, and not in MBSPayload: an ENTITY tin is KNOWN-VALID (it passed
// ValidateTIN + Luhn on the way in, and WE stripped the hyphen), so restoring
// the spelling we removed is a faithful MAPPING, not a repair. A
// user-supplied invoice TIN (POST /v1/invoices's buyer_tin, or a
// caller-supplied supplier_tin -- which Store.Create now overrides rather
// than trusts, see its own doc comment) has UNKNOWN validity, and
// re-hyphenating it in a pure wire mapper would be FIXING USER DATA --
// breaking store-invalid-faithfully (migrations/20260714103137_invoices.sql's
// header), under which a malformed TIN MUST still violate. Restore the format
// only where WE stripped it -- an entity's own tin, never buyer_tin.
//
// SHAPES -- the exact inverse of tin.go's canonicalization, which is the only
// thing that writes business_entities.tin:
//   - 12 bare digits -> NNNNNNNN-NNNN. Both FIRS spellings canonicalize to the
//     same 12 digits and ARE the same TIN (tin.go's own doc), so both map onto
//     the single MBS spelling.
//   - 10-digit JTB TIN -> UNCHANGED. There is no hyphen to restore, and an 8+4
//     split would fabricate a FIRS TIN out of a JTB one. Such a TIN genuinely
//     cannot satisfy supplier-tin-format: that is a REAL violation to report
//     (flagged by M4-04-08), not a formatting bug to paper over here.
//   - anything else -- an already-hyphenated row we never canonicalized
//     (db/seed.dev.sql's curated literals, raw-seeded fixtures) -> UNCHANGED.
//
// nil (an entity with no TIN) stays nil: supplier-tin-required fires, which is
// the correct pre-existing signal (IMPV-12) -- and Store.Create/buildCreateInput
// both apply this even when the CALLER supplied a value of their own: the
// entity's own (lack of a) tin always wins.
func MBSSupplierTIN(tin *string) *string {
	if tin == nil || !canonicalFIRSTIN.MatchString(*tin) {
		return tin
	}
	wire := (*tin)[:8] + "-" + (*tin)[8:]
	return &wire
}
