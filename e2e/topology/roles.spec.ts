// Settings › Roles — the named approval seats a policy's steps point at, driven as BOTH
// personas, plus the one journey that creates a seat, points a step at it and deletes it.
//
// Why this file exists: Members and Roles have no browser coverage at any layer, and the
// persona axis is what differentiates them — firm and in-house render DISJOINT role sets out
// of one component, and each mode reaches a different unsignable state (firm's seat with
// nobody in it, in-house's seat whose only holder is suspended). The two seeds getting
// crossed, or a per-client re-key, would ship in silence.
//
// MOCK-ONLY: this spec makes ZERO calls into e2e/api/ and creates ZERO fixture rows. Nothing
// it asserts comes from a server — there is no roles endpoint, and the store is App.tsx
// useState. If you find yourself adding an API call here, the screen has grown a backend and
// this header is stale: re-plan the coverage rather than bolting a live read onto fixture
// assertions. See e2e/topology/roleFixtures.ts for the full caveat.
//
// COUNT ASSERTIONS: the literals below count FIXED FRONTEND MOCK sets that no test run can
// grow (SEED_FIRM_ROLES, the picker's selectable roster). persona-surfaces.spec.ts's ban on
// literal counts is about LIVE, tenant-wide lists on a never-reset dev DB, and this file
// reads none.
//
// NO reload or page.goto() after the first sign-in in any test below: every store this file
// touches is useState with no persistence, so a reload would wipe the mutation and the test
// would go red for entirely the wrong reason. The same fact is why the mutation below needs
// no cleanup and cannot poison the two sweeps: only the SESSION reaches localStorage
// (lib/session.ts), so each signInAs reseeds roles, members and policies outright.
import { test, expect, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'
import {
  MOCK_DELETED_ROLE_LINE,
  MOCK_DELETED_ROLE_OPTION,
  MOCK_DRAWER_ROLE_HELPER,
  MOCK_FIRM_PICKER,
  MOCK_FIRM_ROLES,
  MOCK_FIRM_ROSTER_CELLS,
  MOCK_FIRM_TWO_SEAT_STEPS,
  MOCK_FIRM_UNASSIGNED,
  MOCK_INHOUSE_PICKER,
  MOCK_INHOUSE_ROLES,
  MOCK_INHOUSE_ROSTER_CELLS,
  MOCK_INHOUSE_UNASSIGNED,
  MOCK_INVITE_ROLE_HELPER,
  type MockRoleCard,
  type MockRosterCell,
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
 * truncates the render and leaves the text intact.
 */
async function expectRoleCard(page: Page, index: number, role: MockRoleCard): Promise<void> {
  const card = page.getByTestId('role-card').nth(index)
  await expect(card.getByText(role.title, { exact: true }), `role card ${index} title`).toBeVisible()
  await expect(card.getByText(role.desc, { exact: true }), `${role.title} desc`).toBeVisible()
  await expect(card.getByText(role.who, { exact: true }), `${role.title} holder line`).toBeVisible()
  await expect(card.getByText(role.usage), `${role.title} usage line`).toBeVisible()
  await expect(card.getByText(role.holders), `${role.title} holder count`).toBeVisible()
}

/** The roster's Workflow roles cell: `first title +N` on screen, every title in the tooltip. */
async function expectRosterCell(page: Page, cell: MockRosterCell): Promise<void> {
  const target = memberRow(page, cell.member).getByText(cell.text, { exact: true })
  await expect(target, `${cell.member}'s Workflow roles cell`).toHaveCount(1)
  // A roleless row renders `—` and deliberately carries no tooltip at all.
  expect(await target.getAttribute('title'), `${cell.member}'s roles tooltip`).toBe(cell.tooltip || null)
}

// ---------------------------------------------------------------------------------------
// Test 1 -- the FIRM workspace's seats
// ---------------------------------------------------------------------------------------
test('firm Settings › Roles: the seeded seats, the seat nobody holds, and the Members roster column', async ({ page }) => {
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

  await settingsTab(page, 'Roles').click()
  await expect(page.getByTestId('roles-grid')).toBeVisible()
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_FIRM_ROLES.length)
  for (const [i, role] of MOCK_FIRM_ROLES.entries()) {
    // nth(i) is what pins ORDER — a set-equality check would pass on a shuffled grid.
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
  // Test 3. The picker's denominator is the SELECTABLE roster, never its length: an invited
  // person has no row to tick, and the footnote is what says so.
  await page.getByTestId('roles-new').click()
  await expect(page.getByTestId('role-modal-count')).toHaveText(`0 of ${MOCK_FIRM_PICKER.selectable} selected`)
  await expect(page.getByTestId('role-modal-hidden')).toHaveText(MOCK_FIRM_PICKER.hidden)
  // Save is inert on an empty name and nothing else gates it.
  await expect(page.getByTestId('role-modal-save')).toBeDisabled()
  await page.getByTestId('role-modal-cancel').click()
  await expect(page.getByTestId('role-modal')).toHaveCount(0)

  // --- the Members tab speaks in roles ----------------------------------------------------
  await settingsTab(page, 'Members').click()
  await expect(page.getByTestId('members-table')).toBeVisible()
  await expect(page.getByTestId('members-table').getByText('Workflow roles')).toBeVisible()
  for (const cell of MOCK_FIRM_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }
  // The same sentence the Roles tab renders, from the same derivation — and firm is the mode
  // where this banner never used to appear at all.
  await expect(page.getByTestId('members-unassigned')).toContainText(MOCK_FIRM_UNASSIGNED.notice)

  // --- the drawer's pill toggles ----------------------------------------------------------
  const twoSeat = MOCK_FIRM_TWO_SEAT_STEPS
  await memberRow(page, twoSeat.member).getByText(twoSeat.member, { exact: true }).click()
  await expect(page.getByTestId('member-drawer')).toBeVisible()
  for (const role of MOCK_FIRM_ROLES) {
    const pressed = String(twoSeat.held.includes(role.key))
    await expect(page.getByTestId(`drawer-wfrole-${role.key}`), `${role.title} pill`).toHaveAttribute('aria-pressed', pressed)
  }
  await expect(page.getByTestId('drawer-wfrole-helper')).toHaveText(MOCK_DRAWER_ROLE_HELPER)
  // Every seat this person holds, unioned in ONE traversal: a policy naming two of their
  // roles is one row here, not two.
  await expect(page.getByTestId('member-steps-named')).toHaveText(twoSeat.named)
  await expect(page.getByTestId('member-drawer')).toContainText(twoSeat.policies)
  await page.getByTestId('member-drawer-close').click()
  await expect(page.getByTestId('member-drawer')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 2 -- the IN-HOUSE workspace's seats
// ---------------------------------------------------------------------------------------
test('in-house Settings › Roles: its own seats, three that cannot be signed, and the invite modal', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await expect(tabStrip(page).locator('button')).toHaveText(INHOUSE_SETTINGS_TABS)

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

  // --- the Members tab speaks in roles ----------------------------------------------------
  await settingsTab(page, 'Members').click()
  await expect(page.getByTestId('members-table')).toBeVisible()
  await expect(page.getByTestId('members-table').getByText('Workflow roles')).toBeVisible()
  for (const cell of MOCK_INHOUSE_ROSTER_CELLS) {
    await expectRosterCell(page, cell)
  }

  // --- the invite modal offers one seat ---------------------------------------------------
  // Drawn from the live role list rather than a second constant, which is why the option
  // labels below are the mode's own titles and not a fixed eight.
  await page.getByTestId('members-invite').click()
  await expect(page.getByTestId('invite-modal')).toBeVisible()
  const inviteRole = wfSelect(page, 'Workflow role')
  expect(await inviteRole.locator('option').allTextContents()).toEqual(['None', ...MOCK_INHOUSE_ROLES.map((r) => r.title)])
  await expect(page.getByTestId('invite-wfrole-helper')).toHaveText(MOCK_INVITE_ROLE_HELPER)
  await page.getByTestId('invite-cancel').click()
  await expect(page.getByTestId('invite-modal')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 3 -- create a seat, point a step at it, delete it
// ---------------------------------------------------------------------------------------
// In-house because the journey needs a person the modal's picker will offer who holds no seat
// at all: the roster cell renders the first title plus `+N`, so staffing someone who already
// holds one would leave the new title only in a tooltip. Firm has no such candidate — mf1-mf5
// each hold a seat, mf7 is suspended, and mf6 is invited, which the picker excludes outright.
// The `+N` form and its tooltip are covered by Test 1 instead.
test('in-house: a created role is staffable, selectable on a step, and blocks that step once deleted', async ({ page }) => {
  const errors = collectErrors(page)

  // Per-run-unique, so two runs against the same environment cannot collide — and so the
  // title can never be confused with a seeded one.
  const stamp = Date.now()
  const title = `E2E seat ${stamp}`
  const desc = 'Signs off the browser journey'
  const holder = 'Chidi Anyanwu'
  // The first root step of the published in-house policy. Repointing it is what gives the
  // deletion below something to break.
  const policyName = 'Company approval policy'
  const seededStep = 'Line Manager must approve'

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Settings')
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
  // The denominator is the SELECTABLE roster and the footnote names the gap — a live search
  // narrows the rows below it but changes neither.
  await expect(page.getByTestId('role-modal-count')).toHaveText(`1 of ${MOCK_INHOUSE_PICKER.selectable} selected`)
  await expect(page.getByTestId('role-modal-hidden')).toHaveText(MOCK_INHOUSE_PICKER.hidden)
  await page.getByTestId('role-modal-save').click()

  // The flash clears itself after 3s, so it is asserted before anything else.
  await expect(page.getByTestId('roles-flash')).toHaveText(`${title} saved`)
  await expect(page.getByTestId('role-modal')).toHaveCount(0)
  await expect(page.getByTestId('role-card')).toHaveCount(MOCK_INHOUSE_ROLES.length + 1)
  const created = roleCard(page, title)
  await expect(created.getByText(desc, { exact: true })).toBeVisible()
  await expect(created.getByText(holder, { exact: true })).toBeVisible()
  await expect(created.getByText('not used in any policy')).toBeVisible()
  await expect(created.getByText('1 person')).toBeVisible()

  // --- the roster cell of the person staffed into it ---------------------------------------
  await settingsTab(page, 'Members').click()
  await expectRosterCell(page, { member: holder, text: title, tooltip: title })

  // --- the builder's own list of seats ------------------------------------------------------
  await goTo(page, 'Workflows')
  await page.getByText(policyName, { exact: true }).click()
  // Selection TOGGLES, so this card is clicked exactly once.
  await page.getByText(seededStep, { exact: true }).click()
  const whoApproves = wfSelect(page, 'Who must approve')
  const seatTitles = MOCK_INHOUSE_ROLES.map((r) => r.title)
  expect(await whoApproves.locator('option').allTextContents()).toEqual([...seatTitles, title])
  await whoApproves.selectOption({ label: title })

  // The resolved holder, in the inspector's own wording — the canvas says `Chidi Anyanwu`,
  // the inspector `Currently: Chidi Anyanwu`, off one resolution.
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
