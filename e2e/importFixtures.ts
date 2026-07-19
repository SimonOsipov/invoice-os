// M4-08-07 (task-176): a THIRD, independent in-memory CSV generator for
// e2e/topology/import-wizard.spec.ts's UI-driven wizard specs. NOT shared with
// e2e/api/import.spec.ts's or e2e/api/perf.spec.ts's own buildPerfCsv() -- the
// task's Implementation Plan §2 rejects extraction on the evidence that the repo
// already carries two independent buildPerfCsv definitions with different bodies, by
// design (one is the plain known-clean 500/1500 shape, the other is a 450-clean/
// 50-violating shape) -- so a third, UI-purposed copy is the established convention
// here, not a defect.
//
// Package root, deliberately outside every Playwright testDir: playwright.topology
// .config.ts's testMatch '**/*.spec.ts' under testDir './topology' never sees this
// file, so it registers no tests of its own -- mirroring targets.ts/rule-set.ts's own
// placement. NEVER import this from a *.spec.ts file into another spec's module graph
// via a re-export chain that could register its tests twice; it has none, but the
// discipline is the same reason NEITHER of the two existing buildPerfCsv definitions
// is imported here.
import type { Page } from '@playwright/test'

export const PERF_HEADER = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'

// PERF_MAPPING: the canonical importer field -> this fixture's header column, the
// same 11-key contract e2e/api/import.spec.ts and perf.spec.ts's own PERF_MAPPINGs
// use (internal/importer/service.go's canonicalFields). The UI test does NOT submit
// this object on the wire -- CreateMapping.tsx maps fields by click-to-place, and
// initMappingFromHeaders auto-recognizes every key below except invoice_number and
// subtotal (lib/mapping.ts's ALIAS table has no entry for either) -- so the spec
// click-maps exactly those two by hand. Exported here as the documented column
// contract this fixture satisfies, mirrored for readability against its API-level
// siblings, not because the spec consumes it directly.
export const PERF_MAPPING: Record<string, string> = {
  invoice_number: 'Invoice No',
  issue_date: 'Issue Date',
  buyer_tin: 'Buyer TIN',
  buyer_name: 'Buyer',
  currency: 'Currency',
  subtotal: 'Subtotal',
  vat: 'VAT',
  total: 'Total',
  line_description: 'Item',
  line_quantity: 'Qty',
  line_unit_price: 'Unit Price',
}

// buildPerfCsv(): a 500-invoice / 1,500-row CSV for ONE entity -- 3 clean line rows
// per invoice, header fields (issue_date/buyer_tin/buyer/currency/subtotal/vat/total)
// identical within each invoice's group, ISO dates, distinct invoice numbers
// INV-UI-00001..00500. Shape mirrors import.spec.ts's own buildPerfCsv byte-for-byte
// on values (own copy per this file's header comment), with a UI-distinct
// "INV-UI-" prefix purely so a run's numbers are attributable to this file if ever
// inspected in a report -- each test creates its own fresh entity, so no collision
// with the API suite's fixture is possible either way.
export function buildPerfCsv(): string {
  const lines: string[] = [PERF_HEADER]
  for (let i = 1; i <= 500; i++) {
    const invNo = `INV-UI-${String(i).padStart(5, '0')}`
    for (let line = 1; line <= 3; line++) {
      lines.push(
        [invNo, '2026-01-15', '87654321-0002', 'M4-08 UI Buyer Co', 'NGN', '1000.00', '75.00', '1075.00', `Item ${line}`, '1', '100.00'].join(
          ',',
        ),
      )
    }
  }
  return lines.join('\n')
}

// buildMixedCsv(): a small fixture firing BOTH report channels in one import.
//
//  - STRUCTURAL quarantine: two rows sharing invoice number INV-UI-MIX-STRUCT that
//    disagree on Issue Date. headerConflictField (internal/importer/service.go)
//    checks "issue_date" FIRST in headerFieldOrder, so this alone quarantines the
//    whole group with RowError{Message: "rows disagree on issue_date"} -- verified
//    directly against service.go's headerFieldOrder/headerConflictField, not guessed.
//
//  - RULE violation: INV-UI-MIX-VIOLATE carries VAT 0.00 against Subtotal 1000.00.
//    vat-standard-rate (the ONLY designed-violating rule; perf.spec.ts's header
//    comment) is a tax_math rule with params {"base":"subtotal","rate":0.075,
//    "expected":"vat","tolerance":0.005} (migrations/20260711121327_seed_mbs_v1.sql).
//    |0 - 1000*0.075| = 75 > 0.005 -> violation. Qty 10 x Unit Price 100.00 = 1000.00
//    reconciles EXACTLY to Subtotal, so line-items-sum-subtotal (v2,
//    migrations/20260716185106_rule_set_v2.sql) does not also fire -- verified
//    against every active v2 rule so this invoice violates vat-standard-rate ALONE.
//
//  - CLEAN: INV-UI-MIX-CLEAN carries VAT 75.00 == Subtotal 1000.00 * 7.5% exactly,
//    same Qty/Unit Price reconciliation -- zero violations against any active v2
//    rule, proving the violations list is a SUBSET of the report, not the whole
//    thing (E2E-04).
//
// IMPORTANT, not obvious from this file alone: `subtotal` has no entry in
// lib/mapping.ts's ALIAS table, so CreateMapping's auto-recognize NEVER maps it --
// the spec MUST click-map it by hand (alongside invoice_number) for this fixture, or
// every invoice (including the "clean" one) reads subtotal as ABSENT, and tax_math's
// data-fault rule ("a base/expected path that is absent... => violation" --
// internal/validation/evaluators_math.go) fires vat-standard-rate on ALL of them,
// destroying the clean/violating split this fixture exists to prove. Money values
// avoid the F5 leading-zero trap (a bare "0.00" is fine; nothing else has a leading
// zero), mirroring perf.spec.ts's own fixture hygiene note.
export function buildMixedCsv(): string {
  const lines: string[] = [PERF_HEADER]
  lines.push(
    ['INV-UI-MIX-STRUCT', '2026-01-15', '87654321-0002', 'M4-08 UI Mixed Co', 'NGN', '1000.00', '75.00', '1075.00', 'Item 1', '1', '100.00'].join(
      ',',
    ),
  )
  lines.push(
    ['INV-UI-MIX-STRUCT', '2026-01-16', '87654321-0002', 'M4-08 UI Mixed Co', 'NGN', '1000.00', '75.00', '1075.00', 'Item 2', '1', '100.00'].join(
      ',',
    ),
  )
  lines.push(
    ['INV-UI-MIX-VIOLATE', '2026-01-15', '87654321-0002', 'M4-08 UI Mixed Co', 'NGN', '1000.00', '0.00', '1000.00', 'Perf line', '10', '100.00'].join(
      ',',
    ),
  )
  lines.push(
    ['INV-UI-MIX-CLEAN', '2026-01-15', '87654321-0002', 'M4-08 UI Mixed Co', 'NGN', '1000.00', '75.00', '1075.00', 'Perf line', '10', '100.00'].join(
      ',',
    ),
  )
  return lines.join('\n')
}

// statValue(): locates a CreateReport.tsx `Stat` tile's rendered value by its exact
// label text (`<div class="label">{label}</div>` immediately followed by
// `<div class="mono">{value}</div>` -- CreateReport.tsx's Stat component renders
// exactly this two-child shape, nothing between them). The xpath sibling step is what
// makes this precise: there is no other distinguishing class/role on either div, and
// a same-page ancestor-filter approach would need to disambiguate nesting depth,
// where "the label's very next sibling" cannot mismatch. Shared by every topology
// spec that reads a report tile, so it lives here rather than being copy-pasted per
// call site.
export function statValue(page: Page, label: string) {
  return page.getByText(label, { exact: true }).locator('xpath=following-sibling::div[1]')
}
