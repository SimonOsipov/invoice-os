// MOCK FIXTURE EXPECTATIONS — NOT A BACKEND CONTRACT.
//
// Every value below is transcribed from frontend/app/src/lib/workflows.ts's
// SEED_FIRM_POLICIES (:135-165) / SEED_INHOUSE_POLICIES (:167-194) and the derived
// labels policySummary() (:422-428) and POLICY_TONE (WorkflowParts.tsx:171-174).
// lib/workflows.ts:9 states it outright: "Everything here is mock data + pure
// functions. There is no approvals endpoint (the backend has no approval concept at
// all)". The policy store is App.tsx:206 useState — it resets on page reload and no
// row of it exists in any database.
//
// Consequence: a spec importing this module pins FIXTURE BEHAVIOUR. It proves the
// screen renders and that the store is keyed per workspace; it proves NOTHING about
// any server. When an approvals endpoint lands, these constants must be replaced by
// live reads and every assertion re-derived — do not "update the strings" and call it
// covered.
//
// Deliberately NOT filed in topology/targets.ts: that file holds deployed URLs and
// DB-seeded tenants, and its VALIDATION_EXPECTED describes the LIVE rule set. Parking
// frontend mock data beside it would blur exactly the line this header draws. The
// constants are MOCK_*, never SEED_* — in this package `seed` means db/seed.dev.sql.
//
// Collected by nothing: Playwright's topology config matches '**/*.spec.ts' and vitest
// matches '**/*.test.ts'. It IS typechecked (e2e/tsconfig.json includes `topology`).

export interface MockPolicyRow {
  /** policy.name, rendered beside the status pill. */
  name: string
  /** POLICY_TONE[status].label — the pill's only text. */
  pill: 'PUBLISHED' | 'DRAFT'
  /**
   * The row's whole `.mono` line: `{policy.scope} · {policySummary(policy)}`
   * (WorkflowsView.tsx:104-106). Every separator is U+00B7 MIDDLE DOT with a space
   * either side — note the firm's B2G policy carries a THIRD one inside its own scope
   * string ('Document type · B2G'), which is why the scope is not split out.
   */
  summaryLine: string
  /** policy.updated — a human string, rendered as `Updated {updated}`. */
  updated: string
}

// countApprovals() recurses into BOTH condition branches and counts `approval` nodes only
// (`notify` and `autoapprove` do not count); countConditions() counts ROOT-level conditions
// only. Hand-executed against the seed for each row below — the firm's first policy is 4
// approvals, not 3: its two conditions contribute f1n3 and f1n5, while the notify node f1n6
// does not.
export const MOCK_FIRM_POLICIES: readonly MockPolicyRow[] = [
  {
    name: 'Standard approval policy',
    pill: 'PUBLISHED',
    summaryLine: 'All invoices · 4 approvals · 2 conditions',
    updated: '2 days ago',
  },
  {
    name: 'Cross-border & FX',
    pill: 'PUBLISHED',
    summaryLine: 'Foreign-currency invoices · 3 approvals · 1 condition',
    updated: '1 week ago',
  },
  {
    name: 'Government supply (B2G)',
    pill: 'DRAFT',
    summaryLine: 'Document type · B2G · 3 approvals · 1 condition',
    updated: '3 weeks ago',
  },
]

export const MOCK_INHOUSE_POLICIES: readonly MockPolicyRow[] = [
  {
    name: 'Company approval policy',
    pill: 'PUBLISHED',
    // 3 approvals, not 4: h1n6 is the seed's only `autoapprove` node and countApprovals
    // does not count it.
    summaryLine: 'All invoices · 3 approvals · 2 conditions',
    updated: 'yesterday',
  },
  {
    name: 'Capital expenditure',
    pill: 'DRAFT',
    summaryLine: 'Capex & fixed assets · 4 approvals · 1 condition',
    updated: '5 days ago',
  },
]
