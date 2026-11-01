// The deployed v2 Overview, Invoices and Approvals: resolved values and layout relationships on the PR environment.
// Every state is reached with page.route (D-12); no tenant data is written and no confirm button is clicked (D-13).
// The api-client Loading and ErrorState assert placement only; the in-card and dense empties assert resolved values (OV-04, IN-02).
// Screenshots are attached for the reviewer and never asserted.
import { test, expect, type Locator, type Page, type Response, type Route, type TestInfo } from '@playwright/test'
import type { DashboardClient, RollupHistoryRow } from '../api/client'
import { collectErrors, signInAs } from '../personaSession'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, GATEWAY_URL, shardTenants } from './targets'

const SHARD = shardTenants('overview-register-approvals.spec.ts')
test.beforeAll(() => { expect(test.info().project.name, 'overview-register-approvals.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

test.use({ viewport: { width: 1440, height: 900 } })

const GATEWAY_ORIGIN = new URL(GATEWAY_URL).origin
const CORS = { 'access-control-allow-origin': new URL(APP_URL).origin }
const EMPTY_INVOICES = { invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }
const EMPTY_PAGE = { invoices: [], pagination: { limit: 50, offset: 0, total: 120 } }
const EMPTY_ENTITIES = { entities: [], pagination: { limit: 200, offset: 0, total: 0 } }
const UNAVAILABLE = { error: 'service unavailable' }
const VIOLATIONS = [
  { rule_key: 'no-duplicate-invoice-number', invoices: 12345 }, // longest seeded key: ruleKeyDuplicateInvoiceNumber, internal/importer/service.go
  { rule_key: 'buyer-tin-format', invoices: 3 },
  { rule_key: 'vat-standard-rate', invoices: 1 },
  { rule_key: 'custom:3f2a0000-0000-4000-8000-000000000001', invoices: 2 }, // the key cell draws "Custom rule"
]
const APPROVALS_LOADING = 'Loading approvals…' // APPROVALS_COPY.loading, frontend/app/src/lib/approvals.ts

const LINE = 'rgb(220, 231, 228)'
const WHITE = 'rgb(255, 255, 255)'
const BAR_TINT = 'rgba(7, 60, 61, 0.1)'
const BAR_EDGE = 'rgb(203, 241, 221)'
const ACTION = 'rgb(7, 60, 61)'

const firstFamily = (raw: string): string => raw.split(',')[0].replace(/["']/g, '').trim()

// A copy of app-shell.spec.ts's gatedErrors: collectErrors minus the listed deliberate non-2xx answers.
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

async function radii(loc: Locator): Promise<string[]> {
  return Object.values(await styles(loc, CORNERS))
}

const corners = (px: string): string[] => Array(4).fill(px)

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

async function attachShot(page: Page, testInfo: TestInfo, name: string): Promise<void> {
  await testInfo.attach(`${name}.png`, { body: await page.screenshot(), contentType: 'image/png' })
}

const navButton = (page: Page, label: RegExp) => page.locator('aside.pf-sidebar nav button.pf-nav', { hasText: label })
const heading = (page: Page) => page.locator('main.pf-main .pf-scroll h1:visible').first()
const main = (page: Page) => page.locator('main.pf-main')
const column = (page: Page) => page.locator('main.pf-main .pf-scroll')

// Click a nav item and wait until it is the active one and its screen's heading has drawn.
async function openNav(page: Page, button: Locator): Promise<void> {
  await button.click()
  await expect(button, 'the clicked nav row never became the active one (weight 600)').toHaveCSS('font-weight', '600')
  await expect(heading(page), 'the screen drew no visible h1').toBeVisible()
}

// A screen re-fetches when it mounts again: leave it and come back.
async function bounce(page: Page, away: RegExp, back: RegExp): Promise<void> {
  await openNav(page, navButton(page, away))
  await openNav(page, navButton(page, back))
}

// Named boxes in one settled read; a locator with no box throws, which expect.poll retries.
async function boxes<K extends string>(page: Page, named: Record<K, Locator>): Promise<Record<K, Rect>> {
  await settle(page, ...Object.values<Locator>(named))
  const out = {} as Record<K, Rect>
  for (const k of Object.keys(named) as K[]) {
    const box = await named[k].boundingBox()
    if (!box) throw new Error(`${k} has no box`)
    out[k] = box
  }
  return out
}

// The child of the padded page wrapper (`30px 36px 56px`) that holds `loc`.
async function wrapperChild(page: Page, loc: Locator): Promise<Rect> {
  await settle(page, loc)
  return loc.evaluate((el) => {
    let node = el as HTMLElement
    for (;;) {
      const parent = node.parentElement
      if (!parent) throw new Error('no padded page wrapper above the element')
      const cs = getComputedStyle(parent)
      if (cs.paddingTop === '30px' && cs.paddingLeft === '36px') {
        const r = node.getBoundingClientRect()
        return { x: r.x, y: r.y, width: r.width, height: r.height }
      }
      node = parent
    }
  })
}

const within = (outer: Rect, inner: Rect, what: string): string[] => (enclosesRect(outer, inner, 1) ? [] : [`${what}: sticks out`])
const apart = (a: Rect, b: Rect, what: string): string[] => (rectsOverlap(a, b) ? [`${what}: overlap`] : [])
const below = (above: Rect, lower: Rect, what: string): string[] => (lower.y >= above.y + above.height - 1 ? [] : [`${what}: not below`])
const leftOf = (a: Rect, b: Rect, what: string): string[] => (a.x + a.width <= b.x + 1 ? [] : [`${what}: not left of`])
const sameLeft = (a: Rect, b: Rect, what: string): string[] => (Math.abs(a.x - b.x) <= 1 ? [] : [`${what}: left edges ${a.x} vs ${b.x}`])
const centreY = (r: Rect): number => r.y + r.height / 2

type Read = { problems: string[]; rects: Record<string, unknown> }

// Reads at every wide width until `read` reports no problem; the entry viewport is restored.
async function atWidths(page: Page, label: string, read: () => Promise<Read>, widths: readonly number[] = WIDE_WIDTHS): Promise<unknown[]> {
  const entry = page.viewportSize()
  const out: unknown[] = []
  try {
    for (const width of widths) {
      await page.setViewportSize({ width, height: 1080 })
      let rects: unknown
      await expect
        .poll(
          async () => {
            // expect.poll does not retry a throw, so a throw becomes a problem line.
            try {
              const r = await read()
              rects = r.rects
              return r.problems
            } catch (err) {
              return [`read threw: ${String((err as Error).message).split('\n')[0]}`]
            }
          },
          { message: `${label} at ${width}px`, timeout: 10_000 },
        )
        .toEqual([])
      out.push({ width, ...(rects as object) })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  return out
}

type Mode = 'pass' | 'hold' | '503' | 'empty' | 'emptyPage' | 'transform'
type Stub = { mode: Mode; hold(): void; release(next: Mode): void }

// GET-only route over the gateway. OPTIONS preflights and other methods go through untouched (D-12).
async function routeGet(page: Page, match: (url: URL) => boolean, handler: (route: Route, url: URL) => Promise<void>): Promise<void> {
  await page.route(
    (url) => url.origin === GATEWAY_ORIGIN && match(url),
    (route) => {
      const req = route.request()
      if (req.method() !== 'GET') return route.continue()
      return handler(route, new URL(req.url()))
    },
  )
}

const fulfil = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', headers: CORS, body: JSON.stringify(body) })

// One route serving a test's states in turn. `hold` parks every matching GET until `release` picks the answer.
async function stubGet(
  page: Page,
  match: (url: URL) => boolean,
  opts: { mode?: Mode; empty?: unknown; source?: (url: URL) => URL; transform?: (body: Record<string, any>) => unknown } = {},
): Promise<Stub> {
  let open: () => void = () => undefined
  let gate: Promise<void> = Promise.resolve()
  const stub: Stub = {
    mode: opts.mode ?? 'pass',
    hold() {
      stub.mode = 'hold'
      gate = new Promise<void>((resolve) => (open = resolve))
    },
    release(next) {
      stub.mode = next
      open()
    },
  }
  await routeGet(page, match, async (route, url) => {
    if (stub.mode === 'hold') await gate
    switch (stub.mode) {
      case '503':
        return void (await fulfil(route, UNAVAILABLE, 503))
      case 'empty':
        return void (await fulfil(route, opts.empty))
      case 'emptyPage':
        return void (await fulfil(route, EMPTY_PAGE))
      case 'transform': {
        const response = await route.fetch({ url: (opts.source?.(url) ?? url).toString() })
        return void (await route.fulfill({ response, json: opts.transform!(await response.json()) }))
      }
      default:
        return void (await route.continue())
    }
  })
  return stub
}

const onPath = (url: URL, suffix: string): boolean => url.pathname.endsWith(suffix)
const isRollup = (url: URL): boolean => onPath(url, '/api/dashboard/v1/rollup')
const isEntities = (url: URL): boolean => onPath(url, '/api/portfolio/v1/entities')
const isRegister = (url: URL): boolean => onPath(url, '/api/invoice/v1/invoices') && !url.searchParams.has('awaiting_approval')
const isQueue = (url: URL): boolean => onPath(url, '/api/invoice/v1/invoices') && url.searchParams.get('awaiting_approval') === 'true'

// Gateway requests by kind (D-13). `reads` is the control: an empty `writes` proves nothing if the listener saw no traffic.
function recordGateway(page: Page): { writes: string[]; reads: string[] } {
  const seen = { writes: [] as string[], reads: [] as string[] }
  page.on('request', (r) => {
    if (new URL(r.url()).origin !== GATEWAY_ORIGIN) return
    if (r.method() === 'GET') seen.reads.push(r.url())
    else if (r.method() !== 'OPTIONS') seen.writes.push(`${r.method()} ${r.url()}`)
  })
  return seen
}

function expectNoWrites(seen: { writes: string[]; reads: string[] }): void {
  expect(seen.reads.length, 'the request listener saw no gateway GET, so an empty write list proves nothing').toBeGreaterThan(0)
  expect(seen.writes, 'arming and cancelling must send no gateway write (D-13)').toEqual([])
}

// An in-flight row starts the register's 2s poll, and each poll's fresh rows disarm the bulk bar.
const notInFlight = (r: { status?: string }) => (r.status === 'queued' || r.status === 'submitted' ? { ...r, status: 'accepted' } : r)
const canSubmitFirst = (body: Record<string, any>) => ({
  ...body,
  invoices: body.invoices.map((r: object, i: number) => notInFlight(i === 0 ? { ...r, can_submit: true, submit_blocked_reason: null } : r)),
})
const canApproveFirst = (body: Record<string, any>) => ({ ...body, invoices: body.invoices.map((r: object, i: number) => (i === 0 ? { ...r, can_approve: true, approve_blocked_reason: null } : r)) })

async function expectVisibleRows(rows: Locator, why: string): Promise<void> {
  await expect(rows.first(), why).toBeVisible({ timeout: 20_000 })
  expect(await rows.count(), why).toBeGreaterThanOrEqual(1)
}

async function expectH1(page: Page, size: string, what: string): Promise<Record<string, string>> {
  const h1 = await styles(heading(page), ['font-family', 'font-weight', 'font-size'])
  expect(firstFamily(h1['font-family']), `${what} h1 family`).toBe('Manrope')
  expect(h1['font-weight'], `${what} h1 weight`).toBe('700')
  expect(h1['font-size'], `${what} h1 size`).toBe(size)
  return h1
}

async function expectListCard(list: Locator, what: string): Promise<Record<string, string>> {
  const card = await styles(list, [...CORNERS, 'border-top-width', 'border-right-width', 'border-bottom-width', 'border-left-width', 'border-top-color', 'border-right-color', 'border-bottom-color', 'border-left-color', 'box-shadow'])
  expect(Object.values(card).slice(0, 4), `${what} corners`).toEqual(corners('6px'))
  for (const side of ['top', 'right', 'bottom', 'left']) {
    expect(card[`border-${side}-width`], `${what} border-${side} width`).toBe('1px')
    expect(card[`border-${side}-color`], `${what} border-${side} colour`).toBe(LINE)
  }
  expect(card['box-shadow'], `${what} shadow`).toBe('none')
  return card
}

type Bulk = { list: string; row: string; select: string; bar: string; submit: string; cancel: string; confirm: string; pager: string }

// Idle bar, armed bar (never confirmed), then the bar and pager relations at every wide width. Leaves the row unticked.
async function bulkFlow(
  page: Page,
  testInfo: TestInfo,
  ids: Bulk,
  o: { armedShot: string; centred: boolean; header?: () => Promise<Read> },
): Promise<Record<string, unknown>> {
  const bar = page.getByTestId(ids.bar)
  const tick = page.locator(`[data-testid="${ids.select}"]:not([disabled])`).first()
  await expect(tick, 'a selectable row: the transformed first row').toBeVisible()
  await tick.check()
  await expect(bar, 'ticking a row shows the bulk bar').toBeVisible()
  await settle(page, bar, tick)

  const idle = await styles(bar, ['background-color', 'border-top-color', ...CORNERS])
  expect(idle['background-color'], 'idle bar background').toBe(BAR_TINT)
  expect(idle['border-top-color'], 'idle bar border').toBe(BAR_EDGE)
  expect(Object.values(idle).slice(2), 'idle bar corners').toEqual(corners('6px'))
  const submit = page.getByTestId(ids.submit)
  expect(await radii(submit), 'bulk submit corners').toEqual(corners('7px'))
  const label = bar.locator('span.mono').first()
  expect(firstFamily((await styles(label, ['font-family']))['font-family']), 'count label family').toBe('IBM Plex Mono')
  const box = await styles(tick, ['width', 'height', 'accent-color'])
  expect(box, 'ticked checkbox').toEqual({ width: '15px', height: '15px', 'accent-color': ACTION })

  await submit.click()
  const cancel = page.getByTestId(ids.cancel)
  const confirm = page.getByTestId(ids.confirm)
  await expect(confirm, 'arming shows the confirm button').toBeVisible()
  const actionRow = bar.locator('> div').first()
  const block = bar.locator('> div').last()
  const armed = await boxes(page, { bar, actionRow, block, cancel, confirm })
  const blockStyle = await styles(block, ['border-top-width', 'border-top-color'])
  expect(blockStyle, 'confirm block top border').toEqual({ 'border-top-width': '1px', 'border-top-color': BAR_EDGE })
  expect(
    [
      ...below(armed.actionRow, armed.block, 'confirm block vs action row'),
      ...within(armed.bar, armed.block, 'confirm block in the bar'),
      ...apart(armed.cancel, armed.confirm, 'cancel vs confirm'),
      ...leftOf(armed.cancel, armed.confirm, 'cancel vs confirm'),
    ],
    'armed bar relations',
  ).toEqual([])
  expect(await radii(cancel), 'cancel corners').toEqual(corners('7px'))
  expect(await radii(confirm), 'confirm corners').toEqual(corners('7px'))
  await attachShot(page, testInfo, o.armedShot)
  await cancel.click()
  await expect(submit, 'cancel disarms the bar').toBeVisible()

  const list = page.getByTestId(ids.list)
  const pager = page.getByTestId(ids.pager)
  const prev = pager.getByRole('button', { name: /Previous/ })
  const next = pager.getByRole('button', { name: /Next/ })
  expect(await radii(prev), 'Previous corners').toEqual(corners('7px'))
  expect(await radii(next), 'Next corners').toEqual(corners('7px'))
  const barButtons = await bar.getByRole('button').all()
  expect(barButtons.length, 'the idle bar draws its buttons').toBeGreaterThanOrEqual(1)
  const widths = await atWidths(page, `${ids.bar} and ${ids.pager}`, async () => {
    const named: Record<string, Locator> = {
      bar, label, list, pager, prev, next,
      lastRow: page.getByTestId(ids.row).last(),
      range: pager.locator('> span.mono').nth(0),
      pageLabel: pager.locator('> span.mono').nth(1),
    }
    barButtons.forEach((b, i) => (named[`button${i}`] = b))
    const b = await boxes(page, named)
    const problems = [
      ...within(b.list, b.pager, 'pager in the list'),
      ...below(b.lastRow, b.pager, 'pager vs last row'),
      ...leftOf(b.range, b.pageLabel, 'range vs page label'),
      ...leftOf(b.pageLabel, b.prev, 'page label vs Previous'),
      ...leftOf(b.prev, b.next, 'Previous vs Next'),
    ]
    for (const [name, r] of [['Previous', b.prev], ['Next', b.next]] as const) if (Math.abs(r.height - 30) > 0.5) problems.push(`${name} is ${r.height}px tall`)
    barButtons.forEach((_, i) => {
      problems.push(...apart(b.label, b[`button${i}`], `count label vs button ${i}`), ...within(b.bar, b[`button${i}`], `button ${i} in the bar`))
    })
    problems.push(...within(b.bar, b.label, 'count label in the bar'))
    if (o.centred && Math.abs(centreY(b.label) - centreY(b.button0)) > 1) problems.push('count label and button centres differ')
    const extra = await o.header?.()
    await assertPageDoesNotScrollSideways(page, ids.list)
    return { problems: [...problems, ...(extra?.problems ?? [])], rects: { ...b, ...extra?.rects } }
  })

  await tick.uncheck()
  return { idle, checkbox: box, armedBlock: blockStyle, armed, widths }
}

test('OV-01 firm overview at 1440: heading, KPI cards, pill, button, legend', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const kpis = page.locator('.pf-dash-row-a .pf-grid-2 > div')
  await expect(kpis, 'the dashboard drew its four KPI tiles').toHaveCount(4, { timeout: 20_000 })
  await settle(page, column(page))

  const h1 = await expectH1(page, '28px', 'overview')
  const tiles: Record<string, unknown>[] = []
  for (const tile of await kpis.all()) {
    const card = await styles(tile, [...CORNERS, 'border-top-width', 'border-top-style', 'border-top-color', 'box-shadow', 'background-color'])
    const money = await styles(tile.locator('.money').first(), ['font-family', 'font-variant-numeric', 'font-weight'])
    expect(Object.values(card).slice(0, 4), 'KPI card corners').toEqual(corners('6px'))
    expect(card['border-top-width'], 'KPI card border width').toBe('1px')
    expect(card['border-top-style'], 'KPI card border style').toBe('solid')
    expect(card['border-top-color'], 'KPI card border colour').toBe(LINE)
    expect(card['box-shadow'], 'KPI card shadow').toBe('none')
    expect(card['background-color'], 'KPI card background').toBe(WHITE)
    expect(firstFamily(money['font-family']), 'KPI value family').toBe('Manrope')
    expect(money['font-variant-numeric'], 'KPI value numerals').toBe('tabular-nums')
    expect(money['font-weight'], 'KPI value weight').toBe('700')
    tiles.push({ card, money })
  }

  const pill = page.locator('.pf-dash-row-b span.mono').filter({ hasText: /^(REJECTED \/ FAILED \/ BLOCKED \/ SENT BACK|ALL CLEAR)$/ })
  await expect(pill, 'exactly one Needs-attention pill').toHaveCount(1)
  const pillRadii = await radii(pill)
  expect(pillRadii, 'pill corners').toEqual(corners('4px'))
  const cta = page.locator('.pf-dash-row-b button.pf-btn')
  expect(await cta.count(), 'the Needs-attention button').toBeGreaterThanOrEqual(1)
  const ctaRadii: string[][] = []
  for (const b of await cta.all()) {
    ctaRadii.push(await radii(b))
    expect(ctaRadii.at(-1), 'Needs-attention button corners').toEqual(corners('7px'))
  }
  const legend = page.locator('.pf-dash-row-b > div:nth-child(2) [style*="grid-template-columns: 10px"]')
  await expect(legend, 'the Invoice status legend holds the seven states').toHaveCount(7)
  const swatches: string[][] = []
  for (const row of await legend.all()) {
    swatches.push(await radii(row.locator('> span').first()))
    expect(swatches.at(-1), 'legend swatch corners').toEqual(corners('2px'))
  }
  const scroll = await assertPageDoesNotScrollSideways(page, 'overview')

  await attachJson(testInfo, 'ov-01-measurements', { h1, tiles, pillRadii, ctaRadii, swatches, scroll })
  await attachShot(page, testInfo, 'overview-active')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('OV-02 in-house overview: tiles hold their head and body, columns align, failure rows keep their cells apart', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await stubGet(page, isRollup, {
    mode: 'transform',
    transform: (b) => ({
      ...b,
      top_violations: VIOLATIONS,
      totals: { ...b.totals, top_violations: VIOLATIONS },
      clients: (b.clients ?? []).map((c: object) => ({ ...c, top_violations: VIOLATIONS })),
    }),
  })
  // Fixture check: the snapshot worker records a row per day, so a gate re-run on a later WAT day holds two.
  await stubGet(page, (url) => onPath(url, '/api/dashboard/v1/rollup/history'), { mode: 'empty', empty: { history: [] } })
  await signInAs(page, 'inhouse', { tenantId: SHARD.b.id })
  await expect(page.locator('.pf-dash-row-a .pf-grid-2 > div'), 'the dashboard drew its four KPI tiles').toHaveCount(4, { timeout: 20_000 })

  const groups = [
    ['readiness tile', page.locator('.pf-dash-row-a > div:first-child'), 1],
    ['KPI tile', page.locator('.pf-dash-row-a .pf-grid-2 > div'), 4],
    ['row B tile', page.locator('.pf-dash-row-b > div'), 2],
    ['row C tile', page.locator('.pf-dash-row-c > div'), 2],
    ['trend tile', page.getByText('Readiness trend', { exact: true }).locator('xpath=../..'), 1],
  ] as const
  const tiles: [string, Locator][] = []
  for (const [name, loc, want] of groups) {
    await expect(loc, `${name} count`).toHaveCount(want)
    for (const [i, tile] of (await loc.all()).entries()) tiles.push([`${name} ${i}`, tile])
  }
  expect(tiles.length, 'ten tiles on the overview').toBe(10)

  const kpis = (await groups[1][1].all()).map((tile, i) => [`KPI tile ${i}`, tile] as const)
  expect(kpis.length, 'four KPI tiles to measure').toBe(4)
  for (const [name, tile] of kpis) await expect(tile.locator('svg'), `${name} draws no sparkline`).toHaveCount(0)
  const trend = tiles.find(([name]) => name === 'trend tile 0')![1]
  await expect(trend.locator('> :first-child .mono'), 'the trend tile meta is exactly 12 WEEKS').toHaveText('12 WEEKS')
  await expect(trend, 'the trend tile carries no sample label').not.toContainText('SAMPLE')

  const needs = page.locator('.pf-dash-row-b > div:first-child')
  const sentence = needs.locator('p')
  const needsCta = needs.getByRole('button')
  const edges = [page.locator('.pf-dash-row-b > div:nth-child(2)'), page.locator('.pf-dash-row-c > div:nth-child(2)'), page.locator('.pf-grid-2 > div:nth-child(2)')]
  // The key cell carries the raw key as its title; a custom key's text is the label "Custom rule".
  const failureKeys = VIOLATIONS.map((v) => page.locator('.pf-dash-row-c').locator(`[title="${v.rule_key}"]`))
  for (const key of failureKeys) await expect(key, 'the injected failure row drew').toBeVisible()
  await expect(failureKeys[VIOLATIONS.length - 1], 'a custom key reads as "Custom rule"').toHaveText('Custom rule')

  const widths = await atWidths(page, 'overview tiles', async () => {
    const problems: string[] = []
    const rects: Record<string, unknown> = {}
    for (const [name, tile] of tiles) {
      const b = await boxes(page, { tile, head: tile.locator('> :first-child'), body: tile.locator('> :last-child') })
      problems.push(...within(b.tile, b.head, `${name} head`), ...within(b.tile, b.body, `${name} body`), ...below(b.head, b.body, `${name} body vs head`))
      rects[name] = b
    }
    // 138 is the KPI tile minHeight in DashboardActive.tsx.
    for (const [name, tile] of kpis) {
      const k = (await boxes(page, { tile })).tile
      if (k.height < 138) problems.push(`${name} is ${k.height}px tall, under 138px`)
      rects[`${name} height`] = k
    }
    const e = await boxes(page, { a: edges[0], b: edges[1], c: edges[2] })
    problems.push(...sameLeft(e.a, e.b, 'row B vs row C right column'), ...sameLeft(e.a, e.c, 'row B right column vs KPI column'))
    const n = await boxes(page, { needs, sentence, cta: needsCta })
    problems.push(...within(n.needs, n.cta, 'Needs-attention button'), ...(n.cta.y >= n.sentence.y + n.sentence.height - 1 ? [] : ['button above the sentence bottom']))
    if (!(n.cta.width < n.sentence.width)) problems.push(`button ${n.cta.width}px is not narrower than the sentence ${n.sentence.width}px`)
    for (const [i, key] of failureKeys.entries()) {
      const row = key.locator('xpath=..')
      const cells = row.locator('> *')
      if ((await cells.count()) !== 4) problems.push(`failure row ${i} has ${await cells.count()} cells, not 4`)
      const cellBoxes: Rect[] = []
      for (const cell of await cells.all()) cellBoxes.push((await boxes(page, { cell })).cell)
      const rowBox = (await boxes(page, { row })).row
      cellBoxes.forEach((c, a) => {
        problems.push(...within(rowBox, c, `failure row ${i} cell ${a}`))
        cellBoxes.slice(a + 1).forEach((d, k) => problems.push(...apart(c, d, `failure row ${i} cells ${a}/${a + 1 + k}`)))
      })
      // Lines are distinct rect tops: a clipped one-line key reports a second, wider rect on the same line.
      const { lines, clipped } = await key.evaluate((el) => {
        const range = document.createRange()
        range.selectNodeContents(el)
        const tops = new Set([...range.getClientRects()].map((r) => Math.round(r.top)))
        return { lines: tops.size, clipped: el.scrollWidth > el.clientWidth }
      })
      if (lines !== 1) problems.push(`failure key ${i} draws on ${lines} lines`)
      if (VIOLATIONS[i].rule_key.startsWith('custom:') && clipped) problems.push(`the Custom rule label is clipped in its key cell (failure key ${i})`)
      const count = await cells.last().evaluate((el) => el.scrollWidth - el.clientWidth)
      if (count > 1) problems.push(`failure count ${i} overflows its cell by ${count}px`)
      rects[`failure row ${i}`] = { row: rowBox, cells: cellBoxes, lines, clipped, countOverflow: count }
    }
    await assertPageDoesNotScrollSideways(page, 'overview')
    return { problems, rects }
  })

  await attachJson(testInfo, 'ov-02-measurements', widths)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('OV-03 overview loading and error: placement under the header', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, /\/api\/dashboard\/v1\/rollup/)])
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const rollup = await stubGet(page, isRollup)
  const read = async (text: Locator): Promise<{ header: Rect; state: Rect; h1: Rect; problems: string[] }> => {
    const b = await boxes(page, { header: heading(page).locator('xpath=..'), h1: heading(page) })
    const state = await wrapperChild(page, text)
    return { ...b, state, problems: [...below(b.header, state, 'state vs header'), ...sameLeft(state, b.h1, 'state vs h1')] }
  }

  rollup.hold()
  await bounce(page, /^Invoices/, /^Overview/)
  const loading = main(page).getByText('Loading dashboard…')
  await expect(loading, 'the held rollup shows the loading label').toBeVisible()
  const loadingRead = await read(loading)
  expect(loadingRead.problems, 'loading placement').toEqual([])
  await attachShot(page, testInfo, 'overview-loading')

  rollup.release('503')
  const failed = main(page).getByText('Something went wrong', { exact: true })
  await expect(failed, 'the 503 shows the error card').toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Retry' }), 'the error card offers Retry').toBeVisible()
  const errorRead = await read(failed)
  expect(errorRead.problems, 'error placement').toEqual([])
  await attachShot(page, testInfo, 'overview-error')

  await attachJson(testInfo, 'ov-03-measurements', { loading: loadingRead, error: errorRead })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

test('OV-04 empty workspace: loading, task card, error, placement', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, /\/api\/portfolio\/v1\/entities/)])
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const entities = await stubGet(page, isEntities, { empty: EMPTY_ENTITIES })

  entities.hold()
  await page.reload()
  const loadingLabel = page.getByText('Loading your workspace…')
  await expect(loadingLabel, 'the held entities GET shows the onboarding loading label').toBeVisible({ timeout: 20_000 })
  const loadingRoot = (await boxes(page, { root: loadingLabel.locator('xpath=..') })).root
  await attachShot(page, testInfo, 'onboarding-loading')

  entities.release('empty')
  const task = page.getByTestId('add-company-task')
  await expect(task, 'an empty entities envelope shows the task card').toBeVisible()
  const h1 = await expectH1(page, '28px', 'add-client')
  const add = task.getByRole('button', { name: /Add client/ })
  expect(await radii(add), 'Add client corners').toEqual(corners('7px'))
  expect(await task.locator('> *').count(), 'the task holds one child, the card').toBe(1)
  const cardLoc = task.locator('> :first-child')
  const messageLoc = cardLoc.locator('> p')
  const tileLoc = cardLoc.locator('> span')
  const titleLoc = cardLoc.locator('> div')
  const t = await boxes(page, { task, card: cardLoc, message: messageLoc, add })
  expect(
    [...within(t.task, t.add, 'Add client in the task'), ...within(t.card, t.add, 'Add client in the card'), ...below(t.message, t.add, 'Add client vs message')],
    'task card relations',
  ).toEqual([])
  expect(Math.abs(t.add.y - (t.message.y + t.message.height) - 20), 'Add client sits 20px below the message').toBeLessThanOrEqual(1)
  const cardRead = await styles(cardLoc, [
    'padding-top',
    'padding-right',
    'padding-bottom',
    'padding-left',
    'background-color',
    'border-top-width',
    'border-top-style',
    'border-top-color',
    ...CORNERS,
  ])
  expect([cardRead['padding-top'], cardRead['padding-right'], cardRead['padding-bottom'], cardRead['padding-left']], 'task card paddings').toEqual(Array(4).fill('48px'))
  expect(cardRead['background-color'], 'task card background').toBe('rgba(0, 0, 0, 0)')
  expect(`${cardRead['border-top-width']} ${cardRead['border-top-style']} ${cardRead['border-top-color']}`, 'task card border-top').toBe('1px dashed rgb(170, 196, 189)')
  expect(await radii(cardLoc), 'task card corners').toEqual(corners('6px'))
  const tileRead = await styles(tileLoc, ['width', 'height'])
  expect([tileRead.width, tileRead.height], 'task card tile size').toEqual(['40px', '40px'])
  expect(await radii(tileLoc), 'task card tile corners').toEqual(corners('6px'))
  const titleRead = await styles(titleLoc, ['font-size', 'font-weight'])
  expect([titleRead['font-size'], titleRead['font-weight']], 'task card title').toEqual(['15px', '700'])
  const messageRead = await styles(messageLoc, ['font-size', 'max-width', 'line-height'])
  expect([messageRead['font-size'], messageRead['max-width'], messageRead['line-height']], 'task card message').toEqual(['13px', '460px', '20.15px'])
  await attachShot(page, testInfo, 'overview-empty-workspace')

  entities.mode = '503'
  await page.reload()
  const failed = main(page).getByText('Something went wrong', { exact: true })
  await expect(failed, 'the 503 shows the error card').toBeVisible({ timeout: 20_000 })
  await expect(main(page).getByRole('button', { name: 'Retry' }), 'the error card offers Retry').toBeVisible()
  const errorCard = await wrapperChild(page, failed)
  await attachShot(page, testInfo, 'onboarding-error')

  entities.mode = 'empty'
  await main(page).getByRole('button', { name: 'Retry' }).click()
  await expect(task, 'Retry with an empty envelope shows the task card').toBeVisible()
  const h1Box = (await boxes(page, { h1: heading(page) })).h1
  expect([...sameLeft(loadingRoot, h1Box, 'loading root vs h1'), ...sameLeft(errorCard, h1Box, 'error card vs h1')], 'state placement').toEqual([])

  await attachJson(testInfo, 'ov-04-measurements', { h1, task: t, cardRead, tileRead, titleRead, messageRead, loadingRoot, errorCard, h1Box })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

test('OV-05 recent activity rows stay inside their tile', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  const ago = (min: number): string => new Date(Date.now() - min * 60_000).toISOString()
  // Codes are in AUDIT_EVENTS (lib/auditVocabulary.ts); the 50-character one is unrecognised.
  const event = (i: number, code: string, over: Record<string, unknown> = {}) => ({
    id: `00000000-0000-4000-8000-00000000050${i}`,
    created_at: ago(i + 1),
    event: code,
    actor: `user-${i}`,
    actor_name: `Person ${i}`,
    actor_kind: 'person',
    entity_id: `00000000-0000-4000-8000-00000000051${i}`,
    company_name: `Company ${i}`,
    company_scope: 'company',
    payload: {},
    ...over,
  })
  const events = [
    event(0, 'invoice.created', { actor_name: 'A'.repeat(60) }),
    event(1, 'invoice.created', { company_name: 'B'.repeat(60) }),
    event(2, 'x'.repeat(50)),
    event(3, 'invoice.updated'),
    event(4, 'portfolio.entity.created'),
  ]
  const body = {
    events,
    page: { limit: 5, has_more: false, next_cursor: null },
    total: events.length,
    log_is_empty: false,
    facets: { event: [], actor: [], company: [] },
  }
  const requests: string[] = []
  await routeGet(page, (url) => onPath(url, '/api/invoice/v1/audit-log'), async (route, url) => {
    requests.push(url.toString())
    await fulfil(route, body)
  })
  await signInAs(page, 'inhouse', { tenantId: SHARD.b.id })

  const tile = page.getByText('Recent activity', { exact: true }).locator('xpath=ancestor::div[2]')
  const rowsLoc = tile.locator('> :last-child > div')
  await expect(rowsLoc, 'the stubbed events drew as rows').toHaveCount(events.length, { timeout: 20_000 })
  expect(requests.length, 'the tile read the audit log').toBeGreaterThan(0)
  for (const u of requests) {
    const q = new URL(u).searchParams
    expect(q.get('limit'), `limit in ${u}`).toBe('5')
    expect(q.has('company'), `no company in ${u}`).toBe(false)
  }

  const widths = await atWidths(page, 'recent activity rows', async () => {
    const problems: string[] = []
    const rects: Record<string, unknown> = {}
    const b = await boxes(page, { tile, body: tile.locator('> :last-child') })
    problems.push(...within(b.tile, b.body, 'tile body in tile'))
    rects.tile = b
    let prev: Rect | null = null
    for (const [i, row] of (await rowsLoc.all()).entries()) {
      const text = row.locator('> div:last-child')
      const r = await boxes(page, { row, dot: row.locator('> div:first-child > span:first-child'), text })
      problems.push(...within(b.body, r.row, `row ${i} in body`), ...within(r.row, r.dot, `row ${i} dot in row`), ...within(r.row, r.text, `row ${i} text in row`), ...leftOf(r.dot, r.text, `row ${i} dot vs text`))
      // A block's box never grows with an unbreakable word: its content overflows it.
      const spill = await text.evaluate((el) => el.scrollWidth - el.clientWidth)
      if (spill > 1) problems.push(`row ${i} text spills ${spill}px past its block`)
      if (prev) problems.push(...below(prev, r.row, `row ${i} vs previous`))
      prev = r.row
      rects[`row ${i}`] = { ...r, spill }
    }
    await assertPageDoesNotScrollSideways(page, 'overview')
    return { problems, rects }
  })

  await attachJson(testInfo, 'ov-05-measurements', { requests, widths })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// Fixture check: the history call is rewritten onto the live rollup and answered with 40 stubbed WAT days
// for every rollup client, so the sparkline and chart markup is reached.
test('OV-07 a KPI tile keeps its height while the history arrives, and the curves keep inside their tiles (fixture check)', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  const isHistory = (url: URL): boolean => onPath(url, '/api/dashboard/v1/rollup/history')
  const todayMs = Date.parse(new Date(Date.now() + 3_600_000).toISOString().slice(0, 10) + 'T00:00:00Z')
  const stub = await stubGet(page, isHistory, {
    mode: 'transform',
    empty: { history: [] },
    source: (url) => {
      const rollup = new URL(url.toString())
      rollup.pathname = rollup.pathname.replace(/\/history$/, '')
      rollup.search = ''
      return rollup
    },
    // Every client gets rows: the SPA's active client comes from the entities list, whose order differs from the rollup's.
    transform: (b) => ({
      history: b.clients.flatMap((c: { entity_id: string }) =>
        Array.from({ length: 40 }, (_, i) => ({
          day: new Date(todayMs - (39 - i) * 86_400_000).toISOString().slice(0, 10),
          entity_id: c.entity_id,
          total: 20 + i,
          readiness_num: 8 + Math.floor(i / 2),
          needs_attention: 3 + (i % 5),
          not_submitted: 5 + (i % 7),
          vat_kobo: 100_000 + i * 5_000,
        })),
      ),
    }),
  })
  stub.hold()
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })

  const kpis = page.locator('.pf-dash-row-a .pf-grid-2 > div')
  await expect(kpis, 'the dashboard drew its four KPI tiles').toHaveCount(4, { timeout: 20_000 })

  const trend = page.getByText('Readiness trend', { exact: true }).locator('xpath=../..')
  // Per width and KPI tile: tile and delta rects; the row B, trend tile and failures row offsets from the KPI grid top.
  type KpiRead = { tile: Rect; delta: Rect }
  type KpiPhase = { width: number; rowB: number; rowC: number; trend: Rect; trendText: string; trendSvgs: number; tiles: KpiRead[] }
  const readKpis = async (label: string): Promise<KpiPhase[]> => {
    await expect(kpis.locator('svg'), `${label}: sparklines`).toHaveCount(label.startsWith('curve') ? 4 : 0)
    const phase = await atWidths(page, `KPI tiles ${label}`, async () => {
      const grid = await boxes(page, {
        grid: page.locator('.pf-dash-row-a .pf-grid-2'),
        rowB: page.locator('.pf-dash-row-b > div:first-child'),
        rowC: page.locator('.pf-dash-row-c'),
        trend,
      })
      const trendText = (await trend.textContent()) ?? ''
      const trendSvgs = await trend.locator('svg').count()
      const tiles: KpiRead[] = []
      for (let i = 0; i < 4; i++) {
        const b = await boxes(page, { tile: kpis.nth(i), delta: kpis.nth(i).locator('span.mono') })
        tiles.push({ tile: b.tile, delta: b.delta })
      }
      return { problems: [], rects: { rowB: grid.rowB.y - grid.grid.y, rowC: grid.rowC.y - grid.grid.y, trend: grid.trend, trendText, trendSvgs, tiles } }
    })
    return phase as KpiPhase[]
  }
  const pending = await readKpis('pending')
  const historyResponse = page.waitForResponse((res) => onPath(new URL(res.url()), '/api/dashboard/v1/rollup/history'))
  stub.release('empty')
  await historyResponse
  const empty = await readKpis('empty')
  stub.mode = 'transform'
  await page.reload()
  await expect(kpis, 'the dashboard drew its four KPI tiles after the reload').toHaveCount(4, { timeout: 20_000 })
  await expect(kpis.locator('svg'), 'one sparkline per KPI tile').toHaveCount(4, { timeout: 20_000 })
  const curve = await readKpis('curve')

  const close = (a: number, b: number, what: string): string[] => (Math.abs(a - b) <= 1 ? [] : [`${what}: ${a} vs ${b}`])
  const fromBottom = (k: KpiRead): number => k.tile.y + k.tile.height - (k.delta.y + k.delta.height)
  const relations: string[] = []
  for (const [w, p] of pending.entries()) {
    const e = empty[w]
    const c = curve[w]
    relations.push(...close(p.rowB, e.rowB, `${p.width}px row B offset, pending vs empty`), ...close(e.rowB, c.rowB, `${p.width}px row B offset, empty vs curve`))
    relations.push(
      ...close(p.trend.height, e.trend.height, `${p.width}px trend tile height, pending vs empty`),
      ...close(e.trend.height, c.trend.height, `${p.width}px trend tile height, empty vs curve`),
      ...close(p.rowC, e.rowC, `${p.width}px failures row offset, pending vs empty`),
      ...close(e.rowC, c.rowC, `${p.width}px failures row offset, empty vs curve`),
    )
    if (!p.trendText.includes('No history yet') || !e.trendText.includes('No history yet')) relations.push(`${p.width}px trend tile does not show "No history yet" while pending or empty`)
    if ([p.trendSvgs, e.trendSvgs, c.trendSvgs].join() !== '0,0,1') relations.push(`${p.width}px trend svg counts ${[p.trendSvgs, e.trendSvgs, c.trendSvgs]} are not 0,0,1`)
    for (const [i, t] of p.tiles.entries()) {
      relations.push(
        ...close(t.tile.height, e.tiles[i].tile.height, `${p.width}px KPI tile ${i} height, pending vs empty`),
        ...close(e.tiles[i].tile.height, c.tiles[i].tile.height, `${p.width}px KPI tile ${i} height, empty vs curve`),
        ...close(e.tiles[i].delta.x, c.tiles[i].delta.x, `${p.width}px KPI tile ${i} delta x, empty vs curve`),
        ...close(fromBottom(e.tiles[i]), fromBottom(c.tiles[i]), `${p.width}px KPI tile ${i} delta offset from the tile bottom, empty vs curve`),
      )
    }
  }
  expect(relations, 'a KPI tile, its delta and the trend tile keep their place while the history arrives').toEqual([])
  await attachJson(testInfo, 'ov-07-kpi-phases', { pending, empty, curve })

  const chart = trend.locator('svg')
  const months = trend.locator('> :last-child > div:last-child > span')
  // Counted so a missing draw cannot pass the relations below vacuously.
  await expect(kpis.locator('svg'), 'one sparkline per KPI tile').toHaveCount(4, { timeout: 20_000 })
  await expect(chart, 'one trend chart').toHaveCount(1)
  expect(await months.count(), 'the 40 days span two months, so two month labels').toBeGreaterThanOrEqual(2)
  const tiles = await kpis.all()
  const labels = await months.all()

  const widths = await atWidths(page, 'sparklines, trend chart and month labels', async () => {
    const problems: string[] = []
    const rects: Record<string, unknown> = {}
    for (const [i, tile] of tiles.entries()) {
      const b = await boxes(page, { tile, spark: tile.locator('svg'), delta: tile.locator('span.mono') })
      problems.push(...within(b.tile, b.spark, `KPI tile ${i} sparkline in the tile`), ...leftOf(b.delta, b.spark, `KPI tile ${i} delta vs sparkline`))
      if (b.tile.height < 138) problems.push(`KPI tile ${i} is ${b.tile.height}px tall, under 138px`)
      const lines = await tile.locator('span.mono').evaluate((el) => {
        const range = document.createRange()
        range.selectNodeContents(el)
        return new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size
      })
      if (lines !== 1) problems.push(`KPI tile ${i} delta draws on ${lines} lines`)
      rects[`KPI tile ${i}`] = { ...b, lines }
    }
    const t = await boxes(page, {
      tile: trend,
      head: trend.locator('> :first-child'),
      meta: trend.locator('> :first-child .mono'),
      headline: trend.locator('.money'),
      headRow: trend.locator('> :last-child > div:first-child'),
      chart,
      row: trend.locator('> :last-child > div:last-child'),
    })
    problems.push(
      ...within(t.tile, t.chart, 'chart in the trend tile'),
      ...below(t.headline, t.chart, 'chart vs headline'),
      ...below(t.chart, t.row, 'months row vs chart'),
      ...within(t.tile, t.row, 'months row in the trend tile'),
      ...within(t.head, t.meta, 'meta in the head'),
      ...within(t.headRow, t.headline, 'headline in its pinned row'),
    )
    let prev: Rect | null = null
    for (const [i, label] of labels.entries()) {
      const l = (await boxes(page, { label })).label
      problems.push(...within(t.tile, l, `month label ${i} in the trend tile`))
      if (prev) problems.push(...leftOf(prev, l, `month label ${i - 1} vs ${i}`))
      prev = l
      rects[`month label ${i}`] = l
    }
    await assertPageDoesNotScrollSideways(page, 'overview')
    return { problems, rects: { ...rects, trend: t } }
  })

  await attachJson(testInfo, 'ov-07-measurements', widths)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('IN-01 invoices register: heading, card, badge, toggle, bulk bar, header and pager relations', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await stubGet(page, isRegister, { mode: 'transform', transform: canSubmitFirst })
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const gateway = recordGateway(page)
  await openNav(page, navButton(page, /^Invoices/))
  await expectVisibleRows(page.getByTestId('invoice-row'), 'the firm register must hold at least one invoice on the PR environment (the seed holds firm invoices, D-14)')
  await expect(page.getByTestId('invoices-pager'), 'the pager renders only with rows').toBeVisible()
  await settle(page, column(page))

  const h1 = await expectH1(page, '26px', 'invoices')
  const listCard = await expectListCard(page.getByTestId('invoices-list'), 'invoices list')
  const badge = page.getByTestId('invoice-status-badge').first()
  expect(await radii(badge), 'status badge corners').toEqual(corners('4px'))
  const dot = (await styles(badge.locator('> span').first(), ['border-top-left-radius']))['border-top-left-radius']
  expect(dot, 'status dot corner').toBe('50%')
  const toggle = page.getByTestId('needs-attention-toggle')
  const off = await styles(toggle, [...CORNERS, 'font-weight', 'background-color'])
  expect(Object.values(off).slice(0, 4), 'toggle corners').toEqual(corners('4px'))
  expect(off['font-weight'], 'toggle weight').toBe('600')
  expect(off['background-color'], 'toggle off background').toBe(WHITE)
  await attachShot(page, testInfo, 'invoices-default')

  const header = async (): Promise<Read> => {
    const b = await boxes(page, { header: heading(page).locator('xpath=../..'), title: heading(page).locator('xpath=..'), toggle })
    return {
      problems: [...within(b.header, b.title, 'title block in the header'), ...within(b.header, b.toggle, 'toggle in the header'), ...apart(b.title, b.toggle, 'title block vs toggle')],
      rects: { header: b.header, title: b.title, toggle: b.toggle },
    }
  }
  const bulk = await bulkFlow(
    page,
    testInfo,
    { list: 'invoices-list', row: 'invoice-row', select: 'invoice-select', bar: 'batch-submit-summary', submit: 'batch-submit', cancel: 'batch-submit-cancel', confirm: 'batch-submit-confirm', pager: 'invoices-pager' },
    { armedShot: 'invoices-armed', centred: true, header },
  )

  const filteredGet = page.waitForResponse((r) => {
    const u = new URL(r.url())
    return r.request().method() === 'GET' && onPath(u, '/api/invoice/v1/invoices') && u.searchParams.get('needs_attention') === 'true'
  })
  await toggle.click()
  await filteredGet
  await expect(page.getByTestId('invoice-row').first().or(page.getByTestId('invoices-empty-filtered')), 'the Needs-attention list settled').toBeVisible()
  await settle(page, toggle) // .pf-chip transitions background and colour for 120ms
  const on = await styles(toggle, [...CORNERS, 'background-color', 'color'])
  expect(on['background-color'], 'toggle on background').toBe(ACTION)
  expect(on.color, 'toggle on colour').toBe(WHITE)
  expect(Object.values(on).slice(0, 4), 'toggle on corners').toEqual(corners('4px'))
  await attachShot(page, testInfo, 'invoices-needs-attention')

  expectNoWrites(gateway)
  await attachJson(testInfo, 'in-01-measurements', { h1, listCard, dot, off, on, bulk })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('IN-02 invoices states: loading, error, empty, filtered empty, empty page', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, /\/api\/invoice\/v1\/invoices/)])
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const register = await stubGet(page, isRegister, { empty: EMPTY_INVOICES })
  const measured: Record<string, unknown> = {}

  // The card sits inside the main column with its left edge on the h1's, at every wide width.
  const placed = (name: string, card: () => Promise<Rect>) =>
    atWidths(page, name, async () => {
      const b = await boxes(page, { column: column(page), h1: heading(page) })
      const c = await card()
      await assertPageDoesNotScrollSideways(page, name)
      return { problems: [...within(b.column, c, `${name} in the column`), ...sameLeft(c, b.h1, `${name} vs h1`)], rects: { column: b.column, h1: b.h1, card: c } }
    })
  const buttonInCard = async (wrapper: Locator, button: Locator, maxWidth: number, what: string) => {
    expect(await wrapper.locator('> *').count(), `${what} holds one child, the card`).toBe(1)
    const card = wrapper.locator('> :first-child')
    const message = card.locator('> p')
    const b = await boxes(page, { card, message, button })
    expect([...within(b.card, b.button, `${what} button in its card`), ...below(b.message, b.button, `${what} button vs message`)], what).toEqual([])
    const cardRead = await styles(card, ['background-color', 'padding-top'])
    expect(cardRead['background-color'], `${what} card background`).toBe(WHITE)
    expect(cardRead['padding-top'], `${what} card padding-top`).toBe('56px')
    expect((await styles(message, ['max-width']))['max-width'], `${what} message max-width`).toBe(`${maxWidth}px`)
  }

  register.hold()
  await bounce(page, /^Overview/, /^Invoices/)
  await expect(main(page).getByText('Loading invoices…'), 'the held register shows the loading label').toBeVisible()
  await attachShot(page, testInfo, 'invoices-loading')

  register.release('503')
  const failed = main(page).getByText('Something went wrong', { exact: true })
  await expect(failed, 'the 503 shows the error card').toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Retry' }), 'the error card offers Retry').toBeVisible()
  await attachShot(page, testInfo, 'invoices-error')
  measured.error = await placed('invoices error card', () => wrapperChild(page, failed))

  register.mode = 'empty'
  await main(page).getByRole('button', { name: 'Retry' }).click()
  const empty = page.getByTestId('invoices-empty')
  await expect(empty, 'an empty envelope shows the empty card').toBeVisible()
  await buttonInCard(empty, empty.getByRole('button', { name: /New invoice/ }), 320, 'invoices-empty')
  await attachShot(page, testInfo, 'invoices-empty')
  measured.empty = await placed('invoices empty card', async () => (await boxes(page, { card: empty.locator('> :first-child') })).card)

  const toggle = page.getByTestId('needs-attention-toggle')
  await toggle.click()
  const filtered = page.getByTestId('invoices-empty-filtered')
  await expect(filtered, 'the filter over an empty envelope shows the filtered-empty card').toBeVisible()
  await buttonInCard(filtered, filtered.getByRole('button', { name: 'Show all invoices' }), 360, 'invoices-empty-filtered')
  await attachShot(page, testInfo, 'invoices-empty-filtered')
  measured.filtered = await placed('invoices filtered-empty card', async () => (await boxes(page, { card: filtered.locator('> :first-child') })).card)

  register.mode = 'emptyPage'
  await toggle.click()
  const emptyPage = page.getByTestId('invoices-empty-page')
  await expect(emptyPage, 'an empty page of a 120-row register shows the empty-page card').toBeVisible()
  const p = await boxes(page, { card: emptyPage.locator('> :first-child'), pager: page.getByTestId('invoices-pager') })
  expect(below(p.card, p.pager, 'pager vs empty-page card'), 'empty-page pager placement').toEqual([])
  await attachShot(page, testInfo, 'invoices-empty-page')

  await attachJson(testInfo, 'in-02-measurements', { ...measured, emptyPage: p })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

test('AP-01 approvals queue: heading, card, bulk bar, pager relations', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  // D-14: the queue is the firm register's own page for the same entity, limit and offset.
  await stubGet(page, isQueue, {
    mode: 'transform',
    source: (url) => {
      const u = new URL(url)
      u.searchParams.delete('awaiting_approval')
      return u
    },
    transform: canApproveFirst,
  })
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const gateway = recordGateway(page)
  await openNav(page, navButton(page, /^Approvals/))
  await expectVisibleRows(page.getByTestId('approval-row'), 'the approvals queue is served from the firm register page (D-14), which must hold at least one invoice on the PR environment')
  await settle(page, column(page))

  const h1 = await expectH1(page, '26px', 'approvals')
  const listCard = await expectListCard(page.getByTestId('approvals-list'), 'approvals list')
  await attachShot(page, testInfo, 'approvals')
  const bulk = await bulkFlow(
    page,
    testInfo,
    { list: 'approvals-list', row: 'approval-row', select: 'approval-select-row', bar: 'approvals-bulk-bar', submit: 'approvals-bulk-submit', cancel: 'approvals-bulk-cancel', confirm: 'approvals-bulk-confirm', pager: 'approvals-pager' },
    { armedShot: 'approvals-armed', centred: false },
  )

  expectNoWrites(gateway)
  await attachJson(testInfo, 'ap-01-measurements', { h1, listCard, bulk })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// No deployed route creates a note before CRDDBTNOTE-02 merges, so page.route turns the first queue row into one.
test("(fixture check) AP: a note's type badge stays inside the approvals number cell", async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  const buyerName = 'Fixture Buyer Holdings Limited'
  await stubGet(page, isQueue, {
    mode: 'transform',
    source: (url) => {
      const u = new URL(url)
      u.searchParams.delete('awaiting_approval')
      return u
    },
    transform: (body) => {
      // Fixture: pad the queue to 3 rows so the 1180px buyer-width guard reads more than the two note rows.
      const invoices: Array<Record<string, unknown>> = [...body.invoices]
      for (let n = 1; invoices.length < 3; n++) {
        invoices.push({ ...invoices[0], id: `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`, invoice_number: `INV-PAD-${n}`, buyer_name: `Pad Buyer ${n}` })
      }
      return {
        ...body,
        invoices: invoices.map((r, i) => {
          if (i === 0) return { ...r, document_type: 'credit_note', invoice_number: 'CN-INV-2026-0001-1', buyer_name: buyerName, can_approve: true, approve_blocked_reason: null }
          // reason string: handlers.go approve gate (wantApproveStatusReason)
          if (i === 1) return { ...r, document_type: 'debit_note', invoice_number: 'DN-INV-2026-0001-2', buyer_name: buyerName, can_approve: false, approve_blocked_reason: 'Only a validated invoice can be approved or rejected.' }
          return r
        }),
      }
    },
  })
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await openNav(page, navButton(page, /^Approvals/))
  const rows = page.getByTestId('approval-row')
  await expectVisibleRows(rows, 'the approvals queue is served from the firm register page (D-14), which must hold at least one invoice on the PR environment')
  expect(await rows.count(), 'the fixture pads the queue to at least 3 rows').toBeGreaterThanOrEqual(3)
  const card = page.getByTestId('approvals-list')
  const head = card.locator('.pf-list-head')
  const credit = rows.filter({ hasText: 'CN-INV-2026-0001-1' })
  const debit = rows.filter({ hasText: 'DN-INV-2026-0001-2' })
  const parts = (row: Locator) => ({
    badge: row.getByTestId('approval-type-badge'),
    cell: row.getByTestId('approval-number-cell'),
    buyer: row.getByTestId('approval-buyer-cell'),
  })
  const c = parts(credit)
  const d = parts(debit)
  const icon = debit.getByTestId('approval-blocked-icon')
  const dNumber = d.cell.locator(':scope > span').first()
  await expect(c.badge).toHaveText('CREDIT')
  await expect(d.badge).toBeVisible()

  const box = async (loc: Locator, what: string): Promise<Rect> => {
    const r = await loc.boundingBox()
    if (!r) throw new Error(`${what} has no box`)
    return r
  }
  const childBoxes = async (parent: Locator, what: string): Promise<Rect[]> => {
    const kids = await parent.locator(':scope > *').all()
    if (kids.length !== 7) throw new Error(`${what} has ${kids.length} children, want 7 (APPROVALS_GRID_COLUMNS tracks)`)
    return Promise.all(kids.map((k, i) => box(k, `${what} child ${i}`)))
  }

  const measured = await atWidths(
    page,
    'approvals note badge',
    async () => {
      await settleAnimations(c.cell, c.badge, d.cell, d.badge, icon)
      const k = await box(card, 'approvals-list')
      const problems: string[] = []
      const [headKids, creditKids, debitKids] = await Promise.all([childBoxes(head, 'head'), childBoxes(credit, 'credit row'), childBoxes(debit, 'debit row')])
      for (const [what, list] of [['head', headKids], ['credit row', creditKids], ['debit row', debitKids]] as const) {
        list.forEach((r, i) => problems.push(...within(k, r, `${what} child ${i} in the card`)))
      }
      const [cb, cc, cy, db, dc, dy, di, dn] = await Promise.all([
        box(c.badge, 'credit badge'), box(c.cell, 'credit number cell'), box(c.buyer, 'credit buyer cell'),
        box(d.badge, 'debit badge'), box(d.cell, 'debit number cell'), box(d.buyer, 'debit buyer cell'),
        box(icon, 'blocked icon'), box(dNumber, 'debit number'),
      ])
      problems.push(
        ...within(cc, cb, 'credit badge in the number cell'),
        ...apart(cb, cy, 'credit badge vs buyer cell'),
        ...within(dc, db, 'debit badge in the number cell'),
        ...within(dc, di, 'blocked icon in the number cell'),
        ...apart(db, di, 'debit badge vs blocked icon'),
        ...apart(db, dy, 'debit badge vs buyer cell'),
        ...apart(di, dy, 'blocked icon vs buyer cell'),
        ...within(dc, dn, 'debit number in the number cell'),
        ...leftOf(dn, db, 'debit number vs badge'),
        ...leftOf(dn, di, 'debit number vs blocked icon'),
      )
      const rects: Record<string, unknown> = { k, cb, cc, cy, db, dc, dy, di, dn }
      if (page.viewportSize()?.width === 1180) {
        const buyerWidths: number[] = []
        for (const row of await rows.all()) {
          const w = (await box(row.getByTestId('approval-buyer-cell'), 'approval-buyer-cell')).width
          buyerWidths.push(w)
          if (w < 50) problems.push(`a buyer cell is ${w}px wide, under 50px`)
        }
        for (const [what, loc] of [['credit', c.buyer], ['debit', d.buyer]] as const) {
          if (!((await loc.textContent()) ?? '').trim()) problems.push(`${what} buyer cell has no text`)
        }
        rects.buyerWidths = buyerWidths
      }
      return { problems, rects }
    },
    [...WIDE_WIDTHS, 1180],
  )
  expect(measured.length, 'the sweep measured every width and 1180').toBe(WIDE_WIDTHS.length + 1)

  await expect(c.cell.locator(':scope > span').first(), 'the credit number title is the whole number').toHaveAttribute('title', 'CN-INV-2026-0001-1')
  await expect(dNumber, 'the debit number title is the whole number').toHaveAttribute('title', 'DN-INV-2026-0001-2')
  await attachJson(testInfo, 'ap-08-buyer-measurements', measured)

  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AP-02 approvals states: loading, error, empty, empty page', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, /\/api\/invoice\/v1\/invoices/)])
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  const queue = await stubGet(page, isQueue, { empty: EMPTY_INVOICES })

  queue.hold()
  await bounce(page, /^Overview/, /^Approvals/)
  await expect(main(page).getByText(APPROVALS_LOADING), 'the held queue shows the loading label').toBeVisible()
  await attachShot(page, testInfo, 'approvals-loading')

  queue.release('503')
  await expect(main(page).getByText('Something went wrong', { exact: true }), 'the 503 shows the error card').toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Retry' }), 'the error card offers Retry').toBeVisible()
  await attachShot(page, testInfo, 'approvals-error')

  queue.mode = 'empty'
  await main(page).getByRole('button', { name: 'Retry' }).click()
  const empty = page.getByTestId('approvals-empty')
  await expect(empty, 'an empty envelope shows the empty card').toBeVisible()
  const e = await boxes(page, { empty, h1: heading(page) })
  expect(sameLeft(e.empty, e.h1, 'approvals-empty vs h1'), 'empty card placement').toEqual([])
  await attachShot(page, testInfo, 'approvals-empty')

  queue.mode = 'emptyPage'
  await bounce(page, /^Overview/, /^Approvals/)
  const emptyPage = page.getByTestId('approvals-empty-page')
  await expect(emptyPage, 'an empty page of a 120-row queue shows the empty-page card').toBeVisible()
  const p = await boxes(page, { card: emptyPage.locator('> :first-child'), pager: page.getByTestId('approvals-pager') })
  expect(below(p.card, p.pager, 'pager vs empty-page card'), 'empty-page pager placement').toEqual([])
  await attachShot(page, testInfo, 'approvals-empty-page')

  await attachJson(testInfo, 'ap-02-measurements', { empty: e, emptyPage: p })
  expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
})

// No stubs. The first gate run holds one recorded day (the boot snapshot); a re-run on a later WAT day holds two or more.
// The branch follows the captured history, never a clock. Last in the file so a data failure hides no layout test.
test('OV-06 the Overview draws recorded history only: one day keeps the empty states, two draw the curves, no SAMPLE', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  const gateway = recordGateway(page)
  const isHistoryRes = (r: Response): boolean => r.request().method() === 'GET' && onPath(new URL(r.url()), '/api/dashboard/v1/rollup/history')
  const isRollupRes = (r: Response): boolean => r.request().method() === 'GET' && isRollup(new URL(r.url()))
  type HistoryRow = RollupHistoryRow
  type Client = DashboardClient
  const captured = async (load: () => Promise<unknown>): Promise<{ history: HistoryRow[]; clients: Client[] }> => {
    const [history, rollup] = await Promise.all([
      page.waitForResponse(isHistoryRes, { timeout: 30_000 }).catch(() => { throw new Error('the Overview sent no GET /rollup/history within 30 s') }),
      page.waitForResponse(isRollupRes, { timeout: 30_000 }).catch(() => { throw new Error('the Overview sent no GET /rollup within 30 s') }),
      load(),
    ])
    const rows: HistoryRow[] = (await history.json()).history
    const clients: Client[] = (await rollup.json()).clients
    expect(Array.isArray(rows), 'the history response carries no history array').toBe(true)
    expect(Array.isArray(clients) && clients.length > 0, 'the rollup response lists no client').toBe(true)
    return { history: rows, clients }
  }
  const kpis = page.locator('.pf-dash-row-a .pf-grid-2 > div')

  let seen = await captured(() => signInAs(page, 'firm', { tenantId: SHARD.a.id }))
  await expect(kpis, 'the dashboard drew its four KPI tiles').toHaveCount(4, { timeout: 20_000 })
  await expect(heading(page)).toBeVisible()
  const historyReads = gateway.reads.filter((u) => onPath(new URL(u), '/api/dashboard/v1/rollup/history'))
  expect(historyReads, 'the first Overview load reads the history once').toHaveLength(1)

  // The SPA's active client is the one the h1 names, not the rollup's first.
  let clientName = (await heading(page).innerText()).trim()
  const rowsOf = (): HistoryRow[] => seen.history.filter((r) => r.entity_id === seen.clients.find((c) => c.entity_name === clientName)?.entity_id)
  const deadline = Date.now() + 30_000
  while (rowsOf().length === 0 && Date.now() < deadline) {
    await page.waitForTimeout(3_000)
    seen = await captured(() => page.reload())
    await expect(kpis, 'the dashboard drew its four KPI tiles').toHaveCount(4, { timeout: 20_000 })
    clientName = (await heading(page).innerText()).trim()
  }
  const client = seen.clients.find((c) => c.entity_name === clientName)
  expect(client, `the rollup holds no client named "${clientName}" (the h1)`).toBeDefined()
  const invoiceCount = Object.values(client!.counts).reduce((a, b) => a + b, 0)
  expect(invoiceCount, `client "${clientName}" holds no invoice: the shard invoice copy in db/seed.e2e-shards.sql did not run`).toBeGreaterThanOrEqual(1)
  const days = [...new Set(rowsOf().map((r) => r.day))].sort()
  expect(days.length, `no history row for "${clientName}": the reconciliation snapshot worker did not run at boot`).toBeGreaterThanOrEqual(1)

  const trend = page.getByText('Readiness trend', { exact: true }).locator('xpath=../..')
  await expect(trend).toBeVisible()
  const meta = trend.locator('> :first-child .mono')
  if (days.length < 2) {
    for (const [i, tile] of (await kpis.all()).entries()) await expect(tile.locator('svg'), `KPI tile ${i} draws no sparkline from one recorded day`).toHaveCount(0)
    await expect(trend).toContainText('No history yet')
    await expect(trend).toContainText('The trend starts on the first day this workspace records.')
    await expect(meta).toHaveText('12 WEEKS')
  } else {
    for (const [i, tile] of (await kpis.all()).entries()) await expect(tile.locator('svg'), `KPI tile ${i} draws one sparkline`).toHaveCount(1)
    await expect(trend.locator('svg'), 'the trend tile draws its chart').toHaveCount(1)
    const today = new Date(Date.now() + 3_600_000).toISOString().slice(0, 10)
    const span = Math.round((Date.parse(today) - Date.parse(days[0])) / 86_400_000) + 1
    const mon = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'][Number(days[0].slice(5, 7)) - 1]
    await expect(meta).toHaveText(span >= 84 ? '12 WEEKS' : `SINCE ${Number(days[0].slice(8, 10))} ${mon} · ${span} DAYS`)
  }

  await expect(page.locator('body'), 'the Overview carries no sample label').not.toContainText('SAMPLE')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
