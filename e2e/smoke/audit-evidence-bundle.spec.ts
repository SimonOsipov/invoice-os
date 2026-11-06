import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { unzipSync } from 'fflate'
import {
  approveUntilClosed,
  createEntity,
  createInvoice,
  firmApproverTokens,
  getAuditLog,
  getInvoice,
  login,
  PERSONAS,
  submitAndAwait,
  validateInvoice,
} from '../api/client'
import { freshTin } from '../api/fixtures'
import { collectErrors, signInAs } from '../personaSession'
import { resolveTarget } from '../targets'
import { settleAnimations } from '../topology/layout'
import { isProductionHost } from './sentryHost'

const FALSE_SENTENCE = 'evidence bundle is not reachable'
// Label of submission.accepted in frontend/app/src/lib/auditVocabulary.ts.
const ACCEPTED_LABEL = 'Transmission accepted'

// Own copy of e2e/api/evidence-bundle.spec.ts acceptInvoiceFields.
// 99999999-0001 is the mock adapter's explicit accept trigger (.claude/rules/submission-mock.md).
function acceptInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '99999999-0001',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

const utcDay = (iso: string) => new Date(iso).toISOString().slice(0, 10)

// Per-run data on tenant A; no firm-policy write, so the parallel smoke suite is undisturbed.
async function setup() {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `Zz AUDIT-16 ${freshTin()}`, tin: freshTin() })
  const created = await createInvoice(token, { entity_id: entity.id, ...acceptInvoiceFields(`INV-AUDIT-16-${freshTin()}`) })
  expect((await validateInvoice(token, created.id)).status, 'the clean fixture validates').toBe('validated')
  await approveUntilClosed(created.id, await firmApproverTokens())
  await submitAndAwait(token, created.id, 'accepted')

  let event: Awaited<ReturnType<typeof getAuditLog>>['events'][number] | undefined
  await expect
    .poll(
      async () => {
        event = (await getAuditLog(token, { invoice_id: created.id, event: ['submission.accepted'] })).events[0]
        return event?.id
      },
      { message: 'submission.accepted should reach the audit log', timeout: 30_000 },
    )
    .toBeTruthy()
  const invoice = await getInvoice(token, created.id)
  const toDay = utcDay(event!.created_at)
  const invoiceDay = utcDay(invoice.created_at)
  return { entity, invoice, event: event!, fromDay: invoiceDay < toDay ? invoiceDay : toDay, toDay }
}

type Setup = Awaited<ReturnType<typeof setup>>

async function expectPrefilledDrawer(page: Page, s: Setup) {
  const drawer = page.getByTestId('evidence-bundle-drawer')
  await expect(drawer).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('evidence-company-trigger')).toContainText(s.entity.name)
  await expect(page.getByTestId('evidence-period-from')).toHaveValue(s.fromDay)
  await expect(page.getByTestId('evidence-period-to')).toHaveValue(s.toDay)
  await expect(page.getByTestId('evidence-confirm-company')).toContainText(s.entity.name, { timeout: 30_000 })
  const count = page.getByTestId('evidence-confirm-row-value').first()
  await expect(count).toBeVisible()
  expect(Number.parseInt((await count.textContent()) ?? '', 10), 'the preview counts at least one invoice').toBeGreaterThanOrEqual(1)
  return drawer
}

test.describe('audit evidence bundle entry points (deployed)', () => {
  test.beforeEach(() => {
    const url = resolveTarget('APP_URL')
    test.skip(isProductionHost(url), `${new URL(url).hostname} is a production host`)
  })

  test('auditLog_submissionRowOpensTheBundleForItsCompanyAndDays', async ({ page }) => {
    test.setTimeout(240_000)
    const errors = collectErrors(page)
    const s = await setup()

    await signInAs(page, 'firm', { path: `/audit?invoice=${s.invoice.id}` })
    const row = page.getByTestId('audit-row').filter({ has: page.getByTestId('audit-what').filter({ hasText: ACCEPTED_LABEL }) })
    await expect(row).toBeVisible({ timeout: 30_000 })
    await row.click()
    await page.getByTestId('audit-evidence-affordance').click()

    await expectPrefilledDrawer(page, s)

    await page.getByTestId('evidence-bundle-prepare').click()
    await expect(page.getByTestId('evidence-ready')).toBeVisible({ timeout: 120_000 })
    const downloadEvent = page.waitForEvent('download', { timeout: 60_000 })
    await page.getByTestId('evidence-ready-download').click()
    const download = await downloadEvent
    expect(await download.failure(), 'the bundle download must complete').toBeNull()
    const zip = unzipSync(new Uint8Array(readFileSync(await download.path())))
    expect(new TextDecoder().decode(zip['invoices.csv']), 'invoices.csv holds the event invoice').toContain(s.invoice.id)

    await page.getByTestId('evidence-bundle-close').click()
    await expect(page.getByTestId('evidence-bundle-drawer')).toHaveCount(0)
    await expect(page.getByTestId('audit-expansion')).toBeVisible()
    expect(await page.locator('body').innerText()).not.toContain(FALSE_SENTENCE)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('invoiceDetail_activityRowOpensTheBundleOnThePage', async ({ page }) => {
    test.setTimeout(240_000)
    const errors = collectErrors(page)
    const s = await setup()

    const path = `/invoices/${s.invoice.id}`
    await signInAs(page, 'firm', { path })
    const card = page.getByTestId('invoice-activity')
    const row = card.getByTestId('audit-row').filter({ has: page.getByTestId('audit-what').filter({ hasText: ACCEPTED_LABEL }) })
    await expect(row).toBeVisible({ timeout: 30_000 })
    await row.click()
    await card.getByTestId('audit-evidence-affordance').click()

    const drawer = await expectPrefilledDrawer(page, s)
    expect(new URL(page.url()).pathname, 'the drawer opens on the invoice page').toBe(path)

    // Only a relationship check sees a drawer clipped or moved by an ancestor.
    await settleAnimations(drawer, page.getByTestId('evidence-bundle-scrim'))
    expect(await drawer.evaluate((el) => getComputedStyle(el).position)).toBe('fixed')
    const box = (await drawer.boundingBox())!
    const view = page.viewportSize()!
    expect(Math.abs(box.y), 'drawer top at the viewport top').toBeLessThanOrEqual(1)
    expect(Math.abs(box.x + box.width - view.width), 'drawer right edge at the viewport right').toBeLessThanOrEqual(1)
    expect(Math.abs(box.height - view.height), 'drawer height equals the viewport height').toBeLessThanOrEqual(1)

    await page.getByTestId('evidence-bundle-close').click()
    await expect(page.getByTestId('evidence-bundle-drawer')).toHaveCount(0)
    await expect(card.getByTestId('audit-expansion')).toBeVisible()
    expect(await page.locator('body').innerText()).not.toContain(FALSE_SENTENCE)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})
