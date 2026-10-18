// Unit tests for approvalRun404Dropper's pure half. AUDIT-09-06 AC-6 says the exception is
// "unchanged and still needed", and nothing ran against it: a widened URL pattern, or a
// dropper that swallowed every 404, would have passed the whole suite. The "still needed"
// half is a browser fact and stays with invoice-surfaces.spec.ts; the "still narrow" half
// is arithmetic, and lives here.
import { describe, expect, it } from 'vitest'

import {
  approvalRun404Dropper,
  collectSourcedErrors,
  consoleErrorsLine,
  expectedStatusDropper,
  notFoundIdDropper,
  sourcedConsoleError,
} from './consoleGate'

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

// The status-parameterised dropper, over the supply URL.
const SUPPLY_URL = 'https://gw.test/api/invoice/v1/imports/document/invoice'
const SUPPLY_PATTERN = /\/api\/invoice\/v1\/imports\/document\/invoice$/
const CONFLICT_409 = 'Failed to load resource: the server responded with a status of 409 ()'

function fakeStatusPage(status: number, pattern: RegExp) {
  const listeners: ResponseListener[] = []
  const page = { on: (event: string, fn: ResponseListener) => { if (event === 'response') listeners.push(fn) } }
  return {
    dropper: expectedStatusDropper(page as unknown as Parameters<typeof expectedStatusDropper>[0], status, pattern),
    respond(respStatus: number, url: string) {
      for (const fn of listeners) fn({ status: () => respStatus, url: () => url })
    },
  }
}

describe('expectedStatusDropper (EXTR27-G1..G3)', () => {
  it('G1: drops an attributed 409 from its own url', () => {
    const { dropper } = fakeStatusPage(409, SUPPLY_PATTERN)
    expect(dropper(CONFLICT_409, SUPPLY_URL)).toBe(true)
    expect(dropper('Uncaught TypeError: x is not a function', SUPPLY_URL)).toBe(false)
  })

  it('G2: never drops a 409 from another url, or a 404 from its own', () => {
    const { dropper } = fakeStatusPage(409, SUPPLY_PATTERN)
    // Positive control first: the budget/attribution path is live.
    expect(dropper(CONFLICT_409, SUPPLY_URL)).toBe(true)

    for (const url of [
      'https://gw.test/api/invoice/v1/imports/document',
      'https://gw.test/api/invoice/v1/imports/document/reading?document_id=x',
      'https://gw.test/api/invoice/v1/invoices',
    ]) {
      expect(dropper(CONFLICT_409, url), url).toBe(false)
    }
    expect(dropper(CONFLICT_409.replace('409', '404'), SUPPLY_URL), 'a 404 from the same url').toBe(false)
  })

  it('G3: spends a nameless 409 only against observed 409s on its url', () => {
    const { dropper, respond } = fakeStatusPage(409, SUPPLY_PATTERN)
    expect(dropper(CONFLICT_409, undefined), 'nothing observed, nothing to spend').toBe(false)

    respond(404, SUPPLY_URL)
    respond(409, 'https://gw.test/api/invoice/v1/invoices')
    expect(dropper(CONFLICT_409, undefined), 'neither observed response was a 409 on this url').toBe(false)

    respond(409, SUPPLY_URL)
    expect(dropper(CONFLICT_409, '')).toBe(true)
    expect(dropper(CONFLICT_409, undefined), 'the budget is exactly one').toBe(false)
  })
})

describe('sourcedConsoleError', () => {
  const T401 = 'Failed to load resource: the server responded with a status of 401 ()'

  it('appends the location url', () => {
    const out = sourcedConsoleError(T401, 'https://gw.test/api/x', [])
    expect(out.startsWith(T401)).toBe(true)
    expect(out).toContain('url: https://gw.test/api/x')
  })

  it('lists the responses of the status the text names', () => {
    const out = sourcedConsoleError(T401, '', [
      '401 POST https://gw.test/auth/sign-out',
      '404 GET https://gw.test/a',
      '401 GET https://gw.test/v1/me',
    ])
    expect(out).toContain('401 POST https://gw.test/auth/sign-out')
    expect(out).toContain('401 GET https://gw.test/v1/me')
    expect(out).not.toContain('404 GET')
  })

  it('says none seen when no response matches', () => {
    expect(sourcedConsoleError(T401, '', ['404 GET https://gw.test/a'])).toContain('401 responses: none seen')
  })

  it('prints origin and path only, for the location url and the listed responses', () => {
    const out = sourcedConsoleError(T401, 'https://gw.test/auth/v1/verify?token=eyJabc.def.ghi#x=eyJabc.def.ghi', [
      '401 GET https://gw.test/auth/v1/verify?token=eyJabc.def.ghi',
    ])
    expect(out).toContain('url: https://gw.test/auth/v1/verify;')
    expect(out).toContain('401 GET https://gw.test/auth/v1/verify]')
    expect(out).not.toContain('token')
    expect(out).not.toContain('eyJ')
  })

  it('scrubs a jwt inside the path', () => {
    const out = sourcedConsoleError(T401, 'https://gw.test/a/eyJabc.def.ghi/b', ['401 GET https://gw.test/a/eyJabc.def.ghi/b'])
    expect(out).toContain('url: https://gw.test/a/<jwt>/b;')
    expect(out).toContain('401 GET https://gw.test/a/<jwt>/b]')
    expect(out).not.toContain('eyJ')
  })

  it('leaves a text with no url and no status unchanged', () => {
    expect(sourcedConsoleError('Uncaught TypeError: x', '', [])).toBe('Uncaught TypeError: x')
  })
})

describe('collectSourcedErrors', () => {
  const T401 = 'Failed to load resource: the server responded with a status of 401 ()'
  type Listener = (arg: unknown) => void

  function fake() {
    const listeners: Record<string, Listener[]> = {}
    const page = { on: (event: string, fn: Listener) => (listeners[event] ??= []).push(fn) }
    const emit = (event: string, arg: unknown) => listeners[event]?.forEach((fn) => fn(arg))
    return {
      errors: collectSourcedErrors(page as unknown as Parameters<typeof collectSourcedErrors>[0]),
      response: (status: number, url: string) =>
        emit('response', { status: () => status, url: () => url, request: () => ({ method: () => 'GET' }) }),
      consoleMsg: (type: string, text: string, url = '') =>
        emit('console', { type: () => type, text: () => text, location: () => ({ url }) }),
      pageError: (message: string) => emit('pageerror', { message }),
    }
  }

  it('records a console error with its url and the 401 response seen before it', () => {
    const f = fake()
    f.response(401, 'https://gw.test/v1/me')
    f.consoleMsg('error', T401, 'https://gw.test/api/x')
    expect(f.errors).toHaveLength(1)
    expect(f.errors[0]).toContain('https://gw.test/v1/me')
    expect(f.errors[0]).toContain('url: https://gw.test/api/x')
  })

  it('ignores non-error console messages and keeps the pageerror format', () => {
    const f = fake()
    f.consoleMsg('warning', 'careful')
    f.pageError('boom')
    expect(f.errors).toEqual(['pageerror: boom'])
  })

  it('rewrites a none-seen entry when its response arrives late', () => {
    const f = fake()
    f.consoleMsg('error', T401)
    expect(f.errors[0]).toContain('none seen')
    f.response(401, 'https://gw.test/auth/sign-out')
    expect(f.errors[0]).toContain('https://gw.test/auth/sign-out')
    expect(f.errors[0]).not.toContain('none seen')
  })

  it('leaves an entry of another status alone when a response arrives late', () => {
    const f = fake()
    f.consoleMsg('error', T401)
    f.response(404, 'https://gw.test/a')
    expect(f.errors[0]).toContain('none seen')
    f.response(401, 'https://gw.test/v1/me')
    expect(f.errors[0]).toContain('https://gw.test/v1/me')
  })

  it('does not list a response that arrived after the entry console line', () => {
    const f = fake()
    f.response(401, 'https://gw.test/v1/me')
    f.consoleMsg('error', T401)
    f.response(401, 'https://gw.test/auth/sign-out')
    f.consoleMsg('error', T401)
    expect(f.errors[0]).toContain('https://gw.test/v1/me')
    expect(f.errors[0]).not.toContain('/auth/sign-out')
    expect(f.errors[1]).toContain('https://gw.test/auth/sign-out')
  })

  it('fills a none-seen entry once, not with responses after the first', () => {
    const f = fake()
    f.consoleMsg('error', T401)
    f.response(401, 'https://gw.test/v1/me')
    f.response(401, 'https://gw.test/auth/sign-out')
    expect(f.errors[0]).toContain('https://gw.test/v1/me')
    expect(f.errors[0]).not.toContain('/auth/sign-out')
  })

  it('returns an empty array when nothing errs', () => {
    const f = fake()
    f.response(200, 'https://gw.test/ok')
    expect(f.errors).toEqual([])
  })
})

describe('consoleErrorsLine', () => {
  it('joins multi-line and sourced entries onto one line that keeps the url', () => {
    const sourced = sourcedConsoleError(
      'Failed to load resource: the server responded with a status of 401 ()',
      'https://gateway-pr-460.up.railway.app/api/tenancy/v1/me',
      ['401 GET https://gateway-pr-460.up.railway.app/api/tenancy/v1/me'],
    )
    const out = consoleErrorsLine(['pageerror: boom\n    at foo (x.js:1:1)', sourced])
    expect(out).not.toMatch(/[\r\n]/)
    expect(out).toContain('pageerror: boom at foo (x.js:1:1) | ')
    expect(out).toContain('[url: https://gateway-pr-460.up.railway.app/api/tenancy/v1/me; 401 responses:')
  })

  it('gives an empty string for no errors', () => {
    expect(consoleErrorsLine([])).toBe('')
  })
})
