// The deployed v2 Clients, Customers, Reports and Workflows surfaces: resolved values and layout
// relationships on the PR environment, plus a live capture of each state. jsdom has no cascade, so this file
// pins what the cascade resolves. Layout claims assert a relationship; screenshots are attached, never asserted.
// It reads the seeded firm policy and never writes one: every builder test fails on a POST, PUT or DELETE.
import { test, expect, type Locator, type Page, type Route, type TestInfo } from '@playwright/test'
import { login, createEntity, createInvoice, validateInvoice, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { collectErrors, signInAs } from '../personaSession'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, GATEWAY_URL } from './targets'

test.use({ viewport: { width: 1440, height: 900 } })

// internal/demopolicy firmPlan.policyName, seeded on 1111 only.
const SEEDED_POLICY = 'Standard approval policy'
// frontend AGGREGATE_MAX_PAGES x AGGREGATE_PAGE_SIZE is 2000; one more invoice truncates the fetch.
const TRUNCATING_TOTAL = 2001
const APP_ORIGIN = new URL(APP_URL).origin
const GATEWAY_ORIGIN = new URL(GATEWAY_URL).origin

// Resolved v2 token values.
const LINE_1 = 'rgb(220, 231, 228)'
const LINE_2 = 'rgb(201, 217, 214)'
const ACTION = 'rgb(7, 60, 61)'
const WHITE = 'rgb(255, 255, 255)'
const FG_3 = 'rgb(83, 107, 109)'
const FG_4 = 'rgb(125, 146, 145)'
const GHOST_BORDER = 'rgb(170, 196, 189)'

let entity: { id: string; name: string }

// Fires exactly ['supplier-tin-format', 'vat-standard-rate'] (a copy of portfolio.spec.ts's helper).
function badInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: 'BADTIN',
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '70',
    total: '1070',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

test.beforeAll(async () => {
  const token = await login(PERSONAS.A)
  // "Ae" sorts just after the seeded default (Adeyemi): the switcher shows only the first 200 entities by name.
  entity = await createEntity(token, { name: `Ae RESKIN2-05 ${Date.now()}`, tin: freshTin() })
  const invoice = await createInvoice(token, { entity_id: entity.id, ...badInvoiceFields(`INV-RESKIN2-05-${Date.now()}`) })
  await validateInvoice(token, invoice.id)
})

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

// Waits until no /api/ request has been in flight for 300 ms and no loading label is left in main.
function trackApi(page: Page): { quiet: (where: string) => Promise<void> } {
  const inflight = new Set<unknown>()
  const isApi = (url: string) => new URL(url).pathname.includes('/api/')
  page.on('request', (r) => { if (isApi(r.url())) inflight.add(r) })
  const done = (r: { url(): string }) => inflight.delete(r)
  page.on('requestfinished', done)
  page.on('requestfailed', done)
  return {
    quiet: async (where) => {
      const deadline = Date.now() + 15_000
      let idleSince = Date.now()
      while (Date.now() - idleSince < 300) {
        if (Date.now() > deadline) throw new Error(`${where}: /api/ requests still in flight after 15s (${inflight.size})`)
        if (inflight.size > 0) idleSince = Date.now()
        await page.waitForTimeout(50)
      }
      await expect(page.locator('main.pf-main').getByText(/^Loading .+…$/), `${where}: a loading label never went away`).toHaveCount(0, { timeout: 15_000 })
    },
  }
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

async function expectCorners(loc: Locator, corner: string, label: string): Promise<string[]> {
  const got = await radii(loc)
  expect(got, `${label} corners`).toEqual(Array(4).fill(corner))
  return got
}

async function border(loc: Locator): Promise<string> {
  const s = await styles(loc, ['border-top-width', 'border-top-style', 'border-top-color'])
  return `${s['border-top-width']} ${s['border-top-style']} ${s['border-top-color']}`
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

// The resolved width of a 1.5px border in this browser, which rounds it at DPR 1.
function probeBorderWidth(scope: Locator): Promise<string> {
  return scope.evaluate((el) => {
    const probe = document.createElement('div')
    probe.style.border = '1.5px solid'
    el.appendChild(probe)
    const value = getComputedStyle(probe).borderTopWidth
    probe.remove()
    return value
  })
}

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

async function attachShot(page: Page, testInfo: TestInfo, name: string): Promise<void> {
  await testInfo.attach(`${name}.png`, { body: await page.screenshot(), contentType: 'image/png' })
}

const aside = (page: Page) => page.locator('aside.pf-sidebar')
const navButton = (page: Page, label: RegExp) => aside(page).locator('nav button.pf-nav', { hasText: label })
const main = (page: Page) => page.locator('main.pf-main')
const h1Of = (page: Page, name: string) => page.getByRole('heading', { level: 1, name, exact: true })

async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// Signs in as the firm, selects this run's entity and waits for the shell to go quiet.
async function openFirm(page: Page, api: ReturnType<typeof trackApi>): Promise<void> {
  await signInAs(page, 'firm')
  await selectEntity(page, entity.name)
  await expect(page.getByTestId('company-switcher'), 'the company switcher never showed the created entity').toContainText(entity.name)
  await api.quiet('shell after selecting the entity')
}

async function openNav(page: Page, label: RegExp, heading: string): Promise<void> {
  await navButton(page, label).click()
  await expect(h1Of(page, heading), `the ${heading} screen drew no h1`).toBeVisible()
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

const centreY = (r: Rect): number => r.y + r.height / 2

function noOverlap(rects: Record<string, Rect>, names: string[], problems: string[]): void {
  for (let i = 0; i < names.length; i++)
    for (let j = i + 1; j < names.length; j++)
      if (rects[names[i]] && rects[names[j]] && rectsOverlap(rects[names[i]], rects[names[j]])) problems.push(`${names[i]} overlaps ${names[j]}`)
}

function inside(rects: Record<string, Rect>, outer: string, names: string[], problems: string[]): void {
  for (const n of names) if (rects[outer] && rects[n] && !enclosesRect(rects[outer], rects[n], 1)) problems.push(`${n} sticks out of ${outer}`)
}

// Runs `read` at every WIDE_WIDTHS width, polling until it reports no problem; restores the entry viewport.
async function sweepWide(
  page: Page,
  label: string,
  read: () => Promise<{ problems: string[]; rects: Record<string, Rect> }>,
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

// --- Clients, Customers, Reports ------------------------------------------------------------------------

function clientRow(page: Page): Locator {
  return page.locator('.pf-list-row').filter({ hasText: entity.name })
}

test('PW-01 Clients at 1440: heading, roster table, chips, filters, Add client', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  const api = trackApi(page)
  await openFirm(page, api)
  await openNav(page, /^Clients/, 'Client portfolio')
  const row = clientRow(page)
  await expect(row, 'the created entity never reached the roster').toBeVisible()
  await expect(row.getByText('1 NEEDS ATTENTION', { exact: true }), 'the health chip never resolved from the rollup').toBeVisible()
  await api.quiet('Clients')
  await settle(page, main(page), row)

  const h1 = await styles(h1Of(page, 'Client portfolio'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const table = page.locator('.pf-list-head').locator('xpath=..')
  const tableCorners = await expectCorners(table, '6px', 'roster table')
  expect(await border(table), 'roster table border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(table, ['box-shadow']))['box-shadow'], 'roster table shadow').toBe('none')

  const statusCell = row.locator('xpath=./span[3]')
  const healthCell = row.locator('xpath=./span[4]')
  const status = statusCell.locator('span.mono')
  const health = healthCell.locator('span.mono')
  const chipReads: Record<string, unknown> = {}
  for (const [name, cell, chip] of [['status', statusCell, status], ['health', healthCell, health]] as const) {
    await expect(chip, `${name} chip count`).toHaveCount(1)
    expect(await chip.locator('*').count(), `${name} chip has a child element`).toBe(0)
    chipReads[name] = await expectCorners(chip, '4px', `${name} chip`)
    const cellBox = await cell.boundingBox()
    const chipBox = await chip.boundingBox()
    expect(cellBox && chipBox, `${name} chip or its cell has no box`).toBeTruthy()
    expect(enclosesRect(cellBox as Rect, chipBox as Rect, 1), `${name} chip sticks out of its cell`).toBe(true)
    expect((chipBox as Rect).width, `${name} chip is not narrower than its cell`).toBeLessThan((cellBox as Rect).width)
  }
  expect(firstFamily((await styles(status, ['font-family']))['font-family']), 'status chip first family').toBe('IBM Plex Mono')
  const avatar = await expectCorners(row.locator('xpath=./span[1]/span[1]'), '50%', 'avatar')

  const filters = ['All', 'Active', 'Archived'].map((n) => main(page).getByRole('button', { name: n, exact: true }))
  const all = await styles(filters[0], ['background-color', 'color'])
  expect(all['background-color'], 'All filter background').toBe(ACTION)
  expect(all.color, 'All filter label').toBe(WHITE)
  for (const f of filters) await expectCorners(f, '4px', 'filter')
  const add = await expectCorners(main(page).getByRole('button', { name: /Add client/ }), '7px', 'Add client')
  const scroll = await assertPageDoesNotScrollSideways(page, 'Clients')

  await attachJson(testInfo, 'pw-01-measurements', { h1, tableCorners, chipReads, avatar, all, add, scroll })
  await attachShot(page, testInfo, 'clients')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('PW-02 Clients header row fits at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  const api = trackApi(page)
  await openFirm(page, api)
  await openNav(page, /^Clients/, 'Client portfolio')
  await expect(clientRow(page), 'the roster never drew').toBeVisible()
  const h1 = h1Of(page, 'Client portfolio')
  const named: Named = {
    header: h1.locator('xpath=../..'),
    titles: h1.locator('xpath=..'),
    all: main(page).getByRole('button', { name: 'All', exact: true }),
    active: main(page).getByRole('button', { name: 'Active', exact: true }),
    archived: main(page).getByRole('button', { name: 'Archived', exact: true }),
    add: main(page).getByRole('button', { name: /Add client/ }),
  }
  const controls = ['all', 'active', 'archived', 'add']
  const measured = await sweepWide(page, 'Clients header', async () => {
    await settle(page, ...Object.values(named))
    const { rects, problems } = await boxes(named)
    inside(rects, 'header', controls, problems)
    noOverlap(rects, controls, problems)
    for (const c of controls) noOverlap(rects, [c, 'titles'], problems)
    const centres = controls.filter((c) => rects[c]).map((c) => centreY(rects[c]))
    if (Math.max(...centres) - Math.min(...centres) > 1) problems.push(`the controls do not share a vertical centre (${centres.join(', ')})`)
    return { problems, rects }
  })
  await attachJson(testInfo, 'pw-02-measurements', measured)
  await attachShot(page, testInfo, 'clients-header')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('PW-03 Customers at 1440: heading, table, buyer chip, avatar', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  const api = trackApi(page)
  await openFirm(page, api)
  await openNav(page, /^Customers/, 'Customers & vendors')
  const row = page.locator('.pf-list-row').filter({ hasText: 'Buyer Ltd' })
  await expect(row, 'the created invoice\'s buyer never reached the table').toBeVisible()
  await api.quiet('Customers')
  await settle(page, main(page), row)

  const h1 = await styles(h1Of(page, 'Customers & vendors'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const table = page.locator('.pf-list-head').locator('xpath=..')
  const tableCorners = await expectCorners(table, '6px', 'customers table')
  expect(await border(table), 'customers table border').toBe(`1px solid ${LINE_1}`)
  expect((await styles(table, ['box-shadow']))['box-shadow'], 'customers table shadow').toBe('none')

  const chip = row.getByText(/^(VALID|NEEDS TIN)$/)
  await expect(chip, 'tax status chip count').toHaveCount(1)
  expect(await chip.locator('*').count(), 'tax status chip has a child element').toBe(0)
  const chipCorners = await expectCorners(chip, '4px', 'tax status chip')
  const cellBox = await chip.locator('xpath=..').boundingBox()
  const chipBox = await chip.boundingBox()
  expect(cellBox && chipBox, 'chip or its cell has no box').toBeTruthy()
  expect(enclosesRect(cellBox as Rect, chipBox as Rect, 1), 'chip sticks out of its cell').toBe(true)
  expect((chipBox as Rect).width, 'chip is not narrower than its cell').toBeLessThan((cellBox as Rect).width)
  const avatar = await expectCorners(row.locator('xpath=./span[1]/span[1]'), '50%', 'avatar')
  const scroll = await assertPageDoesNotScrollSideways(page, 'Customers')

  await attachJson(testInfo, 'pw-03-measurements', { h1, tableCorners, chipCorners, avatar, scroll })
  await attachShot(page, testInfo, 'customers')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

async function openReports(page: Page): Promise<void> {
  const api = trackApi(page)
  await openFirm(page, api)
  await openNav(page, /^Reports/, 'Reports & analytics')
  await expect(page.locator('.pf-grid-5'), 'the KPI grid never drew').toBeVisible()
  await expect(page.getByText(/^\d+% PASS$/), 'the validation summary never resolved from the rollup').toBeVisible()
  await expect(page.getByText('FIRM-WIDE', { exact: true }), 'the rollup carried no top failures').toBeVisible()
  await api.quiet('Reports')
  await settle(page, main(page))
}

test('PW-04 Reports at 1440: KPI tiles, chips, PASS colour, disabled exports', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openReports(page)

  const h1 = await styles(h1Of(page, 'Reports & analytics'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const tiles = page.locator('.pf-grid-5 > div')
  await expect(tiles, 'the KPI row draws exactly five tiles').toHaveCount(5)
  const tileReads: Record<string, unknown>[] = []
  for (const tile of await tiles.all()) {
    const corners = await expectCorners(tile, '6px', 'KPI tile')
    const edge = await border(tile)
    expect(edge, 'KPI tile border').toBe(`1px solid ${LINE_1}`)
    expect((await styles(tile, ['box-shadow']))['box-shadow'], 'KPI tile shadow').toBe('none')
    const figure = await styles(tile.locator('span.money'), ['font-family', 'font-weight', 'font-variant-numeric'])
    expect(firstFamily(figure['font-family']), 'KPI figure first family').toBe('Manrope')
    expect(figure['font-weight'], 'KPI figure weight').toBe('700')
    expect(figure['font-variant-numeric'], 'KPI figure numerals').toBe('tabular-nums')
    tileReads.push({ corners, edge, figure })
  }
  const chips = {
    sample: await expectCorners(page.getByText('SAMPLE', { exact: true }), '4px', 'SAMPLE chip'),
    firmWide: await expectCorners(page.getByText('FIRM-WIDE', { exact: true }), '4px', 'FIRM-WIDE chip'),
  }
  const pass = (await styles(page.getByText(/^\d+% PASS$/), ['color'])).color
  expect(pass, 'PASS chip colour').toBe('rgb(29, 115, 67)')

  const exportsList = page.locator('button[disabled][aria-describedby="exports-blocked-reason-text"]')
  await expect(exportsList, 'the export row draws four disabled buttons').toHaveCount(4)
  const exportReads: Record<string, unknown>[] = []
  for (const btn of await exportsList.all()) {
    const corners = await expectCorners(btn, '7px', 'export button')
    const paint = await styles(btn, ['background-color', 'color', 'opacity'])
    expect(paint['background-color'], 'export background keeps the ghost fill').toBe('rgba(0, 0, 0, 0)')
    expect(paint.color, 'export colour keeps the ghost ink').toBe('rgb(11, 48, 50)')
    expect(paint.opacity, 'export opacity').toBe('0.45')
    exportReads.push({ corners, paint })
  }
  const scroll = await assertPageDoesNotScrollSideways(page, 'Reports')

  await attachJson(testInfo, 'pw-04-measurements', { h1, tileReads, chips, pass, exportReads, scroll })
  await attachShot(page, testInfo, 'reports')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('PW-05 Reports chips sit beside their labels at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openReports(page)
  const sample = page.getByText('SAMPLE', { exact: true })
  const sampleHead = sample.locator('xpath=..')
  const card = page.getByText('Validation summary', { exact: true }).locator('xpath=../..')
  const firmWide = card.getByText('FIRM-WIDE', { exact: true })
  const named: Named = {
    sampleTile: sample.locator('xpath=../..'),
    sampleLabel: sampleHead.locator('div.label'),
    sample,
    card,
    failuresLabel: card.getByText('Top failures', { exact: true }),
    firmWide,
  }
  const measured = await sweepWide(page, 'Reports chips', async () => {
    await settle(page, ...Object.values(named))
    const { rects, problems } = await boxes(named)
    inside(rects, 'sampleTile', ['sample', 'sampleLabel'], problems)
    inside(rects, 'card', ['firmWide', 'failuresLabel'], problems)
    for (const [label, chip] of [['sampleLabel', 'sample'], ['failuresLabel', 'firmWide']] as const) {
      if (!rects[label] || !rects[chip]) continue
      noOverlap(rects, [label, chip], problems)
      if (rects[chip].x < rects[label].x + rects[label].width - 0.5) problems.push(`${chip} is not right of ${label}`)
      if (Math.abs(centreY(rects[chip]) - centreY(rects[label])) > 1) problems.push(`${chip} and ${label} do not share a vertical centre`)
    }
    return { problems, rects }
  })
  await attachJson(testInfo, 'pw-05-measurements', measured)
  await attachShot(page, testInfo, 'reports-chips')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// --- Workflows list and builder ---------------------------------------------------------------------------

const screen = (page: Page) => page.locator('[data-screen-label="Workflow builder"]')
const policyRow = (page: Page) => screen(page).locator('.pf-row').filter({ hasText: SEEDED_POLICY })

// Fails the test on any write to an approval policy; read the result after the test body.
function watchPolicyWrites(page: Page): string[] {
  const writes: string[] = []
  page.on('request', (r) => {
    if (['POST', 'PUT', 'DELETE', 'PATCH'].includes(r.method()) && new URL(r.url()).pathname.includes('/approval-policies')) writes.push(`${r.method()} ${r.url()}`)
  })
  return writes
}

async function openWorkflows(page: Page): Promise<void> {
  await signInAs(page, 'firm')
  await openNav(page, /^Workflows/, 'Approval policies')
  await expect(policyRow(page), `the seeded "${SEEDED_POLICY}" never reached the list`).toBeVisible()
  await settle(page, main(page), policyRow(page))
}

test('PW-06 Workflows list at 1440: heading, policy row, pill, standing, buttons', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openWorkflows(page)

  const h1 = await styles(h1Of(page, 'Approval policies'), ['font-family', 'font-weight'])
  expect(firstFamily(h1['font-family']), 'h1 first family').toBe('Manrope')
  expect(h1['font-weight'], 'h1 weight').toBe('700')

  const row = policyRow(page)
  const rowCorners = await expectCorners(row, '6px', 'policy row')
  expect((await styles(row, ['box-shadow']))['box-shadow'], 'policy row shadow').toBe('none')
  const pill = await expectCorners(row.getByText(/^(PUBLISHED|DRAFT)$/), '4px', 'status pill')
  const standing = (await styles(row.locator('xpath=./div[contains(@class,"mono")]'), ['color'])).color
  expect(standing, 'standing colour').toBe(FG_3)
  const buttons: Record<string, unknown> = {}
  for (const [name, btn] of [['edit', row.getByRole('button', { name: 'Edit', exact: true })], ['delete', row.getByRole('button', { name: /^Delete / })]] as const) {
    const corners = await expectCorners(btn, '7px', `${name} button`)
    const edge = (await styles(btn, ['border-top-color']))['border-top-color']
    expect(edge, `${name} ghost border`).toBe(GHOST_BORDER)
    buttons[name] = { corners, edge }
  }
  const scroll = await assertPageDoesNotScrollSideways(page, 'Workflows list')

  await attachJson(testInfo, 'pw-06-measurements', { h1, rowCorners, pill, standing, buttons, scroll })
  await attachShot(page, testInfo, 'workflows-list')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('PW-07 Workflows list row and intro fit at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openWorkflows(page)
  const row = policyRow(page)
  const named: Named = {
    row,
    standing: row.locator('xpath=./div[contains(@class,"mono")]'),
    edit: row.getByRole('button', { name: 'Edit', exact: true }),
    delete: row.getByRole('button', { name: /^Delete / }),
    count: screen(page).getByText(/^\d+ POLICIES$/),
    intro: screen(page).getByText(/^Each policy decides who signs off/),
  }
  const parts = ['standing', 'edit', 'delete']
  const measured = await sweepWide(page, 'Workflows list', async () => {
    await settle(page, ...Object.values(named))
    const { rects, problems } = await boxes(named)
    inside(rects, 'row', parts, problems)
    noOverlap(rects, parts, problems)
    noOverlap(rects, ['count', 'intro'], problems)
    if (rects.count && rects.intro && Math.abs(centreY(rects.count) - centreY(rects.intro)) > 1) problems.push('the count and the intro do not share a vertical centre')
    return { problems, rects }
  })
  await attachJson(testInfo, 'pw-07-measurements', measured)
  await attachShot(page, testInfo, 'workflows-list-row')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

async function openBuilder(page: Page): Promise<void> {
  await openWorkflows(page)
  await policyRow(page).getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(page.getByLabel('Policy name'), 'Edit never opened the builder on the seeded policy').toHaveValue(SEEDED_POLICY)
  await expect(page.getByText('Invoice submitted', { exact: true }), 'the canvas never drew').toBeVisible()
  await settle(page, main(page))
}

test('PW-08 builder at 1440: canvas, nodes, inspector, simulator, header controls', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  const writes = watchPolicyWrites(page)
  await openBuilder(page)

  const trigger = page.getByText('Invoice submitted', { exact: true }).locator('xpath=../..')
  const canvas = trigger.locator('xpath=../..')
  const terminal = page.getByText('Transmit to NRS / MBS', { exact: true }).locator('xpath=../..')
  // internal/demopolicy firmPlan.steps: approval, condition > 250000000.00, condition > 1000000000.00, approval.
  const simple = canvas.locator('xpath=./div/div/div[@draggable="true"]')
  const selected = simple.first()
  const unselected = simple.nth(1)
  await expect(unselected, 'the seeded policy has two top-level approval cards').toBeVisible()
  await selected.click({ position: { x: 6, y: 6 } })
  const inspector = page.getByTestId('step-inspector-body').locator('xpath=..')
  await expect(inspector.getByText('Approval step', { exact: true }), 'selecting the first card never opened its inspector').toBeVisible()
  await settle(page, canvas, inspector)

  // Canvas, trigger, terminal.
  const canvasStyle = await styles(canvas, ['background-color', 'background-image'])
  await expectCorners(canvas, '6px', 'canvas')
  expect(canvasStyle['background-color'], 'canvas background').toBe('rgb(243, 241, 233)')
  expect(canvasStyle['background-image'], 'canvas background-image').toBe('none')
  const triggerStyle = await styles(trigger, ['background-color', 'box-shadow'])
  await expectCorners(trigger, '6px', 'trigger')
  expect(triggerStyle['background-color'], 'trigger background').toBe('rgb(8, 47, 49)')
  expect(triggerStyle['box-shadow'], 'trigger shadow').toBe('none')
  const terminalStyle = await styles(terminal, ['border-top-color', 'box-shadow'])
  await expectCorners(terminal, '6px', 'terminal')
  expect(terminalStyle['border-top-color'], 'terminal border').toBe(ACTION)
  expect(terminalStyle['box-shadow'], 'terminal shadow').toBe('none')

  // Nodes: an unselected card, the selected card, a badge, the Move glyph at rest.
  const probe = await probeBorderWidth(canvas)
  const rest = await styles(unselected, ['border-top-color', 'border-top-style', 'border-top-width', 'box-shadow'])
  await expectCorners(unselected, '6px', 'unselected node')
  expect(rest['border-top-color'], 'unselected node border').toBe(LINE_2)
  expect(rest['border-top-style'], 'unselected node border style').toBe('solid')
  expect(rest['border-top-width'], 'unselected node border width equals the 1.5px probe').toBe(probe)
  expect(rest['box-shadow'], 'unselected node shadow').toBe('none')
  const lit = await styles(selected, ['border-top-color', 'box-shadow'])
  expect(lit['border-top-color'], 'selected node border').toBe(ACTION)
  expect(lit['box-shadow'], 'selected node shadow').toBe('none')
  const badge = await expectCorners(selected.getByText('APPROVAL', { exact: true }), '4px', 'badge')
  const move = (await styles(selected.getByRole('button', { name: /^Move / }), ['color'])).color
  expect(move, 'resting Move glyph').toBe(FG_4)

  // Palette tiles.
  const tiles = page.locator('button.pf-upcard')
  await expect(tiles.last(), 'the palette drew no tiles').toBeVisible()
  for (const tile of await tiles.all()) await expectCorners(tile, '6px', 'palette tile')

  // Header controls (the layout sweep is PW-09).
  const left = page.getByLabel('Policy name').locator('xpath=../..')
  const applies = left.locator('select.pf-select')
  const appliesStyle = await styles(applies, ['background-color'])
  expect(appliesStyle['background-color'], 'Applies select background').toBe(WHITE)
  const appliesBox = await applies.boundingBox()
  expect(appliesBox?.width, 'Applies select width (the builder passes width 200)').toBe(200)
  const publish = await expectCorners(page.getByRole('button', { name: 'Publish', exact: true }), '7px', 'Publish')

  // Inspector, approval step.
  const inspectorStyle = await styles(inspector, ['box-shadow'])
  await expectCorners(inspector, '6px', 'inspector')
  expect(inspectorStyle['box-shadow'], 'inspector shadow').toBe('none')
  const title = await styles(inspector.getByText('Approval step', { exact: true }), ['font-family', 'font-weight', 'font-size'])
  expect(firstFamily(title['font-family']), 'inspector title family').toBe('Manrope')
  expect(title['font-weight'], 'inspector title weight').toBe('700')
  expect(title['font-size'], 'inspector title size').toBe('15px')
  const remove = await styles(inspector.getByRole('button', { name: 'Remove', exact: true }), ['font-size', 'font-weight'])
  expect(remove, 'Remove').toEqual({ 'font-size': '12.5px', 'font-weight': '600' })
  const manage = (await styles(inspector.getByRole('button', { name: 'Manage roles', exact: true }), ['font-weight']))['font-weight']
  expect(manage, 'Manage roles weight').toBe('500')
  const delegationRow = inspector.getByText('Allow delegation', { exact: true }).locator('xpath=..')
  expect((await styles(delegationRow, ['opacity'])).opacity, 'delegation row opacity').toBe('0.55')
  const track = inspector.getByRole('switch', { name: 'Allow delegation' })
  const trackBox = await track.boundingBox()
  const trackRadius = parseFloat((await styles(track, ['border-top-left-radius']))['border-top-left-radius'])
  expect(trackRadius, 'delegation track corner is at least half its height').toBeGreaterThanOrEqual((trackBox as Rect).height / 2)
  expect((await styles(track.locator('span.pf-knob'), ['box-shadow']))['box-shadow'], 'knob shadow').toBe('none')

  // Simulator.
  const simulator = page.getByText('Test a scenario', { exact: true }).locator('xpath=..')
  const simStyle = await styles(simulator, ['box-shadow'])
  await expectCorners(simulator, '6px', 'simulator')
  expect(simStyle['box-shadow'], 'simulator shadow').toBe('none')
  const simTitle = await styles(simulator.getByText('Test a scenario', { exact: true }), ['font-family', 'font-weight', 'font-size'])
  expect(firstFamily(simTitle['font-family']), 'simulator title family').toBe('Manrope')
  expect(simTitle['font-weight'], 'simulator title weight').toBe('700')
  expect(simTitle['font-size'], 'simulator title size').toBe('15px')
  const dots = simulator.locator('span.mono').filter({ hasText: /^(\d+|✓|·)$/ })
  await expect(dots.nth(1), 'the scenario draws at least two steps').toBeVisible()
  for (const dot of await dots.all()) await expectCorners(dot, '50%', 'simulator dot')
  const connectors = simulator.locator('xpath=.//span[contains(@style,"width: 1.5px")]')
  await expect(connectors, 'one connector between each pair of steps').toHaveCount((await dots.count()) - 1)
  for (const line of await connectors.all()) expect(await line.locator('*').count(), 'a connector carries an arrowhead element').toBe(0)

  await attachJson(testInfo, 'pw-08-approval-measurements', { canvasStyle, triggerStyle, terminalStyle, probe, rest, lit, badge, move, appliesStyle, appliesWidth: appliesBox?.width, publish, title, remove, manage, trackRadius, simTitle })
  await attachShot(page, testInfo, 'builder-selected')

  // Condition step: the preset equal to the value is lit, the others rest.
  await canvas.locator('div[draggable="true"]', { hasText: 'CONDITION' }).nth(1).click({ position: { x: 6, y: 6 } })
  await expect(inspector.getByText('Condition', { exact: true }), 'selecting the condition never opened its inspector').toBeVisible()
  const actionTint = await resolveColor(inspector, '--action-tint')
  const bg1 = await resolveColor(inspector, '--bg-1')
  const preset = (label: string) => inspector.getByRole('button', { name: label, exact: true })
  const presetPaint = async (label: string) => styles(preset(label), ['border-top-color', 'color', 'background-color'])
  const litPaint = await presetPaint('₦1B')
  expect(litPaint, 'the ₦1B preset matches the condition value').toEqual({ 'border-top-color': ACTION, color: ACTION, 'background-color': actionTint })
  const restPaint = await presetPaint('₦100M')
  expect(restPaint['border-top-color'], 'a non-matching preset border').toBe(LINE_2)
  expect(restPaint['background-color'], 'a non-matching preset background').toBe(bg1)
  await preset('₦500M').click()
  // .pf-btn eases border-color over --dur-fast, so a read right after the click is mid-transition.
  await expect.poll(async () => (await presetPaint('₦500M'))['border-top-color'], { message: 'the clicked preset lights' }).toBe(ACTION)
  await expect.poll(async () => (await presetPaint('₦1B'))['border-top-color'], { message: 'the previous preset rests' }).toBe(LINE_2)
  await expectCorners(inspector.getByText('RULE', { exact: true }).locator('xpath=..'), '6px', 'RULE box')
  await attachJson(testInfo, 'pw-08-condition-measurements', { actionTint, litPaint, restPaint })

  const scroll = await assertPageDoesNotScrollSideways(page, 'builder')
  await attachJson(testInfo, 'pw-08-scroll', scroll)
  expect(writes, `a policy write reached the gateway:\n${writes.join('\n')}`).toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('PW-09 builder header fits at every wide width and at 860px', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  const writes = watchPolicyWrites(page)
  await openBuilder(page)

  // Local edit only: Save draft is never clicked.
  const nameInput = page.getByLabel('Policy name')
  await nameInput.fill('N'.repeat(60))
  await expect(page.getByTestId('publish-blocked-reason'), 'a local rename must block Publish, which is the reason the sweep reads').toBeVisible()

  const publish = page.getByRole('button', { name: 'Publish', exact: true })
  const left = nameInput.locator('xpath=../..')
  const named: Named = {
    back: page.getByRole('button', { name: /All policies/ }),
    left,
    nameRow: nameInput.locator('xpath=..'),
    pill: nameInput.locator('xpath=..').getByText(/^(PUBLISHED|DRAFT)$/),
    applies: left.locator('select.pf-select'),
    right: publish.locator('xpath=../..'),
    publish,
    reason: page.getByTestId('publish-blocked-reason'),
  }
  const consequence = page.getByTestId('publish-consequence')
  const measured = await sweepWide(page, 'builder header', async () => {
    await settle(page, ...Object.values(named))
    const present = (await consequence.count()) > 0 // rendered only for an unsealed policy
    const { rects, problems } = await boxes(present ? { ...named, consequence } : named)
    noOverlap(rects, ['left', 'right'], problems)
    inside(rects, 'left', ['pill', 'applies'], problems)
    if (rects.publish && rects.nameRow && Math.abs(rects.publish.y - rects.nameRow.y) > 1) problems.push('Publish and the name row do not share a top')
    if (rects.left && rects.right) {
      if (rects.right.x < rects.left.x + rects.left.width - 1) problems.push('the right column is not beside the left column')
      if (Math.abs(rects.right.y - rects.left.y) > 1) problems.push('the columns do not share a top')
    }
    if (rects.back && rects.left && rects.back.y + rects.back.height > rects.left.y + 1) problems.push('the back link is not above the header row')
    for (const n of ['reason', 'consequence']) {
      if (rects[n] && rects.publish && Math.abs(rects[n].x + rects[n].width - (rects.publish.x + rects.publish.width)) > 1) problems.push(`${n} right edge differs from Publish`)
    }
    return { problems, rects }
  })

  const entry = page.viewportSize()
  let narrow: Record<string, Rect> = {}
  try {
    await page.setViewportSize({ width: 860, height: 900 })
    await expect
      .poll(async () => {
        await settle(page, left)
        const { rects, problems } = await boxes({ back: named.back, left, right: named.right })
        noOverlap(rects, ['left', 'right', 'back'], problems)
        narrow = rects
        return problems
      }, { message: 'builder header at 860px', timeout: 10_000 })
      .toEqual([])
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  await attachJson(testInfo, 'pw-09-measurements', { wide: measured, narrow })
  await attachShot(page, testInfo, 'builder-long-name')

  await named.back.click()
  await expect(h1Of(page, 'Approval policies'), 'All policies did not return to the list').toBeVisible()
  expect(writes, `a policy write reached the gateway:\n${writes.join('\n')}`).toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// --- State captures (PW-10) ----------------------------------------------------------------------------------

// Answers the first GET that `match` accepts. Every other request, the CORS preflight included, passes
// through, so the stub reaches the view's own request and not the shell's or a later page's.
async function stubOnce(page: Page, match: (url: URL) => boolean, answer: (route: Route) => Promise<void>): Promise<void> {
  let used = false
  await page.route(
    (url) => url.origin === GATEWAY_ORIGIN && match(url),
    async (route) => {
      if (used || route.request().method() !== 'GET') return route.continue()
      used = true
      await answer(route)
    },
  )
}

const JSON_HEADERS = { 'content-type': 'application/json', 'access-control-allow-origin': APP_ORIGIN }

const matchers = {
  clients: (u: URL) => u.pathname === '/api/portfolio/v1/entities' && u.searchParams.get('limit') === '200' && !u.searchParams.has('status'),
  invoices: (u: URL) =>
    u.pathname === '/api/invoice/v1/invoices' && u.searchParams.get('offset') === '0' && u.searchParams.get('limit') === '200' && u.searchParams.get('entity_id') === entity.id,
  rollup: (u: URL) => u.pathname === '/api/dashboard/v1/rollup',
  policies: (u: URL) => u.pathname === '/api/invoice/v1/approval-policies',
}

type Kind = 'loading' | 'error' | 'empty' | 'truncated'
type StateCase = {
  name: string
  screen: 'Clients' | 'Customers' | 'Reports' | 'Workflows'
  kind: Kind
  match: keyof typeof matchers
  copy: string | RegExp
  copyTestId?: string
  reshape?: (body: Record<string, unknown>) => Record<string, unknown>
}

const withTotal = (body: Record<string, unknown>, total: number) => ({ ...body, pagination: { ...(body.pagination as object), total } })
const TRUNCATED_COPY = new RegExp(`^Showing \\d+ of ${TRUNCATING_TOTAL} invoices — refine your search to see the rest\\.$`)

const STATE_CASES: StateCase[] = [
  { name: 'Clients loading', screen: 'Clients', kind: 'loading', match: 'clients', copy: 'Loading entities…' },
  { name: 'Clients error', screen: 'Clients', kind: 'error', match: 'clients', copy: 'Something went wrong' },
  { name: 'Clients empty', screen: 'Clients', kind: 'empty', match: 'clients', copy: 'No entities yet', reshape: (b) => ({ ...withTotal(b, 0), entities: [] }) },
  { name: 'Customers loading', screen: 'Customers', kind: 'loading', match: 'invoices', copy: 'Loading customers…' },
  { name: 'Customers error', screen: 'Customers', kind: 'error', match: 'invoices', copy: 'Something went wrong' },
  { name: 'Customers truncated', screen: 'Customers', kind: 'truncated', match: 'invoices', copy: TRUNCATED_COPY, copyTestId: 'customers-truncated-notice', reshape: (b) => withTotal(b, TRUNCATING_TOTAL) },
  { name: 'Reports loading', screen: 'Reports', kind: 'loading', match: 'invoices', copy: 'Loading reports…' },
  { name: 'Reports error', screen: 'Reports', kind: 'error', match: 'invoices', copy: 'Something went wrong' },
  { name: 'Reports truncated', screen: 'Reports', kind: 'truncated', match: 'invoices', copy: TRUNCATED_COPY, copyTestId: 'reports-truncated-notice', reshape: (b) => withTotal(b, TRUNCATING_TOTAL) },
  { name: 'Reports rollup loading', screen: 'Reports', kind: 'loading', match: 'rollup', copy: 'Loading validation summary…' },
  { name: 'Reports rollup error', screen: 'Reports', kind: 'error', match: 'rollup', copy: 'Something went wrong' },
  { name: 'Workflows loading', screen: 'Workflows', kind: 'loading', match: 'policies', copy: 'Loading approval policies…' },
  { name: 'Workflows error', screen: 'Workflows', kind: 'error', match: 'policies', copy: 'Something went wrong' },
  { name: 'Workflows empty', screen: 'Workflows', kind: 'empty', match: 'policies', copy: 'No approval policies yet', reshape: (b) => ({ ...b, approval_policies: [] }) },
]

const SCREEN_NAV: Record<StateCase['screen'], { nav: RegExp; heading: string }> = {
  Clients: { nav: /^Clients/, heading: 'Client portfolio' },
  Customers: { nav: /^Customers/, heading: 'Customers & vendors' },
  Reports: { nav: /^Reports/, heading: 'Reports & analytics' },
  Workflows: { nav: /^Workflows/, heading: 'Approval policies' },
}

// The Clients loading, error and empty cards at 1440 (shared Loading, ErrorState and EmptyState).
async function readClientsState(page: Page, kind: Kind): Promise<Record<string, unknown>> {
  if (kind === 'loading') {
    const spinner = page.locator('main.pf-main .apic-loading-spin')
    const label = page.getByText('Loading entities…', { exact: true })
    const s = await styles(spinner, ['width', 'height', 'border-top-left-radius', 'border-top-color', 'border-right-color', 'border-right-width', 'border-right-style'])
    expect(s.width, 'spinner width').toBe('16px')
    expect(s.height, 'spinner height').toBe('16px')
    expect(s['border-top-left-radius'], 'spinner corner').toBe('50%')
    expect(s['border-top-color'], 'spinner arc colour').toBe(ACTION)
    expect(`${s['border-right-width']} ${s['border-right-style']} ${s['border-right-color']}`, 'spinner track').toBe(`2px solid ${LINE_2}`)
    const { rects, problems } = await boxes({ spinner, label })
    if (rects.spinner && rects.label) {
      if (rects.spinner.x + rects.spinner.width > rects.label.x) problems.push('the spinner is not left of its label')
      if (Math.abs(centreY(rects.spinner) - centreY(rects.label)) > 1) problems.push('the spinner and its label do not share a vertical centre')
    }
    expect(problems, 'spinner against label').toEqual([])
    return { spinner: s, rects }
  }
  if (kind === 'error') {
    const title = page.getByText('Something went wrong', { exact: true })
    const card = title.locator('xpath=..')
    const paint = await styles(card, ['background-color', 'box-shadow'])
    const corners = await expectCorners(card, '6px', 'error card')
    expect(paint['background-color'], 'error card background').toBe(WHITE)
    expect(await border(card), 'error card border').toBe(`1px solid ${LINE_1}`)
    expect(paint['box-shadow'], 'error card shadow').toBe('none')
    const { rects, problems } = await boxes({ main: main(page), card })
    inside(rects, 'main', ['card'], problems)
    if (rects.card && rects.card.width > 520.5) problems.push(`the error card is ${rects.card.width}px wide`)
    expect(problems, 'error card placement').toEqual([])
    const titleStyle = await styles(title, ['font-family', 'font-weight'])
    expect(firstFamily(titleStyle['font-family']), 'error title family').toBe('Manrope')
    expect(titleStyle['font-weight'], 'error title weight').toBe('700')
    const retry = await expectCorners(card.getByRole('button', { name: 'Retry' }), '7px', 'Retry')
    return { paint, corners, rects, titleStyle, retry }
  }
  const title = page.getByText('No entities yet', { exact: true })
  const corners = await expectCorners(title.locator('xpath=..'), '6px', 'empty card')
  const weight = (await styles(title, ['font-weight']))['font-weight']
  expect(weight, 'empty title weight').toBe('700')
  return { corners, weight }
}

// The Reports rollup states draw inline in the Validation summary card: no card chrome, no 40px block padding.
async function readRollupState(page: Page, kind: Kind): Promise<Record<string, unknown>> {
  if (kind === 'loading') {
    const row = page.getByText('Loading validation summary…', { exact: true })
    const rowPad = await styles(row, ['padding-top', 'padding-bottom', 'gap'])
    expect(rowPad['padding-top'], 'rollup loading row top padding').toBe('0px')
    expect(rowPad['padding-bottom'], 'rollup loading row bottom padding').toBe('0px')
    expect(rowPad.gap, 'rollup loading row gap').toBe('10px')
    const spinner = row.locator('> span')
    const s = await styles(spinner, ['width', 'height', 'border-top-left-radius', 'border-top-color', 'border-right-color', 'border-right-width', 'border-right-style', 'animation-name'])
    expect(s.width, 'rollup spinner width').toBe('16px')
    expect(s.height, 'rollup spinner height').toBe('16px')
    expect(s['border-top-left-radius'], 'rollup spinner corner').toBe('50%')
    expect(s['border-top-color'], 'rollup spinner arc colour').toBe(ACTION)
    expect(`${s['border-right-width']} ${s['border-right-style']} ${s['border-right-color']}`, 'rollup spinner track').toBe(`2px solid ${LINE_2}`)
    expect(s['animation-name'], 'rollup spinner animation').toBe('spin')
    return { rowPad, spinner: s }
  }
  const title = page.getByText('Something went wrong', { exact: true })
  const body = title.locator('xpath=..')
  const paint = await styles(body, ['border-top-width', 'box-shadow', 'padding-top', 'padding-left', 'max-width'])
  expect(paint['border-top-width'], 'rollup error draws no nested card border').toBe('0px')
  expect(paint['box-shadow'], 'rollup error draws no nested card shadow').toBe('none')
  expect(paint['padding-top'], 'rollup error sits at the summary card padding').toBe('18px')
  expect(paint['padding-left'], 'rollup error sits at the summary card side padding').toBe('20px')
  expect(paint['max-width'], 'rollup error has no 520px card width').toBe('none')
  return { paint }
}

// The Workflows empty card: its message draws at 360 and sits inside the card.
async function readWorkflowsEmpty(page: Page): Promise<Record<string, unknown>> {
  const empty = page.getByTestId('policies-empty')
  const message = empty.locator('p')
  const card = empty.locator('> :first-child')
  const maxWidth = (await styles(message, ['max-width']))['max-width']
  expect(maxWidth, 'Workflows empty message max-width').toBe('360px')
  const { rects, problems } = await boxes({ card, message })
  inside(rects, 'card', ['message'], problems)
  if (rects.message && rects.message.width > 360.5) problems.push(`the message is ${rects.message.width}px wide`)
  expect(problems, 'Workflows empty message placement').toEqual([])
  return { maxWidth, rects }
}

for (const c of STATE_CASES) {
  test(`PW-10 ${c.name}`, async ({ page }, testInfo) => {
    test.setTimeout(90_000)
    const drops: Dropper[] = c.kind === 'error' ? [expectedStatusDropper(page, 503, /\/api\//)] : []
    const errors = gatedErrors(page, drops)
    const api = trackApi(page)
    let release!: () => void
    const gate = new Promise<void>((resolve) => (release = resolve))

    const answer = async (route: Route): Promise<void> => {
      if (c.kind === 'loading') {
        await gate
        await route.continue()
      } else if (c.kind === 'error') {
        await route.fulfill({ status: 503, headers: JSON_HEADERS, body: JSON.stringify({ error: 'stubbed for a screenshot' }) })
      } else {
        const res = await route.fetch()
        const body = (await res.json()) as Record<string, unknown>
        await route.fulfill({ status: res.status(), headers: JSON_HEADERS, body: JSON.stringify((c.reshape as NonNullable<StateCase['reshape']>)(body)) })
      }
    }
    // The policy list is a shell request at boot, so its stub goes in before sign-in; the other three
    // are the view's own request, so their stub goes in after the shell settles.
    if (c.screen === 'Workflows') {
      await stubOnce(page, matchers[c.match], answer)
      await signInAs(page, 'firm')
    } else {
      await openFirm(page, api)
      await stubOnce(page, matchers[c.match], answer)
    }
    const { nav, heading } = SCREEN_NAV[c.screen]
    await navButton(page, nav).click()
    await expect(h1Of(page, heading), `the ${heading} screen drew no h1`).toBeVisible()

    const copy = c.copyTestId ? page.getByTestId(c.copyTestId) : page.getByText(c.copy, typeof c.copy === 'string' ? { exact: true } : undefined)
    await expect(copy, `${c.name}: the live copy never rendered`).toBeVisible()
    if (c.copyTestId) await expect(copy, `${c.name}: the notice text`).toHaveText(c.copy)
    if (c.kind === 'error') await expect(page.getByText('HTTP 503', { exact: true }), `${c.name}: the status line`).toBeVisible()
    await settle(page, main(page))

    const reading =
      c.screen === 'Clients'
        ? await readClientsState(page, c.kind)
        : c.screen === 'Reports' && c.match === 'rollup'
          ? await readRollupState(page, c.kind)
          : c.screen === 'Workflows' && c.kind === 'empty'
            ? await readWorkflowsEmpty(page)
            : {}
    if (c.screen !== 'Workflows') {
      await expect(page.getByTestId('company-switcher'), `${c.name}: the stub reached the shell, so the switcher lost the entity`).toContainText(entity.name)
    }
    await attachJson(testInfo, `pw-10-${c.name.toLowerCase().replace(/ /g, '-')}`, reading)
    await attachShot(page, testInfo, c.name.toLowerCase().replace(/ /g, '-'))

    release()
    await page.unrouteAll({ behavior: 'wait' })
    expect(errors, `console errors beyond the deliberate 503:\n${errors.join('\n')}`).toEqual([])
  })
}
