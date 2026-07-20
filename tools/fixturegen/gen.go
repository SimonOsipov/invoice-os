// gen.go declares the builder API for the synthetic invoice-import fixture
// generator (M4-11-01, task-194): deterministic CSV generation for the
// green (canonical, all-valid) shape and six edge-case mutations, plus an
// in-memory oversized-body inflator.
//
// STUB FILE. Every builder below is `panic("not implemented")` -- this is
// Mode A (QA Test-Spec), written BEFORE the implementation exists, per
// task-194's Test-first: yes. The executor (Stage 3) fills in each body
// against the frozen signatures here; gen_test.go's 16 tests must not
// change shape when that happens. A panicking stub (not a nil/zero-value
// stub) is deliberate: it forces every test below RED for a "not
// implemented" reason, not a silent vacuous pass (e.g. bytes.Equal(nil,
// nil) == true would let TestGen_SameSeedByteIdentical pass against a
// stub that returns nil).
//
// Canonical header, money math (subtotal/vat/total), and buyer-TIN shape
// mirror internal/importer/service_test.go's stdHeader/stdMapping and
// e2e/importFixtures.ts's PERF_HEADER/PERF_MAPPING byte-for-byte, and the
// seeded vat-standard-rate rule (migrations/20260711121327_seed_mbs_v1.sql:29,
// rate=0.075, tolerance=0.005).
//
// NON-OBVIOUS CONSTRAINT ON buildEdgeBadEncoding: gen_test.go's
// TestGen_EdgeBadEncoding_IsUTF16LEWithoutBOM asserts the UTF-16LE bytes
// are NOT utf8.Valid (task-194's own oracle). A pure-ASCII green base
// re-encoded as UTF-16LE (each char -> low byte, 0x00 high byte) IS still
// valid UTF-8 -- 0x00 and 0x20-0x7E are each valid single-byte UTF-8
// sequences on their own -- so that assertion only holds if the green
// content that gets encoded contains at least one non-ASCII byte (e.g. a
// diacritic in a buyer name; plausible for Nigerian buyer names). Verified
// empirically while authoring this file's tests; the executor should pick
// buyer names (or any field) with non-ASCII content so this edge case
// stays genuinely broken-as-UTF-8, not just differently-encoded.
package main

// canonicalHeader is the byte-for-byte CSV header line (no trailing
// newline). Matches internal/importer/service_test.go's stdHeader and
// e2e/importFixtures.ts's PERF_HEADER exactly, column for column.
const canonicalHeader = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price"

// generateGreen builds the canonical green CSV: `invoices` invoices, 3 line
// rows each, deterministic for a given seed. Header + data rows,
// '\n'-terminated, no trailing timestamp.
func generateGreen(seed int64, invoices int) []byte { panic("not implemented") }

// buildEdgeMissingColumns derives from a green base then drops the Total
// column (header cell and every data-row cell).
func buildEdgeMissingColumns(seed int64, invoices int) []byte { panic("not implemented") }

// buildEdgeInFileDupes derives from a green base then mutates it so that
// at least one invoice has two rows sharing the same Invoice No but
// differing on exactly Issue Date (every other header field identical).
func buildEdgeInFileDupes(seed int64) []byte { panic("not implemented") }

// buildEdgeBadEncoding derives from a green base then re-encodes it as
// UTF-16LE WITHOUT a byte-order-mark. See the file doc comment above for
// the non-ASCII-content constraint this builder must satisfy.
func buildEdgeBadEncoding(seed int64, invoices int) []byte { panic("not implemented") }

// buildEdgeBadTin derives from a green base then mutates exactly one
// invoice's Buyer TIN so it fails ^[0-9]{8}-[0-9]{4}$; every other
// invoice's Buyer TIN stays well-formed.
func buildEdgeBadTin(seed int64, invoices int) []byte { panic("not implemented") }

// buildEdgeVatMathWrong derives from a green base then mutates exactly one
// invoice to VAT=0.00 with subtotal>=1.00 (its lines still reconcile to
// that subtotal); every other invoice keeps normal reconciled VAT.
func buildEdgeVatMathWrong(seed int64, invoices int) []byte { panic("not implemented") }

// buildOversized returns an in-memory CSV body exceeding the importer's
// 10<<20-byte upload cap (internal/importer/handlers.go's maxUploadBytes).
func buildOversized() []byte { panic("not implemented") }
