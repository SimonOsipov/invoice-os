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

import { signInAs } from '../personaSession'
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
})
