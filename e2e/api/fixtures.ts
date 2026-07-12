// M3-14-01: golden invoice payloads + expected violation-key sets, mirrored
// VERBATIM from the M3-10 Go fixtures the migration-seeded v1 engine is
// pinned against (internal/validation/seed_test.go's validInvoicePayload /
// badInvoicePayload, internal/validation/collect_all_integration_test.go's
// manyViolationsPayload) — re-verified against those files and the committed
// goldens under internal/validation/testdata/golden/*.json before writing
// this. Do not hand-edit a fixture without re-diffing against the Go source:
// any drift here silently de-couples this suite from the contract it mirrors.

export interface InvoicePayload {
  invoice: Record<string, unknown>
}

// validInvoicePayload() (seed_test.go): a fully valid invoice — fires zero
// violations against the seeded v1 rule set.
export const validInvoice: InvoicePayload = {
  invoice: {
    invoice_number: 'INV-2026-000123',
    issue_date: '2026-07-11',
    currency: 'NGN',
    supplier: {
      tin: '12345678-0001',
      name: 'Acme Nigeria Ltd',
    },
    buyer: {
      tin: '87654321-0002',
      name: 'Buyer Ltd',
    },
    subtotal: 1000.0,
    vat: 75.0,
    total: 1075.0,
    line_items: [
      {
        id: '1',
        description: 'Widget',
        quantity: 10.0,
        unit_price: 100.0,
        line_total: 1000.0,
      },
    ],
  },
}

// badInvoicePayload() (seed_test.go): validInvoice with a malformed supplier
// TIN + a wrong VAT amount — fires exactly BAD_INVOICE_KEYS below (see the
// committed golden internal/validation/testdata/golden/demo_bad_invoice.json).
export const badInvoice: InvoicePayload = {
  invoice: {
    ...validInvoice.invoice,
    invoice_number: 'INV-2026-000124',
    supplier: {
      ...(validInvoice.invoice.supplier as Record<string, unknown>),
      tin: 'BADTIN',
    },
    vat: 70.0,
    total: 1070.0,
  },
}

// manyViolationsPayload() (collect_all_integration_test.go): validInvoice with
// SIX independently-broken fields (invoice_number/issue_date deleted,
// supplier.name deleted, currency + supplier.tin + subtotal broken, plus a
// duplicate line-item id) — fires exactly MANY_VIOLATION_KEYS below (see the
// committed golden internal/validation/testdata/golden/many_violations.json).
export const manyViolations: InvoicePayload = {
  invoice: (() => {
    const inv: Record<string, unknown> = { ...validInvoice.invoice }
    delete inv.invoice_number
    delete inv.issue_date
    const supplier = { ...(validInvoice.invoice.supplier as Record<string, unknown>) }
    delete supplier.name
    supplier.tin = 'BADTIN'
    inv.supplier = supplier
    inv.currency = 'USD'
    inv.subtotal = -5.0
    inv.vat = 999.0
    inv.line_items = [
      ...(validInvoice.invoice.line_items as unknown[]),
      {
        id: '1',
        description: 'Widget (dup)',
        quantity: 1.0,
        unit_price: 5.0,
        line_total: 5.0,
      },
    ]
    return inv
  })(),
}

// currencyUsdInvoice: validInvoice with only currency broken (the
// TestCollectAll_ManyViolationsBreadth "single_defect_control" shape) — used
// by the kill-switch spec (M3-14-03) to exercise currency-allowed in
// isolation, symmetrically to badInvoice's vat-standard-rate isolation.
export const currencyUsdInvoice: InvoicePayload = {
  invoice: {
    ...validInvoice.invoice,
    currency: 'USD',
  },
}

// Expected violation-key sets (sorted — Engine.Evaluate sorts its output,
// Decision N16), verified against the committed golden files.
export const BAD_INVOICE_KEYS = ['supplier-tin-format', 'vat-standard-rate']

export const MANY_VIOLATION_KEYS = [
  'currency-allowed',
  'invoice-number-required',
  'issue-date-required',
  'no-duplicate-line-items',
  'subtotal-non-negative',
  'supplier-name-required',
  'supplier-tin-format',
  'vat-standard-rate',
]

// freshTin(): a unique NNNNNNNN-NNNN TIN with a correct Luhn check digit,
// generated fresh per call so repeated runs (including against the un-reset
// live dev DB) never collide on business_entities' duplicate-TIN partial
// index (there is no DELETE endpoint — only offboard/onboard = archive/
// active). Replicates internal/portfolio/tin.go's luhnValid exactly: from the
// rightmost digit, double every second digit (subtracting 9 if >9), sum all
// digits; valid iff the sum is a multiple of 10. Uniqueness comes from a
// per-process run seed (pid-derived, stable for the life of this process)
// combined with a module-level call counter — not Date.now()/Math.random(),
// per the story's guidance for test code.
let tinCounter = 0
const tinRunSeed = String(process.pid % 10000).padStart(4, '0')

function luhnCheckDigit(digits: string): string {
  // digits: the 11 digits preceding the check digit. Mirrors tin.go's
  // luhnValid loop, but run over `digits` alone (the check digit itself is
  // excluded from doubling in the full 12-digit checksum, so the digit
  // immediately to its left is the first one doubled here).
  let sum = 0
  let double = true
  for (let i = digits.length - 1; i >= 0; i--) {
    let d = digits.charCodeAt(i) - 48
    if (double) {
      d *= 2
      if (d > 9) d -= 9
    }
    sum += d
    double = !double
  }
  return String((10 - (sum % 10)) % 10)
}

export function freshTin(): string {
  tinCounter += 1
  const sequence = String(tinCounter).padStart(7, '0')
  const digits11 = tinRunSeed + sequence // 4 + 7 = 11 digits
  const twelve = digits11 + luhnCheckDigit(digits11)
  return `${twelve.slice(0, 8)}-${twelve.slice(8)}`
}

// canonicalTin(): the portfolio service canonicalizes an accepted TIN to its
// digits-only form on write and echoes THAT form back (internal/portfolio/tin.go's
// ValidateTIN strips the hyphen before persisting/returning) — so any assertion
// comparing an echoed `.tin` to a hyphenated freshTin() input must compare against
// this canonical form, not the raw input.
export function canonicalTin(tin: string): string {
  return tin.replace(/-/g, '')
}
