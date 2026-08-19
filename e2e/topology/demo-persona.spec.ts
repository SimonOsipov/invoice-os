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
