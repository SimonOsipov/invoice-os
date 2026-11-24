// Settings > ERP connectors on the nightly: connect Intuit's sandbox company through Intuit's own login,
// then disconnect. The nightly only; PR previews run the fake ERP (erp-connectors.spec.ts).
import { test, expect, type Page } from '@playwright/test'
import { login, createEntity, rawFetch, type Persona } from '../api/client'
import { freshTin } from '../api/fixtures'
import { signInAs } from '../personaSession'
import { nightlyTarget } from '../targets'
import { consentUrlProblems, intuitLoginEnv, signInToIntuit } from './intuitLogin'
import { GATEWAY_URL, shardTenants } from './targets'

const SHARD = shardTenants('erp-quickbooks.spec.ts')
const PERSONA: Persona = { ...SHARD.a, tenantId: SHARD.a.id }
const OUTCOME_CONNECTED = 'connected' // erp.Outcome* in internal/erp/complete.go

type Conn = { id: string; system: string; external_key: string | null; status: string }

test.skip(!nightlyTarget(), 'nightly only: PR previews run the fake ERP')
// The sign-in types a password: record nothing.
test.use({ trace: 'off', video: 'off', screenshot: 'off' })

let token: string
let entity: { id: string; name: string }

test.beforeAll(async () => {
  expect(test.info().project.name, 'erp-quickbooks.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces')
  token = await login(PERSONA)
  // "Zenith" sorts after every seeded entity, so it never becomes another spec's default client.
  entity = await createEntity(token, { name: `Zenith QuickBooks ${Date.now()}`, tin: freshTin() })
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

async function listConnections(): Promise<Conn[]> {
  const res = await rawFetch(`/api/portfolio/v1/entities/${entity.id}/erp-connections`, { headers: { Authorization: `Bearer ${token}` } })
  expect(res.status).toBe(200)
  return (res.body as { connections: Conn[] }).connections
}

test('an admin connects the QuickBooks sandbox company and disconnects it', async ({ page, context }) => {
  test.setTimeout(300_000)
  const env = intuitLoginEnv()
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entity.name }).click()
  await page.locator('aside.pf-sidebar nav button.pf-nav', { hasText: /^Settings/ }).click()
  await page.getByRole('button', { name: 'ERP connectors', exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/connectors$/)

  const row = page.locator('[data-testid="erp-connector-row"][data-system="quickbooks"]')
  await expect(row).toContainText('QuickBooks Online')
  const pill = row.getByTestId('erp-connector-status')
  await expect(pill).toHaveText('NOT CONNECTED')

  const [popup] = await Promise.all([context.waitForEvent('page'), row.getByTestId('erp-connector-connect').click()])
  await popup.waitForURL(/appcenter\.intuit\.com/, { timeout: 60_000 })
  expect(consentUrlProblems(popup.url(), GATEWAY_URL)).toEqual([])
  await signInToIntuit(popup, env)
  await expect(popup.locator('main[data-outcome]')).toHaveAttribute('data-outcome', OUTCOME_CONNECTED, { timeout: 60_000 })
  await popup.close()

  await expect(pill).toHaveText('CONNECTED', { timeout: 30_000 })
  const made = (await listConnections()).find((c) => c.system === 'quickbooks' && c.status === 'active')
  expect(made, 'no active quickbooks connection').toBeDefined()
  expect(made!.external_key, 'the realm id is digits').toMatch(/^\d+$/)

  const off = await rawFetch(`/api/portfolio/v1/entities/${entity.id}/erp-connections/${made!.id}/disconnect`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(off.status).toBe(200)
  await expect.poll(async () => (await listConnections()).find((c) => c.id === made!.id)?.status).toBe('disconnected')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
