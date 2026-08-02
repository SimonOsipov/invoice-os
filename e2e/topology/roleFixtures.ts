// MOCK FIXTURE EXPECTATIONS — NOT A BACKEND CONTRACT.
//
// Every value below is hand-executed against frontend/app/src/lib/roles.ts's
// SEED_FIRM_ROLES / SEED_INHOUSE_ROLES over lib/members.ts's rosters and
// lib/workflows.ts's seed policies, through the pure derivations `resolve`,
// `roleUsage(steps(...))`, `holderCount`, `rosterRoleCell`, `unassignedNotice`,
// `pickerSelectionCount` and `hiddenInvitedFootnote`. There is no roles endpoint —
// the store is App.tsx useState, it resets on page reload, and no row of it exists in
// any database.
//
// Consequence: a spec importing this module pins FIXTURE BEHAVIOUR. It proves the
// surfaces render and that the role list is keyed per workspace; it proves NOTHING
// about any server. When a roles endpoint lands these constants must be replaced by
// live reads and every assertion re-derived — do not "update the strings" and call it
// covered. See topology/policyFixtures.ts, which draws the same line for policies.
//
// The constants are MOCK_*, never SEED_* — in this package `seed` means db/seed.dev.sql.
//
// Collected by nothing: Playwright's topology config matches '**/*.spec.ts' and vitest
// matches '**/*.test.ts'. It IS typechecked (e2e/tsconfig.json includes `topology`).

export interface MockRoleCard {
  /** role.key — names the drawer's pill toggle, `${idPrefix}-wfrole-${key}`. */
  key: string
  /** role.title — the card's heading, and the inspector select's option label. */
  title: string
  /** role.desc — the card's second line. Clamped to two lines in CSS only, so the DOM carries it whole. */
  desc: string
  /** `resolve()` — the holder line. No `— suspended` suffix: the tone carries that. */
  who: string
  /** `roleUsage(steps(policies, key))`. The card uppercases it in CSS, so specs match case-insensitively. */
  usage: string
  /** `holderCount(holders.length)`. Same CSS uppercase. */
  holders: string
}

// mf3 Musa Danjuma holds two seats, which is where the roster cell's `+N` form comes from;
// quality_reviewer is the seat nobody holds.
export const MOCK_FIRM_ROLES: readonly MockRoleCard[] = [
  {
    key: 'preparer',
    title: 'Invoice Preparer',
    desc: 'Prepares and imports client invoices',
    who: 'Folake Adesina +1',
    usage: 'not used in any policy',
    holders: '2 people',
  },
  {
    key: 'fin_mgr',
    title: 'Engagement Manager',
    desc: 'First sign-off on a client invoice',
    who: 'Musa Danjuma',
    usage: '2 approval steps · 2 policies',
    holders: '1 person',
  },
  {
    key: 'fin_dir',
    title: 'Senior Manager',
    desc: 'Second sign-off above ₦250m',
    who: 'Musa Danjuma',
    usage: '3 approval steps · 3 policies',
    holders: '1 person',
  },
  {
    key: 'compliance',
    title: 'Tax Reviewer',
    desc: 'Checks VAT, WHT and TIN detail before filing',
    who: 'Chiamaka Nwosu',
    usage: '3 approval steps · 3 policies',
    holders: '1 person',
  },
  {
    key: 'cfo',
    title: 'Engagement Partner',
    desc: 'Signs off invoices above ₦1bn',
    who: 'Chinedu Okafor',
    usage: '2 approval steps · 2 policies',
    holders: '1 person',
  },
  {
    key: 'quality_reviewer',
    title: 'Quality Reviewer',
    desc: 'Second-partner review on flagged engagements',
    who: 'Nobody assigned',
    usage: 'not used in any policy',
    holders: '0 people',
  },
]

// mh6 Adebayo Ogunlesi is suspended and the only CFO holder — the state that makes a seat
// unsignable while still naming a person. fin_mgr and ceo have nobody at all.
export const MOCK_INHOUSE_ROLES: readonly MockRoleCard[] = [
  { key: 'preparer', title: 'Preparer', desc: 'Accounts Payable', who: 'Zainab Lawal', usage: 'not used in any policy', holders: '1 person' },
  {
    key: 'line_mgr',
    title: 'Line Manager',
    desc: 'Requesting dept.',
    who: 'Emeka Uzowulu',
    usage: '2 approval steps · 2 policies',
    holders: '1 person',
  },
  { key: 'fin_mgr', title: 'Finance Manager', desc: 'Finance', who: 'Nobody assigned', usage: 'not used in any policy', holders: '0 people' },
  {
    key: 'controller',
    title: 'Financial Controller',
    desc: 'Finance',
    who: 'Tunde Adeyemi',
    usage: 'not used in any policy',
    holders: '1 person',
  },
  {
    key: 'fin_dir',
    title: 'Finance Director',
    desc: 'Finance',
    who: 'Ngozi Balogun +1',
    usage: '2 approval steps · 2 policies',
    holders: '2 people',
  },
  {
    key: 'compliance',
    title: 'Compliance Officer',
    desc: 'Tax & Compliance',
    who: 'Ibrahim Bello',
    usage: 'not used in any policy',
    holders: '1 person',
  },
  { key: 'cfo', title: 'CFO', desc: 'Executive', who: 'Adebayo Ogunlesi', usage: '2 approval steps · 2 policies', holders: '1 person' },
  { key: 'ceo', title: 'CEO', desc: 'Executive', who: 'Nobody assigned', usage: '1 approval step · 1 policy', holders: '0 people' },
]

/** `unassignedNotice(n)` plus the ' · '-joined titles beneath it — one banner shape, two tabs. */
export interface MockUnassignedBanner {
  notice: string
  titles: string
}

export const MOCK_FIRM_UNASSIGNED: MockUnassignedBanner = {
  notice: '1 role has nobody active assigned. Approval steps pointed at it will block.',
  titles: 'Quality Reviewer',
}

export const MOCK_INHOUSE_UNASSIGNED: MockUnassignedBanner = {
  notice: '3 roles have nobody active assigned. Approval steps pointed at them will block.',
  titles: 'Finance Manager · CFO · CEO',
}

/** `rosterRoleCell()` — the Members table's Workflow roles cell: first title plus `+N`, every title in the tooltip. */
export interface MockRosterCell {
  member: string
  text: string
  /** Newline-joined. Empty means the cell carries no `title` attribute at all. */
  tooltip: string
}

export const MOCK_FIRM_ROSTER_CELLS: readonly MockRosterCell[] = [
  { member: 'Musa Danjuma', text: 'Engagement Manager +1', tooltip: 'Engagement Manager\nSenior Manager' },
  { member: 'Chiamaka Nwosu', text: 'Tax Reviewer', tooltip: 'Tax Reviewer' },
]

// mh8 Chidi Anyanwu holds no seat, which is the em-dash case — and is why the mutation
// journey staffs him: his cell then reads the new title outright rather than `X +1`.
export const MOCK_INHOUSE_ROSTER_CELLS: readonly MockRosterCell[] = [
  { member: 'Ngozi Balogun', text: 'Finance Director', tooltip: 'Finance Director' },
  { member: 'Chidi Anyanwu', text: '—', tooltip: '' },
]

/** The role modal's picker excludes invited people, so its count and its footnote both fork on them. */
export interface MockPicker {
  /** `pickerMembers().length` — the denominator in `X of Y selected`. */
  selectable: number
  /** `hiddenInvitedFootnote()`, rendered only while at least one invited row is hidden. */
  hidden: string
}

export const MOCK_FIRM_PICKER: MockPicker = { selectable: 6, hidden: '1 invited person is hidden until they accept the invite.' }

export const MOCK_INHOUSE_PICKER: MockPicker = { selectable: 14, hidden: '2 invited people are hidden until they accept the invite.' }

/** `stepsForMember` unions every seat mf3 holds in ONE traversal — 5 steps, not 2 + 3 listed twice. */
export const MOCK_FIRM_TWO_SEAT_STEPS: {
  member: string
  /** The keys whose drawer pill reads pressed; every other pill must read unpressed. */
  held: readonly string[]
  named: string
  policies: string
} = {
  member: 'Musa Danjuma',
  held: ['fin_mgr', 'fin_dir'],
  named: 'Named in 5 approval steps',
  policies: 'Standard approval policy · Cross-border & FX · Government supply (B2G)',
}

/** `drawerRoleHelper('reviewer')` — the non-preparer arm. */
export const MOCK_DRAWER_ROLE_HELPER = 'Roles decide which approval steps this person can act on.'

/** `INVITE_ROLE_HELPER`, beneath the invite modal's `Workflow role` select in both modes. */
export const MOCK_INVITE_ROLE_HELPER =
  'The workflow role decides which approval steps they can sign. You can change it later in Settings › Roles.'

/** `roleOf`'s fallback title, prepended to the inspector select when a step names a deleted seat. */
export const MOCK_DELETED_ROLE_OPTION = 'Deleted role'

/** `resolve` / `inspectorResolve` for that same missing seat. */
export const MOCK_DELETED_ROLE_LINE = 'Role no longer exists'
