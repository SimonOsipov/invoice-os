// DEMO-06-07 (task-595): the persona switcher, on a deployed build with VITE_DEMO_MODE
// baked in. T6 is deliberately first — its failure message is what tells a flag-off
// deploy apart from a broken spec on the first run (AC-5); T7-T10 assume the flag is on
// and would otherwise time out on an unhelpful locator wait.
//
// Geometry reads go through expect.poll, never a bare boundingBox() — @keyframes popIn
// (styles/platform.css:30-39) animates translateY(4px) -> none over 140ms, and a raced
// read reflects the transform (layout.ts's own stated idiom). scrollHeight/clientHeight
// are untouched by that transform and are read directly.
//
// AC-10: no assertion here reads a member's `status`, `persona-row-lock` or
// `persona-row-reason`, and no assertion counts clickable rows — the api suite can leave
// a seeded row suspended with no reset before this file runs (Stage 2 correction S2-5).
import { test, expect, type Locator, type Page } from '@playwright/test'

import { login, memberships, PERSONAS, createEntity, createInvoice, getInvoice, validateInvoice } from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { collectErrors, signInAs } from '../personaSession'
import { WIDE_WIDTHS, overlapOf, rectsOverlap } from './layout'
import { SEED_FIRM_MEMBERS } from './settingsFixtures'

// F5: an unresolved roster renders persona-surface-loading and no persona-row-list —
// every geometry test must await the row list, not just the popover.
async function openPersonaSwitcher(page: Page): Promise<{ trigger: Locator; popover: Locator; rowList: Locator }> {
  const trigger = page.getByTestId('persona-trigger')
  await trigger.click()
  const popover = page.getByTestId('persona-popover')
  await expect(popover).toBeVisible()
  const rowList = page.getByTestId('persona-row-list')
  await expect(rowList).toBeVisible()
  return { trigger, popover, rowList }
}

function within1px(a: number, b: number): boolean {
  return Math.abs(a - b) <= 1
}

// T6 (AC-5) — must run first. persona-trigger is tree-shaken out of a flag-off bundle
// (bundleAbsence.test.ts), so its absence here is the named diagnosis, not a locator
// timeout.
test('deployed app: the SPA is a demo build (VITE_DEMO_MODE)', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await expect(
    page.getByTestId('persona-trigger'),
    'persona-trigger did not attach. Either VITE_DEMO_MODE is unset on this deploy, or ' +
      'it was set but never independently re-verified — check the prepare-env job log for ' +
      '"app.VITE_DEMO_MODE = true". Every other test in this file assumes the flag is on.',
  ).toBeAttached()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T7 (AC-6) — a short rail (1280x360) cannot fit the whole roster, so the row list must
// scroll internally rather than push the popover off the top of the screen. The floor is
// rowCount * 44 (the row's own padding+content height), never a pixel literal.
test('deployed app: the roster absorbs its own overflow on a short rail (1280x360)', async ({ page }) => {
  const errors = collectErrors(page)
  await page.setViewportSize({ width: 1280, height: 360 })
  await signInAs(page, 'firm')

  const { trigger, popover, rowList } = await openPersonaSwitcher(page)

  const rowCount = await page.getByTestId('persona-row').count()
  expect(rowCount, 'no persona-row rendered — the overflow floor below would be vacuous').toBeGreaterThanOrEqual(3)

  const metrics = await rowList.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
  const floor = rowCount * 44
  expect(
    metrics.scrollHeight,
    `row list scrollHeight (${metrics.scrollHeight}) should be at least ${rowCount} rows x 44px = ${floor}`,
  ).toBeGreaterThanOrEqual(floor)
  expect(
    metrics.scrollHeight,
    `row list should overflow its own box at 360px tall (scrollHeight=${metrics.scrollHeight}, clientHeight=${metrics.clientHeight})`,
  ).toBeGreaterThan(metrics.clientHeight)

  const triggerBox = await trigger.boundingBox()
  if (!triggerBox) throw new Error('persona-trigger rendered no box')

  await expect
    .poll(async () => (await popover.boundingBox())?.y ?? null, {
      message: 'popover top edge never rendered on screen at 1280x360 (y < 0)',
      timeout: 10_000,
    })
    .toBeGreaterThanOrEqual(0)

  await expect
    .poll(
      async () => {
        const box = await popover.boundingBox()
        return box ? box.y + box.height : null
      },
      {
        message: `popover bottom edge should sit at or above the trigger's top edge (trigger top = ${triggerBox.y})`,
        timeout: 10_000,
      },
    )
    .toBeLessThanOrEqual(triggerBox.y + 1)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T8 (AC-7) — the control needle for T7: proves the cap is `calc(100vh - 240px)`
// (PersonaPopover.tsx:84), not a hardcoded pixel value that would either still scroll at
// 1080 or never grow at 360.
test('deployed app: the roster cap tracks the viewport, not a constant', async ({ page }) => {
  const errors = collectErrors(page)
  await page.setViewportSize({ width: 1280, height: 360 })
  await signInAs(page, 'firm')

  const trigger = page.getByTestId('persona-trigger')
  const popover = page.getByTestId('persona-popover')
  const rowList = page.getByTestId('persona-row-list')

  await trigger.click()
  await expect(rowList).toBeVisible()
  const shortMetrics = await rowList.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))

  await trigger.click()
  await expect(popover).toBeHidden()

  await page.setViewportSize({ width: 1280, height: 1080 })
  await trigger.click()
  await expect(rowList).toBeVisible()
  const tallMetrics = await rowList.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))

  expect(
    tallMetrics.clientHeight,
    `row list clientHeight at 1080 tall (${tallMetrics.clientHeight}) should exceed its 360-tall reading (${shortMetrics.clientHeight}) — a hardcoded cap would not grow with the viewport`,
  ).toBeGreaterThan(shortMetrics.clientHeight)
  expect(tallMetrics.scrollHeight, 'row list scrollHeight should be > 0 at 1280x1080').toBeGreaterThan(0)
  expect(
    tallMetrics.scrollHeight,
    `at 1280x1080 the row list should not scroll (scrollHeight=${tallMetrics.scrollHeight}, clientHeight=${tallMetrics.clientHeight})`,
  ).toBe(tallMetrics.clientHeight)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T9 (AC-8) — the popover is position:absolute over the footer, so opening it must move
// neither the trigger nor the sidebar nav, must paint above the nav (not just overlap its
// box), and must stay on screen. elementFromPoint is the only honest oracle for paint
// order: two overlapping boxes say nothing about which one is on top.
test('deployed app: opening the popover moves neither the trigger nor the nav, and paints above it', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  const trigger = page.getByTestId('persona-trigger')
  const nav = page.locator('nav.pf-nav-list')
  const aside = page.locator('aside.pf-sidebar')
  const popover = page.getByTestId('persona-popover')

  for (const width of WIDE_WIDTHS) {
    await page.setViewportSize({ width, height: 1080 })

    const triggerClosed = await trigger.boundingBox()
    const navClosed = await nav.boundingBox()
    const asideClosed = await aside.boundingBox()
    if (!triggerClosed || !navClosed || !asideClosed) {
      throw new Error(`persona-trigger, nav.pf-nav-list or aside.pf-sidebar did not render a box at ${width}px`)
    }

    await trigger.click()
    await expect(page.getByTestId('persona-row-list')).toBeVisible()

    await expect
      .poll(
        async () => {
          const box = await trigger.boundingBox()
          if (!box) return null
          return within1px(box.x, triggerClosed.x) && within1px(box.y, triggerClosed.y) && within1px(box.width, triggerClosed.width) && within1px(box.height, triggerClosed.height)
        },
        { message: `persona-trigger moved by more than 1px when the popover opened at ${width}px (was ${JSON.stringify(triggerClosed)})` },
      )
      .toBe(true)

    await expect
      .poll(
        async () => {
          const box = await nav.boundingBox()
          if (!box) return null
          return within1px(box.x, navClosed.x) && within1px(box.y, navClosed.y) && within1px(box.width, navClosed.width) && within1px(box.height, navClosed.height)
        },
        { message: `nav.pf-nav-list moved by more than 1px when the popover opened at ${width}px (was ${JSON.stringify(navClosed)})` },
      )
      .toBe(true)

    await expect
      .poll(async () => (await popover.boundingBox())?.y ?? null, {
        message: `popover never rendered on screen at ${width}px (y < 0)`,
        timeout: 10_000,
      })
      .toBeGreaterThanOrEqual(0)

    const popoverBox = await popover.boundingBox()
    const navBox = await nav.boundingBox()
    if (!popoverBox || !navBox) throw new Error(`persona-popover or nav.pf-nav-list vanished after opening at ${width}px`)

    expect(popoverBox.width, `popover width (${popoverBox.width}) should exceed aside.pf-sidebar's width (${asideClosed.width}) at ${width}px`).toBeGreaterThan(asideClosed.width)
    expect(
      popoverBox.y + popoverBox.height,
      `popover bottom edge should sit at or above the trigger's top edge (trigger top = ${triggerClosed.y}) at ${width}px`,
    ).toBeLessThanOrEqual(triggerClosed.y + 1)
    expect(rectsOverlap(popoverBox, navBox), `popover and nav.pf-nav-list should overlap at ${width}px (popover=${JSON.stringify(popoverBox)}, nav=${JSON.stringify(navBox)})`).toBe(true)

    const overlap = overlapOf(popoverBox, navBox)
    const center = { x: overlap.x + overlap.width / 2, y: overlap.y + overlap.height / 2 }
    const paintsAboveNav = await page.evaluate(
      ({ x, y }) => document.elementFromPoint(x, y)?.closest('[data-testid="persona-popover"]') != null,
      center,
    )
    expect(paintsAboveNav, `elementFromPoint(${center.x}, ${center.y}) did not resolve inside persona-popover at ${width}px — the popover is not painting above the nav`).toBe(true)

    await trigger.click()
    await expect(popover).toBeHidden()
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T10 (AC-9) — the popover is 296px wide (PersonaPopover.tsx:40); the longest seeded
// name must still ellipsis-fit rather than overflow it. The comparison against the
// shortest rendered name proves scrollWidth tracks text length, not a collapsed box.
test('deployed app: the longest seeded name is not clipped in the roster (296px)', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await openPersonaSwitcher(page)

  const target = SEED_FIRM_MEMBERS.find((m) => m.name === 'Oluwaseyifunmi Adebanjo-Ogunleye')
  if (!target) throw new Error('settingsFixtures.ts no longer lists "Oluwaseyifunmi Adebanjo-Ogunleye" in SEED_FIRM_MEMBERS')

  const names = page.getByTestId('persona-row-name')
  const count = await names.count()
  expect(count, 'no persona-row-name rendered — the comparison below would be vacuous').toBeGreaterThanOrEqual(3)

  const widths = await names.evaluateAll((els) => els.map((el) => ({ text: el.textContent, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth })))

  const targetWidth = widths.find((w) => w.text === target.name)
  if (!targetWidth) throw new Error(`no rendered persona-row-name read "${target.name}" — got: ${JSON.stringify(widths.map((w) => w.text))}`)

  const shortest = Math.min(...widths.filter((w) => w.text !== target.name).map((w) => w.scrollWidth))
  expect(
    targetWidth.scrollWidth,
    `"${target.name}"'s scrollWidth (${targetWidth.scrollWidth}) should exceed the shortest rendered name's (${shortest}) — otherwise this measurement is not tracking text length`,
  ).toBeGreaterThan(shortest)
  expect(
    targetWidth.scrollWidth,
    `"${target.name}" overflows its box: scrollWidth=${targetWidth.scrollWidth}, clientWidth=${targetWidth.clientWidth}`,
  ).toBeLessThanOrEqual(targetWidth.clientWidth)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T11 (AC-2) — the positive twin of bundleAbsence.test.ts:30-37's flag-off sentinels:
// proves the demo copy actually SHIPS in a flag-on build, not just that a testid attaches
// (that is T6's job). Copy transcribed, never imported (e2e/ has no dependency on
// frontend/app/src).
const MARKER_LABEL = 'DEMO BUILD'
const POPOVER_HEADER = 'DEMO ONLY · BECOME ANOTHER MEMBER'
const POPOVER_NOTE =
  "The app reloads with that person's permissions. This is not account switching — no password, no email."

test('deployed app: the flag-on rail carries the demo copy', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await expect(page.locator('aside.pf-sidebar').getByText(MARKER_LABEL, { exact: true })).toBeVisible()

  await openPersonaSwitcher(page)
  await expect(page.getByTestId('persona-popover-header')).toHaveText(POPOVER_HEADER)
  await expect(page.getByTestId('persona-popover')).toContainText(POPOVER_NOTE)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T12 (AC-3, AC-5) — no hard-coded cast: the rendered roster is compared against a live
// GET /v1/memberships read taken in the same test. Also proves no filter silently drops a
// blocked row (unit tests stub props and cannot see that). The cross-tenant half of the
// original plan is dropped: isolation.spec.ts:81-93 already proves it and the component
// renders ctx.members unfiltered, so repeating it here would be decoration.
test("deployed app: the roster is the tenant's live membership list", async ({ page }) => {
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)
  const wire = await memberships(token)
  expect(wire.memberships.length, 'the live wire returned no memberships — the comparison below would be vacuous').toBeGreaterThanOrEqual(3)

  await signInAs(page, 'firm')
  await openPersonaSwitcher(page)

  const rendered = await page.getByTestId('persona-row-name').allTextContents()
  const expected = wire.memberships.map((m) => m.display_name ?? m.email ?? m.user_id)

  expect(rendered.slice().sort(), `rendered persona-row-name set should equal the wire's display_name/email/user_id set`).toEqual(expected.slice().sort())

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T13 (AC-4) — kills the role vanishing from the meta line (it cannot catch a re-cased
// `m.role`: ROLE_LABEL is byte-identical to naive re-casing, lib/members.ts:68-70).
// startsWith, never the whole meta string — the tail carries status (AC-10). Sentence
// case: the row meta uppercases in CSS, not JS, and textContent does not see text-transform.
const ROLE_LABEL: Record<string, string> = { admin: 'Admin', preparer: 'Preparer', reviewer: 'Reviewer' }

test("deployed app: every roster row states the wire's access role", async ({ page }) => {
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)
  const wire = await memberships(token)
  expect(wire.memberships.length, 'the live wire returned no memberships — the loop below would be vacuous').toBeGreaterThanOrEqual(3)

  await signInAs(page, 'firm')
  const { rowList } = await openPersonaSwitcher(page)

  const rows = await rowList.getByTestId('persona-row').all()
  const rowMeta = new Map<string, string>()
  for (const row of rows) {
    const name = await row.getByTestId('persona-row-name').textContent()
    const meta = await row.getByTestId('persona-row-meta').textContent()
    if (name) rowMeta.set(name, meta ?? '')
  }

  for (const m of wire.memberships) {
    const name = m.display_name ?? m.email ?? m.user_id
    const meta = rowMeta.get(name)
    if (meta === undefined) throw new Error(`no persona-row-name rendered "${name}" — rendered: ${[...rowMeta.keys()].join(', ')}`)
    const label = ROLE_LABEL[m.role]
    if (!label) throw new Error(`wire role "${m.role}" for "${name}" is not in the transcribed ROLE_LABEL map`)
    expect(meta.startsWith(label), `"${name}"'s persona-row-meta ("${meta}") should start with "${label}" for wire role "${m.role}"`).toBe(true)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T14 (AC-9) — the two switchers are distinguishable by DIRECTION and by which yields to
// which. Coexistence is not assertable: the company menu is absolutely positioned over the
// full rail (Sidebar.tsx:182) and its height grows with the entity count, so on a suite run
// it covers the persona trigger and a click on it never becomes actionable. Nor is the
// menu's own height a stable oracle -- the api suite creates entities in this tenant before
// topology runs. Both facts below were measured against a deployed PR environment.
test('deployed app: the company switcher opens downward and takes the rail from the persona popover', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await openPersonaSwitcher(page)

  const companyTrigger = page.getByTestId('company-switcher')
  const triggerBox = await companyTrigger.boundingBox()
  expect(triggerBox, 'the company switcher has no box').not.toBeNull()

  await companyTrigger.click()
  const option = page.getByTestId('company-switcher-option').first()
  await expect(option).toBeVisible()

  // Downward, where the persona popover opens upward (T9). Opposite directions are the
  // geometric half of "two switchers, not one control".
  const menuTop = await option.evaluate((el) => (el.parentElement as HTMLElement).getBoundingClientRect().top)
  expect(menuTop, 'the company menu did not open downward').toBeGreaterThanOrEqual(triggerBox!.y + triggerBox!.height)

  // useDismiss's outside mousedown (PersonaFooter.tsx:22) fires; the company switcher has
  // no such handler, which is why only this direction is reachable.
  await expect(page.getByTestId('persona-popover')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T15 (Stage 2 correction S2-5) — persona-row-tick is the one on-screen mark of "this is
// you"; grep -rn persona-row-tick e2e/ was empty before this row. The seat is derived from
// the live wire via PERSONAS.A.subject, never a hardcoded name. No status/lock/reason read.
test('deployed app: exactly one row carries the current-seat tick, and it is the signed-in seat', async ({ page }) => {
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)
  const wire = await memberships(token)
  const seat = wire.memberships.find((m) => m.user_id === PERSONAS.A.subject)
  if (!seat) throw new Error(`no membership row for the signed-in seat ${PERSONAS.A.subject}`)
  const seatName = seat.display_name ?? seat.email ?? seat.user_id

  await signInAs(page, 'firm')
  const { rowList } = await openPersonaSwitcher(page)

  const rows = await rowList.getByTestId('persona-row').all()
  let tickCount = 0
  let tickRowName: string | null = null
  for (const row of rows) {
    const n = await row.getByTestId('persona-row-tick').count()
    tickCount += n
    if (n > 0) tickRowName = await row.getByTestId('persona-row-name').textContent()
  }

  expect(tickCount, `expected exactly one persona-row-tick, found ${tickCount}`).toBe(1)
  expect(tickRowName, `the ticked row's name should be the signed-in seat's ("${seatName}")`).toBe(seatName)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// selectEntity()/goToInvoices()/openInvoiceRow(): transcribed from
// invoice-surfaces.spec.ts (not exported there -- AC-12 bans a cross-spec import).
// Sidebar.tsx testids: company-switcher/company-switcher-option.
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// Scoped to the sidebar nav so the header's "New invoice" CTA can't collide.
async function goToInvoices(page: Page): Promise<void> {
  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()
  await expect(page.getByTestId('invoices-list')).toBeVisible()
}

// Scoped to invoices-list so a batch-submit results panel showing the same number
// can't collide.
async function openInvoiceRow(page: Page, invoiceNumber: string): Promise<void> {
  await page.getByTestId('invoices-list').getByText(invoiceNumber, { exact: true }).click()
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
}

// A clean flat wire body -- mirrors invoice-surfaces.spec.ts's cleanInvoiceFields
// (createInvoice's shape, not a nested validation-engine envelope).
function validInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// handlers.go's two approvalGate sentences, transcribed verbatim.
const STAFFED_TO_STEP_REASON =
  "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
const ADMIN_OR_REVIEWER_REASON = 'Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team.'

// BUG-14: the six blocked-reason nodes are gone and every control in the cluster now
// renders at every status and for every role, disabled rather than absent. Folake's subject
// is db/seed.dev.sql:42 -- her own token is what makes the wire read below HER refusal
// rather than the seat's ([gates-on-the-wire]).
const CLUSTER_CONTROLS = ['view-ubl', 'detail-approve', 'detail-reject', 'edit-toggle', 'revalidate', 'detail-submit'] as const
const FOLAKE_SUBJECT = 'c0000000-0000-0000-0000-000000000003'

/** The right-aligned action column: view-ubl, detail-decision-actions and invoice-actions. */
function actionCluster(page: Page): Locator {
  return page.getByTestId('invoice-actions').locator('xpath=..')
}

// copy.ts templates, transcribed (e2e/ has no dependency on frontend/app/src).
const TOAST_TITLE = 'You are now {full name}'
const TOAST_META = '{ROLE} · APPROVAL QUEUE AND PERMISSIONS RELOADED'
const BUSY_ROLE = 'RELOADING'

type SwitchLatch = { spinnerSeen: boolean; roles: string[] }

// Installed before the switch click: the busy commit (spinner + RELOADING) and the
// completion commit are separated by an awaited >=700ms delay (App.tsx becomePersona),
// so a MutationObserver callback firing at each commit's microtask checkpoint cannot
// miss the busy beat batching away.
async function installSwitchLatch(page: Page): Promise<void> {
  await page.evaluate(() => {
    const latch: SwitchLatch = { spinnerSeen: false, roles: [] }
    ;(window as unknown as { __demoSwitchLatch: SwitchLatch }).__demoSwitchLatch = latch
    const observer = new MutationObserver(() => {
      if (document.querySelector('[data-testid="persona-spinner"]')) latch.spinnerSeen = true
      const roleText = document.querySelector('[data-testid="persona-role"]')?.textContent
      if (roleText) latch.roles.push(roleText)
    })
    observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true })
  })
}

async function readSwitchLatch(page: Page): Promise<SwitchLatch | undefined> {
  return page.evaluate(() => (window as unknown as { __demoSwitchLatch?: SwitchLatch }).__demoSwitchLatch)
}

// T16 (AC-2/3/4) -- the identity oracle: trigger name/role/dot, the toast, and the busy
// beat. The dot is read only once persona-role has left RELOADING (awaited via
// toHaveText('PREPARER') first) -- the busy beat paints the same amber
// (PersonaFooter.tsx:158), so an unguarded read is a false green for standing-in.
test('deployed app: switching identity changes who the app says you are', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  const name = page.getByTestId('persona-name')
  const role = page.getByTestId('persona-role')
  const dot = page.getByTestId('persona-dot')

  await expect(name).toHaveText('Chinedu Okafor')
  await expect(role).toHaveText(ROLE_LABEL.admin.toUpperCase())
  const dotBefore = await dot.getAttribute('style')
  expect(dotBefore, `persona-dot style should carry --status-green-text before any switch, got: ${dotBefore}`).toContain(
    '--status-green-text',
  )

  await installSwitchLatch(page)

  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  await page.getByTestId('persona-row').filter({ hasText: 'Folake Adesina' }).click()

  // Toast asserted first, before the dismiss click -- it is position:fixed over the
  // lower-left of the register and stalls a later click on actionability otherwise.
  await expect(page.getByTestId('persona-toast-title')).toHaveText(TOAST_TITLE.replace('{full name}', 'Folake Adesina'))
  await expect(page.getByTestId('persona-toast-meta')).toHaveText(TOAST_META.replace('{ROLE}', ROLE_LABEL.preparer.toUpperCase()))
  await page.getByTestId('persona-toast-dismiss').click()

  await expect(name).toHaveText('Folake Adesina')
  await expect(role).toHaveText(ROLE_LABEL.preparer.toUpperCase())
  const dotAfter = await dot.getAttribute('style')
  expect(dotAfter, `persona-dot style should carry --status-amber-text once standing in, got: ${dotAfter}`).toContain(
    '--status-amber-text',
  )

  const latch = await readSwitchLatch(page)
  expect(latch?.spinnerSeen, 'persona-spinner was never observed attached during the switch').toBe(true)
  expect(latch?.roles, `persona-role was never observed reading "${BUSY_ROLE}" during the switch`).toContain(BUSY_ROLE)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T17 (AC-5/6/7) -- the story's claim: the server's own refusal reason changes across
// the switch, on an invoice the admin seat could already see disabled with a reason.
// ensureFirmPolicyActive is called here, not in a file-level beforeAll, so a throw
// cannot preempt T6's flag-off diagnosis.
test('deployed app: as a preparer, the server refuses the same approval it allowed the seat to see', async ({ page }) => {
  // Seven API calls, two nav cycles and the switch's own floor; the comparable
  // armed-fixture journey (invoice-surfaces.spec.ts:642) budgets the same.
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)
  await ensureFirmPolicyActive(token)

  const entity = await createEntity(token, { name: `DEMO-06-09 ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-DEMO0609-${Date.now()}`
  const invoice = await createInvoice(token, { entity_id: entity.id, ...validInvoiceFields(invoiceNumber) })
  await validateInvoice(token, invoice.id)

  await signInAs(page, 'firm')
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // BUG-14-04: the seat's refusal, read off the WIRE and asserted against the control's
  // `title` -- BUG-14-02 deleted approve-blocked-reason, so nothing prints it any more
  // ([reason-text-disappears]) and the disabled state is the whole message.
  const seatWire = await getInvoice(token, invoice.id)
  expect(seatWire.approve_blocked_reason, "the seat's own refusal is the AXIS-2 sentence").toBe(STAFFED_TO_STEP_REASON)
  await expect(page.getByTestId('detail-approve')).toBeDisabled()
  await expect(page.getByTestId('detail-approve')).toHaveAttribute('title', seatWire.approve_blocked_reason!)
  await expect(actionCluster(page), 'the cluster must not print the sentence its title carries').not.toContainText(
    seatWire.approve_blocked_reason!,
  )
  expect(
    await page.getByTestId('persona-blocked-note').count(),
    'persona-blocked-note should not render for the seat -- the access-role rung already passed',
  ).toBe(0)

  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  await page.getByTestId('persona-row').filter({ hasText: 'Folake Adesina' }).click()
  await expect(page.getByTestId('persona-toast-title')).toHaveText(TOAST_TITLE.replace('{full name}', 'Folake Adesina'))
  await page.getByTestId('persona-toast-dismiss').click()

  // carryView collapses detail -> invoices and the remount resets activeEntityId to
  // clients[0] (App.tsx:136,199) -- every locator taken above is against a now-stale
  // page; re-select the entity BEFORE the row can be re-opened.
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  await expect(page.getByTestId('detail-approve')).toBeVisible()

  // BUG-14-04, R1b (AC-3/AC-6): the ROLE axis of the story's stable-control-set claim,
  // which the geometry block in invoice-surfaces.spec.ts cannot make -- that block only
  // ever holds the admin seat. The cluster is the SAME six controls for a preparer as for
  // the seat; what changes is which ones answer.
  for (const testid of CLUSTER_CONTROLS) {
    await expect(page.getByTestId(testid), `${testid} must still resolve for a preparer`).toHaveCount(1)
  }
  // The role-gated pair, and only that pair: approvalGate is the one gate in the cluster
  // that reads the caller's role, so a role switch can only move these two. can_edit and
  // can_view_ubl are status- and content-derived (handlers.go, ubl.Missing) and are
  // deliberately NOT asserted disabled here -- a preparer may legitimately edit a validated
  // invoice and read a complete one's UBL, and claiming otherwise would red on correct code.
  for (const testid of ['detail-approve', 'detail-reject'] as const) {
    await expect(page.getByTestId(testid), `${testid} must be disabled for a preparer`).toBeDisabled()
  }

  // Folake's OWN token, not the seat's: the wire read must be the refusal SHE gets. The
  // sentence is on the wire and in `title` and nowhere on screen (BUG-14-02).
  const preparerWire = await getInvoice(await login({ ...PERSONAS.A, subject: FOLAKE_SUBJECT }), invoice.id)
  expect(preparerWire.approve_blocked_reason, "the preparer's own refusal is the access-role sentence").toBe(ADMIN_OR_REVIEWER_REASON)
  await expect(page.getByTestId('detail-approve')).toHaveAttribute('title', preparerWire.approve_blocked_reason!)
  await expect(actionCluster(page), 'the cluster must not print the preparer refusal either').not.toContainText(
    preparerWire.approve_blocked_reason!,
  )

  await expect(page.getByTestId('persona-blocked-note')).toHaveText(
    'Signed in as Folake Adesina — a Preparer. Switch to a Reviewer to act on this step.',
  )

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// T18 (AC-8/9) -- the YOU chip has no testid (bare <span>YOU</span>, MemberParts.tsx),
// scoped to members-table and matched by exact text. member-row is read, never
// clicked -- its onClick opens the member drawer.
test('deployed app: the YOU chip follows the persona, and the return row restores the seat', async ({ page }) => {
  // Two switches, each with its own 700ms floor, across three nav cycles.
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Settings' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()

  const membersTable = page.getByTestId('members-table')
  await expect(membersTable).toBeVisible()
  await expect(membersTable.getByText('YOU', { exact: true })).toHaveCount(1)
  await expect(
    page.getByTestId('member-row').filter({ hasText: 'Chinedu Okafor' }).getByText('YOU', { exact: true }),
  ).toHaveCount(1)

  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  await page.getByTestId('persona-row').filter({ hasText: 'Folake Adesina' }).click()
  await expect(page.getByTestId('persona-toast-title')).toHaveText(TOAST_TITLE.replace('{full name}', 'Folake Adesina'))
  await page.getByTestId('persona-toast-dismiss').click()

  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Settings' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await expect(membersTable).toBeVisible()
  await expect(membersTable.getByText('YOU', { exact: true })).toHaveCount(1)
  await expect(
    page.getByTestId('member-row').filter({ hasText: 'Folake Adesina' }).getByText('YOU', { exact: true }),
  ).toHaveCount(1)

  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  await page.getByTestId('persona-return-row').click()

  await expect(page.getByTestId('persona-name')).toHaveText('Chinedu Okafor')
  await expect(page.getByTestId('persona-role')).toHaveText(ROLE_LABEL.admin.toUpperCase())
  const dotStyle = await page.getByTestId('persona-dot').getAttribute('style')
  expect(dotStyle, `persona-dot should return to --status-green-text once the seat is restored, got: ${dotStyle}`).toContain(
    '--status-green-text',
  )
  await expect(page.getByTestId('persona-toast-title')).toHaveText(TOAST_TITLE.replace('{full name}', 'Chinedu Okafor'))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
