// Settings › Members and Settings › Roles fixtures. TWO KINDS OF CONSTANT LIVE HERE, and
// which is which decides what a failure means.
//
// SEED_* — db/seed.dev.sql, verbatim. A real membership row: identity, access role and
// status, read back over the wire by GET /api/tenancy/v1/memberships. A failure here is a
// BACKEND contract failure — the seed, the query, the projection or RLS.
//
// MOCK_* — the frontend role store (frontend/app/src/lib/roles.ts's SEED_FIRM_ROLES /
// SEED_INHOUSE_ROLES) and lib/workflows.ts's seed policies. There is no roles endpoint: the
// store is App.tsx useState, it resets on reload, and no row of it exists in any database.
// A failure here proves nothing about any server.
//
// THE CATCH, and the reason this banner is longer than one line: the MOCK_ constants are
// not pure. A role stores MEMBERSHIP SUBJECTS, so every personal NAME, holder COUNT, roster
// CELL and unassigned NOTICE below is the mock role store RESOLVED OVER the seeded
// membership rows — `resolve`, `holderCount`, `rosterRoleCell`, `unassignedNotice`,
// `pickerSelectionCount`. Change either side and every one of them must be re-derived by
// hand; do not "update the strings" and call it covered. Only MOCK_DRAWER_ROLE_HELPER,
// MOCK_DELETED_ROLE_OPTION and MOCK_DELETED_ROLE_LINE are free of the seed.
//
// UNBACKED_* — display copy transcribed from frontend/app/src/lib/members.ts's
// MEMBER_UNBACKED, PROTECTED_ADMIN_NOTE and the drawer's danger-zone strings. Transcribed,
// never imported: this package has no dependency on frontend/app/src (e2e/tsconfig.json),
// and a second copy is what catches a one-sided edit. See topology/policyFixtures.ts, which
// draws the same line for policies.
//
// Collected by nothing: Playwright's topology config matches '**/*.spec.ts' and vitest
// matches '**/*.test.ts'. It IS typechecked (e2e/tsconfig.json includes `topology`).

// ---------------------------------------------------------------------------------------
// SEED — the live membership directory
// ---------------------------------------------------------------------------------------

/** One seeded membership row, as the roster table renders it. */
export interface SeededMember {
  /** `display_name`. Also the handle every locator in roles.spec.ts uses to find the row. */
  name: string
  /** `email`, rendered under the name in the Person cell. */
  email: string
  /** `accessRoleLabel(role)` — the Access role cell. Never the raw enum value. */
  accessRole: string
  /** `MemberStatusPill`'s label — the status cell, uppercased in the DOM. */
  pill: 'ACTIVE' | 'INVITED' | 'SUSPENDED'
}

// Order is deliberately NOT asserted: the list is `ORDER BY created_at, user_id`, and a row
// inserted later would sort after these however it was named. The SET and its size are the
// claim.
//
// A literal length IS pinned, against persona-surfaces.spec.ts's ban on literal counts over
// live lists, because this list cannot grow: no endpoint mints a membership (there is no
// invite), and PATCH writes `status` only. api/isolation.spec.ts already pins the exact
// user_id set for the same reason.
export const SEED_FIRM_MEMBERS: readonly SeededMember[] = [
  { name: 'Chinedu Okafor', email: 'c.okafor@okafor.ng', accessRole: 'Admin', pill: 'ACTIVE' },
  { name: 'Folake Adesina', email: 'f.adesina@okafor.ng', accessRole: 'Preparer', pill: 'ACTIVE' },
  { name: 'Musa Danjuma', email: 'm.danjuma@okafor.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Chiamaka Nwosu', email: 'c.nwosu@okafor.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  {
    name: 'Oluwaseyifunmi Adebanjo-Ogunleye',
    email: 'o.adebanjo-ogunleye@okaforandpartners.com.ng',
    accessRole: 'Preparer',
    pill: 'ACTIVE',
  },
  // The firm's suspended row, and the only seeded person holding no workflow role at all —
  // both of this tab's exceptional states on one line.
  { name: 'Halima Yusuf', email: 'h.yusuf@okafor.ng', accessRole: 'Reviewer', pill: 'SUSPENDED' },
]

// …0012 Adebayo Ogunlesi is suspended AND the sole `cfo` holder, which is what makes a seat
// unsignable while still naming a person.
export const SEED_INHOUSE_MEMBERS: readonly SeededMember[] = [
  { name: 'Ngozi Balogun', email: 'n.balogun@honeywell.ng', accessRole: 'Admin', pill: 'ACTIVE' },
  { name: 'Yetunde Fashola', email: 'y.fashola@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Emeka Uzowulu', email: 'e.uzowulu@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Tunde Adeyemi', email: 't.adeyemi@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Ibrahim Bello', email: 'i.bello@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Adebayo Ogunlesi', email: 'a.ogunlesi@honeywell.ng', accessRole: 'Reviewer', pill: 'SUSPENDED' },
  { name: 'Zainab Lawal', email: 'z.lawal@honeywell.ng', accessRole: 'Preparer', pill: 'ACTIVE' },
]

/**
 * The roster's column heads, in order. The trailing '' is the unlabelled `⋯` column.
 *
 * Pinned as an EXACT list because three columns were deleted rather than hidden — firm's
 * client scoping, in-house's department, and Last active. A "does not contain" sweep would
 * pass on a column re-added under a different label.
 */
export const MEMBERS_TABLE_HEADS: readonly string[] = ['Person', 'Access role', 'Workflow roles', 'Status', '']

// ---------------------------------------------------------------------------------------
// UNBACKED — the sentence each dead control states for itself
// ---------------------------------------------------------------------------------------

/**
 * `MEMBER_UNBACKED`, `PROTECTED_ADMIN_NOTE` and the drawer's suspend explanation. Every one
 * of these is rendered as VISIBLE text beside its control, not only as a `title`: a disabled
 * control is out of the tab order and `title` never fires on one in Chromium, so the visible
 * sibling is the only layer a screenshot, a keyboard user and an assertion can all reach.
 */
export const UNBACKED = {
  invite: 'There is no invite endpoint yet — nothing mints a token, tracks an expiry, or sends the email.',
  remove: 'Deleting a membership locks that person out on their next request, and nothing undoes it. That decision has not been taken.',
  role: "The membership endpoint writes status only. Changing someone's access role has no server call behind it.",
  department: 'A membership stores a name, an email, an access role and a status. There is no department column.',
  clientAccess: 'Client access is not stored per person — everyone in this workspace sees the same clients.',
} as const

/** The §9 last-admin lock, on the sole active admin's own Suspend. Derived from LIVE rows. */
export const PROTECTED_ADMIN_NOTE = "You're the only admin. Promote someone else first."

/**
 * What suspension actually does — and the copy `[suspend-copy-is-true]` flagged. It is
 * SUSPEND-ONLY: beside `Reactivate` it would assert the opposite of the button's effect, so
 * a suspended member's drawer must NOT carry it. Both halves are asserted.
 */
export const SUSPEND_EXPLANATION =
  'Removes their approver rights and keeps all history. Sign-in is not blocked yet. Their name stays on every invoice they touched.'

/** The drawer's amber note on a suspended person who is named in approval steps. */
export const SUSPENDED_STEPS_NOTE = 'They are suspended, so those steps will block until someone else holds this role.'

// ---------------------------------------------------------------------------------------
// MOCK — the role store, resolved over the rows above
// ---------------------------------------------------------------------------------------

export interface MockRoleCard {
  /** role.key — names the drawer's pill toggle, `${idPrefix}-wfrole-${key}`. */
  key: string
  /** role.title — the card's heading, and the inspector select's option label. */
  title: string
  /** role.desc — the card's second line. Clamped to two lines in CSS only, so the DOM carries it whole. */
  desc: string
  /** `resolve()` — the holder line. A SEEDED display_name; no `— suspended` suffix, the tone carries that. */
  who: string
  /** `roleUsage(steps(policies, key))`. The card uppercases it in CSS, so specs match case-insensitively. */
  usage: string
  /** `holderCount(holders.length)` — counts SEEDED rows. Same CSS uppercase. */
  holders: string
}

// …0004 Musa Danjuma holds two seats, which is where the roster cell's `+N` form comes from;
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

// …0012 Adebayo Ogunlesi is suspended and the only CFO holder — the state that makes a seat
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

// Three, and the middle one only because …0012's SEEDED status is `suspended` — a status
// write against that row moves this string.
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

// …0007 Halima Yusuf is the em-dash case: seeded, suspended, and staffed into no role at
// all. She is READ-ONLY to every spec — nothing may PATCH her.
export const MOCK_FIRM_ROSTER_CELLS: readonly MockRosterCell[] = [
  { member: 'Musa Danjuma', text: 'Engagement Manager +1', tooltip: 'Engagement Manager\nSenior Manager' },
  { member: 'Chiamaka Nwosu', text: 'Tax Reviewer', tooltip: 'Tax Reviewer' },
  { member: 'Halima Yusuf', text: '—', tooltip: '' },
]

// One entry only. Every one of the seven seeded in-house members is staffed into some role,
// so this mode has no em-dash example — the firm carries that case above. Zainab Lawal is
// deliberately absent too: her workflow-role cell and her Access role cell both read
// `Preparer`, which no exact-text locator can tell apart.
export const MOCK_INHOUSE_ROSTER_CELLS: readonly MockRosterCell[] = [
  { member: 'Ngozi Balogun', text: 'Finance Director', tooltip: 'Finance Director' },
]

/**
 * `pickerMembers().length` — the denominator in `X of Y selected`, and the SEEDED roster's
 * length: the picker excludes `invited` people and no seeded row is invited.
 *
 * The invited footnote (`role-modal-hidden`) therefore never renders. It is asserted ABSENT
 * rather than dropped — the derivation still exists (roles.ts's `hiddenInvitedFootnote`,
 * unit-tested), and an environment that grew an invited row would surface it here.
 */
export const MOCK_FIRM_PICKER_SELECTABLE = 6

export const MOCK_INHOUSE_PICKER_SELECTABLE = 7

/** `stepsForMember` unions every seat …0004 holds in ONE traversal — 5 steps, not 2 + 3 listed twice. */
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

/** …0012 Adebayo Ogunlesi holds `cfo` alone, which two in-house steps name. */
export const MOCK_INHOUSE_SUSPENDED_STEPS: { member: string; named: string; rowWarning: string } = {
  member: 'Adebayo Ogunlesi',
  named: 'Named in 2 approval steps',
  rowWarning: 'Named in 2 approval steps · those steps will block',
}

/** `drawerRoleHelper('reviewer')` — the non-preparer arm. */
export const MOCK_DRAWER_ROLE_HELPER = 'Roles decide which approval steps this person can act on.'

/** `roleOf`'s fallback title, prepended to the inspector select when a step names a deleted seat. */
export const MOCK_DELETED_ROLE_OPTION = 'Deleted role'

/** `resolve` / `inspectorResolve` for that same missing seat. */
export const MOCK_DELETED_ROLE_LINE = 'Role no longer exists'
