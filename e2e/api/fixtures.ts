// BAD_INVOICE_KEYS: the sorted verdict set a malformed supplier TIN plus a wrong VAT
// amount fires against the seeded rule set (verified against the committed golden
// internal/validation/testdata/golden/demo_bad_invoice.json). It has no code consumer --
// it survives as the canonical NAME for that verdict set, which
// topology/invoice-surfaces.spec.ts cites in prose at :21, :156 and :460.
export const BAD_INVOICE_KEYS = ['supplier-tin-format', 'vat-standard-rate']

// freshTin(): an NNNNNNNN-NNNN TIN with a correct Luhn check digit, generated
// fresh per call so nothing already in the database collides with it on
// business_entities' duplicate-TIN partial index (there is no DELETE endpoint —
// only offboard/onboard = archive/active). A run now starts from the curated
// seed rather than from prior runs' residue (docs/e2e-convention.md "One
// browser, serial"), but the three suites share one deployment with no reset
// between them and a retry re-runs against what its first attempt left, so
// "fresh" means fresh WITHIN the run — which is the collision that was ever
// reachable from inside a spec anyway. Replicates
// internal/portfolio/tin.go's luhnValid exactly: from the rightmost digit,
// double every second digit (subtracting 9 if >9), sum all digits; valid iff
// the sum is a multiple of 10.
//
// UNIQUENESS IS PROBABILISTIC, NOT GUARANTEED (finding F-G). It comes from a
// per-process run seed combined with a module-level call counter — not
// Date.now()/Math.random(), per the story's guidance for test code. The seed is
// `process.pid % 10000` (see tinRunSeed below), so there are only 10,000 of
// them and PIDs recycle: two suite PROCESSES whose PIDs are congruent mod 10000
// emit byte-identical TIN sequences — and `test:api` and `test:topology` are two
// such processes inside every single run, so this is not only a cross-run
// concern. That is nonetheless adequate, because a
// collision cannot produce a FALSE PASS — it fails loud. Postgres raises 23505 on
// `business_entities_tenant_tin_uq` (UNIQUE (tenant_id, tin) WHERE tin IS NOT
// NULL, migrations/20260709155011_business_entities.sql:55), which
// internal/portfolio/store.go:58-59 maps to ErrDuplicateTIN and the API returns
// as an error the calling spec asserts on. The index is also per-TENANT, so
// only a colliding run against the same tenant can trip it at all.
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

// freshRoleTitle(): a per-run-unique workflow-role title, on freshTin's seed + counter
// above — never Date.now()/Math.random(). Unlike a TIN, a colliding title cannot fail
// loud: duplicate titles are legal and the server just suffixes the minted key to -2. So
// this buys identifiability for cleanup, not constraint safety — which is why no
// assertion may depend on a key value.
export function freshRoleTitle(): string {
  tinCounter += 1
  return `Probe Role ${tinRunSeed}-${tinCounter}`
}

// freshPolicyName(): a per-run-unique approval-policy name, on the same seed + counter as
// freshRoleTitle above. Like a role title and unlike a TIN, a colliding name cannot fail
// loud — nothing constrains approval_policies.name — so this buys identifiability in a
// never-reset environment, not constraint safety.
export function freshPolicyName(): string {
  tinCounter += 1
  return `Probe Policy ${tinRunSeed}-${tinCounter}`
}

// canonicalTin(): the portfolio service canonicalizes an accepted TIN to its
// digits-only form on write and echoes THAT form back (internal/portfolio/tin.go's
// ValidateTIN strips the hyphen before persisting/returning) — so any assertion
// comparing an echoed `.tin` to a hyphenated freshTin() input must compare against
// this canonical form, not the raw input.
export function canonicalTin(tin: string): string {
  return tin.replace(/-/g, '')
}
