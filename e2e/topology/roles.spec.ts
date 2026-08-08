// Settings › Members and Settings › Roles, driven as BOTH personas, plus the one journey
// that creates a seat, points a step at it and deletes it.
//
// HALF LIVE, HALF MOCK — and the split is the first thing to know before changing anything:
//
//   LIVE. The Members roster is a real backend read: App.tsx fetches GET
//   /api/tenancy/v1/memberships once and shares it with the Members tab, the Roles tab and
//   the Workflows builder. Every NAME, email, access role and status pill below is a
//   membership row, and every holder line on a role card is that row resolved through the
//   mock role store. A failure in those is a backend failure.
//
//   MOCK. Role DEFINITIONS and STAFFING (SEED_FIRM_ROLES / SEED_INHOUSE_ROLES, RoleModal,
//   WorkflowRolePills) have no endpoint at all — the store is App.tsx useState and resets on
//   reload. Real staffing is APPR-02's, per Decision [roles-staffing-stays-mock].
//
// See e2e/topology/roleFixtures.ts, which separates the two kinds of constant.
//
// NO WRITE IS DRIVEN FROM THE BROWSER. Suspend/Reactivate are real, audited PATCHes, and
// this environment is shared and never reset between specs — a status write started here
// could not be restored on a failed assertion the way api/contract-tenancy.spec.ts's
// try/finally does. The controls are asserted ENABLED and correctly labelled; the write
// itself is proven over the wire in that spec instead.
//
// COUNT ASSERTIONS: persona-surfaces.spec.ts bans literal counts over LIVE, tenant-wide
// lists on a never-reset dev DB. The roster is exempt and the exemption is narrow: no
// endpoint mints a membership (there is no invite) and PATCH writes `status` only, so this
// list cannot grow. api/isolation.spec.ts already pins its exact user_id set.
//
// NO reload or page.goto() after the first sign-in in any test below: the ROLE store is
// useState with no persistence, so a reload would wipe the mutation in Test 3 and the test
// would go red for entirely the wrong reason. The same fact is why that mutation needs no
// cleanup and cannot poison the two sweeps: only the SESSION reaches localStorage
// (lib/session.ts), so each signInAs reseeds roles and policies outright — and it never
// touched a database to begin with.
import { test, expect, type Locator, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'
import {
  MEMBERS_TABLE_HEADS,
  MOCK_DELETED_ROLE_LINE,
  MOCK_DELETED_ROLE_OPTION,
  MOCK_DRAWER_ROLE_HELPER,
  MOCK_FIRM_PICKER_SELECTABLE,
  MOCK_FIRM_ROLES,
  MOCK_FIRM_ROSTER_CELLS,
  MOCK_FIRM_TWO_SEAT_STEPS,
  MOCK_FIRM_UNASSIGNED,
  MOCK_INHOUSE_PICKER_SELECTABLE,
  MOCK_INHOUSE_ROLES,
  MOCK_INHOUSE_ROSTER_CELLS,
  MOCK_INHOUSE_SUSPENDED_STEPS,
  MOCK_INHOUSE_UNASSIGNED,
  PROTECTED_ADMIN_NOTE,
  SEED_FIRM_MEMBERS,
  SEED_INHOUSE_MEMBERS,
  SUSPEND_EXPLANATION,
  SUSPENDED_STEPS_NOTE,
  UNBACKED,
  type MockRoleCard,
  type MockRosterCell,
  type SeededMember,
} from './roleFixtures'
import { FIRM_PERSONA, INHOUSE_PERSONA } from './targets'

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
 * One role card, whole. `usage` and `holders` are matched case-INSENSITIVELY: the card
 * uppercases both in CSS, and this assertion must not depend on whether that reaches the
 * text. `desc` is matched exactly — the two-line clamp on it is `-webkit-line-clamp`, which
 * truncates the render and leaves the text intact. `who` is a LIVE display_name.
 */
async function expectRoleCard(page: Page, index: number, role: MockRoleCard): Promise<void> {
  const card = page.getByTestId('role-card').nth(index)
  await expect(card.getByText(role.title, { exact: true }), `role card ${index} title`).toBeVisible()
  await expect(card.getByText(role.desc, { exact: true }), `${role.title} desc`).toBeVisible()
  await expect(card.getByText(role.who, { exact: true }), `${role.title} holder line`).toBeVisible()
  await expect(card.getByText(role.usage), `${role.title} usage line`).toBeVisible()
  await expect(card.getByText(role.holders), `${role.title} holder count`).toBeVisible()
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
async function expectRosterCell(page: Page, cell: MockRosterCell): Promise<void> {
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
test('firm Settings: the live member directory, the seeded seats, and every control that has no endpoint', async ({ page }) => {
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

  // The suspended row carries no blocking warning: the warning is derived from the STEPS a
  // person's roles are named in, and …0007 holds none. Status alone must not raise it.
  await expect(page.getByTestId('member-steps-warning')).toHaveCount(0)

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
  for (const cell of MOCK_FIRM_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }
  // The same sentence the Roles tab renders, from the same derivation — and firm is the mode
  // where this banner never used to appear at all.
  await expect(page.getByTestId('members-unassigned')).toContainText(MOCK_FIRM_UNASSIGNED.notice)

  // --- the drawer: pill toggles, and three controls with nothing behind them ------------------
  const twoSeat = MOCK_FIRM_TWO_SEAT_STEPS
  await memberRow(page, twoSeat.member).getByText(twoSeat.member, { exact: true }).click()
  const drawer = page.getByTestId('member-drawer')
  await expect(drawer).toBeVisible()
  for (const role of MOCK_FIRM_ROLES) {
    const pressed = String(twoSeat.held.includes(role.key))
    await expect(page.getByTestId(`drawer-wfrole-${role.key}`), `${role.title} pill`).toHaveAttribute('aria-pressed', pressed)
  }
  await expect(page.getByTestId('drawer-wfrole-helper')).toHaveText(MOCK_DRAWER_ROLE_HELPER)
  // Every seat this person holds, unioned in ONE traversal: a policy naming two of their
  // roles is one row here, not two.
  await expect(page.getByTestId('member-steps-named')).toHaveText(twoSeat.named)
  await expect(drawer).toContainText(twoSeat.policies)

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
  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_FIRM_ROLES.length)
  for (const [i, role] of MOCK_FIRM_ROLES.entries()) {
    // nth(i) is what pins ORDER — a set-equality check would pass on a shuffled grid. Each
    // card's holder line resolves LIVE names through the mock role store's member ids.
    await expectRoleCard(page, i, role)
  }

  // The unsignable seat, named where the reader already is. Quality Reviewer's card reads
  // `Nobody assigned` above, and the banner is the workspace-level statement of the same
  // fact — it sits above the grid, which a search box cannot change.
  const banner = page.getByTestId('roles-unassigned')
  await expect(banner).toContainText(MOCK_FIRM_UNASSIGNED.notice)
  await expect(banner).toContainText(MOCK_FIRM_UNASSIGNED.titles)

  // Disjointness, firm half. Scoped to the grid and EXACT: `Preparer` is a substring of this
  // mode's own `Invoice Preparer`, so a containment check here would fail for the wrong
  // reason. Two disjoint sets is what "the store is keyed firm/inhouse" means observationally.
  const grid = page.getByTestId('roles-grid')
  for (const role of MOCK_INHOUSE_ROLES) {
    await expect(grid.getByText(role.title, { exact: true }), `${role.title} must not leak into firm`).toHaveCount(0)
  }

  // The create modal opens here but writes nothing — the full create/staff/delete journey is
  // Test 3. The picker's denominator is the SELECTABLE roster: an invited person has no row
  // to tick, and no seeded row is invited, so the footnote that names them never renders.
  await page.getByTestId('roles-new').click()
  await expect(page.getByTestId('role-modal-count')).toHaveText(`0 of ${MOCK_FIRM_PICKER_SELECTABLE} selected`)
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
test('in-house Settings: its own live roster, three seats that cannot be signed, and the suspended holder', async ({ page }) => {
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
  await expect(tableHeads(page)).toHaveText(MEMBERS_TABLE_HEADS)

  // One column set, both modes: in-house's department went with the column, and the drawer
  // says why below.
  await expectDisabledWithReason(
    page.getByTestId('members-invite'),
    page.getByTestId('members-invite-reason'),
    UNBACKED.invite,
    'Invite people',
  )

  for (const cell of MOCK_INHOUSE_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }
  await expect(page.getByTestId('members-unassigned')).toContainText(MOCK_INHOUSE_UNASSIGNED.notice)

  // --- the suspended holder's row and drawer --------------------------------------------------
  // …0012's status is a SERVER value. It is what puts the red pill on the row, the blocking
  // strip under it, and the amber note in the drawer — three surfaces off one column.
  const suspended = MOCK_INHOUSE_SUSPENDED_STEPS
  await expect(page.getByTestId('member-steps-warning')).toHaveText(suspended.rowWarning)

  await memberRow(page, suspended.member).getByText(suspended.member, { exact: true }).click()
  const drawer = page.getByTestId('member-drawer')
  await expect(drawer).toBeVisible()
  await expect(page.getByTestId('member-steps-named')).toHaveText(suspended.named)
  await expect(page.getByTestId('member-drawer-steps-warning')).toHaveText(SUSPENDED_STEPS_NOTE)

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
  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_INHOUSE_ROLES.length)
  for (const [i, role] of MOCK_INHOUSE_ROLES.entries()) {
    await expectRoleCard(page, i, role)
  }

  // The OTHER unsignable state, and the one that reads as a person rather than as an absence:
  // the only CFO holder is suspended, so the card names him and says nothing more. The word
  // is deliberately absent — the tone carries the fact, and appending it would make the card
  // read as an accusation rather than as a rota gap.
  await expect(roleCard(page, 'CFO')).not.toContainText(/suspended/i)

  const banner = page.getByTestId('roles-unassigned')
  await expect(banner).toContainText(MOCK_INHOUSE_UNASSIGNED.notice)
  await expect(banner).toContainText(MOCK_INHOUSE_UNASSIGNED.titles)

  const grid = page.getByTestId('roles-grid')
  for (const role of MOCK_FIRM_ROLES) {
    await expect(grid.getByText(role.title, { exact: true }), `${role.title} must not leak into in-house`).toHaveCount(0)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 3 -- create a seat, point a step at it, delete it
// ---------------------------------------------------------------------------------------
// In-house because the mutation needs a person the picker will offer and a policy with a
// seeded root step to repoint. The holder is …0010 Tunde Adeyemi, who holds exactly ONE seat
// (Financial Controller) — so staffing him into a second one moves his roster cell from a
// bare title to the `X +1` form, tooltip and all, and the transition is asserted from both
// ends. Nobody in this workspace holds zero seats; the em-dash case is the firm's …0007
// (Test 1), who is read-only.
//
// Nothing here reaches a database: role staffing is App.tsx useState.
test('in-house: a created role is staffable, selectable on a step, and blocks that step once deleted', async ({ page }) => {
  const errors = collectErrors(page)

  // Per-run-unique, so two runs against the same environment cannot collide — and so the
  // title can never be confused with a seeded one.
  const stamp = Date.now()
  const title = `E2E seat ${stamp}`
  const desc = 'Signs off the browser journey'
  const holder = 'Tunde Adeyemi'
  const heldSeat = 'Financial Controller'
  // The first root step of the published in-house policy. Repointing it is what gives the
  // deletion below something to break.
  const policyName = 'Company approval policy'
  const seededStep = 'Line Manager must approve'

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
  await expect(page.getByTestId('role-modal-count')).toHaveText(`1 of ${MOCK_INHOUSE_PICKER_SELECTABLE} selected`)
  await expect(page.getByTestId('role-modal-hidden')).toHaveCount(0)
  await page.getByTestId('role-modal-save').click()

  // The flash clears itself after 3s, so it is asserted before anything else.
  await expect(page.getByTestId('roles-flash')).toHaveText(`${title} saved`)
  await expect(page.getByTestId('role-modal')).toHaveCount(0)
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_INHOUSE_ROLES.length + 1)
  const created = roleCard(page, title)
  await expect(created.getByText(desc, { exact: true })).toBeVisible()
  // The holder line resolves the LIVE membership row the mock role now names.
  await expect(created.getByText(holder, { exact: true })).toBeVisible()
  await expect(created.getByText('not used in any policy')).toBeVisible()
  await expect(created.getByText('1 person')).toBeVisible()

  // --- the roster cell of the person staffed into it ---------------------------------------
  // The AFTER half. `addRole` appends and `rolesOfMember` iterates in array order, so the
  // seat he already held still renders first and the new one lives in the `+1`.
  await settingsTab(page, 'Members').click()
  await expectRosterCell(page, { member: holder, text: `${heldSeat} +1`, tooltip: `${heldSeat}\n${title}` })

  // --- the builder's own list of seats ------------------------------------------------------
  await goTo(page, 'Workflows')
  await page.getByText(policyName, { exact: true }).click()
  // Selection TOGGLES, so this card is clicked exactly once.
  await page.getByText(seededStep, { exact: true }).click()
  const whoApproves = wfSelect(page, 'Who must approve')
  const seatTitles = MOCK_INHOUSE_ROLES.map((r) => r.title)
  expect(await whoApproves.locator('option').allTextContents()).toEqual([...seatTitles, title])
  await whoApproves.selectOption({ label: title })

  // The resolved holder, in the inspector's own wording — the canvas says `Tunde Adeyemi`,
  // the inspector `Currently: Tunde Adeyemi`, off one resolution.
  await expect(page.getByText(`Currently: ${holder}`, { exact: true })).toBeVisible()
  await expect(page.getByText(`${title} must approve`, { exact: true })).toBeVisible()

  // --- delete it, from the affordance the inspector offers ----------------------------------
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
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_INHOUSE_ROLES.length)

  // --- the step it pointed at now blocks -----------------------------------------------------
  // No policy was rewritten by the delete: the published policy still names the key, and the
  // step renders the truth rather than a raw id.
  await goTo(page, 'Workflows')
  await expect(page.getByText(`${MOCK_DELETED_ROLE_OPTION} must approve`, { exact: true })).toBeVisible()
  await page.getByText(`${MOCK_DELETED_ROLE_OPTION} must approve`, { exact: true }).click()
  await expect(page.getByText(MOCK_DELETED_ROLE_LINE, { exact: true })).toBeVisible()
  // The missing key is PREPENDED as an option, or the select would render blank on a step
  // whose seat is gone.
  expect(await wfSelect(page, 'Who must approve').locator('option').allTextContents()).toEqual([
    MOCK_DELETED_ROLE_OPTION,
    ...seatTitles,
  ])

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
