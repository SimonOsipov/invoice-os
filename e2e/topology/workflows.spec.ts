// APPR-09-07 (Backlog task-511): the FIRM persona's Workflows surface, LIVE end to end.
//
// This file's previous header declared the screen MOCK-ONLY and forbade any call into
// e2e/api/ ("if you find yourself adding an API call here, the screen has grown a backend
// and this header is stale: stop and re-plan"). It has. APPR-09 wired the list, the builder,
// Save draft, Publish and delete to internal/approval over five real routes, so the whole
// coverage was re-planned rather than patched: nothing below asserts a frontend constant,
// and policyFixtures.ts is no longer imported here. APPR-09-08 then took its last importer,
// persona-surfaces.spec.ts, and APPR-10 deleted the file.
//
// The IN-HOUSE half of the coverage still lives in persona-surfaces.spec.ts, and is now a
// heading, a tenant-driven subtitle and a terminal-arm settle — seed-independent, because
// either terminal arm satisfies it. internal/demopolicy seeds ONE policy onto the IN-HOUSE
// tenant; the FIRM tenant this file drives still carries none. Nothing in this file signs
// in as in-house.
//
// THIS SPEC CREATES ITS OWN POLICY, THROUGH THE UI. docs/e2e-convention.md:63-74 decides
// that: every spec creates per-run-unique data, acts on rows it created, and asserts
// containment or a live-read comparison rather than a literal count. The approval-policy
// tables are also named there among the ones EXCLUDED from the per-deploy reset, so a
// seeded row would be a permanently mutable shared fixture across three suites with no
// reset between them (internal/platform/db/reset.go:238-250). The one exception,
// internal/demopolicy's policy, is on the IN-HOUSE tenant and sealed; this file drives the
// FIRM one. A seeded row would also prove nothing about the write path, which is the half
// of this screen that is new.
//
// [topology-never-publishes] — create, save, delete; NEVER publish. Publishing on a
// deployment this suite shares seals a version permanently, takes the tenant's ONE active
// slot (approval_policy_versions_one_active is UNIQUE (tenant_id) WHERE is_active) with no
// undo, and arms an approval run per validated invoice on an environment where
// api/perf.spec.ts already created 500. Because the spec never publishes, it never contends
// that index. Assertion 8 is the in-file proof that this is structural rather than a
// promise: Publish is DISABLED while the tree is dirty.
//
// Assertion 8's ORDER is load-bearing and does not read left-to-right. `save()` assigns one
// object to both `working` and `server` (WorkflowBuilder.tsx:203-207), so `dirty` is false
// the instant a save lands and Publish becomes ENABLED. The only window where "Publish is
// disabled" is a true claim is between an edit and its save, so the assertion is interleaved
// into the rename rather than following it.
//
// [reload-is-the-per-tenant-proof] — the old file forbade a reload, because the policy store
// was `useState` and a reload wiped the mutation. That inverts here: the reload IS the
// assertion. It re-fetches from the gateway, so a row that survives it is a row the server
// holds. The client switch is the second half, and the ORDER matters: `switchClient`
// (App.tsx:452-476) does NOT refetch policies — the list re-renders off the surviving
// in-memory mirror — and the reload resets the active client to clients[0]. So the reload's
// own listApprovalPolicies is the server round trip, the switch that follows proves the set
// is not re-keyed per client, and a switch placed BEFORE the reload would simply be undone
// by it. Do not read the switch as a second server read; it is not one.
//
// COUNTS are baseline-relative, never literal: `baseline` is taken off the row locator once
// the ladder settles, and every later count is `baseline` or `baseline + 1`. Not off the
// `N POLICIES` counter — that renders only in the roster arm (WorkflowsView.tsx:107-111) and
// a zero baseline is legal on this deployment.
//
// ISOLATION, by ID first and name second. `ctx.createPolicy()` (App.tsx:1033-1037) mints the
// row named `Untitled policy`; the per-run-unique name lands only on Save draft. A run that
// dies in that window leaves a stray no prefix filter can see. So the create POST is awaited
// with `page.waitForResponse` armed BEFORE the click and its id stashed for the sweep —
// contract-approvals.spec.ts:427-428's rule, "an id is only ever learned, never predicted" —
// and the name sweep (`/^APPR09 \d+$/` plus the literal `Untitled policy`) is the belt.
// Nothing else mints either name: contract-approvals.spec.ts calls its rows
// `Probe Policy <seed>-<n>` (e2e/api/fixtures.ts:191-194).
//
// THE DRAG ASSERTIONS, and what they do not cover.
//
// Driven by this repo's own synthetic-`DataTransfer` idiom (import-wizard.spec.ts:854-863),
// never `locator.dragTo()` — that file's comment records real Playwright drag as flaky here.
// The read-back is `dispatchEvent`'s boolean: `false` means `preventDefault()` ran.
//
// DRAG-1 alone is WEAK. Anything not-prevented returns `true`, so a handler that had been
// deleted outright would pass it. The oracle is DRAG-1 and DRAG-2 as a PAIR — same locator,
// same dispatch, opposite answers — which is why they are never separated.
//
// DRAG-3 needs its own control for the same reason. A freshly created policy has `steps: []`,
// so it has no condition, so it has no branch lane, and "every slot starts with `root#`" is
// then true no matter what `canDrop` does. DRAG-3a appends a Condition (palette click, local
// only) and asserts branch-lane slots DO render under an Approval drag; only then does
// DRAG-3b assert they do NOT under a Condition drag.
//
// NOT covered: a real OS-level mouse drag — no assertion here says the browser's own
// drag-and-drop machinery works, only that the handlers answer correctly when driven. Nor
// `onSlotOver`'s second `canDrop` re-check (WorkflowBuilder.tsx:296), which is unreachable:
// WorkflowCanvas.tsx:142 returns null for exactly the slots that check would reject, so the
// event never has a target to fire on.
//
// Verified line citations, current at 8787b67: WorkflowBuilder.tsx:295 `if (!drag) return`,
// :296 the unreachable re-check, :318 `const pending = drag ?? armed`, :450 the palette's
// `onDragStart`; WorkflowCanvas.tsx:142 the render gate, :173 `Release to place`.
//
// The 18-step journey, in order: sign-in + firm MODE guard · h1 · eyebrow · firm-fork
// subtitle · ladder settles and baseline captured · create opens the builder · rename
// (with 8 interleaved) · Publish disabled while dirty · DRAG-1 · DRAG-2 · DRAG-4 places the
// step · save persists · reload: row present, count baseline+1, DRAFT, Never published, step
// survives · DRAG-3a · DRAG-3b · client switch leaves the list unchanged · delete returns the
// count to baseline · console-error gate.
import { test, expect, type Locator, type Page } from '@playwright/test'

import { deleteApprovalPolicy, listApprovalPolicies, login, PERSONAS } from '../api/client'
import { collectErrors, signInAs } from '../personaSession'
import { assertFillsColumn, WIDE_WIDTHS } from './layout'
import { FIRM_PERSONA } from './targets'

// Per-run-unique, and shaped so the sweep below can recognise it without the id.
const POLICY_NAME = `APPR09 ${Date.now()}`
const NAME_SWEEP = /^APPR09 \d+$/
// What `ctx.createPolicy()` names the row before Save draft renames it.
const UNSAVED_NAME = 'Untitled policy'
// `newNode('approval')` defaults to role `fin_mgr` (lib/workflows.ts:194), seeded for the firm
// tenant as `Engagement Manager` (db/seed.dev.sql:65), so a placed step renders this.
const PLACED_STEP = 'Engagement Manager must approve'
const PUBLISH_DIRTY_REASON = 'Save your changes first — Publish seals the last saved draft.'

// Learned from the create POST, never predicted. Module scope so `afterAll` can reach it.
let createdPolicyId: string | null = null

// sidebar/navButton/goTo: file-local copies of persona-surfaces.spec.ts:69-84, this package's
// stated convention for small Page-driving helpers. navButton is scoped to the aside so it can
// never pick up a same-named control elsewhere on the screen.
function sidebar(page: Page) {
  return page.locator('aside.pf-sidebar')
}

function navButton(page: Page, label: string) {
  return sidebar(page).getByRole('button', { name: label })
}

async function goTo(page: Page, label: string): Promise<void> {
  await navButton(page, label).click()
}

// By the description line, not the name: a chip's accessible name is two spans concatenated,
// and once a step is placed the canvas renders 'Engagement Manager must approve', so a loose
// name match drifts across the journey (WorkflowBuilder.tsx:46-51).
function approvalChip(page: Page): Locator {
  return page.locator('button.pf-upcard', { hasText: 'Someone must sign off' })
}

function conditionChip(page: Page): Locator {
  return page.locator('button.pf-upcard', { hasText: 'Branch on amount' })
}

type DragKind = 'dragstart' | 'dragover' | 'drop' | 'dragend'

/**
 * One synthetic drag event, and its `defaultPrevented` answer inverted: `false` back from
 * `dispatchEvent` means a handler called `preventDefault()`. `cancelable` is what makes that
 * return value mean anything; `bubbles` is what lets React's root delegation see it at all.
 * A `DataTransfer` rides on every one because only `startDrag` reads it
 * (WorkflowBuilder.tsx:274-283) and it costs nothing to hand the others one too.
 */
function dispatchDrag(target: Locator, kind: DragKind): Promise<boolean> {
  return target.evaluate(
    (el, k) => el.dispatchEvent(new DragEvent(k, { bubbles: true, cancelable: true, dataTransfer: new DataTransfer() })),
    kind,
  )
}

test('firm Workflows, live: a policy built through the canvas survives a reload, stays keyed per TENANT, and the canvas refuses an illegal drop', async ({ page }, testInfo) => {
  // One sign-in, one reload, five gateway writes and three list refetches.
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  // WorkflowsView's root div — data-screen-label="Workflow builder" is the ONLY occurrence of
  // that attribute in frontend/app/src. Scoping .pf-row to it matters: RulesView.tsx:67 uses
  // the same class on a different screen.
  const screen = page.locator('[data-screen-label="Workflow builder"]')
  const rows = screen.locator('.pf-row')
  const row = rows.filter({ hasText: POLICY_NAME })
  const h1 = page.getByRole('heading', { level: 1, name: 'Approval policies', exact: true })

  // --- 1. sign in, and guard the MODE before touching the nav ------------------------------
  // Without it a slow or failed persona hand-off surfaces as an opaque timeout further down
  // rather than as "the wrong workspace rendered". Stated as MODE and not tenant on purpose:
  // Sidebar.tsx:42 hardcodes the org label in firm mode, so this string proves the FIRM branch
  // drew, not that /v1/me returned this tenant. The live-tenant proof is signInAs's own /v1/me
  // discriminator, which already ran.
  await signInAs(page, 'firm')
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Workflows')

  // --- 2-4. the list header ----------------------------------------------------------------
  await expect(h1).toBeVisible()
  await expect(screen.getByText('APPROVAL WORKFLOW', { exact: true })).toBeVisible()
  // The copy that forks on `mode` (WorkflowsView.tsx:52-55) — and the only half of that fork no
  // in-house test can reach. The dash is an EM DASH (U+2014); the in-house subtitle has none.
  await expect(
    screen.getByText('Who must sign off before an invoice is transmitted — one set of policies across the firm.', { exact: true }),
  ).toBeVisible()

  // --- 5. settle the ladder, then take the baseline ----------------------------------------
  // Off the ROW locator, never the `N POLICIES` counter: that counter renders only in the
  // roster arm, and a tenant with zero policies is a legal starting state on this deployment.
  // Waiting on either terminal arm is both the settle and the guard — a baseline read while
  // the fetch is still in flight would be 0 and would make every later count a lie.
  await expect(
    page.locator('[data-testid="policies-list"], [data-testid="policies-empty"]'),
    'the policies fetch must land before the baseline is taken',
  ).toBeVisible()
  const baseline = await rows.count()

  // --- 6. create, through the UI ------------------------------------------------------------
  // Armed BEFORE the click: the row is named `Untitled policy` until Save draft lands, so the
  // id is the only handle a sweep has on a run that dies in between.
  const createPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/v1/approval-policies'),
  )
  await screen.getByRole('button', { name: 'New policy' }).click()
  const createRes = await createPost
  expect(createRes.ok(), `POST /v1/approval-policies answered HTTP ${createRes.status()}`).toBeTruthy()
  createdPolicyId = ((await createRes.json()) as { id: string }).id
  expect(createdPolicyId, 'the created id is what the afterAll sweep deletes first').toBeTruthy()

  // Creating opens the builder on the new policy in the same step (App.tsx:1036).
  const nameInput = page.getByLabel('Policy name')
  await expect(nameInput, 'the create opened the builder').toHaveValue(UNSAVED_NAME)

  // --- 7-8. rename, with the Publish gate asserted INSIDE the dirty window ------------------
  await nameInput.fill(POLICY_NAME)
  await expect(nameInput).toHaveValue(POLICY_NAME)
  await expect(
    page.getByRole('button', { name: 'Publish', exact: true }),
    'an unsaved tree is not publishable — this spec structurally cannot seal a version',
  ).toBeDisabled()
  await expect(page.getByTestId('publish-blocked-reason')).toHaveText(PUBLISH_DIRTY_REASON)

  // The blocked reason DISAPPEARING is the settle, not the 'Saved' flash: that flash lives for
  // 1700ms (WorkflowBuilder.tsx:164-168) and asserting inside it races a cold gateway. The
  // reason is gone exactly when `dirty` is false, which is exactly when the write landed.
  await page.getByRole('button', { name: 'Save draft', exact: true }).click()
  await expect(page.getByTestId('publish-blocked-reason'), 'the rename landed').toHaveCount(0)

  // --- 9. DRAG-1: no drag in flight, so the slot is not a target ----------------------------
  // The empty canvas still renders its root slot as a connector line, which is what gives this
  // assertion an element to fire on. Half of an oracle — see DRAG-2 immediately below.
  const rootSlot = page.locator('[data-wf-slot="root#0"]')
  await expect(rootSlot, 'the empty canvas offers a root slot at rest').toBeVisible()
  const idleOver = await dispatchDrag(rootSlot, 'dragover')
  expect(idleOver, 'with no drag in flight onSlotOver returns before preventDefault (WorkflowBuilder.tsx:295)').toBe(true)

  // --- 10. DRAG-2: the same slot, the same dispatch, the opposite answer --------------------
  await dispatchDrag(approvalChip(page), 'dragstart')
  // Gates the React re-render. `dispatchEvent`'s return is a one-shot read that never retries,
  // so every one of them is preceded by a retrying assertion that pins the state it needs.
  await expect(page.getByText('Drop step here').first(), 'the palette drag is in flight').toBeVisible()
  const armedOver = await dispatchDrag(rootSlot, 'dragover')
  expect(armedOver, 'an Approval over a ROOT slot is a legal target, so preventDefault ran').toBe(false)
  await expect(rootSlot, 'and the hinted slot re-renders as the release target').toContainText('Release to place')

  // --- 11. DRAG-4: the drop actually places a step ------------------------------------------
  // The only drag assertion that is new BEHAVIOUR rather than a pre-existing guard.
  await dispatchDrag(rootSlot, 'drop')
  await expect(page.getByText(PLACED_STEP, { exact: true }), 'the dispatched drop placed a step').toBeVisible()
  await expect(page.getByText('Drop step here'), 'onSlotDrop ends the drag').toHaveCount(0)

  // --- 11b. LAYOUT: the delegation reason shares the gutters of the control it explains -------
  // APPR-10-04 AC-9. jsdom applies no stylesheet and runs no layout, so the unit suite can only
  // hold what the component ASKS for; the rendered check is owed here. Two sentences and a
  // picker land in a FIXED 320px column (WorkflowBuilder.tsx:490,
  // `gridTemplateColumns: 'minmax(360px, 1fr) 320px'`), which is where a reason node that
  // over-flows or gets inset would show.
  //
  // RELATIONSHIP, never a raw width — layout.ts's own header: a width assertion passes on the
  // very bug it should catch. The reason node must share the inspector BODY's content gutters
  // with the `Deadline` select above it, at every width, so the two sweeps are compared to each
  // other rather than to a number.
  //
  // PLACEMENT is load-bearing. The drop above auto-selects the placed step (`place()` calls
  // `setSelId`, WorkflowBuilder.tsx:299), so the inspector is already open — and `save()` clears
  // the selection ([selection-clears-on-save], WorkflowBuilder.tsx:210), so this cannot move
  // below step 12.
  const inspectorBody = page.getByTestId('step-inspector-body')
  const reasonFit = await assertFillsColumn(page, page.getByTestId('delegation-blocked-reason'), inspectorBody, 'the delegation reason')
  const deadlineFit = await assertFillsColumn(
    page,
    page.getByLabel('Deadline').locator('xpath=ancestor::label[1]'),
    inspectorBody,
    'the Deadline select',
  )
  expect(reasonFit.length, 'the reason sweep measured nothing, so the comparison below is vacuous').toBe(WIDE_WIDTHS.length)
  expect(deadlineFit.length, 'the control sweep measured nothing, so the comparison below is vacuous').toBe(WIDE_WIDTHS.length)
  for (const [i, fit] of reasonFit.entries()) {
    const ctrl = deadlineFit[i]
    expect(fit.width, 'the two sweeps measured different viewports').toBe(ctrl.width)
    // 1px, not 0: sub-pixel rounding only. A reason node inset inside the control it explains —
    // the defect this exists for — strands whole digits, nowhere near this bound.
    expect(Math.abs(fit.left - ctrl.left), `the reason node's LEFT gutter disagrees with the Deadline select at ${fit.width}px`).toBeLessThanOrEqual(1)
    expect(Math.abs(fit.right - ctrl.right), `the reason node's RIGHT gutter disagrees with the Deadline select at ${fit.width}px`).toBeLessThanOrEqual(1)
  }
  await testInfo.attach('delegation-reason-column-fit.json', {
    body: JSON.stringify({ reason: reasonFit, deadline: deadlineFit }, null, 2),
    contentType: 'application/json',
  })

  // --- 12. save the placed step -------------------------------------------------------------
  await expect(page.getByTestId('publish-blocked-reason'), 'placing a step makes the tree dirty again').toBeVisible()
  await page.getByRole('button', { name: 'Save draft', exact: true }).click()
  await expect(page.getByTestId('publish-blocked-reason'), 'the step reached the server').toHaveCount(0)

  // --- 13. the reload IS the per-tenant proof ------------------------------------------------
  // `?persona=` is stripped at boot and lib/session.ts rehydrates from localStorage, so the
  // session survives (roles.spec.ts:837 already relies on this). `view` and `editingPolicyId`
  // are NOT persisted, so the nav and the builder are re-driven by hand below.
  await page.reload()
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Workflows')
  await expect(page.getByTestId('policies-list')).toBeVisible()
  await expect(row, 'the created policy is on the list exactly once after a reload').toHaveCount(1)
  await expect(rows, 'the list grew by exactly the one row this test created').toHaveCount(baseline + 1)
  await expect(screen.getByText(`${baseline + 1} POLICIES`, { exact: true })).toBeVisible()
  await expect(row, 'a policy that has never been published').toContainText('DRAFT')
  await expect(row, 'policyStanding, off version 1 / draft / no active version').toContainText('Never published')
  await expect(row, 'and the dropped step survived the round trip').toContainText('1 approval')

  // --- 14. DRAG-3a: branch lanes exist and ARE reachable -------------------------------------
  // The control. Without it, 3b below passes on a canvas rendering no lanes at all.
  await page.getByText(POLICY_NAME, { exact: true }).click()
  await expect(page.getByLabel('Policy name'), 'the builder reopened on this policy').toHaveValue(POLICY_NAME)
  await expect(page.getByText(PLACED_STEP, { exact: true }), 'the saved step renders in the reopened builder').toBeVisible()

  // A palette CLICK appends locally and touches no network (WorkflowBuilder.tsx:263-267). It is
  // never saved: leaving via `All policies` discards it, which assertion 16 then re-asserts.
  await conditionChip(page).click()
  await expect(page.getByText('IF TRUE →', { exact: true }), 'the palette click appended a condition').toBeVisible()
  const branchSlots = page.locator('[data-wf-slot*=":then#"], [data-wf-slot*=":else#"]')
  await expect(branchSlots, 'branch lanes have no resting form').toHaveCount(0)

  await dispatchDrag(approvalChip(page), 'dragstart')
  await expect(page.getByText('Drop step here').first(), 'the Approval drag is in flight').toBeVisible()
  await expect(branchSlots, 'an Approval drag DOES open both branch lanes — one slot per empty lane').toHaveCount(2)
  await dispatchDrag(approvalChip(page), 'dragend')
  await expect(page.getByText('Drop step here'), 'dragend clears the drag before the next one starts').toHaveCount(0)

  // --- 15. DRAG-3b: a condition may only sit in the root lane --------------------------------
  await dispatchDrag(conditionChip(page), 'dragstart')
  await expect(page.getByText('Drop step here').first(), 'the Condition drag is in flight').toBeVisible()
  await expect(branchSlots, 'a nested condition has no lane key of its own, so no branch slot renders').toHaveCount(0)
  const slots = await page.locator('[data-wf-slot]').evaluateAll((els) => els.map((el) => el.getAttribute('data-wf-slot') ?? ''))
  expect(slots.length, 'the root lane still offers targets, or the sweep below is vacuous').toBeGreaterThan(0)
  expect(slots.filter((s) => !s.startsWith('root#')), 'every slot rendered under a Condition drag is a ROOT slot').toEqual([])
  await dispatchDrag(conditionChip(page), 'dragend')

  // --- 16. the client switch: the set is keyed per TENANT ------------------------------------
  await page.getByRole('button', { name: 'All policies' }).click()
  await expect(h1).toBeVisible()
  await expect(row, 'the unsaved condition left with the builder').toContainText('1 approval')

  const switcher = page.getByTestId('company-switcher')
  // The switcher button holds exactly two text spans (Sidebar.tsx:147-150): the client's short
  // name (unclassed) and the TIN line (.mono). `span > span` excludes the initials and chevron
  // spans, whose parent is the button itself.
  const switcherName = switcher.locator('span > span:not(.mono)')
  const switcherTin = switcher.locator('span.mono')
  // Re-taken AFTER the reload, which reset the active client to clients[0]. Until the portfolio
  // fetch lands, `active` is emptyClient() — short 'No client', tin '—'. Seeded TINs all begin
  // with a digit, the placeholder does not, so this retrying assertion is both guard and wait.
  await expect(switcherTin, 'the switcher must be on a REAL client before the baseline is taken').toHaveText(/^TIN \d/)
  const beforeName = (await switcherName.innerText()).trim()
  const beforeTin = (await switcherTin.innerText()).trim()

  await switcher.click()
  const options = page.getByTestId('company-switcher-option')
  await expect(options.first()).toBeVisible()
  expect(
    await options.count(),
    'the firm seed must offer >=2 ACTIVE clients to switch between (db/seed.dev.sql seeds 8)',
  ).toBeGreaterThanOrEqual(2)

  // Index 1, never a name: a fresh boot leaves the switcher on clients[0] (portfolio ORDER BY
  // name ASC), so index 1 is always a different client — and the top of the list is the only
  // region guaranteed inside the dropdown, which is position:absolute in a height:100vh;
  // overflow:hidden shell with no max-height. Reading the target's name is not selecting by it:
  // the click stays positional.
  const target = options.nth(1)
  const targetName = (await target.locator('span > span:not(.mono)').innerText()).trim()
  expect(targetName, 'switcher option 1 must differ from the active client, or the click no-ops').not.toBe(beforeName)
  await target.click()
  // The POSITIVE equality first: an equality against the clicked option's own name cannot go
  // green on an empty or half-rendered value the way a bare `not.toHaveText` can. The TIN check
  // then pins IDENTITY rather than display name (business_entities_tenant_tin_uq).
  await expect(switcherName, 'the switcher must now show the client that was clicked').toHaveText(targetName)
  await expect(switcherTin, 'and its TIN line must have moved off the previous client').not.toHaveText(beforeTin)

  // switchClient forces view='dashboard' and clears editingPolicyId, so this navigates back.
  // A per-CLIENT policy store would answer a different set here; the tenant-keyed one does not.
  await goTo(page, 'Workflows')
  await expect(h1).toBeVisible()
  await expect(rows, 'a client switch does not re-key the policy set').toHaveCount(baseline + 1)
  await expect(row, 'and the policy this test created is still in it').toHaveCount(1)

  // --- 17. delete, through the UI ------------------------------------------------------------
  // The row's own control (WorkflowsView.tsx:203-215), reached by its aria-label. It
  // stopPropagation()s before onDelete, so the click cannot fall through to the row's onEdit —
  // if that regressed, the builder would open and the h1 re-asserted below would fail loudly.
  // toHaveCount(baseline) holds in both the roster and the empty arm.
  await screen.getByRole('button', { name: `Delete ${POLICY_NAME}`, exact: true }).click()
  await expect(rows, 'the delete returned the list to its baseline').toHaveCount(baseline)
  await expect(screen.getByText(POLICY_NAME, { exact: true })).toHaveCount(0)
  await expect(h1, 'and the list is still the surface on screen').toBeVisible()

  // --- 18. the console-error gate -------------------------------------------------------------
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Best-effort, idempotent on purpose — the shape roles.spec.ts:990-1023 and
// contract-approvals.spec.ts:410-425 already use. On the happy path the test above deleted its
// own row, so the id delete 404s and the sweep finds nothing; this exists for the run that dies
// mid-journey. Hooks replay on retry (retries: 1 in CI) and a second delete is 404, so a throw
// here is expected and must never mask a real assertion failure.
//
// ID FIRST, name second: between `New policy` and `Save draft` the row is named
// `Untitled policy`, which no per-run prefix can match.
test.afterAll(async () => {
  const token = await login(PERSONAS.A)

  if (createdPolicyId) {
    try {
      await deleteApprovalPolicy(token, createdPolicyId)
    } catch {
      // already deleted, or never created
    }
  }

  const live = await listApprovalPolicies(token)
  const strays = live.approval_policies.filter((p) => NAME_SWEEP.test(p.name) || p.name === UNSAVED_NAME)
  for (const stray of strays) {
    try {
      await deleteApprovalPolicy(token, stray.id)
    } catch {
      // already deleted by the line above
    }
  }
})
