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
import { gaps, WIDE_WIDTHS } from './layout'
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
})
