// RED specs (M3-09-01, Validation Playground Surface, V1-V8) — pin the validation
// data-access helper, the severityStyle pill mapper, and the shouldValidate/
// playgroundState render-decision helpers before the executor implements the bodies in
// validationApi.ts.
//
// V1-V5 mirror portfolio.test.ts's `vi.stubGlobal('fetch', ...)` pattern: `fetch` is
// stubbed, but `createAuthedFetch`/`apiFetch` are the REAL @invoice-os/api-client +
// src/lib/authedFetch.ts exports, so a stubbed 200/400/401/network failure produces a
// genuine ApiError{kind, ...} — proof at the integration level, not a re-implementation
// of apiFetch's own contract (already covered by client.test.ts).
//
// Every spec below currently fails because validateInvoice/severityStyle/
// shouldValidate/playgroundState's stub bodies throw `new Error('not implemented')`
// before ever calling the real authedFetch/fetch (or, for the pure helpers, before
// returning anything) — that IS the correct RED reason (assertion / not-implemented),
// not an import/compile/setup error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncState } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  playgroundState,
  severityStyle,
  shouldValidate,
  validateInvoice,
  type InvoicePayload,
  type Severity,
  type ValidateResponse,
  type Violation,
} from './validationApi'

interface MockResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
}

function mockFetchOnce(response: MockResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function mockFetchRejecting(err: unknown) {
  const fetchMock = vi.fn().mockRejectedValue(err)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a (currently throwing) helper and returns the caught error, tolerating both a
// synchronous throw (today's stub) and an eventual async rejection — mirrors
// portfolio.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const base = 'https://gw'

const payload: InvoicePayload = { invoice: { supplier: { name: 'x' } } }

describe('validateInvoice', () => {
  it('V1: POSTs .../api/validation/v1/validate with Authorization: Bearer <token>, Content-Type: application/json, and the raw payload as body; resolves the response verbatim', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ rule_set_version: 1, violations: [] }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await validateInvoice(af, base, payload)

    expect(result).toEqual({ rule_set_version: 1, violations: [] })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/validation/v1/validate')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer tok')
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(init.body).toBe(JSON.stringify(payload))
  })

  it('V2: violations resolve verbatim — one with a path, one without (missing path stays undefined, not coerced to null/empty string)', async () => {
    const violations: Violation[] = [
      { rule_key: 'RULE_TIN_FORMAT', severity: 'error', message: 'invalid TIN', path: 'invoice.supplier.tin' },
      { rule_key: 'RULE_MISSING_ADDRESS', severity: 'warning', message: 'address missing' },
    ]
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ rule_set_version: 3, violations }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await validateInvoice(af, base, payload)

    expect(result.violations).toEqual(violations)
    expect(result.violations[0].path).toBe('invoice.supplier.tin')
    expect(result.violations[1].path).toBeUndefined()
  })
})

describe('validateInvoice: non-2xx / transport failures reject with the ApiError unchanged (not swallowed)', () => {
  it('V3: a 400 rejects ApiError{kind:"http", status:400} carrying the "invalid request body" message', async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid request body' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toContain('invalid request body')
  })

  it("V4: a 401 rejects ApiError{status:401} AND fires the authedFetch seam's onUnauthorized handler once (validateInvoice does not intercept 401 itself — that is the seam's job, M3-07-02)", async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'token expired' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    // seam handles 401 (M3-07-02) — asserted here only to prove validateInvoice didn't
    // intercept/swallow the error before it reached the seam, mirroring P15 in
    // portfolio.test.ts.
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('QA: a 400 does NOT fire onUnauthorized — the seam only triggers on kind:"http"+status:401, not on any http error (boundary complement to V4)', async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid request body' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    await captureRejection(() => validateInvoice(af, base, payload))

    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('QA: a 500 rejects ApiError{kind:"http", status:500} (non-2xx handling generalizes beyond 400/401)', async () => {
    mockFetchOnce({ ok: false, status: 500, json: () => Promise.resolve({ error: 'internal error' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(500)
  })

  it('QA: a 503 {"error":"no active rule-set"} rejects ApiError{status:503} with the message preserved verbatim', async () => {
    mockFetchOnce({ ok: false, status: 503, json: () => Promise.resolve({ error: 'no active rule-set' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(503)
    expect(apiErr.message).toContain('no active rule-set')
  })

  it('V5: fetch itself rejecting (network failure) propagates as ApiError{kind:"network", status:null}', async () => {
    mockFetchRejecting(new TypeError('Failed to fetch'))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('network')
    expect(apiErr.status).toBeNull()
  })

  it('QA: a 200 with an unparseable JSON body rejects ApiError{kind:"malformed", status:200} — the "malformed" failure mode is otherwise untested by V1-V5', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.reject(new Error('bad json')) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => validateInvoice(af, base, payload))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('malformed')
    expect(apiErr.status).toBe(200)
  })
})

describe('severityStyle', () => {
  const cases: Array<[Severity, string]> = [
    ['error', 'red'],
    ['warning', 'amber'],
    ['info', 'muted'],
  ]

  it('V6: each severity returns a well-formed StatusStyle (bg/border/text/label all truthy)', () => {
    for (const [severity] of cases) {
      const style = severityStyle(severity)
      expect(style.bg).toBeTruthy()
      expect(style.border).toBeTruthy()
      expect(style.text).toBeTruthy()
      expect(style.label).toBeTruthy()
    }
  })

  it("V6: colors map error->red, warning->amber, info->muted (mirrors entityStatusStyle's var(--status-<color>-*) convention)", () => {
    for (const [severity, color] of cases) {
      const style = severityStyle(severity)
      expect(style.bg).toBe(`var(--status-${color}-bg)`)
      expect(style.border).toBe(`var(--status-${color}-border)`)
      expect(style.text).toBe(`var(--status-${color}-text)`)
    }
  })

  it('V6: the three severity styles are mutually distinct', () => {
    const [error, warning, info] = cases.map(([s]) => severityStyle(s))
    expect(error).not.toEqual(warning)
    expect(warning).not.toEqual(info)
    expect(error).not.toEqual(info)
  })

  // QA FAILS today (M3-09-01 defect, not a test bug): the System Design stub's own code
  // comment ("error->red, warning->amber, info->muted, else->muted") and the Architect
  // Decisions section ("Severity -> token mapping ... unknown->muted fallback ... the
  // mapper is made total so M4/future rule-sets with warning/info render correctly",
  // Obsidian "M3-09 Validation Playground Surface.md") require severityStyle to be a
  // TOTAL mapping — any Severity value outside the current 3 must still resolve to the
  // muted style, not `undefined`. The wire `Violation.severity` comes from JSON.parse
  // (apiFetch<ValidateResponse>) and is NOT runtime-validated against the `Severity`
  // union, so a future rule-set (or a malformed row) sending an unrecognized severity
  // string reaches this function for real. The current implementation
  // (`SEVERITY_STYLE[sev]` with no fallback branch) returns `undefined` for any key not
  // in the literal map — proven directly against the map literal in a bare node repro
  // (`severityStyle('critical')` -> undefined). A UI pill component destructuring
  // `{bg, border, text, label}` off that `undefined` would throw. Needs an executor fix:
  // fall back to the `info`/muted entry for any unrecognized severity.
  it('QA: an out-of-enum severity (cast) still resolves to the muted style, all four fields truthy — total-mapping fallback required by the story', () => {
    const style = severityStyle('critical' as Severity)

    expect(style).toBeDefined()
    expect(style.bg).toBe('var(--status-muted-bg)')
    expect(style.border).toBe('var(--status-muted-border)')
    expect(style.text).toBe('var(--status-muted-text)')
    expect(style.label).toBeTruthy()
  })
})

describe('shouldValidate', () => {
  it('V7: false iff base == null; an empty string and a real URL are both true (strict null-check, not truthiness)', () => {
    expect(shouldValidate(null)).toBe(false)
    expect(shouldValidate('')).toBe(true)
    expect(shouldValidate('https://gw')).toBe(true)
  })
})

describe('playgroundState', () => {
  it('V8: base==null is "idle" regardless of async status (no-gateway zero-network short-circuit wins)', () => {
    const readyState: AsyncState<ValidateResponse> = {
      status: 'ready',
      data: { rule_set_version: 1, violations: [] },
      error: null,
    }

    expect(playgroundState(null, readyState)).toBe('idle')
  })

  it('V8: base present mirrors async.status exactly, for idle/loading/error/empty/ready', () => {
    const cases: Array<AsyncState<ValidateResponse>> = [
      { status: 'idle', data: null, error: null },
      { status: 'loading', data: null, error: null },
      { status: 'error', data: null, error: new ApiError('network', 'boom') },
      { status: 'empty', data: null, error: null },
      { status: 'ready', data: { rule_set_version: 1, violations: [] }, error: null },
    ]

    for (const asyncState of cases) {
      expect(playgroundState(base, asyncState)).toBe(asyncState.status)
    }
  })
})
