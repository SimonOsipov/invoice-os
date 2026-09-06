// Unit tests for approvalRun404Dropper's pure half. AUDIT-09-06 AC-6 says the exception is
// "unchanged and still needed", and nothing ran against it: a widened URL pattern, or a
// dropper that swallowed every 404, would have passed the whole suite. The "still needed"
// half is a browser fact and stays with invoice-surfaces.spec.ts; the "still narrow" half
// is arithmetic, and lives here.
import { describe, expect, it } from 'vitest'

import { approvalRun404Dropper, notFoundIdDropper } from './consoleGate'

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

// The same "still narrow" arithmetic for the ROUTE-02-07 dropper. The property that
// matters is the second test: an id the spec never deep-linked is never masked, so a
// regression that 404s a REAL invoice still fails the gate.
function fakeIdPage(ids: string[]) {
  const listeners: ResponseListener[] = []
  const page = { on: (event: string, fn: ResponseListener) => { if (event === 'response') listeners.push(fn) } }
  return {
    dropper: notFoundIdDropper(page as unknown as Parameters<typeof notFoundIdDropper>[0], ids),
    respond(status: number, url: string) {
      for (const fn of listeners) fn({ status: () => status, url: () => url })
    },
  }
}

const XT_ID = '9f1c7e2a-0000-0000-0000-0000000000aa'
const RANDOM_ID = '9f1c7e2a-0000-0000-0000-0000000000bb'
const OTHER_ID = '9f1c7e2a-0000-0000-0000-0000000000cc'
const NOT_FOUND_404 = 'Failed to load resource: the server responded with a status of 404 ()'
const INVOICES = 'https://gw.test/api/invoice/v1/invoices'

describe('notFoundIdDropper (ROUTE-02-07 AC-5)', () => {
  it('drops every one of a deep-linked id own four 404s', () => {
    const { dropper } = fakeIdPage([XT_ID, RANDOM_ID])
    for (const id of [XT_ID, RANDOM_ID]) {
      for (const suffix of ['', '/history', '/source-document', '/approval']) {
        const url = `${INVOICES}/${id}${suffix}`
        expect(dropper(NOT_FOUND_404, url), url).toBe(true)
      }
    }
  })

  it('never drops a 404 for an id the spec did not deep-link', () => {
    const { dropper, respond } = fakeIdPage([XT_ID, RANDOM_ID])
    respond(404, `${INVOICES}/${XT_ID}`)
    expect(dropper(NOT_FOUND_404, `${INVOICES}/${XT_ID}`), 'positive control: the budget is live').toBe(true)

    for (const url of [
      `${INVOICES}/${OTHER_ID}`,
      `${INVOICES}/${OTHER_ID}/history`,
      'https://gw.test/api/invoice/v1/entities',
      'https://gw.test/api/audit/v1/audit-log',
    ]) {
      expect(dropper(NOT_FOUND_404, url), url).toBe(false)
    }
  })

  it('does not mask a non-404 console line from a listed id', () => {
    const { dropper } = fakeIdPage([XT_ID])
    expect(dropper('Uncaught TypeError: x is not a function', `${INVOICES}/${XT_ID}`)).toBe(false)
  })

  it('spends a nameless 404 only against observed 404s on the listed ids', () => {
    const { dropper, respond } = fakeIdPage([XT_ID])
    expect(dropper(NOT_FOUND_404, undefined), 'nothing observed, nothing to spend').toBe(false)
    respond(404, `${INVOICES}/${OTHER_ID}`)
    expect(dropper(NOT_FOUND_404, undefined), 'an unlisted 404 is not budget').toBe(false)

    respond(404, `${INVOICES}/${XT_ID}/history`)
    expect(dropper(NOT_FOUND_404, '')).toBe(true)
    expect(dropper(NOT_FOUND_404, undefined), 'the budget is exactly one').toBe(false)
  })
})
