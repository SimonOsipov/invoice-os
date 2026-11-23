// Settings > ERP connectors on the deployed preview: connect and disconnect the fake ERP through its
// consent tab, and the row's layout. Every step acts on a client this run created.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { login, createEntity, rawFetch, type Persona } from '../api/client'
import { freshTin } from '../api/fixtures'
import { signInAs } from '../personaSession'
import { assertPageDoesNotScrollSideways, enclosesRect, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { shardTenants } from './targets'

const SHARD = shardTenants('erp-connectors.spec.ts')
const PERSONA: Persona = { ...SHARD.a, tenantId: SHARD.a.id }
const CO1 = 'fake-co-1' // erpweb.FakeCompanies
const OUTCOME_CONNECTED = 'connected' // erp.Outcome* in internal/erp/complete.go

type Conn = { id: string; system: string; external_key: string | null; status: string }

test.describe.configure({ mode: 'serial' })

let token: string
let entity: { id: string; name: string }
let connId = ''

test.beforeAll(async () => {
  expect(test.info().project.name, 'erp-connectors.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces')
  token = await login(PERSONA)
  // "Zenith" sorts after every seeded entity, so it never becomes another spec's default client.
  entity = await createEntity(token, { name: `Zenith ERP connectors ${Date.now()}`, tin: freshTin() })
})

function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

// A local copy of portfolio.spec.ts's helper of the same name.
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

async function openConnectors(page: Page): Promise<void> {
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)
  await page.locator('aside.pf-sidebar nav button.pf-nav', { hasText: /^Settings/ }).click()
  await page.getByRole('button', { name: 'ERP connectors', exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/connectors$/)
}

const fakeRow = (page: Page) => page.locator('[data-testid="erp-connector-row"][data-system="fake"]')
const rowById = (page: Page, id: string) => page.locator(`[data-testid="erp-connector-row"][data-connection-id="${id}"]`)
const pillOf = (row: Locator) => row.getByTestId('erp-connector-status')

async function listConnections(): Promise<Conn[]> {
  const res = await rawFetch(`/api/portfolio/v1/entities/${entity.id}/erp-connections`, { headers: { Authorization: `Bearer ${token}` } })
  expect(res.status).toBe(200)
  return (res.body as { connections: Conn[] }).connections
}

test('an admin connects the fake ERP through the fake consent page', async ({ page, context }) => {
  const errors = collectErrors(page)
  await openConnectors(page)
  const row = fakeRow(page)
  await expect(pillOf(row)).toHaveText('NOT CONNECTED')

  const [popup] = await Promise.all([context.waitForEvent('page'), row.getByTestId('erp-connector-connect').click()])
  await popup.locator('select').selectOption(CO1)
  await popup.getByRole('button', { name: 'Allow', exact: true }).click()
  await expect(popup.locator('main[data-outcome]')).toHaveAttribute('data-outcome', OUTCOME_CONNECTED)
  await popup.close()

  // The app tab polls the list; no reload.
  await expect(pillOf(row)).toHaveText('CONNECTED', { timeout: 30_000 })
  const conns = await listConnections()
  const made = conns.find((c) => c.system === 'fake' && c.status === 'active' && c.external_key === CO1)
  expect(made, `no active fake connection for ${CO1} in:\n${JSON.stringify(conns)}`).toBeDefined()
  connId = made!.id
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// Row relationships at one width: inside the row, clear of each other; the open confirm inside the card.
async function rowProblems(page: Page, id: string, withConfirm: boolean): Promise<string[]> {
  const row = rowById(page, id)
  const parts: Record<string, Locator> = {
    tile: row.locator(':scope > span').first(),
    name: row.locator(':scope > div').first(),
    pill: pillOf(row),
  }
  const buttons = row.locator(':scope > button')
  for (let i = 0; i < (await buttons.count()); i++) parts[`button ${i}`] = buttons.nth(i)
  await settleAnimations(row, ...Object.values(parts))
  const rowBox = await row.boundingBox()
  const rects: Record<string, Rect> = {}
  const problems: string[] = []
  for (const [n, loc] of Object.entries(parts)) {
    const b = await loc.boundingBox()
    if (b) rects[n] = b
    else problems.push(`${n} has no box`)
  }
  if (!rowBox) return [...problems, 'the row has no box']
  const names = Object.keys(rects)
  for (const n of names) if (!enclosesRect(rowBox, rects[n], 1)) problems.push(`${n} is outside the row`)
  for (let i = 0; i < names.length; i++)
    for (let j = i + 1; j < names.length; j++) if (rectsOverlap(rects[names[i]], rects[names[j]])) problems.push(`${names[i]} overlaps ${names[j]}`)
  if (withConfirm) {
    const confirm = page.getByTestId('erp-disconnect-confirm')
    const [card, box] = await Promise.all([row.locator('xpath=..').boundingBox(), confirm.boundingBox()])
    if (!card || !box) problems.push('the card or the confirm has no box')
    else if (!enclosesRect(card, box, 1)) problems.push('the confirm is outside the card')
  }
  return problems
}

async function sweep(page: Page, id: string, withConfirm: boolean): Promise<void> {
  const entry = page.viewportSize()
  try {
    for (const width of [...WIDE_WIDTHS, ...PHONE_WIDTHS]) {
      await page.setViewportSize({ width, height: 900 })
      await expect.poll(() => rowProblems(page, id, withConfirm), { message: `the connector row at ${width}px wide`, timeout: 10_000 }).toEqual([])
      await assertPageDoesNotScrollSideways(page, `ERP connectors at ${width}px wide`)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
}

test('the connector row fits at every width', async ({ page }) => {
  expect(connId, 'the connect test captured no connection').not.toBe('')
  await openConnectors(page)
  const row = rowById(page, connId)
  await expect(pillOf(row)).toHaveText('CONNECTED')
  await row.getByTestId('erp-connector-disconnect').click()
  await expect(page.getByTestId('erp-disconnect-confirm')).toBeVisible()
  await sweep(page, connId, true)
  await page.getByTestId('erp-disconnect-keep').click()
  await expect(page.getByTestId('erp-disconnect-confirm')).toHaveCount(0)
  await sweep(page, connId, false)
})

test('an admin disconnects it after one confirmation', async ({ page }) => {
  expect(connId, 'the connect test captured no connection').not.toBe('')
  const errors = collectErrors(page)
  const posts: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/erp-connections')) posts.push(r.url())
  })
  await openConnectors(page)
  const row = rowById(page, connId)
  await expect(pillOf(row)).toHaveText('CONNECTED')
  await row.getByTestId('erp-connector-disconnect').click()
  await expect(page.getByTestId('erp-disconnect-confirm')).toBeVisible()
  expect(posts, 'the first click must write nothing').toEqual([])

  await page.getByTestId('erp-disconnect-confirmed').click()
  await expect(pillOf(row)).toHaveText('DISCONNECTED', { timeout: 30_000 })
  await expect.poll(async () => (await listConnections()).find((c) => c.id === connId)?.status).toBe('disconnected')
  await sweep(page, connId, false)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
