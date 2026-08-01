// Canonical forbidden-claim list, shared by envCopy.test.ts and
// topology/environment-posture.spec.ts — both live in this package, so importing here
// is a same-package import, not a cross-package dependency. `frontend/app/src/envPosture.test.ts`
// cannot import this (app has no dependency on e2e, and adding one purely to share an
// array would invert the package graph); envCopy.test.ts cross-checks that copy's
// contents against this list instead. `·` below is U+00B7, matching the live copy.
export const FORBIDDEN_STRINGS = [
  'legally-valid',
  'legally valid',
  'clearance evidence',
  'sent to NRS',
  'transmits to NRS',
  'transmitted to NRS',
  'acknowledged by NRS',
  'PRODUCTION · NRS',
  'NRS-accepted',
  'IRN + CSID returned',
  'NRS test adapter',
  // The leading `the ` is load-bearing: it separates a per-action write claim from
  // landing's immutable-audit-log product bullets, which are in scope but legitimate.
  'the immutable audit log',
  // Same class as `acknowledged by NRS`/`NRS-accepted`, different word order — which is
  // how `accepted by NRS/MBS` shipped green. Both spellings; the copy treats them as one.
  'accepted by NRS',
  'accepted by MBS',
  // A completed audit write, tag form and prose form. No audit_log row exists until
  // accreditation (support TopBar); the honest register is `attributed`, not `recorded`.
  'AUDIT LOGGED',
  'recorded against your',
  // audit_log has no digest column, and the evidence fixtures do not chain (charts.test.ts).
  'hash-chained',
  'cryptographic proof',
  // Leading `the ` again — separates the assertion from a conditional/capability sentence.
  'the tax authority rejected',
] as const
