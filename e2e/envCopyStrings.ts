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
  // The evidence bundle's digests are deterministic fabrications, so calling it SIGNED is a
  // cryptographic claim nothing performs. A bare `signed` needle is impossible: `signed in`,
  // `assigned`, `unassigned`, `designed` and landing's real `signed webhooks` are all
  // legitimate under a case-insensitive substring match. These five are the forms the copy
  // actually reaches for — the prose/label register, then the `·`-separated tag register in
  // both positions (`SIGNED · SIMULATED`, `ACCEPTED BY NRS/MBS · SIGNED`).
  'signed evidence',
  'signed bundle',
  'evidence signed',
  // Both tag-register needles carry their separator AND a leading boundary, because
  // `assigned`/`designed`/`unsigned` all contain `signed`: `un signed ·` never occurs, so
  // ` signed ·` clears the honest retraction `· UNSIGNED ·` while still catching `SIGNED · x`.
  ' signed ·',
  '· signed',
  // Production is hosted outside Nigeria, so this is a false claim — same class as the
  // NRS/MBS entries above.
  'Data resident in-region',
] as const

// Retired landing positioning copy. Kept out of FORBIDDEN_STRINGS: that list is for claims of
// a regulatory action having OCCURRED, and this is marketing language — a different failure
// mode, not a false-claim category. The two `layer` entries are independent needles; a line
// carrying both reports both, pinned by "a line naming both overlapping phrases reports both".
export const RETIRED_LANDING_COPY = [
  // Row 3 — replaced by "is the solution between".
  'compliance layer',
  // Row 4 — replaced by "compliance solution".
  'compliance workflow layer',
  // Row 5 — replaced by "submit them to the regulatory bodies".
  'licensed transmission partners',
  // Row 10 — footer tagline dropped to one sentence.
  'compliance infrastructure for African businesses',
  'designed to expand',
  // Row 6 — replaced by "CSV / XLSX / PDF import".
  'CSV / XLSX bulk import',
  // Row 7 — replaced by "Golden MBS rule pack" / "Your own company rules".
  'Nigeria rule pack',
  'Field & tax logic checks',
  // Row 11 — the firms body no longer claims a distribution channel.
  'distribution channel for ASComply',
  // Row 12 — the footer no longer carries a build/environment tag. The separator is
  // load-bearing: a bare `SANDBOX` hits the two consoles' legitimate env labels, and a bare
  // `MBS ADAPTER` hits the live fintech-tab feature title "Sandbox MBS adapter".
  'MBS ADAPTER · SANDBOX',
] as const
