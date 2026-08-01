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
] as const
