// Unit tests for approvalRun404Dropper's pure half. AUDIT-09-06 AC-6 says the exception is
// "unchanged and still needed", and nothing ran against it: a widened URL pattern, or a
// dropper that swallowed every 404, would have passed the whole suite. The "still needed"
// half is a browser fact and stays with invoice-surfaces.spec.ts; the "still narrow" half
// is arithmetic, and lives here.
import { describe, expect, it } from 'vitest'

import { approvalRun404Dropper } from './consoleGate'

const APPROVAL_404 = 'Failed to load resource: the server responded with a status of 404 ()'
const APPROVAL_URL = 'https://gw.test/api/invoice/v1/invoices/9f1c7e2a-0000-0000-0000-000000000001/approval'

type ResponseListener = (res: { status: () => number; url: () => string }) => void

// The two Page members the dropper touches. Typed as the real Page at the call boundary so
// a signature change still reds tsc.
function fakePage() {
  const listeners: ResponseListener[] = []
  const page = { on: (event: string, fn: ResponseListener) => { if (event === 'response') listeners.push(fn) } }
  return {
    dropper: approvalRun404Dropper(page as unknown as Parameters<typeof approvalRun404Dropper>[0]),
    respond(status: number, url: string) {
      for (const fn of listeners) fn({ status: () => status, url: () => url })
    },
  }
}

describe('approvalRun404Dropper (AUDIT-09-06 AC-6)', () => {
  it('drops an approval-run 404 that names its own resource', () => {
    const { dropper } = fakePage()
    expect(dropper(APPROVAL_404, APPROVAL_URL)).toBe(true)
  })

  it('never drops a 404 from any other url, even while an approval 404 budget is unspent', () => {
    const { dropper, respond } = fakePage()
    respond(404, APPROVAL_URL)
    // Positive control on the same dropper, first: the budget IS live.
    expect(dropper(APPROVAL_404, APPROVAL_URL), 'the attributed approval 404 is still dropped').toBe(true)

    for (const other of [
      'https://gw.test/api/invoice/v1/invoices/abc/history',
      'https://gw.test/api/invoice/v1/invoices/abc/approval/steps',
      'https://gw.test/api/invoice/v1/approval',
      'https://gw.test/api/audit/v1/audit-log',
    ]) {
      expect(dropper(APPROVAL_404, other), other).toBe(false)
    }
  })

  it('passes through a console line that is not a 404 at all', () => {
    const { dropper } = fakePage()
    expect(dropper('Uncaught TypeError: x is not a function', APPROVAL_URL)).toBe(false)
  })

  it('spends the response budget on a nameless 404 line, once per observed 404 response', () => {
    const { dropper, respond } = fakePage()
    // No response seen yet: a nameless line is a real error, not a freebie.
    expect(dropper(APPROVAL_404, undefined), 'nothing observed, nothing to spend').toBe(false)

    respond(404, APPROVAL_URL)
    respond(404, APPROVAL_URL)
    // A non-404 and a 404 from elsewhere must not top the budget up.
    respond(200, APPROVAL_URL)
    respond(404, 'https://gw.test/api/invoice/v1/invoices/abc/history')

    expect(dropper(APPROVAL_404, '')).toBe(true)
    expect(dropper(APPROVAL_404, undefined)).toBe(true)
    expect(dropper(APPROVAL_404, undefined), 'the budget is exactly two, not unlimited').toBe(false)
  })
})
