// Settings › Members and Settings › Roles, driven as BOTH personas, plus the one journey that
// creates a seat, builds a policy whose step names it, proves both stuck, and deletes both.
//
// LIVE, end to end. Settings › Roles is a real screen over five routes: GET/POST
// /api/invoice/v1/workflow-roles, PATCH/DELETE /workflow-roles/{key} and
// PUT /workflow-roles/{key}/members (internal/approval, wired APPR-04-01..06). Every NAME,
// email, access role and status pill below is a live membership row (GET
// /api/tenancy/v1/memberships), and every holder line on a role card is that row resolved
// through lib/roles.ts's `resolve()` against the same live rows.
//
// e2e/api/contract-approvals.spec.ts already pins the WIRE contract for those five routes —
// status codes, error envelopes, the four-key Role shape, staffing order round-trips. This
// file does not duplicate that: it drives the SCREEN, through the same typed seam
// (e2e/api/client.ts) only where it needs a live read or a cleanup delete of its own.
//
// See e2e/topology/settingsFixtures.ts for the membership half of this file's fixtures
// (SEED_*_MEMBERS, the roster's column heads, the disabled-control copy). The role-card half
// below is NOT a shared fixture: it is derived by hand, in this file, against
// db/seed.dev.sql's workflow_roles/workflow_role_members and lib/roles.ts's resolve() —
// see SEED_FIRM_ROLE_CARDS below for the derivation itself.
//
// NO WRITE TO A MEMBERSHIP is driven from the browser. Suspend/Reactivate are real, audited
// PATCHes, and this environment is shared and never reset between specs — a status write
// started here could not be restored on a failed assertion the way api/contract-tenancy.spec.ts's
// try/finally does. The controls are asserted ENABLED and correctly labelled; the write itself
// is proven over the wire in that spec instead. Workflow-ROLE writes are different: Test 3
// below drives create/staff/delete from the browser on purpose, because that round trip —
// through the UI, against the real store — is exactly what this file exists to prove, and its
// own cleanup (below) is what keeps the environment honest afterward.
//
// COUNT ASSERTIONS: persona-surfaces.spec.ts bans literal counts over LIVE, tenant-wide lists
// on the deployment every suite in the run shares, and permits exactly two shapes — (1) compared against a live API
// read taken in the same test, (2) containment of rows this test itself created. The member
// roster is exempt from the ban and stays a literal count: no endpoint mints a membership
// (there is no invite) and PATCH writes `status` only, so that list cannot grow.
// workflow_roles is NOT exempt — Test 3 creates one from this very screen — so every
// role-grid count below uses shape (1) (Test 1, Test 2, via e2e/api/client.ts's
// listWorkflowRoles) or shape (2) (Test 3, via the created/deleted role's own locator).
//
// CARD ORDER is still asserted (`ListRoles`' `ORDER BY created_at, key`,
// internal/approval/store.go:64-65, seeded ascending in db/seed.dev.sql) — but by the
// RELATIVE position of the expected titles (`expectRelativeOrder` below), never `.nth()` into
// the raw grid: a residue card from an earlier failed run (deletes are soft, keys are never
// reclaimed) can sit anywhere in the DOM without disturbing where these titles fall
// relative to one another.
//
// RELOAD is no longer special. Role definitions and staffing are server rows now, so a
// mid-test `page.reload()` re-fetches the same data rather than wiping an in-memory store —
// Test 3 uses exactly that to prove the round trip is real (AC-8).
//
// APPROVAL POLICIES ARE SERVER ROWS TOO, since APPR-09. Test 3 used to repoint a step inside
// a SEEDED policy and to need no cleanup for it, on the reasoning that policies were frontend
// `useState` that every `signInAs` reseeded. Both halves of that are false now: the only
// seeded policy is internal/demopolicy's, on the IN-HOUSE tenant alone and SEALED (the FIRM
// tenant carries none), and a repoint that is SAVED is a row that outlives the run. So
// Test 3 creates its own policy, saves once, and deletes it through the UI — and the
// `test.afterAll` below sweeps both halves of its mutation, the role by title prefix and the
// policy by id first, name second. Modelled on contract-approvals.spec.ts's sweep and on
// topology/workflows.spec.ts, which mints its own policy exactly this way.
import { test, expect, type Locator, type Page } from '@playwright/test'

import {
  deleteApprovalPolicy,
  deleteWorkflowRole,
  listApprovalPolicies,
  listWorkflowRoles,
  login,
  PERSONAS,
} from '../api/client'
import { collectErrors, signInAs } from '../personaSession'
import {
  MEMBERS_TABLE_HEADS,
  PROTECTED_ADMIN_NOTE,
  SEED_FIRM_MEMBERS,
  SEED_INHOUSE_MEMBERS,
  SUSPEND_EXPLANATION,
  UNBACKED,
  type SeededMember,
} from './settingsFixtures'
import { FIRM_PERSONA, INHOUSE_PERSONA } from './targets'

// ---------------------------------------------------------------------------------------
// SEED role cards — derived BY HAND against db/seed.dev.sql's workflow_roles +
// workflow_role_members and lib/roles.ts's resolve()/activeHolders(), never copied from the
// deleted MOCK_* fixture. `who`/`warn` are the APPR-04-02 predicate's own output: only an
// ACTIVE admin or reviewer is a legal approver, so a seat held only by a preparer (both
// `Preparer` cards) or only by a suspended reviewer (in-house `CFO`) renders its holder's
// name in the warn/red tone, same as an empty seat. `holders` is unaffected by that predicate
// (holderCount counts EVERY seeded holder regardless of approver eligibility) and comes
// straight from the raw workflow_role_members row count — re-derived below, not assumed
// unchanged.
//
// A card's USAGE line carries no literal here, and cannot: `roleUsage` is a function of the
// tenant-wide POLICY list, which neither Test 1 nor Test 2 owns. internal/demopolicy seeds
// ONE policy onto the IN-HOUSE tenant and none onto the FIRM one, and its approval step names
// `fin_dir` — so that one seat reads `1 approval step · 1 policy` on a clean deployment while
// every other seat reads `not used in any policy`, and one stray policy left by a dead run
// flips that for whichever seats it names. The shape is asserted instead; see USAGE_SHAPE
// below, whose second alternative admits the seeded branch. Exact usage strings survive in
// exactly one place, Test 3, over the policy Test 3 itself creates.
interface SeedRoleCard {
  /** role.key — names the drawer's pill toggle, `${idPrefix}-wfrole-${key}` (MemberParts.tsx). */
  key: string
  /** role.title — the card's heading, and the inspector select's option label. */
  title: string
  /** role.desc — the card's second line, verbatim from workflow_roles.description. */
  desc: string
  /** `resolve()`'s text — a SEEDED display_name, or `Nobody assigned`. No `— suspended` suffix. */
  who: string
  /** `resolve()`'s warn: true whenever no ACTIVE approver holds the seat. */
  warn: boolean
  /** `holderCount(held.length)` — ALL seeded holders, active or not. The card uppercases it in CSS. */
  holders: string
}

/**
 * `roleUsage`'s two arms, and the whole of its range — asserted as a SHAPE because the value
 * is not this test's to own (see above). This keeps APPR-09-06's gain rather than dropping it:
 * RolesView renders the EMPTY STRING on this line while the policies fetch is loading or has
 * errored, and '' matches neither alternative, so a card that claims nothing still fails.
 * Case-insensitive because the card uppercases in CSS; anchored so no wider node can match.
 * The two nouns singularise INDEPENDENTLY, so all four combinations are legal and the pattern
 * does not try to pin which one is right — `roles.test.ts` owns the singularisation claim.
 */
const USAGE_SHAPE = /^(not used in any policy|\d+ approval steps? · \d+ (policy|policies))$/i

// …0004 Musa Danjuma holds two seats (fin_mgr, fin_dir), which is where the roster cell's
// `+N` form comes from. …0003/…0006 hold `preparer` between them — both PREPARERS, so
// neither counts as an approver and the seat renders warn despite two live holders.
// quality_reviewer is the seat nobody holds at all.
const SEED_FIRM_ROLE_CARDS: readonly SeedRoleCard[] = [
  {
    key: 'preparer',
    title: 'Invoice Preparer',
    desc: 'Prepares and imports client invoices',
    who: 'Folake Adesina +1',
    warn: true,
    holders: '2 people',
  },
  {
    key: 'fin_mgr',
    title: 'Engagement Manager',
    desc: 'First sign-off on a client invoice',
    who: 'Musa Danjuma',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'fin_dir',
    title: 'Senior Manager',
    desc: 'Second sign-off above ₦250m',
    who: 'Musa Danjuma',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'compliance',
    title: 'Tax Reviewer',
    desc: 'Checks VAT, WHT and TIN detail before filing',
    who: 'Chiamaka Nwosu',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'cfo',
    title: 'Engagement Partner',
    desc: 'Signs off invoices above ₦1bn',
    who: 'Chinedu Okafor',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'quality_reviewer',
    title: 'Quality Reviewer',
    desc: 'Second-partner review on flagged engagements',
    who: 'Nobody assigned',
    warn: true,
    holders: '0 people',
  },
]

// …0012 Adebayo Ogunlesi is SUSPENDED and the sole `cfo` holder — a legal approver's seat that
// still renders warn, because status gates it same as absence does. `fin_mgr`/`ceo` have
// nobody at all; `preparer` has one PREPARER holder, the same not-an-approver case as firm's.
const SEED_INHOUSE_ROLE_CARDS: readonly SeedRoleCard[] = [
  {
    key: 'preparer',
    title: 'Preparer',
    desc: 'Accounts Payable',
    who: 'Zainab Lawal',
    warn: true,
    holders: '1 person',
  },
  {
    key: 'line_mgr',
    title: 'Line Manager',
    desc: 'Requesting dept.',
    who: 'Emeka Uzowulu',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'fin_mgr',
    title: 'Finance Manager',
    desc: 'Finance',
    who: 'Nobody assigned',
    warn: true,
    holders: '0 people',
  },
  {
    key: 'controller',
    title: 'Financial Controller',
    desc: 'Finance',
    who: 'Tunde Adeyemi',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'fin_dir',
    title: 'Finance Director',
    desc: 'Finance',
    who: 'Ngozi Balogun +1',
    warn: false,
    holders: '2 people',
  },
  {
    key: 'compliance',
    title: 'Compliance Officer',
    desc: 'Tax & Compliance',
    who: 'Ibrahim Bello',
    warn: false,
    holders: '1 person',
  },
  {
    key: 'cfo',
    title: 'CFO',
    desc: 'Executive',
    who: 'Adebayo Ogunlesi',
    warn: true,
    holders: '1 person',
  },
  { key: 'ceo', title: 'CEO', desc: 'Executive', who: 'Nobody assigned', warn: true, holders: '0 people' },
]

/** `unassignedNotice(n)` plus the ' · '-joined titles beneath it — one banner shape, two tabs. */
interface SeedUnassignedBanner {
  notice: string
  titles: string
}

// Two roles: `preparer` (both seeded holders are PREPARERS, so `activeHolders` is empty) and
// `quality_reviewer` (no holders at all). The predicate change (APPR-04-02) is what moves this
// from the pre-change '1 role … Quality Reviewer' to this.
const FIRM_UNASSIGNED: SeedUnassignedBanner = {
  notice: '2 roles have nobody active assigned. Approval steps pointed at them will block.',
  titles: 'Invoice Preparer · Quality Reviewer',
}

// Four roles: `preparer` (a preparer-only seat), `fin_mgr`/`ceo` (nobody at all), and `cfo`
// (its one holder is suspended). Pre-predicate-change this read '3 roles … Finance Manager ·
// CFO · CEO' — `preparer` is the new arrival.
const INHOUSE_UNASSIGNED: SeedUnassignedBanner = {
  notice: '4 roles have nobody active assigned. Approval steps pointed at them will block.',
  titles: 'Preparer · Finance Manager · CFO · CEO',
}

/** `rosterRoleCell()` — the Members table's Workflow roles cell: first title plus `+N`, every title in the tooltip. */
interface SeedRosterCell {
  member: string
  text: string
  /** Newline-joined. Empty means the cell carries no `title` attribute at all. */
  tooltip: string
}

// `rosterRoleCell` unions every seat a member holds regardless of approver eligibility, so
// this is unaffected by the APPR-04-02 predicate — re-derived and confirmed unchanged, not
// assumed so. …0007 Halima Yusuf is the em-dash case: seeded, suspended, and staffed into no
// role at all. She is READ-ONLY to every spec — nothing may PATCH her.
const FIRM_ROSTER_CELLS: readonly SeedRosterCell[] = [
  { member: 'Musa Danjuma', text: 'Engagement Manager +1', tooltip: 'Engagement Manager\nSenior Manager' },
  { member: 'Chiamaka Nwosu', text: 'Tax Reviewer', tooltip: 'Tax Reviewer' },
  { member: 'Halima Yusuf', text: '—', tooltip: '' },
]

// One entry: every seeded in-house member is staffed into some role, so this mode has no
// em-dash example — the firm carries that case above.
const INHOUSE_ROSTER_CELLS: readonly SeedRosterCell[] = [{ member: 'Ngozi Balogun', text: 'Finance Director', tooltip: 'Finance Director' }]

// `pickerMembers().length` — every seeded member, since none of them is `invited`.
const FIRM_PICKER_SELECTABLE = SEED_FIRM_MEMBERS.length
const INHOUSE_PICKER_SELECTABLE = SEED_INHOUSE_MEMBERS.length

// …0004 Musa Danjuma holds TWO seats, which is what makes his drawer's pill loop a real check
// rather than an all-false one. The drawer's step count and its policy list used to be
// asserted here too; both are gone with the seeded policies that produced them. `stepsForMember`
// answers null at zero (lib/roles.ts, pinned by MemberDrawer.test.tsx:242), so on a tenant with
// no policies those elements do not render at all — the assertions would not be merely wrong,
// they would have nothing to match.
const FIRM_TWO_SEAT_MEMBER: { member: string; held: readonly string[] } = {
  member: 'Musa Danjuma',
  held: ['fin_mgr', 'fin_dir'],
}

/** …0012 Adebayo Ogunlesi: SUSPENDED, and the sole `cfo` holder. His STATUS is the live fact. */
const INHOUSE_SUSPENDED_MEMBER = 'Adebayo Ogunlesi'

/** `drawerRoleHelper('reviewer')` — the non-preparer arm. Free of the seed. */
const DRAWER_ROLE_HELPER = 'Roles decide which approval steps this person can act on.'

/** `roleOf`'s fallback title, prepended to the inspector select when a step names a deleted seat. */
const DELETED_ROLE_OPTION = 'Deleted role'

/** `resolve` / `inspectorResolve` for that same missing seat. */
const DELETED_ROLE_LINE = 'Role no longer exists'

// The Settings tab strip, in render order. In-house prepends its own Company tab ahead of
// the shared list, so the two differ by that one entry — and Roles sits directly after
// Members in both.
const FIRM_SETTINGS_TABS = ['Members', 'Roles', 'ERP connectors', 'API & webhooks', 'Signing & certificates']
const INHOUSE_SETTINGS_TABS = ['Company', ...FIRM_SETTINGS_TABS]

// sidebar/navButton/goTo: file-local copies of persona-surfaces.spec.ts's, this package's
// stated convention for small Page-driving helpers.
function sidebar(page: Page) {
  return page.locator('aside.pf-sidebar')
}

async function goTo(page: Page, label: string): Promise<void> {
  await sidebar(page).getByRole('button', { name: label }).click()
}

/** A Settings tab by its exact label — never a substring, or `Roles` would catch nothing else. */
function settingsTab(page: Page, label: string) {
  return page.getByRole('button', { name: label, exact: true })
}

/** The strip is the tab buttons' own parent, so it can never widen to some other button row. */
function tabStrip(page: Page) {
  return settingsTab(page, 'Members').locator('xpath=..')
}

/**
 * A role card by its EXACT title. Substring matching would make `Preparer` select in-house's
 * card and firm's `Invoice Preparer` alike, which is the assertion this file most needs to
 * stay honest.
 */
function roleCard(page: Page, title: string) {
  return page.getByTestId('role-card').filter({ has: page.getByText(title, { exact: true }) })
}

function memberRow(page: Page, name: string) {
  return page.getByTestId('member-row').filter({ has: page.getByText(name, { exact: true }) })
}

/**
 * The row's four data cells, in grid order: Person, Access role, Workflow roles, Status.
 * Positional on purpose — in-house's Zainab Lawal holds a workflow role whose title is
 * `Preparer`, exactly what her Access role cell reads, and no exact-text locator can tell
 * the two apart. The `⋯` column is a div, so it is not among these.
 */
function rowCells(page: Page, name: string): Locator {
  return memberRow(page, name).locator('xpath=./span')
}

/** The roster's head row — the table's first direct child — and its five column labels. */
function tableHeads(page: Page): Locator {
  return page.getByTestId('members-table').locator('xpath=./div[1]/span')
}

/**
 * A `WfSelect`'s underlying <select>, found through its own <label>. Structural on purpose:
 * the label wraps the control, so an accessible-name lookup would fold the selected option's
 * text into the name.
 */
function wfSelect(page: Page, label: string) {
  return page.locator('label').filter({ hasText: label }).locator('select')
}

/**
 * One role card, whole, found by its EXACT title — never `.nth(index)`, which a residue card
 * elsewhere in the grid could shift. `holders` is matched case-insensitively (the card
 * uppercases it in CSS); `desc` exactly (`-webkit-line-clamp` truncates the render, not the
 * DOM). `who`'s tone is checked separately, below. The usage line is matched by SHAPE — see
 * USAGE_SHAPE: this test does not own the tenant's policy list, so it cannot name the value,
 * but a card that renders nothing there is still a failure.
 */
async function expectRoleCard(page: Page, role: SeedRoleCard): Promise<void> {
  const card = roleCard(page, role.title)
  await expect(card, `role card "${role.title}" should render exactly once`).toHaveCount(1)
  await expect(card.getByText(role.desc, { exact: true }), `${role.title} desc`).toBeVisible()
  const who = card.getByText(role.who, { exact: true })
  await expect(who, `${role.title} holder line`).toBeVisible()
  await expectHolderTone(who, role.warn, `${role.title} holder line`)
  await expect(card.getByText(USAGE_SHAPE), `${role.title} usage line`).toBeVisible()
  await expect(card.getByText(role.holders), `${role.title} holder count`).toBeVisible()
}

/**
 * `who.warn` renders as an inline `color: var(--status-red-text)` (else `var(--fg-2)`) —
 * RolesView.tsx:227-231. Checked on the literal CSS-variable reference the DOM carries, not
 * the browser's *resolved* color: this file's own font-weight check on the active nav item
 * (below persona-surfaces.spec.ts:347-349) already flagged a resolved design-system color as
 * "a moving target" across engines, which `element.style.color` never is — it echoes back
 * exactly what React wrote, the same property the unit suite checks directly
 * (InvoicesList.test.tsx's `tin.style.color`).
 */
async function expectHolderTone(who: Locator, warn: boolean, label: string): Promise<void> {
  const color = await who.evaluate((el) => (el as HTMLElement).style.color)
  expect(color, `${label} should render in its ${warn ? 'warn/red' : 'default'} tone`).toBe(warn ? 'var(--status-red-text)' : 'var(--fg-2)')
}

/** The DOM index of the card carrying this EXACT title, among every `role-card` on screen. */
async function cardIndex(page: Page, title: string): Promise<number> {
  return roleCard(page, title).evaluate((el) => Array.from(document.querySelectorAll('[data-testid="role-card"]')).indexOf(el))
}

/**
 * Card order is pinned server-side (see the header) — asserted here by RELATIVE position: each
 * title in `titles` must render strictly after the one before it. A set-equality check would
 * pass on a shuffled grid; `.nth()` would break the moment a residue card (soft-deleted, never
 * reclaimed) landed among these.
 */
async function expectRelativeOrder(page: Page, titles: readonly string[]): Promise<void> {
  const indices = await Promise.all(titles.map((t) => cardIndex(page, t)))
  for (let i = 1; i < indices.length; i++) {
    expect(indices[i], `"${titles[i]}" should render after "${titles[i - 1]}"`).toBeGreaterThan(indices[i - 1])
  }
}

/**
 * AC-4, shape (1): the grid's cardinality against a live read taken in this same test — never
 * a literal, since Test 3 (or an unswept probe) can grow this tenant's workflow_roles forever.
 * `token` must come from the SAME tenant the browser session is signed into.
 */
async function expectLiveGridCount(page: Page, token: string): Promise<void> {
  const live = await listWorkflowRoles(token)
  await expect(page.getByTestId('role-card'), 'grid count must equal a live read taken in this test').toHaveCount(
    live.workflow_roles.length,
  )
}

/** One roster row's server-stated identity: email, access-role label and status pill. */
async function expectRosterRow(page: Page, m: SeededMember): Promise<void> {
  const row = memberRow(page, m.name)
  await expect(row, `${m.name} should have exactly one roster row`).toHaveCount(1)
  const cells = rowCells(page, m.name)
  await expect(cells.nth(0), `${m.name}'s email`).toContainText(m.email)
  await expect(cells.nth(1), `${m.name}'s access role`).toHaveText(m.accessRole)
  await expect(cells.nth(3), `${m.name}'s status pill`).toHaveText(m.pill)
}

/** The roster's Workflow roles cell: `first title +N` on screen, every title in the tooltip. */
async function expectRosterCell(page: Page, cell: SeedRosterCell): Promise<void> {
  const target = memberRow(page, cell.member).getByText(cell.text, { exact: true })
  await expect(target, `${cell.member}'s Workflow roles cell`).toHaveCount(1)
  // A roleless row renders `—` and deliberately carries no tooltip at all.
  expect(await target.getAttribute('title'), `${cell.member}'s roles tooltip`).toBe(cell.tooltip || null)
}

/**
 * A control that is genuinely `disabled` AND states why in text the reader can see. Both
 * halves, always: a dead control with no visible reason is the defect this story exists to
 * fix, and `title` alone does not count — it never fires on a disabled element in Chromium.
 */
async function expectDisabledWithReason(control: Locator, reasonNode: Locator, reason: string, label: string): Promise<void> {
  await expect(control, `${label} should be disabled`).toBeDisabled()
  await expect(control, `${label} should carry its reason as a title`).toHaveAttribute('title', reason)
  await expect(reasonNode, `${label}'s reason should be visible`).toBeVisible()
  await expect(reasonNode, `${label}'s reason text`).toHaveText(reason)
}

/** Opens one row's `⋯` menu. The trigger toggles, so the same call closes it again. */
async function toggleRowMenu(page: Page, name: string): Promise<void> {
  await memberRow(page, name).getByTestId('member-menu-trigger').click()
}

// ---------------------------------------------------------------------------------------
// Test 1 -- the FIRM workspace: the live roster, the seeded seats, the seat nobody holds
// ---------------------------------------------------------------------------------------
test('firm Settings: the live member directory, the live role grid, and every control that has no endpoint', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')
  // MODE guard before touching the nav, the same one persona-surfaces.spec.ts takes: without
  // it a slow or failed persona hand-off surfaces as an opaque timeout further down rather
  // than as "the wrong workspace rendered". Stated as MODE and not tenant on purpose — in
  // firm mode Sidebar.tsx hardcodes the org label, so this proves the FIRM branch drew, not
  // that /v1/me returned this tenant. The live-tenant proof is signInAs's own discriminator.
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()

  // Roles is a TAB, and where it sits is the claim: reading the strip in order is what pins
  // "directly after Members" — a presence check would pass on a tab appended at the end.
  await expect(tabStrip(page).locator('button')).toHaveText(FIRM_SETTINGS_TABS)

  // --- the roster is the SERVER's ----------------------------------------------------------
  // Members is the default tab, so this renders without a click. Six rows, each carrying an
  // email no fixture in this repo ever held — that is what makes this a live read and not a
  // renamed mock.
  await expect(page.getByTestId('members-table')).toBeVisible()
  await expect(page.getByTestId('member-row')).toHaveCount(SEED_FIRM_MEMBERS.length)
  for (const m of SEED_FIRM_MEMBERS) {
    await expectRosterRow(page, m)
  }

  // Removed, not hidden. Firm's client-scoping column, in-house's department and Last active
  // are gone with the fields no membership row carries.
  await expect(tableHeads(page)).toHaveText(MEMBERS_TABLE_HEADS)

  // --- invite: rendered, dead, and saying so ------------------------------------------------
  await expectDisabledWithReason(
    page.getByTestId('members-invite'),
    page.getByTestId('members-invite-reason'),
    UNBACKED.invite,
    'Invite people',
  )

  // --- the `⋯` menu on someone else's row ---------------------------------------------------
  await toggleRowMenu(page, 'Chiamaka Nwosu')
  const menu = page.getByTestId('member-menu')
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('button', { name: 'Edit', exact: true })).toBeEnabled()
  // The one real write this menu offers. Asserted enabled, never clicked — see the header.
  await expect(menu.getByRole('button', { name: 'Suspend', exact: true })).toBeEnabled()
  await expectDisabledWithReason(
    menu.getByRole('button', { name: 'Remove', exact: true }),
    menu.getByTestId('member-menu-reason'),
    UNBACKED.remove,
    "Chiamaka Nwosu's Remove",
  )
  await toggleRowMenu(page, 'Chiamaka Nwosu')
  await expect(menu).toHaveCount(0)

  // --- the same menu on YOUR OWN row, which is the last-admin lock ---------------------------
  // Derived from the LIVE roster: …0001 is the only membership row in this tenant whose role
  // is `admin` and whose status is `active`, so the server's own rows are what disable this.
  await toggleRowMenu(page, 'Chinedu Okafor')
  await expect(menu).toBeVisible()
  await expectDisabledWithReason(
    menu.getByRole('button', { name: 'Suspend', exact: true }),
    menu.getByTestId('member-menu-reason'),
    PROTECTED_ADMIN_NOTE,
    "your own Suspend, as the tenant's only admin",
  )
  // §6: your own menu has no Remove at all — OMITTED, a different fact from disabled.
  await expect(menu.getByRole('button', { name: 'Remove', exact: true })).toHaveCount(0)
  await toggleRowMenu(page, 'Chinedu Okafor')
  await expect(menu).toHaveCount(0)

  // --- the Members tab speaks in roles ------------------------------------------------------
  await expect(page.getByTestId('members-table').getByText('Workflow roles')).toBeVisible()
  for (const cell of FIRM_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }
  // The same sentence the Roles tab renders, from the same derivation — and firm is the mode
  // where this banner never used to appear at all.
  await expect(page.getByTestId('members-unassigned')).toContainText(FIRM_UNASSIGNED.notice)

  // --- the drawer: pill toggles, and three controls with nothing behind them ------------------
  const twoSeat = FIRM_TWO_SEAT_MEMBER
  await memberRow(page, twoSeat.member).getByText(twoSeat.member, { exact: true }).click()
  const drawer = page.getByTestId('member-drawer')
  await expect(drawer).toBeVisible()
  // The pill loop is the membership-derived half, and the one that survives: it reads which
  // seats this person holds, not which policy steps name them.
  for (const role of SEED_FIRM_ROLE_CARDS) {
    const pressed = String(twoSeat.held.includes(role.key))
    await expect(page.getByTestId(`drawer-wfrole-${role.key}`), `${role.title} pill`).toHaveAttribute('aria-pressed', pressed)
  }
  await expect(page.getByTestId('drawer-wfrole-helper')).toHaveText(DRAWER_ROLE_HELPER)

  // Access role: shown at the person's REAL role rather than hidden, and unchangeable. The
  // radios sit inside a card each; the reason is one visible sentence beneath all three.
  await expect(page.getByTestId('drawer-role-reviewer').locator('input')).toBeChecked()
  for (const id of ['admin', 'preparer', 'reviewer']) {
    await expect(page.getByTestId(`drawer-role-${id}`).locator('input'), `${id} card`).toBeDisabled()
  }
  await expect(drawer, 'the access-role reason').toContainText(UNBACKED.role)

  // Firm's fork: a client-access picker inside a disabled <fieldset>, its reason beneath.
  await expect(page.getByTestId('drawer-scope-all').locator('input')).toBeDisabled()
  await expect(drawer, 'the client-access reason').toContainText(UNBACKED.clientAccess)
  // In-house's field must not leak into firm.
  await expect(drawer).not.toContainText(UNBACKED.department)

  // The Activity block is GONE — Last active, Joined and Invited by were three mock fields a
  // membership row does not carry. Absent, not em-dashed.
  await expect(drawer).not.toContainText('Last active')

  // The danger zone: one live control, one dead one.
  await expect(page.getByTestId('member-suspend'), 'an active member suspends').toHaveText('Suspend')
  await expect(page.getByTestId('member-suspend')).toBeEnabled()
  await expect(drawer, 'what suspension actually does').toContainText(SUSPEND_EXPLANATION)
  await expect(page.getByTestId('member-remove')).toBeDisabled()
  await expect(drawer, 'the remove reason').toContainText(UNBACKED.remove)

  await page.getByTestId('member-drawer-close').click()
  await expect(drawer).toHaveCount(0)

  // --- the Roles tab ------------------------------------------------------------------------
  const token = await login(PERSONAS.A)
  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  await expectLiveGridCount(page, token)
  for (const role of SEED_FIRM_ROLE_CARDS) {
    await expectRoleCard(page, role)
  }
  await expectRelativeOrder(
    page,
    SEED_FIRM_ROLE_CARDS.map((r) => r.title),
  )

  // The unsignable seats, named where the reader already is. Each card above already reads
  // its own warn state; the banner is the workspace-level statement of the same facts — it
  // sits above the grid, which a search box cannot change.
  const banner = page.getByTestId('roles-unassigned')
  await expect(banner).toContainText(FIRM_UNASSIGNED.notice)
  await expect(banner).toContainText(FIRM_UNASSIGNED.titles)

  // Disjointness, firm half. Scoped to the grid and EXACT: `Preparer` is a substring of this
  // mode's own `Invoice Preparer`, so a containment check here would fail for the wrong
  // reason. This is a RLS claim, not a client-side one — `workflow_roles` is one table and a
  // tenant claim is the only filter on it, which is why the two grids share no title.
  const grid = page.getByTestId('roles-grid')
  for (const role of SEED_INHOUSE_ROLE_CARDS) {
    await expect(grid.getByText(role.title, { exact: true }), `${role.title} must not leak into firm`).toHaveCount(0)
  }

  // The create modal opens here but writes nothing — the full create/staff/reload/delete
  // journey is Test 3. The picker's denominator is the SELECTABLE roster: an invited person
  // has no row to tick, and no seeded row is invited, so the footnote that names them never
  // renders.
  await page.getByTestId('roles-new').click()
  await expect(page.getByTestId('role-modal-count')).toHaveText(`0 of ${FIRM_PICKER_SELECTABLE} selected`)
  await expect(page.getByTestId('role-modal-hidden')).toHaveCount(0)
  // Save is inert on an empty name and nothing else gates it.
  await expect(page.getByTestId('role-modal-save')).toBeDisabled()
  await page.getByTestId('role-modal-cancel').click()
  await expect(page.getByTestId('role-modal')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 2 -- the IN-HOUSE workspace: its own roster, its own seats, the suspended holder
// ---------------------------------------------------------------------------------------
test('in-house Settings: its own live roster, three unsignable seats, and the suspended holder', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await expect(tabStrip(page).locator('button')).toHaveText(INHOUSE_SETTINGS_TABS)

  // --- a DIFFERENT roster, from the same endpoint --------------------------------------------
  // Seven rows, none of them the firm's. The two tenants sharing one memberships table while
  // each token reads only its own is RLS's claim, proven at the wire in api/isolation.spec.ts
  // and rendered here.
  await expect(page.getByTestId('members-table')).toBeVisible()
  await expect(page.getByTestId('member-row')).toHaveCount(SEED_INHOUSE_MEMBERS.length)
  for (const m of SEED_INHOUSE_MEMBERS) {
    await expectRosterRow(page, m)
  }
  for (const m of SEED_FIRM_MEMBERS) {
    await expect(memberRow(page, m.name), `${m.name} must not leak into the in-house roster`).toHaveCount(0)
  }
  // ONE column set, both modes — byte-identical to the firm's above. In-house's Department
  // and Approval position went with the fields; the drawer says why, below.
  await expect(tableHeads(page)).toHaveText(MEMBERS_TABLE_HEADS)

  await expectDisabledWithReason(
    page.getByTestId('members-invite'),
    page.getByTestId('members-invite-reason'),
    UNBACKED.invite,
    'Invite people',
  )

  for (const cell of INHOUSE_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }
  await expect(page.getByTestId('members-unassigned')).toContainText(INHOUSE_UNASSIGNED.notice)

  // --- the suspended holder's row and drawer --------------------------------------------------
  // …0012's status is a SERVER value, and it is what reverses the danger-zone control below.
  // The row's blocking strip and the drawer's amber note used to be asserted here as well;
  // both are derived from the STEPS his seat is named in. internal/demopolicy's one seeded
  // in-house policy names `fin_dir` and …0012 holds `cfo` alone, so neither element renders
  // here either. MembersTable.test.tsx and MemberDrawer.test.tsx own those two claims,
  // against a fixture that can supply the steps.
  const suspended = INHOUSE_SUSPENDED_MEMBER
  await memberRow(page, suspended).getByText(suspended, { exact: true }).click()
  const drawer = page.getByTestId('member-drawer')
  await expect(drawer).toBeVisible()

  // The direction of travel reverses on a suspended row, and the explanation does NOT travel
  // with it: the sentence describes what suspension does, so beside `Reactivate` it would
  // assert the opposite of the button's effect. Its ABSENCE is the assertion.
  await expect(page.getByTestId('member-suspend')).toHaveText('Reactivate')
  await expect(page.getByTestId('member-suspend')).toBeEnabled()
  await expect(drawer, 'the suspend explanation must not appear beside Reactivate').not.toContainText(SUSPEND_EXPLANATION)

  // In-house's fork of the drawer's unbacked field, and the firm's absent.
  await expect(wfSelect(page, 'Department')).toBeDisabled()
  await expect(drawer, 'the department reason').toContainText(UNBACKED.department)
  await expect(drawer).not.toContainText(UNBACKED.clientAccess)
  await expect(page.getByTestId('member-remove')).toBeDisabled()
  await expect(drawer, 'the remove reason').toContainText(UNBACKED.remove)

  await page.getByTestId('member-drawer-close').click()
  await expect(drawer).toHaveCount(0)

  // --- the Roles tab --------------------------------------------------------------------------
  const token = await login(PERSONAS.B)
  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  await expectLiveGridCount(page, token)
  for (const role of SEED_INHOUSE_ROLE_CARDS) {
    await expectRoleCard(page, role)
  }
  await expectRelativeOrder(
    page,
    SEED_INHOUSE_ROLE_CARDS.map((r) => r.title),
  )

  // The other unsignable state that reads as a person rather than as an absence: the only
  // CFO holder is suspended, so the card names him and says nothing more (checked above by
  // expectRoleCard's tone assertion). The word `suspended` is deliberately absent from the
  // rendered text — the tone carries the fact, and appending it would make the card read as
  // an accusation rather than as a rota gap.
  await expect(roleCard(page, 'CFO')).not.toContainText(/suspended/i)

  const banner = page.getByTestId('roles-unassigned')
  await expect(banner).toContainText(INHOUSE_UNASSIGNED.notice)
  await expect(banner).toContainText(INHOUSE_UNASSIGNED.titles)

  const grid = page.getByTestId('roles-grid')
  for (const role of SEED_FIRM_ROLE_CARDS) {
    await expect(grid.getByText(role.title, { exact: true }), `${role.title} must not leak into in-house`).toHaveCount(0)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Test 3's policy, at module scope so the `test.afterAll` sweep can reach it. The id is
// LEARNED from the create POST and never predicted; the per-run name lands only on the first
// Save draft, which is why the sweep goes by id first and by name second.
let createdPolicyId: string | null = null
const POLICY_NAME_SWEEP = /^E2E policy \d+$/
/** What `ctx.createPolicy()` names the row before Save draft renames it. */
const UNSAVED_POLICY_NAME = 'Untitled policy'

// ---------------------------------------------------------------------------------------
// Test 3 -- create a seat, build a policy that points a step at it, delete the seat
// ---------------------------------------------------------------------------------------
// In-house because the mutation needs a person the picker will offer. The holder is …0010
// Tunde Adeyemi, who holds exactly ONE seat (Financial Controller) — so staffing him into a
// second one moves his roster cell from a bare title to the `X +1` form, tooltip and all, and
// the transition is asserted from both ends. Nobody in this workspace holds zero seats; the
// em-dash case is the firm's …0007 (Test 1), who is read-only.
//
// THE POLICY IS BUILT HERE, not seeded. This test used to open `Company approval policy` and
// repoint its first root step. internal/demopolicy now seeds a policy of exactly that name
// onto this IN-HOUSE tenant (never the FIRM one), but its version is SEALED, so the repoint
// is still impossible — and the sweep above must never match its name: DeletePolicy
// deactivates the governed version in the same transaction, which would drop
// awaiting_approval to 0 mid-run with nothing red.
// It also never clicked Save draft, which mattered more than the missing row: `applyEdit` is
// local to the builder (WorkflowBuilder.tsx) while `roleUsage` reads `ctx.policies`, which
// only `savePolicy` patches (App.tsx). So the repoint has to be SAVED or the usage line, the
// delete-confirm sentence and the blocked step below could not read what this test claims
// they read. One Save draft is enough: the PUT sends name, scope and steps together
// (lib/policies.ts).
//
// [topology-never-publishes] holds here as it does in workflows.spec.ts — create, save,
// delete; NEVER publish. A publish seals a version permanently and takes the tenant's ONE
// active slot on a deployment three suites share.
test('in-house: a created role survives a reload, is selectable on a step this test builds, and blocks it once deleted', async ({
  page,
}) => {
  // One sign-in, one reload, two role writes, two policy writes and several list refetches.
  test.setTimeout(180_000)
  const errors = collectErrors(page)

  // Per-run-unique, so nothing else in this run (nor this test's own pre-retry attempt, nor
  // a role a previous run's afterAll sweep failed to remove -- workflow_roles is EXCLUDED
  // from the per-PR reset, resetTables) can collide with it, the title can never be confused
  // with a seeded one, and the afterAll sweep below can find it by prefix alone.
  const stamp = Date.now()
  const title = `E2E seat ${stamp}`
  const desc = 'Signs off the browser journey'
  const holder = 'Tunde Adeyemi'
  const heldSeat = 'Financial Controller'
  // Same per-run-unique discipline for the policy, and the same sweep shape.
  const policyName = `E2E policy ${stamp}`
  // `newNode('approval')` defaults to role `fin_mgr` (lib/workflows.ts), seeded for THIS
  // tenant as `Finance Manager` — so a step appended and left alone renders this, and the
  // repoint below is observable as a change rather than as a first paint.
  const defaultStep = 'Finance Manager must approve'

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')

  // The BEFORE half of the transition, on the default Members tab.
  await expect(page.getByTestId('members-table')).toBeVisible()
  await expectRosterCell(page, { member: holder, text: heldSeat, tooltip: heldSeat })

  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()

  // --- create, and staff in the same commit -----------------------------------------------
  await page.getByTestId('roles-new').click()
  await page.getByTestId('role-modal-name').fill(title)
  await page.getByTestId('role-modal-desc').fill(desc)
  await page.getByTestId('role-modal-search').fill(holder)
  const pickerRows = page.getByTestId('role-modal-member')
  await expect(pickerRows).toHaveCount(1)
  await pickerRows.locator('input[type="checkbox"]').check()
  // The denominator is the SELECTABLE roster — a live search narrows the rows below it and
  // changes neither the count nor the (absent) invited footnote.
  await expect(page.getByTestId('role-modal-count')).toHaveText(`1 of ${INHOUSE_PICKER_SELECTABLE} selected`)
  await expect(page.getByTestId('role-modal-hidden')).toHaveCount(0)
  await page.getByTestId('role-modal-save').click()

  // The flash clears itself after 3s, so it is asserted before anything else.
  await expect(page.getByTestId('roles-flash')).toHaveText(`${title} saved`)
  await expect(page.getByTestId('role-modal')).toHaveCount(0)
  // AC-4, shape (2): containment of the row this test itself just created — no grid-wide
  // literal count.
  const created = roleCard(page, title)
  await expect(created, 'the created card exists exactly once').toHaveCount(1)
  await expect(created.getByText(desc, { exact: true })).toBeVisible()
  // The holder line resolves the LIVE membership row the new role now names.
  await expect(created.getByText(holder, { exact: true })).toBeVisible()
  await expect(created.getByText('not used in any policy')).toBeVisible()
  await expect(created.getByText('1 person')).toBeVisible()

  // --- AC-8: the round trip is real ---------------------------------------------------------
  // Role definitions and staffing are server rows now, so a reload re-fetches rather than
  // wiping them — the one assertion the deleted MOCK-era spec could structurally never make.
  // `view`/the open Settings tab are plain useState (no hash route for Settings), so both are
  // re-driven after the reload; the SESSION survives it untouched (lib/session.ts).
  await page.reload()
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')
  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  const survived = roleCard(page, title)
  await expect(survived, 'the created seat survives a reload').toHaveCount(1)
  await expect(survived.getByText(desc, { exact: true })).toBeVisible()
  await expect(survived.getByText(holder, { exact: true })).toBeVisible()
  await expect(survived.getByText('1 person')).toBeVisible()

  // --- the roster cell of the person staffed into it ---------------------------------------
  // The AFTER half. `addRole` appends and `rolesOfMember` iterates in array order (and the
  // reload above re-fetched in the server's own `created_at` order, which is the same thing),
  // so the seat he already held still renders first and the new one lives in the `+1`.
  await settingsTab(page, 'Members').click()
  await expectRosterCell(page, { member: holder, text: `${heldSeat} +1`, tooltip: `${heldSeat}\n${title}` })

  // --- build the policy whose step will name that seat --------------------------------------
  // The terminal arm first. The h1 and the subtitle render ABOVE the ladder, so `New policy`
  // is clickable before the list has landed, and a create fired into that window races the
  // fetch already in flight.
  await goTo(page, 'Workflows')
  await expect(
    page.locator('[data-testid="policies-list"], [data-testid="policies-empty"]'),
    'the policies fetch must land before this test creates one',
  ).toBeVisible()

  // Armed BEFORE the click. `ctx.createPolicy()` mints the row as `Untitled policy` and the
  // per-run name lands only on Save draft, so between those two writes the id is the only
  // handle the sweep has — contract-approvals.spec.ts's rule, an id is only ever learned,
  // never predicted.
  const createPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/v1/approval-policies'),
  )
  // `createPolicy` also refetches the whole list right after the POST (App.tsx). That GET
  // lands on top of the builder's server copy, so it is awaited here rather than left to race
  // the edits below.
  const createRefetch = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/v1/approval-policies'),
  )
  await page.getByRole('button', { name: 'New policy' }).click()
  const createRes = await createPost
  expect(createRes.ok(), `POST /v1/approval-policies answered HTTP ${createRes.status()}`).toBeTruthy()
  createdPolicyId = ((await createRes.json()) as { id: string }).id
  expect(createdPolicyId, 'the created id is what the afterAll sweep deletes first').toBeTruthy()
  await createRefetch

  // Creating opens the builder on the new policy in the same step (App.tsx).
  const nameInput = page.getByLabel('Policy name')
  await expect(nameInput, 'the create opened the builder').toHaveValue(UNSAVED_POLICY_NAME)
  await nameInput.fill(policyName)
  await expect(nameInput).toHaveValue(policyName)

  // A palette CLICK appends at the root tail AND selects the new node (`append`,
  // WorkflowBuilder.tsx), so the inspector opens on it with no drag at all. The drag handlers
  // are topology/workflows.spec.ts's claim, not this file's.
  await page.locator('button.pf-upcard', { hasText: 'Someone must sign off' }).click()
  await expect(page.getByText(defaultStep, { exact: true }), 'the palette click appended an approval step').toBeVisible()

  // --- the builder's own list of seats ------------------------------------------------------
  // Containment and ORDER, never full equality — the shape the deleted-step assertions at the
  // foot of this test already use, for the same reason. workflow_roles is EXCLUDED from the
  // per-PR reset and its deletes are soft, so a residue `E2E seat` from a dead run adds an
  // option here without being wrong. A CI retry is the sharper case: `retries: 1` re-runs this
  // whole test, and the first attempt's role is still live because its delete never ran — so
  // an equality would make every retry fail by construction.
  const whoApproves = wfSelect(page, 'Who must approve')
  const seatTitles = SEED_INHOUSE_ROLE_CARDS.map((r) => r.title)
  const seatOptions = await whoApproves.locator('option').allTextContents()
  expect(seatOptions, 'every seeded seat is selectable').toEqual(expect.arrayContaining([...seatTitles]))
  expect(seatOptions, 'and so is the seat this test just created').toContain(title)
  const offered = seatTitles.map((t) => seatOptions.indexOf(t))
  expect(offered, 'seeded seats keep their server order').toEqual([...offered].sort((a, b) => a - b))
  await whoApproves.selectOption({ label: title })

  // The resolved holder, in the inspector's own wording — the canvas says `Tunde Adeyemi`,
  // the inspector `Currently: Tunde Adeyemi`, off one resolution.
  await expect(page.getByText(`Currently: ${holder}`, { exact: true })).toBeVisible()
  await expect(page.getByText(`${title} must approve`, { exact: true })).toBeVisible()

  // --- save it, ONCE -------------------------------------------------------------------------
  // Load-bearing, not hygiene: `applyEdit` is local to the builder, and everything below reads
  // `roleUsage`, which is computed off `ctx.policies` — a mirror only `savePolicy` patches. One
  // PUT carries the name and the tree together (lib/policies.ts), so one click is the whole
  // write. The settle is the blocked reason DISAPPEARING, never the 'Saved' flash: that flash
  // lives 1700ms and asserting inside it races a cold gateway, whereas the reason is gone
  // exactly when `dirty` is false, which is exactly when the write landed.
  await expect(page.getByTestId('publish-blocked-reason'), 'an unsaved tree is not publishable').toBeVisible()
  await page.getByRole('button', { name: 'Save draft', exact: true }).click()
  await expect(page.getByTestId('publish-blocked-reason'), 'the policy and its step reached the server').toHaveCount(0)

  // --- delete it, from the affordance the inspector offers ----------------------------------
  // Re-select first, or there is no inspector to offer it: [selection-clears-on-save] — the PUT
  // re-mints every step id, so `save()` drops the held selection (WorkflowBuilder.tsx:200) and the
  // panel falls back to its no-selection state. Clicking the card is the whole re-selection
  // (`onSelect`, WorkflowCanvas.tsx:205), and the settle is the inspector's OWN resolved line, so
  // a missed click fails here instead of timing out on a button that never renders.
  await page.getByText(`${title} must approve`, { exact: true }).click()
  await expect(
    page.getByText(`Currently: ${holder}`, { exact: true }),
    'the inspector re-opened on the saved step',
  ).toBeVisible()
  await page.getByRole('button', { name: 'Manage roles', exact: true }).click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  // The usage line moved the moment the step was repointed: one step, one policy.
  await expect(roleCard(page, title).getByText('1 approval step · 1 policy')).toBeVisible()
  await roleCard(page, title).getByTestId('role-card-edit').click()
  await page.getByTestId('role-delete').click()
  // The confirm names the role AND its usage, so the consequence is stated before the click.
  await expect(page.getByTestId('role-delete-confirm')).toContainText(
    `Delete ${title}? 1 approval step · 1 policy. Those steps will block until you point them somewhere else.`,
  )
  await expect(page.getByTestId('role-delete-cancel')).toBeVisible()
  await page.getByTestId('role-delete-confirmed').click()
  await expect(page.getByTestId('roles-flash')).toHaveText(`${title} deleted`)
  // AC-4, shape (2) again: containment of ABSENCE, not a grid-wide literal count.
  await expect(roleCard(page, title), 'the deleted card is gone').toHaveCount(0)

  // --- the step it pointed at now blocks -----------------------------------------------------
  // No policy was rewritten by the delete: the saved draft still names the key, and the step
  // renders the truth rather than a raw id.
  await goTo(page, 'Workflows')
  await expect(page.getByText(`${DELETED_ROLE_OPTION} must approve`, { exact: true })).toBeVisible()
  await page.getByText(`${DELETED_ROLE_OPTION} must approve`, { exact: true }).click()
  await expect(page.getByText(DELETED_ROLE_LINE, { exact: true })).toBeVisible()
  // The missing key is PREPENDED as an option, or the select would render blank on a step
  // whose seat is gone. Not full equality: a residue role from an earlier run's incomplete
  // afterAll sweep adds its own option here without being wrong.
  const deletedStepOptions = await wfSelect(page, 'Who must approve').locator('option').allTextContents()
  expect(deletedStepOptions[0], 'the missing key is prepended').toBe(DELETED_ROLE_OPTION)
  expect(deletedStepOptions, 'every seeded seat stays selectable').toEqual(expect.arrayContaining([...seatTitles]))
  const seatPositions = seatTitles.map((t) => deletedStepOptions.indexOf(t))
  expect(seatPositions, 'seeded seats keep their server order').toEqual([...seatPositions].sort((a, b) => a - b))

  // --- clean up the policy this test built ---------------------------------------------------
  // Layer one of two. The row's own control (WorkflowsView.tsx), by its aria-label — the same
  // delete topology/workflows.spec.ts drives. It stopPropagation()s before onDelete, so the
  // click cannot fall through to the row's onEdit. Layer two is the afterAll below, for the
  // run that dies before reaching this line.
  await page.getByRole('button', { name: 'All policies' }).click()
  const wfScreen = page.locator('[data-screen-label="Workflow builder"]')
  await wfScreen.getByRole('button', { name: `Delete ${policyName}`, exact: true }).click()
  await expect(wfScreen.getByText(policyName, { exact: true }), 'the policy this test built is gone').toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Best-effort, idempotent-on-purpose safety net for BOTH halves of Test 3's mutation — the
// shape contract-approvals.spec.ts and topology/workflows.spec.ts already use. On the happy
// path Test 3 deletes its own role and its own policy through the UI, so this finds nothing;
// it exists for the run that dies mid-journey. Hooks replay on retry (retries: 1 in CI) and a
// second delete is 404, so a throw here is expected and must never mask a real assertion
// failure.
//
// Both sweeps match by the per-run-unique PREFIX rather than the exact stamp, so they also
// self-heal a prior failed run's residue. Nothing else mints either name in THIS tenant:
// contract-approvals.spec.ts names its rows `Probe Policy <seed>-<n>` and workflows.spec.ts
// its `APPR09 <stamp>`, and both run as tenant A.
//
// If even this sweep dies, the stray is a policy that was never published — Test 3 does not
// publish at all — so it can never hold the tenant's one active slot, and Test 1/2's usage
// assertions are shape-matched (USAGE_SHAPE) precisely so a stray cannot turn them red.
test.afterAll(async () => {
  const token = await login(PERSONAS.B)

  const liveRoles = await listWorkflowRoles(token)
  for (const role of liveRoles.workflow_roles.filter((r) => /^E2E seat \d+$/.test(r.title))) {
    try {
      await deleteWorkflowRole(token, role.key)
    } catch {
      // already deleted, or never created
    }
  }

  // ID FIRST, name second: between `New policy` and the first `Save draft` the row is named
  // `Untitled policy`, which no per-run prefix can match.
  if (createdPolicyId) {
    try {
      await deleteApprovalPolicy(token, createdPolicyId)
    } catch {
      // already deleted by the test itself
    }
  }

  const livePolicies = await listApprovalPolicies(token)
  const strayPolicies = livePolicies.approval_policies.filter(
    (p) => POLICY_NAME_SWEEP.test(p.name) || p.name === UNSAVED_POLICY_NAME,
  )
  for (const stray of strayPolicies) {
    try {
      await deleteApprovalPolicy(token, stray.id)
    } catch {
      // already deleted by the line above
    }
  }
})
