// PERSONA-01-04 (Backlog task-273): the FIRM persona's Workflows surface -- the approval
// policy LIST -- and the proof that the policy set is keyed per WORKSPACE, not per client.
//
// Why this file exists: the approval-policy screen (PR #118) has never been exercised at
// any layer. The persona axis is what differentiates it -- firm and in-house render
// different subtitle copy and DISJOINT policy sets out of one component -- so the two seeds
// getting crossed, or the store being re-keyed per client the way custom validation rules
// legitimately are, would ship in silence. The IN-HOUSE half of this coverage lives in
// persona-surfaces.spec.ts, inside the sweep that already puts that list on screen; nothing
// in this file signs in as in-house.
//
// MOCK-ONLY, and deliberately checkable: this spec makes ZERO calls into e2e/api/ and
// creates ZERO fixture rows. It needs none, because nothing it asserts comes from a
// server (lib/workflows.ts:9 -- there is no approvals endpoint). If you find yourself
// adding an API call here, the screen has grown a backend and this header is stale:
// stop and re-plan the coverage rather than bolting a live read onto fixture
// assertions. See e2e/topology/policyFixtures.ts for the full caveat.
//
// COUNT ASSERTIONS: the literal counts below ('3 POLICIES', toHaveCount(2)) are legal ONLY
// because they count a fixed FRONTEND MOCK set that no test run can grow -- SEED_FIRM_POLICIES
// is a module constant, not a table. persona-surfaces.spec.ts:19-23's ban on literal counts
// is about LIVE, tenant-wide lists on a never-reset dev DB, and nothing here reads one. The
// one live list this file touches is the company switcher, and it is driven by INDEX and
// asserted only with a `>= 2` floor for exactly that reason.
//
// SCOPE: the policy LIST only. The builder canvas, drag-and-drop, inspector and simulator
// are pure functions already unit-tested in frontend/app/src/lib/workflows.test.ts, and
// WorkflowsView.tsx:23's ternary is the line -- the row body, the row's `Edit` button and
// `New policy` all cross it by setting ctx.editingPolicyId. The row's DELETE button is the
// only list control that does not (it stopPropagation()s at :131-134), which is what makes
// it the legal mutation for the per-workspace proof below.
import { test, expect, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'
import { MOCK_FIRM_POLICIES, MOCK_INHOUSE_POLICIES } from './policyFixtures'
import { FIRM_PERSONA } from './targets'

// sidebar/navButton/goTo: file-local copies of persona-surfaces.spec.ts:69-84, this
// package's stated convention for small Page-driving helpers (invoice-surfaces.spec.ts:31-34
// records why they are not hoisted). navButton is scoped to the aside so it can never pick
// up a same-named control elsewhere on the screen.
function sidebar(page: Page) {
  return page.locator('aside.pf-sidebar')
}

function navButton(page: Page, label: string) {
  return sidebar(page).getByRole('button', { name: label })
}

async function goTo(page: Page, label: string): Promise<void> {
  await navButton(page, label).click()
}

test('firm Workflows (mock policy fixtures): the policy list renders, and the policy set is keyed per WORKSPACE -- a client switch does not reseed it', async ({ page }) => {
  const errors = collectErrors(page)

  // WorkflowsView's root div (WorkflowsView.tsx:22) -- data-screen-label="Workflow builder"
  // is the ONLY occurrence of that attribute in frontend/app/src, so this is an unambiguous
  // screen anchor. Scoping .pf-row to it matters: RulesView.tsx:67 uses the same class on a
  // different screen.
  const screen = page.locator('[data-screen-label="Workflow builder"]')
  const rows = screen.locator('.pf-row')

  await signInAs(page, 'firm')
  // MODE guard before touching the nav, the same one persona-surfaces.spec.ts's Tests 2/3/4
  // take: without it a slow or failed persona hand-off surfaces as an opaque timeout further
  // down rather than as "the wrong workspace rendered". Stated as MODE and not tenant on
  // purpose ([coverage-honesty]): in firm mode Sidebar.tsx:41 hardcodes the org label rather
  // than reading the fetched tenant, so this string proves the FIRM branch drew (in-house
  // would render 'HONEYWELL GROUP · FINANCE'), not that /v1/me returned this tenant. The
  // live-tenant proof is signInAs's own /v1/me discriminator, which already ran. The weak
  // guard is inherited, not introduced here -- recorded for [PERSONA-01-07].
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Workflows')

  // --- the firm list ---------------------------------------------------------------------
  await expect(page.getByRole('heading', { level: 1, name: 'Approval policies', exact: true })).toBeVisible()
  await expect(screen.getByText('APPROVAL WORKFLOW', { exact: true })).toBeVisible()
  // The copy that forks on `mode` (WorkflowsView.tsx:40-43) -- and the only half of that fork
  // no in-house test can reach. The dash is an EM DASH (U+2014); the in-house subtitle has no
  // dash at all.
  await expect(
    screen.getByText('Who must sign off before an invoice is transmitted — one set of policies across the firm.', { exact: true }),
  ).toBeVisible()
  await expect(screen.getByText(`${MOCK_FIRM_POLICIES.length} POLICIES`, { exact: true })).toBeVisible()
  await expect(rows).toHaveCount(MOCK_FIRM_POLICIES.length)
  for (const [i, p] of MOCK_FIRM_POLICIES.entries()) {
    // nth(i) is what pins ORDER -- a set-equality check would pass on a shuffled list.
    const row = rows.nth(i)
    await expect(row).toContainText(p.name)
    await expect(row).toContainText(p.pill)
    await expect(row).toContainText(p.summaryLine)
    await expect(row).toContainText(`Updated ${p.updated}`)
  }

  // --- disjointness, firm half ------------------------------------------------------------
  // The mirror of persona-surfaces.spec.ts's in-house check, asserted here because this is
  // where the firm list is already on screen. Two disjoint sets is what "the store is keyed
  // firm/inhouse" (lib/workflows.ts:204) means observationally -- and a seed crossed between
  // the two modes is the failure it catches.
  for (const p of MOCK_INHOUSE_POLICIES) {
    await expect(screen.getByText(p.name, { exact: true })).toHaveCount(0)
  }

  // --- mutate FIRST, so the invariance below is DISCRIMINATING ----------------------------
  // A bare "switch client, list unchanged" assertion is hollow here. This app's sibling
  // per-client store reads the SEED for any never-edited client (App.tsx:191-199,
  // customRulesFor in lib/rules.ts) -- so an unmutated list renders identically under the
  // real per-workspace design AND under a per-client one. Deleting a policy first is what
  // separates them.
  //
  // The MIDDLE policy deliberately: the survivors are then not a PREFIX of the seed, so a
  // reseed fails on ORDER as well as on count. Deleting the last row would leave a prefix.
  const deleted = MOCK_FIRM_POLICIES[1]
  const survivors = [MOCK_FIRM_POLICIES[0], MOCK_FIRM_POLICIES[2]]
  // The row's own delete control (WorkflowsView.tsx:127-139), reached by its aria-label. A
  // LIST control, not a builder one -- it stopPropagation()s before onDelete, so the click
  // cannot fall through to the row's onEdit. If that ever regressed the builder would open
  // and the `Approval policies` h1 re-asserted below would fail loudly.
  await screen.getByRole('button', { name: `Delete ${deleted.name}`, exact: true }).click()
  await expect(rows).toHaveCount(survivors.length)
  await expect(screen.getByText(`${survivors.length} POLICIES`, { exact: true })).toBeVisible()
  await expect(screen.getByText(deleted.name, { exact: true })).toHaveCount(0)

  // --- switch the active client -----------------------------------------------------------
  // NO page.goto() or reload between the delete above and the assertions below: the policy
  // store is App.tsx:206 useState with no persistence, so a reload would wipe the mutation
  // and this test would go red for entirely the wrong reason. switchClient() is a state
  // update, not a remount (App.tsx:276-292).
  const switcher = page.getByTestId('company-switcher')
  // The switcher button holds exactly two text spans (Sidebar.tsx:151-154): the client's
  // short name (unclassed) and the TIN line (.mono). `span > span` excludes the initials and
  // chevron spans, whose parent is the button itself. The option dropdown is a SIBLING of the
  // button, not a descendant, so neither locator can drift into it while it is open.
  const switcherName = switcher.locator('span > span:not(.mono)')
  const switcherTin = switcher.locator('span.mono')
  // Wait out the portfolio fetch BEFORE capturing the baseline. signInAs only waits on
  // /v1/me (personaSession.ts's `app` discriminator), not on the entity list, so until that
  // second round trip lands `active` is emptyClient() -- short 'No client', tin '—'
  // (App.tsx:158-161, lib/clients.ts:170-185). A baseline captured there would still make
  // the assertions below go green, on a comparison that means nothing. Seeded TINs all begin
  // with a digit (db/seed.dev.sql), the placeholder does not, so this retrying assertion is
  // both the guard and the wait.
  await expect(switcherTin, 'the switcher must be on a REAL client before the baseline is taken').toHaveText(/^TIN \d/)
  const beforeName = (await switcherName.innerText()).trim()
  const beforeTin = (await switcherTin.innerText()).trim()

  await switcher.click()
  const options = page.getByTestId('company-switcher-option')
  await expect(options.first()).toBeVisible()
  expect(
    await options.count(),
    'the firm seed must offer >=2 ACTIVE clients to switch between (db/seed.dev.sql seeds 21)',
  ).toBeGreaterThanOrEqual(2)

  // Index 1, never a name: signing in leaves the switcher on clients[0] (App.tsx:158-161,
  // portfolio ORDER BY name ASC), so index 1 is always a different client -- and the top of
  // the list is the only region guaranteed inside the dropdown, which is position:absolute in
  // a height:100vh; overflow:hidden shell with no max-height (Sidebar.tsx:158). With 21 active
  // clients, a name-based pick (selectEntity) is precisely what that clipping trap breaks.
  // Reading the target's name is not selecting by it: the click below is still positional.
  const target = options.nth(1)
  const targetName = (await target.locator('span > span:not(.mono)').innerText()).trim()
  expect(
    targetName,
    'switcher option 1 must be a DIFFERENT client from the active one, or the click below no-ops',
  ).not.toBe(beforeName)
  await target.click()

  // Load-bearing: without this the invariance assertion is vacuous whenever the switch
  // silently no-ops. The POSITIVE equality comes first on purpose -- an equality against the
  // clicked option's own name cannot go green on an empty or half-rendered value the way a
  // bare `not.toHaveText` can. The TIN check then pins IDENTITY rather than display name
  // (TINs are unique per tenant: business_entities_tenant_tin_uq).
  await expect(switcherName, 'the switcher must now show the client that was clicked').toHaveText(targetName)
  await expect(switcherTin, 'and its TIN line must have moved off the previous client').not.toHaveText(beforeTin)

  // --- the proof: the MUTATED list survived the switch ------------------------------------
  // switchClient() forces view='dashboard' (App.tsx:278), so this navigates back. A per-client
  // policy store would show 3 rows here, reseeded, with the deleted policy back in position 2
  // -- exactly what App.tsx:191-199's per-client customRuleStore does for a never-edited
  // client. So would a full remount of App. Both go red on the count AND on the order below.
  await goTo(page, 'Workflows')
  await expect(page.getByRole('heading', { level: 1, name: 'Approval policies', exact: true })).toBeVisible()
  await expect(rows).toHaveCount(survivors.length)
  await expect(screen.getByText(`${survivors.length} POLICIES`, { exact: true })).toBeVisible()
  for (const [i, p] of survivors.entries()) {
    await expect(rows.nth(i)).toContainText(p.name)
  }
  await expect(screen.getByText(deleted.name, { exact: true })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
