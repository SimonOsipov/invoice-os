// The deployed v2 Audit, evidence-bundle drawer and Settings surfaces: resolved values and layout
// relationships on the PR environment. jsdom has no cascade, so this file pins what the cascade resolves.
// Layout claims assert a relationship; screenshots are attached for the reviewer and never asserted.
// States the seed cannot reach are driven with page.route, each stub scoped to its own URL. No stub is a write.
import { test, expect, type Locator, type Page, type Route, type TestInfo } from '@playwright/test'
import { createEntity, createInvoice, login, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { signInAs } from '../personaSession'
import { approvalRun404Dropper, expectedStatusDropper, type Dropper } from './consoleGate'
import { assertFillsColumn, assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL } from './targets'

test.use({ viewport: { width: 1440, height: 900 } })

const SHADOW_CARD = 'rgba(40, 83, 52, 0.21) 0px 14px 22px -16px'
const SCRIM_RGB: [number, number, number] = [8, 47, 49]
const LINE_1 = 'rgb(220, 231, 228)'
const LINE_2 = 'rgb(201, 217, 214)'
const WHITE = 'rgb(255, 255, 255)'
const BG_1 = 'rgb(250, 248, 242)'
const FG_3 = 'rgb(83, 107, 109)'
const ACTION = 'rgb(7, 60, 61)'
const APP_ORIGIN = new URL(APP_URL).origin
const CORS = { 'content-type': 'application/json', 'access-control-allow-origin': APP_ORIGIN }

const AUDIT_LOG = /\/api\/invoice\/v1\/audit-log(\?|$)/
// Matches the bundle read and not .../evidence-bundle/preview.
const BUNDLE_GLOB = '**/api/invoice/v1/evidence-bundle?*'
const BUNDLE_RE = /\/api\/invoice\/v1\/evidence-bundle\?/
const PREVIEW_GLOB = '**/api/invoice/v1/evidence-bundle/preview?*'
const ROLE_WRITE_GLOB = '**/api/invoice/v1/workflow-roles/*'
const ROLE_WRITE_RE = /\/api\/invoice\/v1\/workflow-roles\/[^/?]+$/
const MEMBERSHIP_GLOB = '**/api/tenancy/v1/memberships/*'
const MEMBERSHIP_RE = /\/api\/tenancy\/v1\/memberships\/[^/?]+$/

let entity: { id: string; name: string }

// A clean flat wire body (a copy of invoice-surfaces.spec.ts's helper; specs do not import each other).
function cleanInvoiceFields(invoiceNumber: string) {
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

let invoiceId = ''

test.beforeAll(async () => {
  const token = await login(PERSONAS.A)
  // "Zenith" sorts after every seeded entity, so it never becomes another spec's default entity.
  entity = await createEntity(token, { name: `Zenith RESKIN2-06 ${Date.now()}`, tin: freshTin() })
  // Never validated: the detail page reads its approval run, which answers 404.
  const invoice = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RESKIN2-06-${Date.now()}`) })
  invoiceId = invoice.id
})

// --- Helpers --------------------------------------------------------------------------------------------------

const firstFamily = (raw: string): string => raw.split(',')[0].replace(/["']/g, '').trim()

// collectErrors minus the listed deliberate non-2xx answers.
function gatedErrors(page: Page, drops: Dropper[]): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drops.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  return errors
}

type Rgb = { r: number; g: number; b: number; a: number }

// Chrome serialises a resolved colour as rgb()/rgba(), or as color(srgb ...) for a colour mix.
function parseColor(raw: string): Rgb {
  const alpha = (a: string | undefined): number => (a === undefined ? 1 : a.endsWith('%') ? parseFloat(a) / 100 : parseFloat(a))
  const rgb = raw.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/)
  if (rgb) return { r: +rgb[1], g: +rgb[2], b: +rgb[3], a: alpha(rgb[4]) }
  const srgb = raw.match(/^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/)
  if (srgb) return { r: +srgb[1] * 255, g: +srgb[2] * 255, b: +srgb[3] * 255, a: alpha(srgb[4]) }
  throw new Error(`cannot parse the resolved colour ${JSON.stringify(raw)}`)
}

function expectColor(raw: string, want: [number, number, number], alpha: number, label: string): void {
  const c = parseColor(raw)
  for (const [i, channel] of (['r', 'g', 'b'] as const).entries()) {
    expect(Math.abs(c[channel] - want[i]), `${label}: ${channel} channel ${c[channel]} vs ${want[i]}`).toBeLessThanOrEqual(1)
  }
  expect(Math.abs(c.a - alpha), `${label}: alpha ${c.a} vs ${alpha}`).toBeLessThanOrEqual(0.01)
}

// Fonts settled and two frames painted; the named elements and their ancestors finish their animations.
async function settle(page: Page, ...targets: Locator[]): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  })
  await settleAnimations(...targets)
}

function styles(loc: Locator, props: string[]): Promise<Record<string, string>> {
  return loc.evaluate((el, props) => {
    const cs = getComputedStyle(el)
    return Object.fromEntries(props.map((p) => [p, cs.getPropertyValue(p)]))
  }, props)
}

const CORNERS = ['border-top-left-radius', 'border-top-right-radius', 'border-bottom-right-radius', 'border-bottom-left-radius']

async function expectCorners(loc: Locator, corner: string, label: string): Promise<string[]> {
  const got = Object.values(await styles(loc, CORNERS))
  expect(got, `${label} corners`).toEqual(Array(4).fill(corner))
  return got
}

async function border(loc: Locator): Promise<string> {
  const s = await styles(loc, ['border-top-width', 'border-top-style', 'border-top-color'])
  return `${s['border-top-width']} ${s['border-top-style']} ${s['border-top-color']}`
}

async function expectShadow(loc: Locator, want: string, label: string): Promise<string> {
  const shadow = (await styles(loc, ['box-shadow']))['box-shadow']
  expect(shadow, `${label} box-shadow`).toBe(want)
  expect(shadow, `${label} box-shadow must be resolved, not oklch`).not.toContain('oklch')
  return shadow
}

// The resolved colour of a token inside `scope`: a probe span, so no custom-property string is compared.
function resolveColor(scope: Locator, token: string): Promise<string> {
  return scope.evaluate((el, token) => {
    const probe = document.createElement('span')
    probe.style.color = `var(${token})`
    el.appendChild(probe)
    const value = getComputedStyle(probe).color
    probe.remove()
    return value
  }, token)
}

async function expectScrim(scrim: Locator, label: string): Promise<Record<string, string>> {
  const s = await styles(scrim, ['background-color', 'backdrop-filter'])
  expectColor(s['background-color'], SCRIM_RGB, 0.55, `${label} scrim`)
  expect(s['backdrop-filter'], `${label} scrim blur`).toBe('blur(6px)')
  return s
}

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

async function attachShot(page: Page, testInfo: TestInfo, name: string): Promise<void> {
  await testInfo.attach(`${name}.png`, { body: await page.screenshot(), contentType: 'image/png' })
}

type Named = Record<string, Locator>

// Boxes of the named locators; a missing box is a problem, never a skipped check.
async function boxes(named: Named): Promise<{ rects: Record<string, Rect>; problems: string[] }> {
  const rects: Record<string, Rect> = {}
  const problems: string[] = []
  for (const [name, loc] of Object.entries(named)) {
    const box = await loc.boundingBox()
    if (!box) problems.push(`${name} has no box`)
    else rects[name] = box
  }
  return { rects, problems }
}

function noOverlap(rects: Record<string, Rect>, names: string[], problems: string[]): void {
  for (let i = 0; i < names.length; i++)
    for (let j = i + 1; j < names.length; j++)
      if (rects[names[i]] && rects[names[j]] && rectsOverlap(rects[names[i]], rects[names[j]])) problems.push(`${names[i]} overlaps ${names[j]}`)
}

function inside(rects: Record<string, Rect>, outer: string, names: string[], problems: string[], slack = 1): void {
  for (const n of names) if (rects[outer] && rects[n] && !enclosesRect(rects[outer], rects[n], slack)) problems.push(`${n} sticks out of ${outer}`)
}

// Horizontal containment only: `outer` is a column band whose height is not the content's (a tab strip, a scrolling main).
function insideX(rects: Record<string, Rect>, outer: string, names: string[], problems: string[], slack = 1): void {
  const o = rects[outer]
  for (const n of names) {
    const r = rects[n]
    if (o && r && (r.x < o.x - slack || r.x + r.width > o.x + o.width + slack)) problems.push(`${n} sticks out of ${outer} sideways`)
  }
}

// Runs `read` at every WIDE_WIDTHS width, polling until it reports no problem; restores the entry viewport.
async function sweep(
  page: Page,
  label: string,
  read: () => Promise<{ problems: string[]; rects: Record<string, unknown> }>,
): Promise<Record<string, unknown>[]> {
  const entry = page.viewportSize()
  const measured: Record<string, unknown>[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 900 })
      await expect.poll(async () => (await read()).problems, { message: `${label} at ${width}px`, timeout: 10_000 }).toEqual([])
      measured.push({ width, ...(await read()).rects })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  return measured
}

const gateOf = () => {
  let release!: () => void
  const gate = new Promise<void>((resolve) => (release = resolve))
  return { gate, release }
}

const failWith = (route: Route, status: number) => route.fulfill({ status, headers: CORS, body: JSON.stringify({ error: 'stubbed for a screenshot' }) })

const aside = (page: Page) => page.locator('aside.pf-sidebar')
const navButton = (page: Page, label: RegExp) => aside(page).locator('nav button.pf-nav', { hasText: label })
const main = (page: Page) => page.locator('main.pf-main')
const h1Of = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true })
const tid = (page: Page, id: string) => page.getByTestId(id)

// --- Audit ----------------------------------------------------------------------------------------------------

async function openAudit(page: Page): Promise<void> {
  await navButton(page, /^Audit/).click()
  await expect(h1Of(page, 'Audit log'), 'the Audit screen drew no h1').toBeVisible()
}

async function openAuditLoaded(page: Page): Promise<void> {
  await signInAs(page, 'firm')
  await openAudit(page)
  await expect(tid(page, 'audit-row').first(), 'the firm audit log drew no rows').toBeVisible({ timeout: 20_000 })
  await expect(tid(page, 'audit-skeleton-row'), 'the skeleton rows never went away').toHaveCount(0)
  await settle(page, main(page))
}

// The first event in the open event popover whose facet count is nonzero.
async function pickCountedEvent(page: Page): Promise<string> {
  const rows = tid(page, 'audit-event-panel').locator('[data-testid^="audit-event-row-"]')
  const total = await rows.count()
  expect(total, 'the event popover listed nothing').toBeGreaterThan(0)
  for (let i = 0; i < total; i++) {
    const id = ((await rows.nth(i).getAttribute('data-testid')) ?? '').replace('audit-event-row-', '')
    if (Number(await tid(page, `audit-event-count-${id}`).textContent()) > 0) return id
  }
  throw new Error('no event type in the popover carries a nonzero count')
}

async function applyEventFilter(page: Page): Promise<void> {
  await tid(page, 'audit-event-trigger').click()
  const id = await pickCountedEvent(page)
  await tid(page, `audit-event-row-${id}`).click()
  await page.keyboard.press('Escape')
  await expect(tid(page, `audit-pill-event:${id}`), 'the event pill never drew').toBeVisible()
  await expect(tid(page, 'audit-row').first(), 'the event filter emptied the table').toBeVisible()
}

async function applySearch(page: Page, needle: string): Promise<void> {
  await tid(page, 'audit-search-trigger').click()
  const input = tid(page, 'audit-search-input')
  await input.fill(needle)
  await input.press('Enter')
  await page.keyboard.press('Escape')
}

test('AE-01 Audit at 1440: heading, strip, filter card, table card, avatar, range pill, header buttons', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)

  const h1 = await styles(h1Of(page, 'Audit log'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const strip = tid(page, 'audit-immutability-strip')
  const stripCorners = await expectCorners(strip, '6px', 'immutability strip')
  expect(await border(strip), 'strip border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(strip, ['background-color']))['background-color'], 'strip background').toBe(BG_1)

  const filterCard = tid(page, 'audit-filter-card')
  const filterCorners = await expectCorners(filterCard, '6px', 'filter card')
  const filterPaint = await styles(filterCard, ['background-color', 'box-shadow'])
  expect(filterPaint['background-color'], 'filter card background').toBe(WHITE)
  expect(filterPaint['box-shadow'], 'filter card shadow').toBe('none')

  const tableCard = tid(page, 'audit-table').locator('xpath=..')
  const tableCorners = await expectCorners(tableCard, '6px', 'table card')
  expect(await border(tableCard), 'table card border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(tableCard, ['box-shadow']))['box-shadow'], 'table card shadow').toBe('none')
  const ownBorder = await styles(tid(page, 'audit-table'), ['border-top-width', 'border-right-width', 'border-bottom-width', 'border-left-width'])
  expect(Object.values(ownBorder), 'audit-table own border').toEqual(['0px', '0px', '0px', '0px'])

  // A free-text actor draws a flat square, so the avatar is read off the first row that has a person or System.
  const avatarRow = tid(page, 'audit-row').filter({ has: page.locator('[data-testid="actor-initials"], [data-testid="actor-bolt"]') }).first()
  await expect(avatarRow, 'no audit row carries a person or System avatar').toBeVisible()
  const avatar = avatarRow.locator('xpath=./span[1]/span[1]')
  expect((await styles(avatar, ['border-top-left-radius']))['border-top-left-radius'], 'avatar corner').toBe('50%')

  const pill = tid(page, 'audit-pill-range')
  await expect(pill, 'the default range pill never drew').toBeVisible()
  const pillCorners = await expectCorners(pill, '4px', 'range pill')
  const pillPaint = await styles(pill, ['font-size', 'font-weight', 'padding-top', 'padding-right', 'padding-bottom', 'padding-left', 'border-top-color'])
  expect(pillPaint['font-size'], 'range pill font size').toBe('12px')
  expect(pillPaint['font-weight'], 'range pill weight').toBe('400')
  expect(`${pillPaint['padding-top']} ${pillPaint['padding-right']} ${pillPaint['padding-bottom']} ${pillPaint['padding-left']}`, 'range pill padding').toBe('3px 5px 3px 10px')
  expect(pillPaint['border-top-color'], 'range pill border colour').toBe(LINE_2)
  const textLine = await pill.evaluate((el) => {
    const range = document.createRange()
    range.selectNodeContents(el.firstChild as Node)
    return range.getBoundingClientRect().height
  })
  const pillBox = await pill.boundingBox()
  expect(pillBox, 'the range pill has no box').not.toBeNull()
  expect(pillBox!.height, 'the range pill is taller than its text line').toBeGreaterThan(textLine)
  expect(pillBox!.height, 'the range pill is shorter than 28px').toBeLessThan(28)

  const bundle = tid(page, 'audit-bundle-open')
  const exportBtn = tid(page, 'audit-export')
  const bundleCorners = await expectCorners(bundle, '7px', 'bundle button')
  const exportCorners = await expectCorners(exportBtn, '7px', 'export button')
  const [bundleBox, exportBox] = await Promise.all([bundle.boundingBox(), exportBtn.boundingBox()])
  expect(bundleBox && exportBox, 'the header buttons have no box').toBeTruthy()
  expect(Math.abs(bundleBox!.height - exportBox!.height), 'the header buttons stand the same height').toBeLessThanOrEqual(0.5)

  await attachJson(testInfo, 'ae-01-measurements', { h1, stripCorners, filterCorners, filterPaint, tableCorners, pillCorners, pillPaint, textLine, pillBox, bundleCorners, exportCorners, bundleBox, exportBox })
  await attachShot(page, testInfo, 'audit-loaded')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-02 the five filter popovers carry the card shadow, 6px corners and stay in the viewport', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  const viewport = page.viewportSize()!
  const reads: Record<string, unknown>[] = []
  const WIDTHS: Record<string, number> = { search: 340, date: 250, event: 340, actor: 280, company: 310 }
  for (const id of ['search', 'date', 'event', 'actor', 'company']) {
    await tid(page, `audit-${id}-trigger`).click()
    const panel = tid(page, `audit-${id}-panel`)
    await expect(panel, `the ${id} popover never opened`).toBeVisible()
    await settle(page, panel)
    const shadow = await expectShadow(panel, SHADOW_CARD, `${id} popover`)
    const corners = await expectCorners(panel, '6px', `${id} popover`)
    const box = await panel.boundingBox()
    expect(box, `the ${id} popover has no box`).not.toBeNull()
    expect(Math.abs(box!.width - WIDTHS[id]), `the ${id} popover is ${box!.width}px wide, the prototype draws ${WIDTHS[id]}px`).toBeLessThanOrEqual(0.5)
    expect(enclosesRect({ x: 0, y: 0, width: viewport.width, height: viewport.height }, box!), `the ${id} popover leaves the viewport: ${JSON.stringify(box)}`).toBe(true)
    reads.push({ id, shadow, corners, box })
    if (id === 'event') await attachShot(page, testInfo, 'audit-event-popover')
    await page.keyboard.press('Escape')
    await expect(panel, `the ${id} popover never closed`).toHaveCount(0)
  }
  await attachJson(testInfo, 'ae-02-measurements', reads)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-03 Audit header, pills, strip and pager hold their relationships at every wide width', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  await applyEventFilter(page)
  await settle(page, main(page))
  await attachShot(page, testInfo, 'audit-filtered')

  const card = tid(page, 'audit-filter-card')
  const strip = tid(page, 'audit-immutability-strip')
  const claim = strip.locator(':scope > span:not(.mono)')
  const count = strip.locator('span.mono')
  await expect(count, 'the strip drew no lifetime count').toHaveCount(1)
  const tableCard = tid(page, 'audit-table').locator('xpath=..')
  const pager = tid(page, 'audit-pager')
  const pills = page.locator('[data-testid^="audit-pill-"]')
  expect(await pills.count(), 'the range pill and the event pill must both draw').toBeGreaterThanOrEqual(2)

  const loaded = await sweep(page, 'Audit pills, strip and pager', async () => {
    await settle(page, card)
    const { rects, problems } = await boxes({ card, strip, claim, count, tableCard, pager })
    inside(rects, 'strip', ['count'], problems)
    const sideways = await main(page).locator('.pf-scroll').evaluate((el) => el.scrollWidth - el.clientWidth)
    if (sideways > 1) problems.push(`the Audit page column scrolls sideways by ${sideways}px`)
    if (rects.claim && rects.count && rects.count.x < rects.claim.x + rects.claim.width - 1) problems.push('the strip count is not right of the claim text')
    noOverlap(rects, ['claim', 'count'], problems)
    if (rects.tableCard && rects.pager && rects.pager.y < rects.tableCard.y + rects.tableCard.height - 1) problems.push('the pager is not below the table card')
    noOverlap(rects, ['tableCard', 'pager'], problems)
    const pillRects = await pills.evaluateAll((els) => els.map((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }))
    if (rects.card) for (const [i, p] of pillRects.entries()) if (!enclosesRect(rects.card, p, 1)) problems.push(`pill ${i} sticks out of the filter card`)
    for (let i = 0; i < pillRects.length; i++) for (let j = i + 1; j < pillRects.length; j++) if (rectsOverlap(pillRects[i], pillRects[j])) problems.push(`pill ${i} overlaps pill ${j}`)
    return { problems, rects: { ...rects, pills: pillRects } }
  })
  const scroll = await assertPageDoesNotScrollSideways(page, 'Audit')

  // The no-match search: the export reason must end at the export button's right edge.
  await applySearch(page, `no-match-${crypto.randomUUID()}`)
  const reason = tid(page, 'audit-export-reason')
  await expect(reason, 'a nonce search must show the export reason').toBeVisible({ timeout: 15_000 })
  const exportBtn = tid(page, 'audit-export')
  const noMatch = await sweep(page, 'Audit export reason', async () => {
    const { rects, problems } = await boxes({ reason, exportBtn })
    if (rects.reason && rects.exportBtn && Math.abs(rects.reason.x + rects.reason.width - (rects.exportBtn.x + rects.exportBtn.width)) > 2) {
      problems.push('the export reason does not end at the export button right edge')
    }
    return { problems, rects }
  })

  await attachJson(testInfo, 'ae-03-measurements', { loaded, noMatch, scroll })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-04 Audit loading, error, new-workspace and filtered states', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, AUDIT_LOG)])
  await signInAs(page, 'firm')
  const away = () => navButton(page, /^Overview/).click()

  // Loading: the audit-log read is held.
  const { gate, release } = gateOf()
  await page.route(AUDIT_LOG, async (route) => {
    await gate
    await route.continue()
  })
  await openAudit(page)
  const rows = tid(page, 'audit-skeleton-row')
  await expect(rows.first(), 'a held read drew no skeleton rows').toBeVisible()
  await settle(page, main(page))
  const bars = await rows.first().evaluate((row) =>
    [...row.querySelectorAll(':scope > span > span')].map((el) => {
      const cs = getComputedStyle(el)
      return { height: cs.height, animation: cs.animationName, image: cs.backgroundImage, radius: cs.borderTopLeftRadius }
    }),
  )
  expect(bars.length, 'the skeleton row drew no bars').toBeGreaterThanOrEqual(5)
  for (const bar of bars) {
    expect(bar.animation, 'skeleton bar animation').toBe('pulse')
    expect(bar.image, 'skeleton bar background image').toBe('none')
    expect(bar.radius, `skeleton bar corner (height ${bar.height})`).toBe(bar.height === '10px' ? '4px' : '50%')
  }
  expect(bars.filter((b) => b.height === '10px').length, 'the skeleton drew no 10px text bars').toBeGreaterThanOrEqual(4)
  await attachShot(page, testInfo, 'audit-loading')
  release()
  await expect(tid(page, 'audit-row').first(), 'the released read drew no rows').toBeVisible({ timeout: 20_000 })
  await page.unroute(AUDIT_LOG)

  // Error: every audit-log read answers 503.
  await away()
  await page.route(AUDIT_LOG, (route) => failWith(route, 503))
  await openAudit(page)
  const title = page.getByText('Something went wrong', { exact: true })
  await expect(title, 'the 503 drew no error card').toBeVisible()
  const errorCard = title.locator('xpath=..')
  await settle(page, errorCard)
  const errorCorners = await expectCorners(errorCard, '6px', 'error card')
  const { rects: errRects, problems: errProblems } = await boxes({ main: main(page), errorCard })
  inside(errRects, 'main', ['errorCard'], errProblems)
  expect(errProblems, 'error card placement').toEqual([])
  await attachShot(page, testInfo, 'audit-error')
  await page.unroute(AUDIT_LOG)

  // New workspace: the log answers an empty page that says the log is empty.
  await away()
  await page.route(AUDIT_LOG, (route) =>
    route.fulfill({
      status: 200,
      headers: CORS,
      body: JSON.stringify({ events: [], page: { limit: 25, has_more: false, next_cursor: null }, total: 0, log_is_empty: true, facets: { event: [], actor: [], company: [] } }),
    }),
  )
  await openAudit(page)
  const fresh = tid(page, 'audit-new-workspace')
  await expect(fresh, 'an empty log drew no new-workspace card').toBeVisible()
  await settle(page, fresh)
  const dashed = (await styles(fresh.locator(':scope > :first-child'), ['border-top-style']))['border-top-style']
  expect(dashed, 'new-workspace card border style').toBe('dashed')
  await attachShot(page, testInfo, 'audit-new-workspace')
  await page.unrouteAll({ behavior: 'wait' })

  // Filtered: the real log narrowed to one event type.
  await away()
  await openAudit(page)
  await expect(tid(page, 'audit-row').first(), 'the log drew no rows').toBeVisible({ timeout: 20_000 })
  await applyEventFilter(page)
  await settle(page, main(page))
  await attachShot(page, testInfo, 'audit-filtered')

  await attachJson(testInfo, 'ae-04-measurements', { bars: bars.slice(0, 6), errorCorners, errRects, dashed })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

test('AE-05 the invoice activity table meets the card edges at every wide width', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [approvalRun404Dropper(page)])
  await signInAs(page, 'firm')
  await page.goto(`${APP_URL}/invoices/${invoiceId}`)
  const card = tid(page, 'invoice-activity')
  await expect(card, 'the activity card never drew').toBeVisible({ timeout: 20_000 })
  await expect(card.getByTestId('audit-row').first(), 'the activity card drew no audit rows').toBeVisible({ timeout: 20_000 })
  const scroller = card.getByTestId('audit-table').locator('xpath=..')

  const qualifying: number[] = []
  const measured = await sweep(page, 'Activity table against its card', async () => {
    await settle(page, card)
    const read = await scroller.evaluate((s) => {
      const c = s.closest('[data-testid="invoice-activity"]') as HTMLElement
      const cr = c.getBoundingClientRect()
      const sr = s.getBoundingClientRect()
      const innerLeft = cr.left + c.clientLeft
      return {
        left: sr.left - innerLeft,
        right: innerLeft + c.clientWidth - sr.right,
        scrollWidth: s.scrollWidth,
        clientWidth: s.clientWidth,
      }
    })
    const problems: string[] = []
    if (Math.abs(read.left) > 1) problems.push(`the scroller left edge is ${read.left}px from the card inner left`)
    if (read.scrollWidth <= read.clientWidth && Math.abs(read.right) > 1) problems.push(`the scroller right edge is ${read.right}px from the card inner right`)
    return { problems, rects: read }
  })
  for (const m of measured) if ((m.scrollWidth as number) <= (m.clientWidth as number)) qualifying.push(m.width as number)
  expect(qualifying.length, `no width let the table fit its card: ${JSON.stringify(measured)}`).toBeGreaterThanOrEqual(1)

  await attachJson(testInfo, 'ae-05-measurements', { measured, qualifying })
  await attachShot(page, testInfo, 'invoice-activity')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// Opens the bundle drawer. With `company`, picks the fixture entity and waits for its confirm block and a live Prepare.
async function openDrawer(page: Page, company: boolean): Promise<void> {
  await tid(page, 'audit-bundle-open').click()
  const drawer = tid(page, 'evidence-bundle-drawer')
  await expect(drawer, 'the bundle drawer never opened').toBeVisible()
  await settle(page, drawer, tid(page, 'evidence-bundle-scrim'))
  if (!company) return
  await tid(page, 'evidence-company-trigger').click()
  await tid(page, `evidence-company-row-${entity.id}`).click()
  await expect(tid(page, 'evidence-confirm-block'), 'the fixture company drew no confirm block').toBeVisible({ timeout: 30_000 })
  await expect(tid(page, 'evidence-bundle-prepare'), 'Prepare stayed disabled for the fixture company').toBeEnabled()
}

test('AE-06 bundle drawer in Form: scrim, panel, title, period chips, Prepare and the confirm block', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  await openDrawer(page, false)

  const scrim = await expectScrim(tid(page, 'evidence-bundle-scrim'), 'bundle drawer')
  const drawer = tid(page, 'evidence-bundle-drawer')
  const panel = await styles(drawer, ['box-shadow', 'border-left-width', 'border-left-style', 'border-left-color'])
  expect(panel['box-shadow'], 'drawer shadow').toBe('none')
  expect(`${panel['border-left-width']} ${panel['border-left-style']} ${panel['border-left-color']}`, 'drawer left border').toBe(`1px solid ${LINE_2}`)
  expect((await styles(tid(page, 'evidence-bundle-title'), ['font-weight']))['font-weight'], 'drawer title weight').toBe('700')
  const close = (await styles(tid(page, 'evidence-bundle-close').locator('svg'), ['width', 'height']))
  expect(close, 'close glyph size').toEqual({ width: '12px', height: '12px' })

  const chips = tid(page, 'evidence-period-chips').locator('button')
  expect(await chips.count(), 'the period chips').toBeGreaterThanOrEqual(3)
  for (const chip of await chips.all()) await expectCorners(chip, '7px', 'period chip')

  const prepare = tid(page, 'evidence-bundle-prepare')
  await expect(prepare, 'Prepare before a company is chosen').toBeDisabled()
  const disabled = await styles(prepare, ['opacity', 'cursor'])
  expect(disabled, 'disabled Prepare').toEqual({ opacity: '0.45', cursor: 'not-allowed' })

  const cancel = tid(page, 'evidence-bundle-cancel')
  const body = tid(page, 'evidence-bundle-body')
  const footerSweep = await sweep(page, 'Drawer footer', async () => {
    await settle(page, drawer)
    const { rects, problems } = await boxes({ prepare, cancel, body })
    const pad = await body.evaluate((el) => parseFloat(getComputedStyle(el).paddingLeft))
    if (rects.prepare && rects.body && Math.abs(rects.prepare.x - (rects.body.x + pad)) > 2) problems.push('Prepare does not start at the body content left edge')
    if (rects.prepare && rects.cancel && rects.prepare.x + rects.prepare.width > rects.cancel.x + 1) problems.push('Prepare is not left of Cancel')
    noOverlap(rects, ['prepare', 'cancel'], problems)
    return { problems, rects }
  })
  await attachShot(page, testInfo, 'bundle-form')

  await tid(page, 'evidence-company-trigger').click()
  await tid(page, `evidence-company-row-${entity.id}`).click()
  const block = tid(page, 'evidence-confirm-block')
  await expect(block, 'the fixture company drew no confirm block').toBeVisible({ timeout: 30_000 })
  const blockCorners = await expectCorners(block, '6px', 'confirm block')
  const blockFill = (await styles(block, ['background-color']))['background-color']
  expect(blockFill, 'confirm block background').toBe(WHITE)
  await attachShot(page, testInfo, 'bundle-confirm')

  await attachJson(testInfo, 'ae-06-measurements', { scrim, panel, close, disabled, footerSweep, blockCorners, blockFill })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-07 bundle drawer Building: a childless pulsing fill inside its card, then Ready', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  await openDrawer(page, true)
  const { gate, release } = gateOf()
  await page.route(BUNDLE_GLOB, async (route) => {
    await gate
    await route.continue()
  })
  try {
    await tid(page, 'evidence-bundle-prepare').click()
    const building = tid(page, 'evidence-building')
    const bar = tid(page, 'evidence-building-bar')
    await expect(building, 'a held bundle request never reached Building').toBeVisible()
    await settle(page, building)
    expect(await bar.locator(':scope > *').count(), 'the building bar has no child').toBe(0)
    // Opacity is not read: the running pulse keyframe owns it.
    const paint = await styles(bar, ['animation-name', 'animation-duration', 'background-color'])
    expect(paint['animation-name'], 'building fill animation').toBe('pulse')
    expect(paint['animation-duration'], 'building fill pulse duration').toBe('1.2s')
    expect(paint['background-color'], 'building fill colour').toBe(ACTION)
    const { rects, problems } = await boxes({ building, bar })
    inside(rects, 'building', ['bar'], problems)
    expect(problems, 'building bar placement').toEqual([])
    await attachShot(page, testInfo, 'bundle-building')
    await attachJson(testInfo, 'ae-07-measurements', { paint, rects })
  } finally {
    release()
  }
  await expect(tid(page, 'evidence-ready'), 'the released build never reached Ready').toBeVisible({ timeout: 120_000 })
  await settle(page, tid(page, 'evidence-bundle-drawer'))
  await attachShot(page, testInfo, 'bundle-ready')
  await page.unrouteAll({ behavior: 'wait' })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-08 an expanded audit row: ground, padding, three payload columns and the ids footer', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  const rows = tid(page, 'audit-row')
  await rows.first().click()
  const expansion = tid(page, 'audit-expansion')
  await expect(expansion, 'the first row did not expand').toBeVisible()
  const paint = await styles(expansion, ['background-color', 'border-top-width', 'padding-left'])
  expect(paint, 'expansion paint').toEqual({ 'background-color': BG_1, 'border-top-width': '1px', 'padding-left': '53px' })
  const ids = (await styles(tid(page, 'audit-event-identifier'), ['color'])).color
  expect(ids, 'ids footer colour').toBe(FG_3)

  const edgesOf = () => tid(page, 'audit-payload-field').evaluateAll((els) => [...new Set(els.map((el) => Math.round(el.getBoundingClientRect().left)))])
  const firstFields = await tid(page, 'audit-payload-field').count()
  const firstEdges = await edgesOf()
  expect(firstEdges.length, 'the first row payload columns').toBeLessThanOrEqual(3)
  if (firstFields >= 2) expect(firstEdges.length, 'a payload with two fields uses two columns').toBeGreaterThanOrEqual(2)

  // A row with two or more fields, so the column claim cannot go vacuous.
  const scan = Math.min(await rows.count(), 25)
  let multi = -1
  for (let i = 0; i < scan; i++) {
    await rows.nth(i).click()
    if ((await tid(page, 'audit-payload-field').count()) >= 2) {
      multi = i
      break
    }
  }
  expect(multi, `none of the first ${scan} rows carries two payload fields`).toBeGreaterThanOrEqual(0)
  const edges = await edgesOf()
  expect(edges.length, 'a multi-field payload uses two or three columns').toBeGreaterThanOrEqual(2)
  expect(edges.length, 'a multi-field payload uses at most three columns').toBeLessThanOrEqual(3)
  await attachShot(page, testInfo, 'audit-expanded')
  await attachJson(testInfo, 'ae-08-measurements', { paint, ids, firstFields, firstEdges, multi, edges })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-09 the first five audit rows stay one line tall at every wide width', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  const rows = tid(page, 'audit-row')
  expect(await rows.count(), 'fewer than five audit rows').toBeGreaterThanOrEqual(5)
  const measured = await sweep(page, 'Audit row heights', async () => {
    await settle(page, main(page))
    const problems: string[] = []
    const heights: number[] = []
    for (let i = 0; i < 5; i++) {
      const row = rows.nth(i)
      const box = await row.boundingBox()
      if (!box) {
        problems.push(`row ${i} has no box`)
        continue
      }
      heights.push(box.height)
      for (const cell of ['audit-what', 'audit-company']) {
        // Distinct line tops, not rect count: an ellipsised line reports its full and its clipped fragment at one top.
        const tops = await row.getByTestId(cell).evaluate((el) => {
          const range = document.createRange()
          range.selectNodeContents(el)
          return [...new Set([...range.getClientRects()].map((r) => Math.round(r.top)))]
        })
        if (tops.length !== 1) problems.push(`row ${i} ${cell} sits on ${tops.length} lines`)
      }
    }
    if (heights.length && Math.max(...heights) - Math.min(...heights) > 1) problems.push(`row heights differ: ${heights.join(', ')}`)
    return { problems, rects: { heights } }
  })
  await attachJson(testInfo, 'ae-09-measurements', measured)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-10 Audit no-match card', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openAuditLoaded(page)
  await applySearch(page, `no-match-${crypto.randomUUID()}`)
  const card = tid(page, 'audit-empty-by-filter')
  await expect(card, 'a nonce search drew no no-match card').toBeVisible({ timeout: 15_000 })
  await settle(page, main(page))
  const corners = await expectCorners(card, '6px', 'no-match card')
  const { rects, problems } = await boxes({ main: main(page), card })
  inside(rects, 'main', ['card'], problems)
  expect(problems, 'no-match card placement').toEqual([])
  await attachJson(testInfo, 'ae-10-measurements', { corners, rects })
  await attachShot(page, testInfo, 'audit-no-match')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AE-11 bundle drawer Failed: retry left of cancel inside the footer', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, BUNDLE_RE)])
  await openAuditLoaded(page)
  await openDrawer(page, true)
  await page.route(BUNDLE_GLOB, (route) => failWith(route, 503))
  await tid(page, 'evidence-bundle-prepare').click()
  await expect(tid(page, 'evidence-bundle-failure'), 'a 503 bundle drew no failure card').toBeVisible()
  const retry = tid(page, 'evidence-failed-retry')
  const cancel = tid(page, 'evidence-failed-cancel')
  const footer = tid(page, 'evidence-bundle-footer')
  await settle(page, tid(page, 'evidence-bundle-drawer'))
  const { rects, problems } = await boxes({ retry, cancel, footer })
  if (rects.retry && rects.cancel && rects.retry.x + rects.retry.width > rects.cancel.x + 1) problems.push('Retry is not left of Cancel')
  noOverlap(rects, ['retry', 'cancel'], problems)
  inside(rects, 'footer', ['retry', 'cancel'], problems)
  expect(problems, 'failed footer pair').toEqual([])
  await attachJson(testInfo, 'ae-11-measurements', rects)
  await attachShot(page, testInfo, 'bundle-failed')
  await page.unrouteAll({ behavior: 'wait' })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

test('AE-12 undrawn states: the CSV export error toast and the over-limit refusal', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 500, AUDIT_LOG)])
  await openAuditLoaded(page)

  await page.route(AUDIT_LOG, (route) => failWith(route, 500))
  await tid(page, 'audit-export').click()
  const toast = tid(page, 'audit-export-toast')
  await expect(toast, 'a failed export drew no toast').toBeVisible()
  await settle(page, toast)
  const toastShadow = await expectShadow(toast, SHADOW_CARD, 'export toast')
  const toastCorners = await expectCorners(toast, '6px', 'export toast')
  await attachShot(page, testInfo, 'audit-export-error')
  await page.unroute(AUDIT_LOG)

  // The preview is the real one with the cap flag raised and a count past the cap.
  await page.route(PREVIEW_GLOB, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    const res = await route.fetch()
    const json = (await res.json()) as { counts: Record<string, number> }
    await route.fulfill({ status: res.status(), headers: CORS, body: JSON.stringify({ ...json, counts: { ...json.counts, invoices: 10001 }, over_limit: true }) })
  })
  await openDrawer(page, false)
  await tid(page, 'evidence-company-trigger').click()
  await tid(page, `evidence-company-row-${entity.id}`).click()
  const reason = tid(page, 'evidence-bundle-reason')
  await expect(reason, 'an over-limit preview drew no refusal').toBeVisible({ timeout: 30_000 })
  const paint = await styles(reason, ['font-size', 'color'])
  expect(paint['font-size'], 'refusal font size').toBe('12px')
  expect(paint.color, 'refusal colour is --fg-2').toBe(await resolveColor(reason, '--fg-2'))
  expect(paint.color, 'refusal colour is not --fg-3').not.toBe(FG_3)
  const prepare = tid(page, 'evidence-bundle-prepare')
  await expect(prepare, 'over-limit Prepare').toBeDisabled()
  expect((await styles(prepare, ['opacity'])).opacity, 'over-limit Prepare opacity').toBe('0.45')
  await attachShot(page, testInfo, 'bundle-over-limit')

  await attachJson(testInfo, 'ae-12-measurements', { toastShadow, toastCorners, paint })
  await page.unrouteAll({ behavior: 'wait' })
  expect(errors, `console errors beyond the deliberate 500:\n${errors.join('\n')}`).toEqual([])
})

// --- Settings -------------------------------------------------------------------------------------------------

const settingsTab = (page: Page, label: string) => page.getByRole('button', { name: label, exact: true })
const tabStrip = (page: Page) => settingsTab(page, 'Members').locator('xpath=..')

async function openSettings(page: Page, persona: 'firm' | 'inhouse'): Promise<void> {
  await signInAs(page, persona)
  await navButton(page, /^Settings/).click()
  await expect(h1Of(page, 'Settings'), 'the Settings screen drew no h1').toBeVisible()
  await expect(page, 'Settings did not open on Members').toHaveURL(/\/settings\/members$/)
}

async function openTab(page: Page, label: string, id: string): Promise<void> {
  await settingsTab(page, label).click()
  await expect(page, `the ${label} tab did not update the URL`).toHaveURL(new RegExp(`/settings/${id}$`))
}

async function openMembers(page: Page): Promise<void> {
  await openSettings(page, 'firm')
  await expect(tid(page, 'member-row').first(), 'the roster drew no rows').toBeVisible({ timeout: 20_000 })
  await expect(page.getByText(/^Loading .+…$/), 'a loading label never went away').toHaveCount(0)
  await settle(page, main(page))
}

async function openRoles(page: Page): Promise<void> {
  await openSettings(page, 'firm')
  await openTab(page, 'Roles', 'roles')
  await expect(tid(page, 'role-card').first(), 'the roles grid drew no cards').toBeVisible({ timeout: 20_000 })
  await settle(page, main(page))
}

test('ST-01 Settings › Members at 1440: heading, table card, status pill, avatar, Invite', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openMembers(page)

  const h1 = await styles(h1Of(page, 'Settings'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const table = tid(page, 'members-table')
  const tableCorners = await expectCorners(table, '6px', 'members table')
  expect(await border(table), 'members table border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(table, ['box-shadow']))['box-shadow'], 'members table shadow').toBe('none')

  const row = tid(page, 'member-row').first()
  const pill = row.locator('xpath=./span[4]/span')
  await expect(pill, 'the first row drew no status pill').toBeVisible()
  const pillCorners = await expectCorners(pill, '4px', 'status pill')
  expect(await pill.locator(':scope > *').count(), 'the status pill has no child element').toBe(0)
  const avatar = (await styles(row.locator('xpath=./span[1]/span[1]'), ['border-top-left-radius']))['border-top-left-radius']
  expect(avatar, 'member avatar corner').toBe('50%')

  const invite = tid(page, 'members-invite')
  const inviteDisabled = await styles(invite, ['opacity', 'cursor'])
  expect(inviteDisabled, 'Invite people').toEqual({ opacity: '0.45', cursor: 'not-allowed' })
  const scroll = await assertPageDoesNotScrollSideways(page, 'Members')

  await attachJson(testInfo, 'st-01-measurements', { h1, tableCorners, pillCorners, avatar, inviteDisabled, scroll })
  await attachShot(page, testInfo, 'settings-members')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-02 the Members toolbar shares one row inside the tab column at every wide width', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openMembers(page)
  const search = page.getByLabel('Search members')
  const roleFilter = page.locator('label[aria-label="Access role"] select')
  const invite = tid(page, 'members-invite')
  const reason = tid(page, 'members-invite-reason')
  const strip = tabStrip(page)
  const measured = await sweep(page, 'Members toolbar', async () => {
    await settle(page, main(page))
    const { rects, problems } = await boxes({ strip, search, roleFilter, invite, reason })
    insideX(rects, 'strip', ['search', 'roleFilter', 'invite'], problems)
    noOverlap(rects, ['search', 'roleFilter', 'invite'], problems)
    for (const n of ['search', 'roleFilter', 'invite']) {
      if (rects.strip && rects[n] && rects[n].y < rects.strip.y + rects.strip.height - 1) problems.push(`${n} sits above the tab strip's bottom edge`)
    }
    const names = ['search', 'roleFilter', 'invite']
    const centres = names.filter((n) => rects[n]).map((n) => rects[n].y + rects[n].height / 2)
    if (centres.length && Math.max(...centres) - Math.min(...centres) > 2) problems.push(`the toolbar spans more than one row: ${centres.join(', ')}`)
    if (rects.reason && rects.invite && Math.abs(rects.reason.x + rects.reason.width - (rects.invite.x + rects.invite.width)) > 2) {
      problems.push('the invite reason does not end at the Invite right edge')
    }
    return { problems, rects }
  })
  await attachJson(testInfo, 'st-02-measurements', measured)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-03 the member drawer: scrim and panel', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openMembers(page)
  await tid(page, 'member-row').first().click()
  const drawer = tid(page, 'member-drawer')
  await expect(drawer, 'the member drawer never opened').toBeVisible()
  const scrimEl = drawer.locator('xpath=preceding-sibling::div[1]')
  await settle(page, drawer, scrimEl)
  const scrim = await expectScrim(scrimEl, 'member drawer')
  const panel = await styles(drawer, ['box-shadow', 'border-left-width', 'border-left-style', 'border-left-color'])
  expect(panel['box-shadow'], 'member drawer shadow').toBe('none')
  expect(`${panel['border-left-width']} ${panel['border-left-style']} ${panel['border-left-color']}`, 'member drawer left border').toBe(`1px solid ${LINE_2}`)
  await attachJson(testInfo, 'st-03-measurements', { scrim, panel })
  await attachShot(page, testInfo, 'member-drawer')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-04 Settings › Roles: card, avatars, holder spacing, role modal, picker focus ring', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  const writes: string[] = []
  page.on('request', (r) => {
    if (/\/workflow-roles/.test(r.url()) && ['POST', 'PATCH', 'PUT', 'DELETE'].includes(r.method())) writes.push(`${r.method()} ${r.url()}`)
  })
  await openRoles(page)

  const cards = tid(page, 'role-card')
  const card = cards.first()
  const cardCorners = await expectCorners(card, '6px', 'role card')
  expect(await border(card), 'role card border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(card, ['box-shadow']))['box-shadow'], 'role card shadow').toBe('none')
  await expectCorners(card.getByTestId('role-card-edit'), '7px', 'role card Edit')

  // A card with one to five holders, so no overflow chip sits between the avatars and the text.
  const index = await cards.evaluateAll((els) =>
    els.findIndex((el) => {
      const n = el.querySelectorAll('span[title]').length
      return n >= 1 && n <= 5
    }),
  )
  expect(index, 'no role card has one to five holders').toBeGreaterThanOrEqual(0)
  const held = cards.nth(index)
  const avatars = held.locator('span[title] > span')
  const avatarCount = await avatars.count()
  expect(avatarCount, 'the held card drew no avatars').toBeGreaterThanOrEqual(1)
  for (const a of await avatars.all()) {
    const s = await styles(a, ['border-top-left-radius', 'box-shadow'])
    expect(s, 'holder avatar').toEqual({ 'border-top-left-radius': '50%', 'box-shadow': 'none' })
  }
  const holderRow = held.locator(':scope > div').nth(1)
  const [lastAvatar, text] = [await avatars.last().boundingBox(), await holderRow.locator(':scope > span').last().boundingBox()]
  expect(lastAvatar && text, 'the holder row has no boxes').toBeTruthy()
  const distance = text!.x - (lastAvatar!.x + lastAvatar!.width)
  expect(Math.abs(distance - 20), `avatars sit ${distance}px from the holder text`).toBeLessThanOrEqual(0.5)

  await held.getByTestId('role-card-edit').click()
  const modal = tid(page, 'role-modal')
  await expect(modal, 'the role modal never opened').toBeVisible()
  const scrimEl = modal.locator('xpath=..')
  await settle(page, modal, scrimEl)
  const modalCorners = await expectCorners(modal, '10px', 'role modal')
  await expectShadow(modal, SHADOW_CARD, 'role modal')
  const scrim = await expectScrim(scrimEl, 'role modal')

  // Keyboard focus rings the painted box; a mouse click does not.
  await tid(page, 'role-modal-search').focus()
  await page.keyboard.press('Tab')
  const focus = await page.evaluate(() => {
    const el = document.activeElement
    return { check: el instanceof HTMLInputElement && el.classList.contains('pf-check'), shadow: el?.nextElementSibling ? getComputedStyle(el.nextElementSibling).boxShadow : '' }
  })
  expect(focus.check, 'Tab from the search box did not reach a picker checkbox').toBe(true)
  const ring = await resolveColor(modal, '--ring')
  expect(focus.shadow, 'keyboard focus ring').toBe(`${ring} 0px 0px 0px 2px`)
  // A different box from the keyboard-focused one: re-clicking a box that already matches :focus-visible keeps the ring.
  const checkboxes = modal.locator('input.pf-check')
  expect(await checkboxes.count(), 'the picker needs two members to separate keyboard from mouse focus').toBeGreaterThanOrEqual(2)
  const checkbox = checkboxes.nth(1)
  await checkbox.click()
  expect(await checkbox.evaluate((el) => el === document.activeElement), 'the mouse click did not focus the checkbox').toBe(true)
  const afterClick = await checkbox.evaluate((el) => getComputedStyle(el.nextElementSibling as Element).boxShadow)
  expect(afterClick, 'a mouse click draws no focus ring').toBe('none')

  await attachShot(page, testInfo, 'role-modal')
  await tid(page, 'role-modal-cancel').click()
  await expect(modal, 'Cancel did not close the role modal').toHaveCount(0)
  expect(writes, `a role write reached the gateway:\n${writes.join('\n')}`).toEqual([])
  await attachJson(testInfo, 'st-04-measurements', { cardCorners, avatarCount, distance, modalCorners, scrim, focus, ring, afterClick })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-05 ERP connectors: status pills, connector detail pill, field-mapping modal', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openSettings(page, 'firm')
  await openTab(page, 'ERP connectors', 'connectors')
  await page.getByRole('button', { name: 'Connect', exact: true }).first().click()
  const connected = page.getByText('CONNECTED', { exact: true }).first()
  await expect(connected, 'Connect did not turn a connector on').toBeVisible()
  await settle(page, main(page))
  const reads: Record<string, unknown> = {}
  for (const [label, text] of [['connected', 'CONNECTED'], ['idle', 'NOT CONNECTED']] as const) {
    const pill = page.getByText(text, { exact: true }).first()
    await expect(pill, `no ${text} pill`).toBeVisible()
    const box = pill.locator('xpath=..')
    reads[label] = {
      corners: await expectCorners(box, '4px', `${text} pill`),
      dot: (await styles(box.locator(':scope > span').first(), ['border-top-left-radius']))['border-top-left-radius'],
    }
    expect((reads[label] as { dot: string }).dot, `${text} dot`).toBe('50%')
  }
  await attachShot(page, testInfo, 'settings-connectors')

  await page.getByRole('button', { name: 'Manage', exact: true }).first().click()
  const envPill = tid(page, 'connector-env-pill')
  await expect(envPill, 'the connector detail never opened').toBeVisible()
  await settle(page, main(page))
  const envCorners = await expectCorners(envPill, '6px', 'env pill')

  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  const modal = page.getByRole('dialog', { name: 'Edit field mapping' })
  await expect(modal, 'the mapping modal never opened').toBeVisible()
  const scrimEl = modal.locator('xpath=..')
  await settle(page, modal, scrimEl)
  const modalCorners = await expectCorners(modal, '10px', 'mapping modal')
  await expectShadow(modal, SHADOW_CARD, 'mapping modal')
  const scrim = await expectScrim(scrimEl, 'mapping modal')
  await attachShot(page, testInfo, 'mapping-modal')
  await modal.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(modal, 'Cancel did not close the mapping modal').toHaveCount(0)

  await attachJson(testInfo, 'st-05-measurements', { reads, envCorners, modalCorners, scrim })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// Status pills are the spans that hold a 5px dot and a mono label.
const STATUS_PILLS = (root: Element) =>
  [...root.querySelectorAll('span')]
    .filter((s) => {
      const dot = s.firstElementChild
      if (!dot || s.children.length !== 2) return false
      const cs = getComputedStyle(dot)
      return cs.width === '5px' && cs.height === '5px' && cs.borderTopLeftRadius === '50%'
    })
    .map((s) => getComputedStyle(s).borderTopLeftRadius + ' ' + getComputedStyle(s).borderBottomRightRadius)

test('ST-06 API & webhooks, Signing & certificates and the in-house Company tab', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [])
  await openSettings(page, 'firm')
  const reads: Record<string, unknown> = {}
  for (const [label, id, empty] of [['API & webhooks', 'api', 'No webhooks yet'], ['Signing & certificates', 'signing', 'No signing certificate yet']] as const) {
    await openTab(page, label, id)
    await expect(page.getByText(empty, { exact: true }), `${label} drew no empty state`).toBeVisible()
    const column = tabStrip(page).locator('xpath=following-sibling::div[1]')
    const cards = column.locator(':scope > div')
    await expect(cards.first(), `${label} drew no cards`).toBeVisible()
    await settle(page, main(page))
    const pills = await main(page).evaluate(STATUS_PILLS)
    // A hand-off session draws no pills; unit SettingsView.test.tsx pins their corners.
    for (const p of pills) expect(p, `${label} status pill corners`).toBe('4px 4px')
    const n = await cards.count()
    for (let i = 0; i < n; i++) {
      await expectCorners(cards.nth(i), '6px', `${label} card ${i}`)
      expect((await styles(cards.nth(i), ['box-shadow']))['box-shadow'], `${label} card ${i} shadow`).toBe('none')
    }
    reads[id] = { pills: pills.length, cards: n }
    await attachShot(page, testInfo, `settings-${id}`)
  }

  await openSettings(page, 'inhouse')
  await openTab(page, 'Company', 'company')
  const companyCard = page.getByText('Your company', { exact: true }).locator('xpath=../..')
  await expect(companyCard, 'the Company tab drew no card').toBeVisible()
  await settle(page, main(page))
  reads.company = {
    card: await expectCorners(companyCard, '6px', 'company card'),
    edit: await expectCorners(page.getByRole('button', { name: 'Edit company', exact: true }), '7px', 'Edit company'),
    scroll: await assertPageDoesNotScrollSideways(page, 'Company'),
  }
  await attachShot(page, testInfo, 'settings-company')
  await attachJson(testInfo, 'st-06-measurements', reads)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-07 Reports exports are dimmed, not-allowed and unfilled', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await signInAs(page, 'firm')
  await navButton(page, /^Reports/).click()
  await expect(h1Of(page, 'Reports & analytics'), 'the Reports screen drew no h1').toBeVisible()
  const exportsList = page.locator('button[disabled][aria-describedby="exports-blocked-reason-text"]')
  await expect(exportsList, 'the export row draws four disabled buttons').toHaveCount(4, { timeout: 20_000 })
  await settle(page, main(page))
  const reads: Record<string, string>[] = []
  for (const btn of await exportsList.all()) {
    const paint = await styles(btn, ['opacity', 'cursor', 'background-color'])
    expect(paint, 'export paint').toEqual({ opacity: '0.45', cursor: 'not-allowed', 'background-color': 'rgba(0, 0, 0, 0)' })
    reads.push(paint)
  }
  await attachJson(testInfo, 'st-07-measurements', reads)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-08 the member menu stays inside its scroller at every wide width', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = gatedErrors(page, [])
  await openMembers(page)
  const rows = tid(page, 'member-row')
  const total = await rows.count()
  expect(total, 'the roster has fewer than two rows').toBeGreaterThanOrEqual(2)
  const table = tid(page, 'members-table')
  const scroller = table.locator('xpath=..')

  // The row whose menu names the most reasons.
  const reasonCounts: number[] = []
  for (let i = 0; i < total; i++) {
    const trigger = rows.nth(i).getByTestId('member-menu-trigger')
    await trigger.click()
    await expect(rows.nth(i).getByTestId('member-menu'), `row ${i} menu never opened`).toBeVisible()
    reasonCounts.push(await rows.nth(i).getByTestId('member-menu-reason').count())
    await trigger.click()
    await expect(rows.nth(i).getByTestId('member-menu'), `row ${i} menu never closed`).toHaveCount(0)
  }
  const most = reasonCounts.indexOf(Math.max(...reasonCounts))
  expect(reasonCounts[most], `no row menu names a reason: ${reasonCounts.join(', ')}`).toBeGreaterThanOrEqual(1)

  const reads: Record<string, unknown>[] = []
  for (const [label, index] of [['most-reasons', most], ['last', total - 1]] as const) {
    const row = rows.nth(index)
    const trigger = row.getByTestId('member-menu-trigger')
    const menu = row.getByTestId('member-menu')
    if (!(await menu.isVisible())) await trigger.click()
    await expect(menu, `${label}: the menu never opened`).toBeVisible()
    const measured = await sweep(page, `Member menu (${label})`, async () => {
      if (!(await menu.isVisible())) await trigger.click()
      await settle(page, menu)
      const { rects, problems } = await boxes({ scroller, menu, trigger })
      inside(rects, 'scroller', ['menu'], problems)
      noOverlap(rects, ['menu', 'trigger'], problems)
      const shadow = (await styles(menu, ['box-shadow']))['box-shadow']
      if (shadow !== SHADOW_CARD) problems.push(`menu shadow is ${shadow}`)
      const scroll = await scroller.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
      // One pixel absorbs the rounding of a fractional menu bottom.
      if (scroll.scrollHeight > scroll.clientHeight + 1) problems.push(`the scroller shows a vertical scrollbar: ${JSON.stringify(scroll)}`)
      return { problems, rects: { ...rects, scroll } }
    })
    reads.push({ label, index, measured })
    if (label === 'last') await attachShot(page, testInfo, 'member-menu')
  }

  // At 1280 no head label is truncated and the page does not scroll sideways.
  await page.setViewportSize({ width: 1280, height: 900 })
  const heads = table.locator(':scope > div').first().locator(':scope > span')
  expect(await heads.count(), 'the members head draws five cells').toBe(5)
  await expect
    .poll(() => heads.evaluateAll((els) => els.map((el) => el.scrollWidth - el.clientWidth)), { message: 'a head label is truncated at 1280', timeout: 10_000 })
    .toEqual([0, 0, 0, 0, 0])
  const scroll = await assertPageDoesNotScrollSideways(page, 'Members at 1280')

  await attachJson(testInfo, 'st-08-measurements', { reasonCounts, reads, scroll })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-09 the role matrix collapses inside its card and opens to three equal tracks', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openMembers(page)
  const toggle = tid(page, 'role-matrix-toggle')
  const card = toggle.locator('xpath=..')
  await expect(toggle, 'collapsed toggle').toHaveAttribute('aria-expanded', 'false')
  await expect(tid(page, 'role-matrix'), 'the matrix renders only when open').toHaveCount(0)
  const closed = await boxes({ card, toggle })
  inside(closed.rects, 'card', ['toggle'], closed.problems)
  expect(closed.problems, 'collapsed toggle placement').toEqual([])
  await attachShot(page, testInfo, 'role-matrix-collapsed')

  await toggle.click()
  await expect(toggle, 'opened toggle').toHaveAttribute('aria-expanded', 'true')
  const matrix = tid(page, 'role-matrix')
  await expect(matrix, 'the matrix never drew').toBeVisible()
  await settle(page, matrix)
  const open = await boxes({ card, matrix })
  inside(open.rects, 'card', ['matrix'], open.problems)
  expect(open.problems, 'open matrix placement').toEqual([])
  // Labelled headers only: an empty corner cell may or may not carry a header role.
  const heads = matrix.locator('[role="columnheader"]').filter({ hasText: /\S/ })
  expect(await heads.count(), 'the matrix draws three role columns').toBe(3)
  const widths = await heads.evaluateAll((els) => els.map((el) => el.getBoundingClientRect().width))
  expect(Math.max(...widths) - Math.min(...widths), `the three tracks differ: ${widths.join(', ')}`).toBeLessThanOrEqual(1)
  await attachShot(page, testInfo, 'role-matrix-open')

  const clientUsers = tid(page, 'client-users-card')
  await expect(clientUsers, 'the firm client-users card').toBeVisible()
  expect((await styles(clientUsers, ['border-top-style']))['border-top-style'], 'client-users border style').toBe('dashed')
  await expectCorners(clientUsers, '6px', 'client-users card')
  await attachJson(testInfo, 'st-09-measurements', { closed: closed.rects, open: open.rects, widths })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-10 the Members and Roles no-match states sit inside their containers', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openMembers(page)
  await page.getByLabel('Search members').fill(`no-match-${crypto.randomUUID()}`)
  const noMatch = tid(page, 'members-no-match')
  await expect(noMatch, 'a nonce search drew no members no-match row').toBeVisible()
  const table = tid(page, 'members-table')
  const head = table.getByText('Person', { exact: true }).locator('xpath=..')
  const members = await boxes({ table, head, noMatch })
  inside(members.rects, 'table', ['noMatch'], members.problems)
  if (members.rects.head && members.rects.noMatch && members.rects.noMatch.y < members.rects.head.y + members.rects.head.height - 1) {
    members.problems.push('the no-match row is not below the head row')
  }
  expect(members.problems, 'members no-match placement').toEqual([])
  await attachShot(page, testInfo, 'members-no-match')

  await openTab(page, 'Roles', 'roles')
  await expect(tid(page, 'role-card').first(), 'the roles grid drew no cards').toBeVisible({ timeout: 20_000 })
  await tid(page, 'roles-search').fill(`no-match-${crypto.randomUUID()}`)
  const rolesNoMatch = tid(page, 'roles-no-match')
  await expect(rolesNoMatch, 'a nonce search drew no roles no-match card').toBeVisible()
  const roles = await boxes({ main: main(page), rolesNoMatch })
  inside(roles.rects, 'main', ['rolesNoMatch'], roles.problems)
  expect(roles.problems, 'roles no-match placement').toEqual([])
  await attachShot(page, testInfo, 'roles-no-match')
  await attachJson(testInfo, 'st-10-measurements', { members: members.rects, roles: roles.rects })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-11 the role delete confirm is a red block with a solid red button, never confirmed', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  const writes: string[] = []
  page.on('request', (r) => {
    if (/\/workflow-roles/.test(r.url()) && ['POST', 'PATCH', 'PUT', 'DELETE'].includes(r.method())) writes.push(`${r.method()} ${r.url()}`)
  })
  await openRoles(page)
  await tid(page, 'role-card-edit').first().click()
  const modal = tid(page, 'role-modal')
  await expect(modal, 'the role modal never opened').toBeVisible()
  await tid(page, 'role-delete').click()
  const confirm = tid(page, 'role-delete-confirm')
  await expect(confirm, 'Delete role drew no confirm block').toBeVisible()
  await settle(page, modal)
  const redBg = await resolveColor(modal, '--status-red-bg')
  const redText = await resolveColor(modal, '--status-red-text')
  expect((await styles(confirm, ['background-color']))['background-color'], 'confirm block background').toBe(redBg)
  const confirmed = tid(page, 'role-delete-confirmed')
  expect((await styles(confirmed, ['background-color']))['background-color'], 'confirm button fill').toBe(redText)
  await attachShot(page, testInfo, 'role-delete-confirm')

  await tid(page, 'role-delete-cancel').click()
  await expect(confirm, 'Keep role did not dismiss the confirm block').toHaveCount(0)
  await expect(tid(page, 'role-modal-name'), 'Keep role did not return to the form').toBeVisible()
  await tid(page, 'role-modal-cancel').click()
  await expect(modal, 'Cancel did not close the role modal').toHaveCount(0)
  expect(writes, `a role write reached the gateway:\n${writes.join('\n')}`).toEqual([])
  await attachJson(testInfo, 'st-11-measurements', { redBg, redText })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-12 connector detail: five cards, 6px, flat, inside main and clear of each other', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [])
  await openSettings(page, 'firm')
  await openTab(page, 'ERP connectors', 'connectors')
  await page.getByRole('button', { name: 'Connect', exact: true }).first().click()
  await expect(page.getByText('CONNECTED', { exact: true }).first(), 'Connect did not turn a connector on').toBeVisible()
  await page.getByRole('button', { name: 'Manage', exact: true }).first().click()
  const envPill = tid(page, 'connector-env-pill')
  await expect(envPill, 'the connector detail never opened').toBeVisible()
  await settle(page, main(page))

  const segments: Record<string, unknown> = {}
  for (const [label, seg] of [['SANDBOX', envPill.getByRole('button', { name: 'SANDBOX' })], ['LIVE', tid(page, 'connector-env-pill-live')]] as const) {
    const s = await styles(seg, ['opacity', 'border-top-left-radius'])
    expect(s, `${label} segment`).toEqual({ opacity: '0.45', 'border-top-left-radius': '4px' })
    segments[label] = s
  }

  const headings = ['Reconciliation · ERP ↔ clearance', 'Documents pulled', 'Sync activity', 'Master data mirror', 'Held documents']
  const named: Named = {}
  const paint: Record<string, unknown> = {}
  for (const h of headings) {
    const card = page.getByText(h, { exact: true }).locator('xpath=ancestor::div[contains(@style,"border-radius")][1]')
    await expect(card, `the "${h}" card never drew`).toBeVisible()
    paint[h] = { corners: await expectCorners(card, '6px', h), shadow: (await styles(card, ['box-shadow']))['box-shadow'] }
    expect((paint[h] as { shadow: string }).shadow, `${h} shadow`).toBe('none')
    named[h] = card
  }
  const { rects, problems } = await boxes({ main: main(page), ...named })
  // main scrolls, so cards below the fold lie outside its visible rect by design; only the horizontal band is a claim.
  insideX(rects, 'main', headings, problems)
  noOverlap(rects, headings, problems)
  expect(problems, 'connector detail card placement').toEqual([])
  await attachJson(testInfo, 'st-12-measurements', { segments, paint, rects })
  await attachShot(page, testInfo, 'connector-detail')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('ST-13 undrawn states: a held and failed role save, a failed member status change', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 500, ROLE_WRITE_RE), expectedStatusDropper(page, 500, MEMBERSHIP_RE)])
  await openRoles(page)
  const { gate, release } = gateOf()
  await page.route(ROLE_WRITE_GLOB, async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    await gate
    await failWith(route, 500)
  })
  try {
    await tid(page, 'role-card-edit').first().click()
    const modal = tid(page, 'role-modal')
    await expect(modal, 'the role modal never opened').toBeVisible()
    const desc = tid(page, 'role-modal-desc')
    await desc.fill(`${await desc.inputValue()} (stub)`)
    const save = tid(page, 'role-modal-save')
    await save.click()
    await expect(save, 'a held save must read Saving…').toHaveText('Saving…')
    await settle(page, modal)
    expect((await styles(save, ['opacity'])).opacity, 'Saving… opacity').toBe('0.45')
    await attachShot(page, testInfo, 'role-saving')
  } finally {
    release()
  }
  const error = tid(page, 'role-modal-error')
  await expect(error, 'a failed save drew no error').toBeVisible()
  const redBg = await resolveColor(error, '--status-red-bg')
  expect((await styles(error, ['background-color']))['background-color'], 'role error background').toBe(redBg)
  const errorCorners = await expectCorners(error, '6px', 'role error')
  await attachShot(page, testInfo, 'role-save-error')
  await tid(page, 'role-modal-cancel').click()
  await expect(tid(page, 'role-modal'), 'Cancel did not close the role modal').toHaveCount(0)
  await page.unrouteAll({ behavior: 'wait' })

  await openTab(page, 'Members', 'members')
  await expect(tid(page, 'member-row').first(), 'the roster drew no rows').toBeVisible({ timeout: 20_000 })
  await page.route(MEMBERSHIP_GLOB, (route) => (route.request().method() === 'PATCH' ? failWith(route, 500) : route.fallback()))
  const rows = tid(page, 'member-row')
  const total = await rows.count()
  let suspended = false
  for (let i = 0; i < total && !suspended; i++) {
    const trigger = rows.nth(i).getByTestId('member-menu-trigger')
    await trigger.click()
    const item = rows.nth(i).getByTestId('member-menu').getByRole('button', { name: 'Suspend', exact: true })
    if ((await item.count()) === 1 && (await item.isEnabled())) {
      await item.click()
      suspended = true
    } else {
      await trigger.click()
    }
  }
  expect(suspended, 'no roster row offers an enabled Suspend').toBe(true)
  const strip = tid(page, 'member-status-error')
  await expect(strip, 'a failed status change drew no error strip').toBeVisible()
  const stripColour = (await styles(strip, ['color'])).color
  expect(stripColour, 'status error colour').toBe(await resolveColor(strip, '--status-red-text'))
  await attachShot(page, testInfo, 'member-status-error')
  await page.unrouteAll({ behavior: 'wait' })

  await attachJson(testInfo, 'st-13-measurements', { redBg, errorCorners, stripColour })
  expect(errors, `console errors beyond the deliberate 500s:\n${errors.join('\n')}`).toEqual([])
})

test('ST-14 no Settings tab scrolls sideways and the roster fills the tab column', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [])
  await openMembers(page)
  const fit = await assertFillsColumn(page, tid(page, 'members-table'), tabStrip(page), 'members table')
  const scroll: Record<string, unknown> = { members: await assertPageDoesNotScrollSideways(page, 'Members') }
  for (const [label, id] of [['Roles', 'roles'], ['ERP connectors', 'connectors'], ['API & webhooks', 'api'], ['Signing & certificates', 'signing']] as const) {
    await openTab(page, label, id)
    await settle(page, main(page))
    scroll[id] = await assertPageDoesNotScrollSideways(page, label)
  }
  await attachJson(testInfo, 'st-14-measurements', { fit, scroll })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
