// The Audit screen, driven as both personas on the deployed build.
//
// Two halves. The CAPABILITY half proves the screen is real: the nav item reaches it in
// both personas, the table draws live rows, a row expands and closes, and the pager
// advances on the cursor the server minted. The LAYOUT half is the one that needs the
// browser at all -- it asserts RELATIONSHIPS, never dimensions, because a numeric width
// assertion passes on the very bug it should catch (BUG-03-05, e2e/topology/layout.ts).
//
// The one deliberate departure from the story's AC text: the scroll assertion runs at
// 900px, not 1280. At 1280 the content box is 1280 - 252 sidebar - 72 page padding = 956,
// which comfortably holds the table's 868px floor -- so an overflow assertion at 1280
// could not go red, and a check that cannot fail is worse than no check. 900 puts the
// content box at 576 and exercises the real degradation.
//
// COUNT ASSERTIONS: the audit log is tenant-wide, append-only and shared with every other
// spec in the run, so no literal row count appears below. Every claim is a relationship,
// a containment, or a comparison against a live read taken in the same test.
import { test, expect, type Locator, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'
import { assertFillsColumn, gaps, rectsOverlap, WIDE_WIDTHS } from './layout'
import { FIRM_PERSONA, INHOUSE_PERSONA } from './targets'

// Mirrors frontend/app/src/components/AuditRow.tsx's AUDIT_TABLE_MIN_WIDTH. Hand-kept:
// e2e/ has no import path into the SPA's source, the same way every other wire mirror in
// this suite is hand-kept.
const TABLE_MIN_WIDTH = 868

// Narrow enough to force the table past its floor -- see the file header.
const SCROLL_WIDTH = 900

async function openAudit(page: Page): Promise<void> {
  await page.getByRole('navigation').getByText('Audit', { exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
}

// The card's own scroll container: AuditTable.tsx's outer div, the element carrying
// overflowX. Reached from the table rather than by class, so a styling change cannot
// silently retarget this at the page.
function scrollContainer(page: Page): Locator {
  return page.getByTestId('audit-table').locator('xpath=..')
}

// Picks the first event-type row in the (already open) event popover whose facet count
// is nonzero -- every persona tenant has recorded something within the default 30-day
// window by the time this suite runs (same assumption as audit_tableDrawsRowsAndARowExpands),
// so a filter applied from this row narrows the table rather than emptying it, which is
// what keeps "every visible row" below from passing vacuously.
async function pickCountedEvent(page: Page): Promise<{ id: string; label: string }> {
  const rows = page.getByTestId('audit-event-panel').locator('[data-testid^="audit-event-row-"]')
  const total = await rows.count()
  expect(total, 'the event popover listed nothing').toBeGreaterThan(0)
  for (let i = 0; i < total; i++) {
    const testId = await rows.nth(i).getAttribute('data-testid')
    const id = (testId ?? '').replace('audit-event-row-', '')
    const countText = await page.getByTestId(`audit-event-count-${id}`).textContent()
    if (Number(countText) > 0) {
      const label = await page.getByTestId(`audit-event-label-${id}`).textContent()
      return { id, label: label ?? '' }
    }
  }
  throw new Error('no event type in the popover carries a nonzero count')
}


// --- AUDIT-08: the evidence bundle drawer -------------------------------------------------
//
// The drawer's state permutations are covered at the unit layer (EvidenceBundleDrawer.test.tsx,
// 67 specs). These four prove the deployed stack integrates: a real archive is streamed, a real
// browser download fires, and the panel's geometry holds at every swept width.
//
// The panel's width is never asserted as a number. It is read live from the viewport instead --
// a width assertion passes on the very bug it should catch (layout.ts's header, BUG-03-05).

// The panel slides in on `pfDrawer` (translateX(24px) -> none, 200ms) and the scrim fades on
// `pfFade`. Geometry read before those settle is the ANIMATION's, not the layout's: the first
// deploy-gate run measured a 24px right gap -- exactly the keyframe's start offset -- and 7.56px
// on the retry. Waiting on the elements' own animations is deterministic where a sleep is not.
async function settleDrawerAnimation(page: Page): Promise<void> {
  for (const id of ['evidence-bundle-drawer', 'evidence-bundle-scrim']) {
    await page
      .getByTestId(id)
      .evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)).then(() => undefined))
  }
}

// The company rows carry an entity uuid this suite cannot know, so they are reached by testid
// prefix -- pickCountedEvent's idiom above. The chosen NAME is read back and every later claim
// compares against it: every entity in ctx.entities is tenant-scoped, so the first row is the
// persona's own company, and asserting a literal name would break the moment another spec's
// entity sorted ahead of it (the drawer sorts by name).
async function openBundleDrawer(page: Page): Promise<string> {
  await page.getByTestId('audit-bundle-open').click({ timeout: 15_000 })
  await expect(page.getByTestId('evidence-bundle-drawer')).toBeVisible({ timeout: 15_000 })

  await page.getByTestId('evidence-company-trigger').click({ timeout: 15_000 })
  const rows = page.getByTestId('evidence-company-panel').locator('[data-testid^="evidence-company-row-"]')
  await expect(rows.first()).toBeVisible({ timeout: 15_000 })
  const chosen = ((await rows.first().textContent()) ?? '').trim()
  expect(chosen, 'the persona owns no company, so the drawer has nothing to bundle').not.toBe('')
  await rows.first().click({ timeout: 15_000 })

  // 30d is the default, but clicking it is what proves the chip commits rather than merely
  // rendering pressed -- and it is the period the confirmation block below is read against.
  await page.getByTestId('evidence-period-30d').click({ timeout: 15_000 })
  await expect(page.getByTestId('evidence-confirm-block')).toBeVisible({ timeout: 30_000 })
  return chosen
}

test.describe('Audit screen', () => {
  test('audit_navItemPresentForBothPersonas', async ({ page }) => {
    test.setTimeout(120_000)

    for (const p of [FIRM_PERSONA, INHOUSE_PERSONA]) {
      await signInAs(page, p.param)
      await openAudit(page)
      // The SUBTITLE, not a bare name match: the tenant name also sits in the header and
      // the switcher, so `getByText(tenantName)` would pass without this screen drawing.
      await expect(page.getByTestId('audit-subtitle')).toContainText(p.tenantName)
      // The immutability strip is unconditional -- it states a database guarantee, which
      // holds on an empty workspace exactly as it does on a full one.
      await expect(page.getByTestId('audit-immutability-strip')).toBeVisible()
      await expect(page.getByTestId('audit-immutability-strip')).toContainText('append-only')
    }
  })

  test('audit_tableDrawsRowsAndARowExpands', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)

    // Every persona tenant has recorded something by the time this suite runs (the seed
    // alone writes audit rows), so the table rung is the expected one. If this ever fails
    // on an empty log, the new-workspace state is the honest answer and the assertion
    // below names which rung actually drew.
    await expect(page.getByTestId('audit-table'), 'expected the table rung, not an empty state').toBeVisible()
    const rows = page.getByTestId('audit-row')
    await expect(rows.first()).toBeVisible()

    // Skeleton rows share the table's geometry but must be GONE once data lands -- their
    // presence beside real rows would mean the loading rung never cleared.
    await expect(page.getByTestId('audit-skeleton-row')).toHaveCount(0)

    // Disclosure: closed, open, closed. Only one panel exists at a time.
    await expect(page.getByTestId('audit-expansion')).toHaveCount(0)
    await rows.first().click()
    await expect(page.getByTestId('audit-expansion')).toHaveCount(1)
    await expect(page.getByTestId('audit-event-identifier')).toBeVisible()

    const second = rows.nth(1)
    if (await second.isVisible()) {
      await second.click()
      await expect(page.getByTestId('audit-expansion'), 'opening a second row must close the first').toHaveCount(1)
    }
  })

  test('audit_pagerAdvancesOnTheServersCursor', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-pager')).toBeVisible()

    const next = page.getByTestId('audit-pager-next')
    const prev = page.getByTestId('audit-pager-prev')
    // Page one, always: prev has no cursor to go back to.
    await expect(prev).toBeDisabled()

    if (await next.isEnabled()) {
      // The first ROW's text, not `audit-event-id`: that testid lives inside an expansion,
      // and nothing is expanded here, so reaching for it hangs until the test budget is
      // gone (found on the deploy gate, PR #180).
      const firstRowText = await page.getByTestId('audit-row').first().innerText()
      const requests: string[] = []
      page.on('request', (r) => {
        if (r.url().includes('/v1/audit-log')) requests.push(r.url())
      })
      await next.click({ timeout: 15_000 })
      await expect(prev, 'advancing a page must arm prev').toBeEnabled()
      // The forward-only reader mints cursors; an offset here would mean the screen fell
      // back to a pagination the endpoint does not implement.
      await expect
        .poll(() => requests.some((u) => u.includes('cursor=')), { message: 'next must send a cursor' })
        .toBe(true)
      expect(requests.every((u) => !u.includes('offset='))).toBe(true)

      // Page two is a different page: forward-only keyset means the rows must have moved.
      await expect
        .poll(async () => (await page.getByTestId('audit-row').first().innerText()) !== firstRowText, {
          message: 'advancing a page must change the rows on screen',
          timeout: 15_000,
        })
        .toBe(true)

      await prev.click({ timeout: 15_000 })
      await expect(prev).toBeDisabled()
      // Back on page one, the client-held cursor stack returned the original rows.
      await expect
        .poll(async () => (await page.getByTestId('audit-row').first().innerText()) === firstRowText, {
          message: 'prev must land back on the first page',
          timeout: 15_000,
        })
        .toBe(true)
    }
  })

  test('audit_tableScrollsCardNotBody', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await page.setViewportSize({ width: SCROLL_WIDTH, height: 1080 })
    await expect(page.getByTestId('audit-table')).toBeVisible()

    const card = scrollContainer(page)
    // The card overflows: this is the "degrades by scrolling" half.
    await expect
      .poll(async () => card.evaluate((el) => el.scrollWidth - el.clientWidth), {
        message: `the card must overflow horizontally at ${SCROLL_WIDTH}px`,
        timeout: 10_000,
      })
      .toBeGreaterThan(0)

    // And the page does NOT: this is the half that stops the h1 and the strip being
    // dragged sideways with the table.
    const bodyOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(bodyOverflow, 'the page body must not scroll sideways when the table does').toBeLessThanOrEqual(1)
  })

  test('audit_rowHonoursMinWidthAtEveryWidth', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-row').first()).toBeVisible()

    const measured: Array<{ width: number; row: number; head: number }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const row = page.getByTestId('audit-row').first()
      const head = page.getByTestId('audit-table-head')
      await expect
        .poll(async () => (await row.boundingBox())?.width ?? null, {
          message: `the row must render at ${width}px`,
          timeout: 10_000,
        })
        .toBeGreaterThanOrEqual(TABLE_MIN_WIDTH)
      const [rowBox, headBox] = await Promise.all([row.boundingBox(), head.boundingBox()])
      if (rowBox && headBox) measured.push({ width, row: rowBox.width, head: headBox.width })

      // Attached, never compared to a baseline -- visual regression is banned here
      // (docs/e2e-convention.md). This is the rendered half of
      // [layout-needs-rendered-verification]: BUG-03-05 shipped 32% dead space with its
      // numeric assertion passing, and a human eye on the render is what caught it.
      await test.info().attach(`audit-${width}`, { body: await page.screenshot(), contentType: 'image/png' })
    }

    // The sweep is only evidence if it ran: an empty collection would pass every loop
    // assertion above by never entering one.
    expect(measured.length, 'the width sweep measured nothing').toBe(WIDE_WIDTHS.length)
    // Head and body draw the same grid, so a column boundary cannot drift between them.
    for (const m of measured) {
      expect(Math.abs(m.row - m.head), `head and row must share a width at ${m.width}px`).toBeLessThanOrEqual(1)
    }
    await test.info().attach('audit-row-widths', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  })

  test('audit_guttersAreSymmetric', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-table')).toBeVisible()

    const measured: Array<{ width: number; left: number; right: number }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [table, container] = await Promise.all([
        page.getByTestId('audit-table').boundingBox(),
        scrollContainer(page).boundingBox(),
      ])
      if (!table || !container) continue
      const g = gaps(table, container)
      measured.push({ width, left: g.left, right: g.right })
      // Both sides, never one: a right-only reading calls a left-pinned card a perfect
      // fit (layout.ts's own note on why gaps() returns a pair).
      expect(Math.abs(g.left - g.right), `gutters must be symmetric at ${width}px`).toBeLessThanOrEqual(2)
    }
    expect(measured.length, 'the gutter sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-gutters', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  })

  test('audit_eventFilterNarrowsTheServerRequest', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-row').first()).toBeVisible({ timeout: 15_000 })

    const requests: string[] = []
    page.on('request', (r) => {
      if (r.url().includes('/v1/audit-log')) requests.push(r.url())
    })

    await page.getByTestId('audit-event-trigger').click({ timeout: 15_000 })
    await expect(page.getByTestId('audit-event-panel')).toBeVisible({ timeout: 15_000 })
    const picked = await pickCountedEvent(page)
    await page.getByTestId(`audit-event-row-${picked.id}`).click({ timeout: 15_000 })
    await page.keyboard.press('Escape')

    await expect
      .poll(() => requests.some((u) => new URL(u).searchParams.getAll('event').includes(picked.id)), {
        message: 'the event filter must reach the wire as event=',
        timeout: 15_000,
      })
      .toBe(true)

    await expect(page.getByTestId('audit-row').first(), 'the filtered table must draw at least one row').toBeVisible({ timeout: 15_000 })
    const whats = page.getByTestId('audit-what')
    const shown = await whats.count()
    expect(shown, 'the filtered table drew nothing').toBeGreaterThan(0)
    for (let i = 0; i < shown; i++) {
      await expect(whats.nth(i)).toHaveText(picked.label)
    }
  })

  test('audit_filterCardSurvivesAFilterChange', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    const card = page.getByTestId('audit-filter-card')
    await expect(card).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('audit-row').first()).toBeVisible({ timeout: 15_000 })

    await page.getByTestId('audit-date-trigger').click({ timeout: 15_000 })
    await expect(page.getByTestId('audit-date-panel')).toBeVisible({ timeout: 15_000 })

    // Bounds the sampling window on the response the click itself causes, not a fixed
    // sleep -- a fixed sleep would either miss a fast flight or waste the run on a slow one.
    const responseDone = page.waitForResponse((res) => res.url().includes('/v1/audit-log'), { timeout: 15_000 })
    const clickDone = page.getByTestId('audit-date-preset-7d').click({ timeout: 15_000 })

    let settled = false
    void responseDone.then(() => {
      settled = true
    })
    const samples: boolean[] = []
    while (!settled) {
      samples.push(await card.isVisible())
    }
    await clickDone

    expect(samples.length, 'the poll never sampled during the in-flight window').toBeGreaterThan(0)
    expect(samples.every(Boolean), 'the filter card must stay visible through a filter change').toBe(true)
  })

  test('audit_pillRemovalDropsTheParam', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-row').first()).toBeVisible({ timeout: 15_000 })

    const requests: string[] = []
    page.on('request', (r) => {
      if (r.url().includes('/v1/audit-log')) requests.push(r.url())
    })

    await page.getByTestId('audit-event-trigger').click({ timeout: 15_000 })
    await expect(page.getByTestId('audit-event-panel')).toBeVisible({ timeout: 15_000 })
    const picked = await pickCountedEvent(page)
    await page.getByTestId(`audit-event-row-${picked.id}`).click({ timeout: 15_000 })
    await page.keyboard.press('Escape')

    // Instrument floor: an absence reading below is worthless if the recorder never saw a
    // live request, so prove it caught the filter reaching the wire before trusting it.
    await expect
      .poll(() => requests.some((u) => u.includes('/v1/audit-log') && u.includes('event=')), {
        message: 'the request recorder never observed an event= request -- the floor did not hold',
        timeout: 15_000,
      })
      .toBe(true)

    const pill = page.getByTestId(`audit-pill-event:${picked.id}`)
    await expect(pill).toBeVisible({ timeout: 15_000 })
    requests.length = 0
    await pill.click({ timeout: 15_000 })

    await expect
      .poll(() => requests.length > 0, { message: 'removing the pill must fire a new request', timeout: 15_000 })
      .toBe(true)
    expect(requests.every((u) => !u.includes('event='))).toBe(true)
  })

  test('audit_exportDownloadsADatedCsv', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-row').first()).toBeVisible({ timeout: 15_000 })

    const exportButton = page.getByTestId('audit-export')
    await expect(exportButton).toBeEnabled({ timeout: 15_000 })

    const downloadEvent = page.waitForEvent('download', { timeout: 15_000 })
    await exportButton.click({ timeout: 15_000 })
    const download = await downloadEvent

    expect(await download.failure(), 'the export download must complete').toBeNull()
    expect(download.suggestedFilename()).toMatch(/^audit-log-\d{4}-\d{2}-\d{2}\.csv$/)
  })

  test('audit_exportIsDisabledWhenNothingMatches', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await expect(page.getByTestId('audit-row').first()).toBeVisible({ timeout: 15_000 })

    // A fresh random string cannot appear in any event, payload value, actor name or
    // company name -- searchFragment's four routes are all substring matches on real data.
    const nonce = crypto.randomUUID()
    await page.getByTestId('audit-search-trigger').click({ timeout: 15_000 })
    const input = page.getByTestId('audit-search-input')
    await input.fill(nonce, { timeout: 15_000 })
    await input.press('Enter', { timeout: 15_000 })
    await page.keyboard.press('Escape')

    await expect(page.getByTestId('audit-empty-by-filter'), 'a nonce search must match nothing').toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('audit-export')).toBeDisabled({ timeout: 15_000 })
    await expect(page.getByTestId('audit-export-reason')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('audit-export-reason')).toContainText('No rows match')
  })

  test('audit_filterCardFillsTheContentColumn', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    const card = page.getByTestId('audit-filter-card')
    await expect(card).toBeVisible({ timeout: 15_000 })

    // Compared against the table's own scroll container, not the window: at every width in
    // WIDE_WIDTHS the table sits well above its 868px floor (see the file header), so that
    // container's edges are the content column's real edges here.
    const fit = await assertFillsColumn(page, card, scrollContainer(page), 'audit-filter-card')
    expect(fit.length, 'the fill sweep measured nothing').toBe(WIDE_WIDTHS.length)

    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await test.info().attach(`audit-filter-card-fill-${width}`, { body: await page.screenshot(), contentType: 'image/png' })
    }
    await test.info().attach('audit-filter-card-fit', { body: JSON.stringify(fit, null, 2), contentType: 'application/json' })
  })

  test('audit_exportControlIsRightAlignedToTheCard', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    const card = page.getByTestId('audit-filter-card')
    const button = page.getByTestId('audit-export')
    await expect(card).toBeVisible({ timeout: 15_000 })
    await expect(button).toBeVisible({ timeout: 15_000 })

    // 2px: both elements are flush against the same content-column edge (the card is
    // full-width, the button's row is a space-between flex whose last item abuts the same
    // edge), so this is sub-pixel rounding across two independent boundingBox() reads, not
    // a scrollbar gutter -- assertFillsColumn's 24px default budgets for that gutter, which
    // does not apply to a same-edge comparison like this one.
    const SLACK_PX = 2

    const measured: Array<{ width: number; drift: number }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [cardBox, buttonBox] = await Promise.all([card.boundingBox(), button.boundingBox()])
      if (!cardBox || !buttonBox) continue
      const drift = Math.abs(cardBox.x + cardBox.width - (buttonBox.x + buttonBox.width))
      measured.push({ width, drift })
      expect(drift, `export control must align with the filter card's right edge at ${width}px`).toBeLessThanOrEqual(SLACK_PX)
    }
    expect(measured.length, 'the alignment sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-export-alignment', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  })

  test('audit_cardAndStripNeverOverlap', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    const strip = page.getByTestId('audit-immutability-strip')
    const card = page.getByTestId('audit-filter-card')
    await expect(strip).toBeVisible({ timeout: 15_000 })
    await expect(card).toBeVisible({ timeout: 15_000 })

    const measured: Array<{ width: number; overlap: boolean }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [stripBox, cardBox] = await Promise.all([strip.boundingBox(), card.boundingBox()])
      if (!stripBox || !cardBox) continue
      const overlap = rectsOverlap(stripBox, cardBox)
      measured.push({ width, overlap })
      expect(overlap, `the strip and the filter card must not overlap at ${width}px`).toBe(false)
    }
    expect(measured.length, 'the overlap sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-strip-card-overlap', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  })

  test('audit_openPopoverDoesNotScrollTheBody', async ({ page }) => {
    test.setTimeout(120_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await page.setViewportSize({ width: 1280, height: 1080 })
    await expect(page.getByTestId('audit-filter-card')).toBeVisible({ timeout: 15_000 })

    await page.getByTestId('audit-company-trigger').click({ timeout: 15_000 })
    await expect(page.getByTestId('audit-company-panel')).toBeVisible({ timeout: 15_000 })

    const bodyOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(bodyOverflow, 'an open popover must not widen the page past its own scrollbar').toBeLessThanOrEqual(1)
  })

  // AUDIT-08 AC-1/2/3/9/10. The one deployed-build oracle for the whole
  // fetch -> stream -> blob -> object-URL -> download path.
  test('audit_bundleFlowBuildsAndDownloadsAZip', async ({ page }) => {
    test.setTimeout(180_000)
    // First statement, before signInAs: a listener registered after navigation misses
    // everything that already fired. This is the file's FIRST console gate.
    const errors = collectErrors(page)

    await signInAs(page, 'firm')
    await openAudit(page)
    const company = await openBundleDrawer(page)

    // The block states what the SERVER echoed, so these are the facts the bundle is built
    // from -- not the local selection.
    await expect(page.getByTestId('evidence-confirm-company')).toContainText(company)
    const statedFilename = ((await page.getByTestId('evidence-confirm-filename').textContent()) ?? '').trim()
    expect(statedFilename).toMatch(/^ASComply_evidence_.+_\d{8}_\d{8}\.zip$/)

    // Both waits are armed BEFORE the click that causes them.
    const bundleResponse = page.waitForResponse(
      (r) => new URL(r.url()).pathname.endsWith('/evidence-bundle') && r.request().method() === 'GET',
      { timeout: 120_000 },
    )
    await page.getByTestId('evidence-bundle-prepare').click({ timeout: 15_000 })

    const res = await bundleResponse
    expect(res.status(), 'the bundle request must succeed').toBe(200)
    const headers = res.headers()
    expect(headers['content-type']).toBe('application/zip')
    expect(headers['x-content-type-options']).toBe('nosniff')
    // Unquoted by construction, not by luck: bundleFilename collapses every non-alphanumeric
    // run to '-' (internal/archive/request.go:21,66-76), so mime.FormatMediaType always sees
    // an RFC-2045 token and never quotes it -- whatever the company is called.
    expect(headers['content-disposition']).toMatch(/^attachment; filename=[A-Za-z0-9._-]+$/)

    await expect(page.getByTestId('evidence-ready')).toBeVisible({ timeout: 120_000 })
    // The archive's own name, which need not equal the preview's guess.
    const readyFilename = ((await page.getByTestId('evidence-ready-filename').textContent()) ?? '').trim()
    expect(readyFilename).toMatch(/^ASComply_evidence_.+_\d{8}_\d{8}\.zip$/)

    const downloadEvent = page.waitForEvent('download', { timeout: 60_000 })
    await page.getByTestId('evidence-ready-download').click({ timeout: 15_000 })
    const download = await downloadEvent
    expect(await download.failure(), 'the bundle download must complete').toBeNull()
    expect(download.suggestedFilename()).toBe(readyFilename)

    // Its own testid, so it cannot be confused with the CSV export's toast.
    await expect(page.getByTestId('evidence-bundle-toast')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('audit-export-toast')).toHaveCount(0)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  // L1, L3, L4, L7, L8. Relationships only -- the panel's own width never appears as a number.
  test('audit_bundleDrawerGeometry', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await page.getByTestId('audit-bundle-open').click({ timeout: 15_000 })

    const panel = page.getByTestId('evidence-bundle-drawer')
    const scrim = page.getByTestId('evidence-bundle-scrim')
    const body = page.getByTestId('evidence-bundle-body')
    const footer = page.getByTestId('evidence-bundle-footer')
    const title = page.getByTestId('evidence-bundle-title')
    // The header band carries no testid; it is the panel's first child. Asserted to contain
    // the title so a structure change fails here rather than silently measuring the wrong box.
    const header = panel.locator(':scope > div').first()
    // Direct children of their bands, so each one's inset from its band IS that band's padding.
    const bodyContent = page.getByTestId('evidence-company-helper')
    const footerContent = page.getByTestId('evidence-bundle-cancel')
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await settleDrawerAnimation(page)
    await expect(header.getByTestId('evidence-bundle-title')).toHaveCount(1)

    // 2px, not assertFillsColumn's 24: every comparison below is against the same edge or
    // the same box, so this budgets sub-pixel rounding across independent boundingBox()
    // reads, never a scrollbar gutter.
    const SLACK_PX = 2

    const measured: Array<{
      width: number
      layoutWidth: number
      rightGap: number
      leftEdge: number
      bandDrift: number
      insetDrift: number
    }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      // A `position:fixed; right:0` element is flush to the LAYOUT viewport, which excludes
      // the classic scrollbar; setViewportSize's width includes it. Measuring against the
      // width we asked for compares two different edges and fails by the gutter -- which is
      // why assertFillsColumn budgets 24px for it rather than 2.
      const layoutWidth = await page.evaluate(() => document.documentElement.clientWidth)
      const [panelBox, scrimBox, bodyBox, footerBox, titleBox, headerBox, bodyContentBox, footerContentBox] =
        await Promise.all([
          panel.boundingBox(),
          scrim.boundingBox(),
          body.boundingBox(),
          footer.boundingBox(),
          title.boundingBox(),
          header.boundingBox(),
          bodyContent.boundingBox(),
          footerContent.boundingBox(),
        ])
      if (!panelBox || !scrimBox || !bodyBox || !footerBox || !titleBox) continue
      if (!headerBox || !bodyContentBox || !footerContentBox) continue

      // L1 -- flush to the layout viewport's right edge.
      const rightGap = Math.abs(layoutWidth - (panelBox.x + panelBox.width))
      expect(rightGap, `the panel must sit flush right at ${width}px`).toBeLessThanOrEqual(SLACK_PX)

      // L3 -- wholly inside the viewport.
      expect(panelBox.x, `the panel must not start left of the viewport at ${width}px`).toBeGreaterThanOrEqual(0)
      expect(panelBox.x + panelBox.width, `the panel must not run past the viewport at ${width}px`).toBeLessThanOrEqual(layoutWidth + SLACK_PX)

      // L8 -- the scrim covers the whole viewport, so there is no dead corner the drawer
      // cannot be dismissed from, and the panel sits inside it.
      expect(Math.abs(scrimBox.x), `the scrim must start at the viewport's left edge at ${width}px`).toBeLessThanOrEqual(SLACK_PX)
      expect(scrimBox.width, `the scrim must span the viewport at ${width}px`).toBeGreaterThanOrEqual(layoutWidth - SLACK_PX)
      expect(rectsOverlap(panelBox, scrimBox), `the panel must lie over the scrim at ${width}px`).toBe(true)

      // L7 -- header, body and footer share one content column. Two claims, and the second is
      // the one that bites: the bands are full-width siblings, AND each band insets its own
      // content by the same amount. Comparing a band's OUTER box against another band's INNER
      // content compares two different edges and fails by exactly the padding -- the first
      // deploy-gate run reported 22, which is the padding, not a misalignment.
      const bandDrift = Math.max(Math.abs(bodyBox.x - footerBox.x), Math.abs(bodyBox.x - headerBox.x))
      expect(bandDrift, `the three bands must be full-width siblings at ${width}px`).toBeLessThanOrEqual(SLACK_PX)

      // The footer is justify-content:flex-end, so its content column is measured from the
      // RIGHT edge; the other two from the left. Equal insets is the relationship, and no
      // number appears -- the three are only ever compared against each other.
      const headerInset = titleBox.x - headerBox.x
      const bodyInset = bodyContentBox.x - bodyBox.x
      const footerInset = footerBox.x + footerBox.width - (footerContentBox.x + footerContentBox.width)
      const insetDrift = Math.max(Math.abs(headerInset - bodyInset), Math.abs(headerInset - footerInset))
      expect(insetDrift, `the three bands must share one content column at ${width}px`).toBeLessThanOrEqual(SLACK_PX)

      measured.push({ width, layoutWidth, rightGap, leftEdge: panelBox.x, bandDrift, insetDrift })
    }
    expect(measured.length, 'the drawer geometry sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-bundle-drawer-geometry', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })

    // L4 -- the pf-drawer collapse, as a RATIO of the live viewport, never an equality.
    //
    // Measured, and not what the AC assumed: the collapse does NOT reach the full viewport.
    // `.pf-drawer` sets `width: 100vw !important` under the breakpoint, but the panel also
    // carries an inline `max-width: 94vw`, and !important on `width` does not override a
    // different property -- so the panel lands at 94vw. An equality against the viewport
    // width would fail on correct, shipped behaviour.
    //
    // The ratio is the relationship that actually matters and it discriminates sharply:
    // ~0.94 collapsed, against well under half above the breakpoint. Dropping the
    // `pf-drawer` class puts the collapsed case around two thirds and fails this.
    const ratioAt = async (width: number): Promise<number> => {
      await page.setViewportSize({ width, height: 1080 })
      const box = await panel.boundingBox()
      expect(box, `the panel vanished at ${width}px`).not.toBeNull()
      const layoutWidth = await page.evaluate(() => document.documentElement.clientWidth)
      return (box?.width ?? 0) / layoutWidth
    }

    expect(await ratioAt(860), 'the panel must span the viewport at the collapse breakpoint').toBeGreaterThan(0.9)
    expect(await ratioAt(1280), 'the panel must NOT span the viewport above the breakpoint').toBeLessThan(0.6)

    // L8's other half: the scrim is reachable, so a click at the far left dismisses.
    await page.mouse.click(4, 540)
    await expect(panel).toHaveCount(0, { timeout: 15_000 })
  })

  // L2, L9, L10, L11, L12, L13, L14, L15, L16 -- the Form phase and the confirmation block.
  test('audit_bundleFormAndConfirmBlockGeometry', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await page.setViewportSize({ width: 1280, height: 1080 })

    await page.getByTestId('audit-bundle-open').click({ timeout: 15_000 })
    const body = page.getByTestId('evidence-bundle-body')
    const footer = page.getByTestId('evidence-bundle-footer')
    await expect(body).toBeVisible({ timeout: 15_000 })
    await settleDrawerAnimation(page)

    const SLACK_PX = 2

    // L11 -- the open company panel is not clipped by the body's scroll container.
    // overflow-y:auto computes overflow-x:auto, so a wider panel is silently cut, and no
    // jsdom spec can see it.
    await page.getByTestId('evidence-company-trigger').click({ timeout: 15_000 })
    const companyPanel = page.getByTestId('evidence-company-panel')
    await expect(companyPanel).toBeVisible({ timeout: 15_000 })
    const [panelBox, bodyBoxForPanel] = await Promise.all([companyPanel.boundingBox(), body.boundingBox()])
    expect(panelBox && bodyBoxForPanel, 'the company panel or the body never rendered').toBeTruthy()
    if (panelBox && bodyBoxForPanel) {
      expect(panelBox.x, 'the company panel must not start left of the body column').toBeGreaterThanOrEqual(bodyBoxForPanel.x - SLACK_PX)
      expect(panelBox.x + panelBox.width, 'the company panel must not be clipped on the right').toBeLessThanOrEqual(
        bodyBoxForPanel.x + bodyBoxForPanel.width + SLACK_PX,
      )
    }

    const rows = companyPanel.locator('[data-testid^="evidence-company-row-"]')
    await expect(rows.first()).toBeVisible({ timeout: 15_000 })
    await rows.first().click({ timeout: 15_000 })

    // L10 -- the helper belongs to the company control: below its trigger, above the
    // Period label, left edges aligned. A text assertion passes with the sentence
    // rendered anywhere on the panel; this is the only oracle for where it sits.
    const trigger = page.getByTestId('evidence-company-trigger')
    const helper = page.getByTestId('evidence-company-helper')
    const chips = page.getByTestId('evidence-period-chips')
    const [triggerBox, helperBox, chipsBox] = await Promise.all([
      trigger.boundingBox(),
      helper.boundingBox(),
      chips.boundingBox(),
    ])
    expect(triggerBox && helperBox && chipsBox, 'the company control, its helper or the chip row never rendered').toBeTruthy()
    if (triggerBox && helperBox && chipsBox) {
      expect(helperBox.y, 'the helper must sit below the company trigger').toBeGreaterThanOrEqual(triggerBox.y + triggerBox.height - SLACK_PX)
      expect(helperBox.y + helperBox.height, 'the helper must sit above the period chips').toBeLessThanOrEqual(chipsBox.y + SLACK_PX)
      expect(Math.abs(helperBox.x - triggerBox.x), "the helper must align with the control it explains").toBeLessThanOrEqual(SLACK_PX)
    }

    // L9 -- the four chips stay inside the body column, and chips sharing a visual row
    // share a baseline. Their gutter is never asserted as a number.
    const chipBoxes = await page.getByTestId('evidence-period-chips').locator('button').all()
    expect(chipBoxes.length, 'the period chip row rendered no chips').toBeGreaterThan(0)
    const chipRects = (await Promise.all(chipBoxes.map((c) => c.boundingBox()))).filter(
      (b): b is { x: number; y: number; width: number; height: number } => b !== null,
    )
    expect(chipRects.length, 'no chip reported a box').toBe(chipBoxes.length)
    if (bodyBoxForPanel) {
      for (const rect of chipRects) {
        expect(rect.x, 'a period chip started left of the body column').toBeGreaterThanOrEqual(bodyBoxForPanel.x - SLACK_PX)
        expect(rect.x + rect.width, 'a period chip overflowed the body column').toBeLessThanOrEqual(
          bodyBoxForPanel.x + bodyBoxForPanel.width + SLACK_PX,
        )
      }
    }
    const firstRowY = chipRects[0].y
    for (const rect of chipRects.filter((r) => Math.abs(r.y - firstRowY) <= SLACK_PX)) {
      expect(rect.height, 'chips on one visual row must share a height').toBe(chipRects[0].height)
    }

    await expect(page.getByTestId('evidence-confirm-block')).toBeVisible({ timeout: 30_000 })
    const block = page.getByTestId('evidence-confirm-block')

    // L2 -- the block fills the body's content column at every swept width. This helper
    // sweeps WIDE_WIDTHS itself and restores the entry viewport.
    const fits = await assertFillsColumn(page, block, body, 'the evidence confirmation block')
    expect(fits.length, 'the confirm-block column sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-bundle-confirm-column', { body: JSON.stringify(fits, null, 2), contentType: 'application/json' })

    await page.setViewportSize({ width: 1280, height: 1080 })

    // L12 -- the manifest reads as two columns on every row: label and value never
    // overlap horizontally and always overlap vertically. A row that wrapped into one
    // column would still satisfy every text assertion at the unit layer.
    const manifestRows = page.getByTestId('evidence-confirm-row')
    const rowCount = await manifestRows.count()
    expect(rowCount, 'the manifest listed no rows').toBeGreaterThan(0)
    // The value cell is conditional -- bundleManifestLines emits rows whose `value` is null,
    // and the drawer renders the span only when it is not. Reading a boundingBox off a
    // locator matching nothing does not return null, it WAITS, so an unconditional read here
    // times the whole test out rather than failing an assertion.
    let twoColumnRows = 0
    for (let i = 0; i < rowCount; i++) {
      const row = manifestRows.nth(i)
      const value = row.getByTestId('evidence-confirm-row-value')
      if ((await value.count()) === 0) continue
      const [labelBox, valueBox] = await Promise.all([
        row.getByTestId('evidence-confirm-row-label').boundingBox(),
        value.boundingBox(),
      ])
      expect(labelBox && valueBox, `manifest row ${i} lost a cell`).toBeTruthy()
      if (labelBox && valueBox) {
        expect(rectsOverlap(labelBox, valueBox), `manifest row ${i} must read as two columns`).toBe(false)
        expect(labelBox.x, `manifest row ${i}'s label must sit left of its value`).toBeLessThan(valueBox.x)
        const sharesABaseline = labelBox.y < valueBox.y + valueBox.height && valueBox.y < labelBox.y + labelBox.height
        expect(sharesABaseline, `manifest row ${i} must keep its label and value on one line`).toBe(true)
        twoColumnRows++
      }
    }
    // Without this the loop above passes by measuring nothing.
    expect(twoColumnRows, 'no manifest row carried a value, so the two-column claim went untested').toBeGreaterThan(0)

    // L13 -- the whole filename is visible. An ellipsis satisfies a textContent equality
    // while hiding the exact name the block exists to state.
    for (const width of [1280, 860, 480]) {
      await page.setViewportSize({ width, height: 1080 })
      const truncated = await page
        .getByTestId('evidence-confirm-filename')
        .evaluate((el) => el.scrollWidth - el.clientWidth)
      expect(truncated, `the confirm filename must not be truncated at ${width}px`).toBeLessThanOrEqual(SLACK_PX)
    }
    await page.setViewportSize({ width: 1280, height: 1080 })

    // L15 -- the Prepare helper belongs to the BODY, not the footer band.
    const prepareHelper = page.getByTestId('evidence-prepare-helper')
    if ((await prepareHelper.count()) > 0) {
      const [helperRect, footerRect] = await Promise.all([prepareHelper.boundingBox(), footer.boundingBox()])
      if (helperRect && footerRect) {
        expect(rectsOverlap(helperRect, footerRect), 'the prepare helper must not sit on the footer band').toBe(false)
        expect(helperRect.y + helperRect.height, 'the prepare helper must sit above the footer').toBeLessThanOrEqual(footerRect.y + SLACK_PX)
      }
    }

    // L14 -- Prepare reads before Cancel, and L16 -- the footer is never pushed off the
    // panel by the tallest reachable body content.
    const prepare = page.getByTestId('evidence-bundle-prepare')
    const cancel = page.getByTestId('evidence-bundle-cancel')
    const [prepareBox, cancelBox, footerBox, drawerBox] = await Promise.all([
      prepare.boundingBox(),
      cancel.boundingBox(),
      footer.boundingBox(),
      page.getByTestId('evidence-bundle-drawer').boundingBox(),
    ])
    expect(prepareBox && cancelBox && footerBox && drawerBox, 'the footer pair never rendered').toBeTruthy()
    if (prepareBox && cancelBox && footerBox && drawerBox) {
      expect(prepareBox.x + prepareBox.width, 'Prepare must read before Cancel').toBeLessThanOrEqual(cancelBox.x + SLACK_PX)
      expect(rectsOverlap(prepareBox, cancelBox), 'the footer pair must not overlap').toBe(false)
      expect(footerBox.y + footerBox.height, 'the footer must stay inside the panel').toBeLessThanOrEqual(
        drawerBox.y + drawerBox.height + SLACK_PX,
      )
      await expect(prepare).toBeEnabled({ timeout: 15_000 })
    }
  })

  // L17, L18, L19, L20, L21 -- the Building and Ready phases.
  //
  // L20 lives here rather than with the other drawer geometry: proving the panel's box is
  // identical across all three phases needs all three phases, and only this test builds.
  test('audit_bundleBuildAndReadyGeometry', async ({ page }) => {
    test.setTimeout(180_000)
    await signInAs(page, 'firm')
    await openAudit(page)
    await page.setViewportSize({ width: 1280, height: 1080 })
    await openBundleDrawer(page)

    const drawer = page.getByTestId('evidence-bundle-drawer')
    const body = page.getByTestId('evidence-bundle-body')
    const footer = page.getByTestId('evidence-bundle-footer')
    const SLACK_PX = 2

    await settleDrawerAnimation(page)
    const formBox = await drawer.boundingBox()
    expect(formBox, 'the panel never rendered in the Form phase').not.toBeNull()

    await page.getByTestId('evidence-bundle-prepare').click({ timeout: 15_000 })

    // Building can be over in well under a second on a small tenant, so everything read
    // here is a single synchronous read. The two-sample width check st06 named would be a
    // race, and a flake in the one deployed oracle is worse than no assertion -- AC-1's
    // "no timed animation" is carried by EB-06-1's source scan instead.
    const building = page.getByTestId('evidence-building')
    const bar = page.getByTestId('evidence-building-bar')
    let buildingBox: { x: number; y: number; width: number; height: number } | null = null
    if (await building.isVisible().catch(() => false)) {
      buildingBox = await drawer.boundingBox()

      // L17 -- indeterminate by structure: a determinate bar carries a fractional-width
      // fill child, an indeterminate one animates its own background and has none.
      const fillChildren = await bar.locator(':scope > *').count()
      expect(fillChildren, 'an indeterminate bar must have no fill child').toBe(0)

      const [barBox, bodyBox] = await Promise.all([bar.boundingBox(), body.boundingBox()])
      if (barBox && bodyBox) {
        expect(barBox.x, 'the bar must start at the body column').toBeGreaterThanOrEqual(bodyBox.x - SLACK_PX)
        expect(barBox.x + barBox.width, 'the bar must fill the body column').toBeGreaterThanOrEqual(
          bodyBox.x + bodyBox.width - 24,
        )
      }
    }

    await expect(page.getByTestId('evidence-ready')).toBeVisible({ timeout: 120_000 })
    const readyBox = await drawer.boundingBox()

    // L20 -- the phase switch lives INSIDE the body and footer, never around the panel.
    // Building's content is much shorter than Form's, so a switch moved outside the panel
    // (or a height:auto panel) would resize the drawer under the pointer mid-build while
    // every unit spec stayed green.
    expect(readyBox, 'the panel never rendered in the Ready phase').not.toBeNull()
    if (formBox && readyBox) {
      expect(Math.abs(readyBox.width - formBox.width), 'the panel must not resize between phases').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(readyBox.height - formBox.height), 'the panel must not resize between phases').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(readyBox.x - formBox.x), 'the panel must not move between phases').toBeLessThanOrEqual(SLACK_PX)
    }
    if (formBox && buildingBox) {
      expect(Math.abs(buildingBox.width - formBox.width), 'the panel must not resize while building').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(buildingBox.height - formBox.height), 'the panel must not resize while building').toBeLessThanOrEqual(SLACK_PX)
    }

    // L19 -- Download reads before Start another, the same relationship L14 asserts for
    // the Form pair, so a phase swap that reverses one and not the other is caught.
    const download = page.getByTestId('evidence-ready-download')
    const startAnother = page.getByTestId('evidence-ready-start-another')
    const [downloadBox, startBox, footerBox] = await Promise.all([
      download.boundingBox(),
      startAnother.boundingBox(),
      footer.boundingBox(),
    ])
    expect(downloadBox && startBox && footerBox, 'the Ready footer pair never rendered').toBeTruthy()
    if (downloadBox && startBox && footerBox) {
      expect(downloadBox.x + downloadBox.width, 'Download must read before Start another').toBeLessThanOrEqual(startBox.x + SLACK_PX)
      expect(rectsOverlap(downloadBox, startBox), 'the Ready footer pair must not overlap').toBe(false)
      expect(downloadBox.x, 'Download must stay inside the footer column').toBeGreaterThanOrEqual(footerBox.x - SLACK_PX)
      expect(startBox.x + startBox.width, 'Start another must stay inside the footer column').toBeLessThanOrEqual(
        footerBox.x + footerBox.width + SLACK_PX,
      )
    }

    // L18 -- the whole filename is visible at 1280 and at both collapse widths. The user
    // is about to save this name; an ellipsis hides it while textContent still matches.
    for (const width of [1280, 860, 480]) {
      await page.setViewportSize({ width, height: 1080 })
      const truncated = await page
        .getByTestId('evidence-ready-filename')
        .evaluate((el) => el.scrollWidth - el.clientWidth)
      expect(truncated, `the Ready filename must not be truncated at ${width}px`).toBeLessThanOrEqual(SLACK_PX)
    }

    // L21 -- the toast and the open panel coexist. Swept over WIDE_WIDTHS and NOTHING
    // narrower: below 1280 the two boxes genuinely overlap, and at <=860 .pf-drawer goes
    // width:100vw so the toast necessarily sits on the panel. That is named, not fixed
    // (mobile is out of scope for this layer) -- an assertion there would go red on
    // correct, shipped behaviour.
    await page.setViewportSize({ width: 1280, height: 1080 })
    const downloadEvent = page.waitForEvent('download', { timeout: 60_000 })
    await download.click({ timeout: 15_000 })
    await downloadEvent

    const toast = page.getByTestId('evidence-bundle-toast')
    await expect(toast).toBeVisible({ timeout: 15_000 })

    const measured: Array<{ width: number; overlap: boolean }> = []
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [toastBox, panelBox] = await Promise.all([toast.boundingBox(), drawer.boundingBox()])
      if (!toastBox || !panelBox) continue
      const overlap = rectsOverlap(toastBox, panelBox)
      measured.push({ width, overlap })
      expect(overlap, `the bundle toast must not sit on the drawer at ${width}px`).toBe(false)
    }
    expect(measured.length, 'the toast/panel overlap sweep measured nothing').toBe(WIDE_WIDTHS.length)
    await test.info().attach('audit-bundle-toast-overlap', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  })
})
