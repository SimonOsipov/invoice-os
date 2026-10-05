// The deployed v2 Overview, Invoices and Approvals: resolved values and layout relationships on the PR environment.
// Every state is reached with page.route (D-12); no tenant data is written and no confirm button is clicked (D-13).
// The api-client Loading and ErrorState assert placement only; the in-card and dense empties assert resolved values (OV-04, IN-02).
// Screenshots are attached for the reviewer and never asserted.
import { test, expect, type Locator, type Page, type Route, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, GATEWAY_URL } from './targets'

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
async function atWidths(page: Page, label: string, read: () => Promise<Read>): Promise<unknown[]> {
  const entry = page.viewportSize()
  const out: unknown[] = []
  try {
    for (const width of WIDE_WIDTHS) {
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
  await signInAs(page, 'firm')
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
  await signInAs(page, 'inhouse')
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

  const needs = page.locator('.pf-dash-row-b > div:first-child')
  const sentence = needs.locator('p')
  const needsCta = needs.getByRole('button')
  const edges = [page.locator('.pf-dash-row-b > div:nth-child(2)'), page.locator('.pf-dash-row-c > div:nth-child(2)'), page.locator('.pf-grid-2 > div:nth-child(2)')]
  const failureKeys = VIOLATIONS.map((v) => page.locator('.pf-dash-row-c').getByText(v.rule_key, { exact: true }))
  for (const key of failureKeys) await expect(key, 'the injected failure row drew').toBeVisible()

  const widths = await atWidths(page, 'overview tiles', async () => {
    const problems: string[] = []
    const rects: Record<string, unknown> = {}
    for (const [name, tile] of tiles) {
      const b = await boxes(page, { tile, head: tile.locator('> :first-child'), body: tile.locator('> :last-child') })
      problems.push(...within(b.tile, b.head, `${name} head`), ...within(b.tile, b.body, `${name} body`), ...below(b.head, b.body, `${name} body vs head`))
      rects[name] = b
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
  await signInAs(page, 'firm')
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
  await signInAs(page, 'firm')
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

test('IN-01 invoices register: heading, card, badge, toggle, bulk bar, header and pager relations', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await stubGet(page, isRegister, { mode: 'transform', transform: canSubmitFirst })
  await signInAs(page, 'firm')
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
  await signInAs(page, 'firm')
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
  await signInAs(page, 'firm')
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

test('AP-02 approvals states: loading, error, empty, empty page', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [expectedStatusDropper(page, 503, /\/api\/invoice\/v1\/invoices/)])
  await signInAs(page, 'firm')
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
