// The reconciliation sweep over the deployed fleet: a queued invoice with no submit job is flagged as drift.
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  createInvoice,
  validateInvoice,
  approveUntilClosed,
  firmApproverTokens,
  transitionInvoice,
  getInvoice,
  getAuditLog,
  rawFetch,
  PERSONAS,
  type AuditEvent,
} from './client'
import { ensureFirmPolicyActive } from './contract-helpers'
import { freshTin } from './fixtures'

// internal/reconciliation/reconciliation.go recordDriftAudit, QueuedNeverSent
const DRIFT_EVENT = 'reconciliation.drift_detected'
const QUEUED_NEVER_SENT = 'queued_never_sent'

// sweep.go defaultReconcileInterval is 5 minutes; previews leave RECONCILE_INTERVAL unset.
// ceiling: one tick (5 min) plus 60 s; a skipped leader tick or a sweep over 60 s fails this test. Widen SWEEP_WAIT_MS if it does.
const SWEEP_WAIT_MS = 360_000

// Own copy (no cross-spec imports).
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

test.describe('reconciliation sweep (API E2E, over the deployed gateway)', () => {
  // A retry repeats a timed wait: it adds up to 8 min and no signal.
  test.describe.configure({ retries: 0 })

  test('reconciliation: the sweep flags a queued invoice with no submit job as queued_never_sent', async () => {
    test.setTimeout(480_000)
    const token = await login(PERSONAS.A)
    await ensureFirmPolicyActive(token)

    const tin = freshTin()
    const entity = await createEntity(token, { name: `Zz RECON ${tin}`, tin })
    const number = `INV-RECON-${tin}`
    const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(number) })
    expect((await validateInvoice(token, created.id)).status).toBe('validated')
    await approveUntilClosed(created.id, await firmApproverTokens())

    // POST /transitions moves the status only; it inserts no submit job (Store.Transition).
    expect((await transitionInvoice(token, created.id, 'queued')).status).toBe('queued')

    let pollError: unknown
    try {
      let drift: AuditEvent | undefined
      await expect
        .poll(
          async () => {
            const log = await getAuditLog(token, { invoice_id: created.id, event: [DRIFT_EVENT] })
            drift = log.events.find((e) => (e.payload as { drift_kind?: string }).drift_kind === QUEUED_NEVER_SENT)
            return drift !== undefined
          },
          {
            intervals: [10_000],
            timeout: SWEEP_WAIT_MS,
            message:
              'no queued_never_sent row within 6 min: is the worker sweep running, and is RECONCILE_INTERVAL unset on this preview?',
          },
        )
        .toBe(true)

      expect(drift!.event).toBe(DRIFT_EVENT)
      expect(drift!.actor).toBe('system')
      expect(drift!.payload).toMatchObject({
        invoice_id: created.id,
        invoice_number: number,
        drift_kind: QUEUED_NEVER_SENT,
      })
      // No job row, so the key is absent (driftPayload).
      expect(drift!.payload).not.toHaveProperty('submission_job_id')
    } catch (err) {
      pollError = err
    }

    // Off queued, so later ticks stop flagging it. rawFetch does not throw, so the poll error survives.
    const off = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { target: 'failed' },
    })

    if (pollError) throw pollError
    expect(off.status, 'queued -> failed').toBe(200)
    expect((await getInvoice(token, created.id)).status).toBe('failed')
  })
})
