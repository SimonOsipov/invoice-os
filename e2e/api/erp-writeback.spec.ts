// An ERP invoice's NRS result goes back to the fake ERP; the audit feed carries the write-back.
// Every step acts on a client this run created; assertions are containment, never a tenant total.
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  listInvoices,
  editInvoice,
  validateInvoice,
  approveUntilClosed,
  firmApproverTokens,
  submitAndAwait,
  getInvoice,
  getAuditLog,
  PERSONAS,
} from './client'
import { freshTin } from './fixtures'
import { connectFake } from './erpFake'

const FAKE_KEY = 'fake-co-1' // fakeSeed key in internal/erp/fake_seed.go
const ACCEPTED_NO = 'FK1-0001' // fakeSeed fake-co-1 invoice numbers
const REJECTED_NO = 'FK1-0003'
const ACCEPT_TIN = '99999999-0001' // mock NRS script: accepts
const REJECT_TIN = '99999999-0002' // mock NRS script: rejects
const SUCCEEDED = 'erp.writeback.succeeded' // write in internal/erp/writeback.go
const IRN_SHAPE = /-FBMOCK01-\d{8}$/ // fixture check: the mock-backed IRN shape, mockServiceID in internal/submission/mock_script.go

interface Payload {
  connection_id: string
  invoice_number: string
  outcome: string
  irn?: string
}

test.describe.serial('ERP write-back (API E2E, over the deployed gateway)', () => {
  let admin: string
  let entityId: string
  let connId: string
  const ids: Record<string, string> = {}

  const writtenBack = async (number: string) => {
    const log = await getAuditLog(admin, { event: [SUCCEEDED], limit: 200 })
    return log.events
      .map((e) => e.payload as Payload)
      .filter((p) => p.connection_id === connId && p.invoice_number === number)
  }

  const settle = async (number: string, tin: string, want: 'accepted' | 'rejected') => {
    const id = ids[number]
    await editInvoice(admin, id, { buyer_tin: tin }) // the pulled draft fails only buyer-tin-format
    await validateInvoice(admin, id)
    await approveUntilClosed(id, await firmApproverTokens())
    await submitAndAwait(admin, id, want)
  }

  test.beforeAll(async () => {
    test.setTimeout(360_000) // a hook has its own timeout; the pull sweep ticks once a minute
    admin = await login(PERSONAS.A)
    const tin = freshTin()
    entityId = (await createEntity(admin, { name: `ERP write-back ${tin}`, tin })).id
    connId = await connectFake(admin, entityId, FAKE_KEY)
    await expect
      .poll(
        async () => {
          const mine = (await listInvoices(admin, { entity_id: entityId, limit: 100 })).invoices
          for (const i of mine) ids[i.invoice_number] = i.id
          return mine.map((i) => i.invoice_number)
        },
        { timeout: 180_000, intervals: [5_000] },
      )
      .toEqual(expect.arrayContaining([ACCEPTED_NO, REJECTED_NO]))
    await settle(ACCEPTED_NO, ACCEPT_TIN, 'accepted')
    await settle(REJECTED_NO, REJECT_TIN, 'rejected')
  })

  test("an accepted ERP invoice's mock IRN is written back to the fake ERP", async () => {
    test.setTimeout(360_000)
    await expect.poll(async () => (await writtenBack(ACCEPTED_NO)).length, { timeout: 120_000, intervals: [3_000] }).toBeGreaterThan(0)
    const [entry] = await writtenBack(ACCEPTED_NO)
    const inv = await getInvoice(admin, ids[ACCEPTED_NO])
    expect(entry.outcome).toBe('accepted')
    expect(entry.irn).toBe(inv.irn)
    expect(entry.irn).toMatch(IRN_SHAPE) // fixture check, mock-backed
  })

  test("a rejected ERP invoice's result is written back without an IRN", async () => {
    test.setTimeout(180_000)
    await expect.poll(async () => (await writtenBack(REJECTED_NO)).length, { timeout: 120_000, intervals: [3_000] }).toBeGreaterThan(0)
    const [entry] = await writtenBack(REJECTED_NO)
    expect(entry.outcome).toBe('rejected')
    expect(entry).not.toHaveProperty('irn')
  })

  test('the write-back leaves the NRS status as it was', async () => {
    test.setTimeout(180_000)
    expect((await getInvoice(admin, ids[ACCEPTED_NO])).status).toBe('accepted')
    expect((await getInvoice(admin, ids[REJECTED_NO])).status).toBe('rejected')
  })
})
