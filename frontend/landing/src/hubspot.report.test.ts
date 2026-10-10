import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { submissionUrl, submitDemoLead, type DemoLead, type HubSpotTarget } from './hubspot'

const h = vi.hoisted(() => ({ captureApiFailure: vi.fn() }))

// Only the Sentry edge is mocked, so countsAsIssue runs for real.
vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure: h.captureApiFailure }))

const TARGET: HubSpotTarget = { portalId: '148915098', formGuid: 'abc-123' }
const FULL_LEAD: DemoLead = {
  name: 'Ada Okafor',
  email: 'ada@okafor.ng',
  company: 'Okafor & Partners',
  role: 'Finance or Accounting lead',
  size: 'Medium ₦1bn–₦5bn',
  volume: '1k–10k',
  consent: true,
  marketing: false,
}
const CONSENT_TEXT_FIXTURE =
  'I agree to ASComply Africa storing and processing my details so a compliance specialist can contact me about this demo request.'

beforeEach(() => {
  h.captureApiFailure.mockReset()
})
afterEach(() => vi.unstubAllGlobals())

async function rejection(thunk: () => Promise<unknown>): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected a rejection')
}

const submit = () => submitDemoLead(TARGET, FULL_LEAD, CONSENT_TEXT_FIXTURE)

describe('submitDemoLead reporting', () => {
  it.each([
    ['TypeError', () => new TypeError('Failed to fetch')],
    ['TimeoutError', () => new DOMException('t', 'TimeoutError')],
  ])('submitDemoLead_reportsANetworkFailureOnce %s', async (_label, make) => {
    const failure = make()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(failure))

    expect(await rejection(submit)).toBe(failure)

    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
    expect(h.captureApiFailure).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'network', status: null, method: 'POST', url: submissionUrl(TARGET) }),
    )
  })

  it.each([500, 503, 599])('submitDemoLead_reportsA5xxOnce %i', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status }))

    const err = await rejection(submit)
    expect((err as Error).message).toBe(`hubspot ${status}`)

    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
    expect(h.captureApiFailure).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'http', status, method: 'POST', url: submissionUrl(TARGET) }),
    )
  })

  it.each([400, 404, 429, 499])('submitDemoLead_reportsA4xxOnce %i', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status }))

    const err = await rejection(submit)
    expect((err as Error).message).toBe(`hubspot ${status}`)

    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
    expect(h.captureApiFailure).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'http', status, method: 'POST', url: submissionUrl(TARGET) }),
    )
  })

  it.each([503, 404])('submitDemoLead_throwsTheValueItReported %i', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status }))

    const err = await rejection(submit)

    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
    expect(err).toBe((h.captureApiFailure.mock.calls[0][0] as { error: unknown }).error)
  })

  it.each([
    ['200', () => vi.fn().mockResolvedValue({ ok: true, status: 200 })],
    ['AbortError', () => vi.fn().mockRejectedValue(new DOMException('a', 'AbortError'))],
  ])('submitDemoLead_reportsNoSuccessOrCancel %s', async (_label, makeFetch) => {
    vi.stubGlobal('fetch', makeFetch())
    await submit().catch(() => undefined)
    expect(h.captureApiFailure).not.toHaveBeenCalled()

    // Control: a 500 through the same path is reported.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500 }))
    await submit().catch(() => undefined)
    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
  })

  it.each([399, 600])('submitDemoLead_reportsNoNonOkOutside4xxOr5xx %i', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status }))
    expect(((await rejection(submit)) as Error).message).toBe(`hubspot ${status}`)
    expect(h.captureApiFailure).not.toHaveBeenCalled()

    // Control: a 404 through the same path is reported.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 404 }))
    await submit().catch(() => undefined)
    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
  })

  it('submitDemoLead_reportCarriesNoLeadValue', async () => {
    const leadValues = [
      FULL_LEAD.email,
      FULL_LEAD.company,
      FULL_LEAD.name,
      ...FULL_LEAD.name.split(' '),
      FULL_LEAD.role,
      FULL_LEAD.size,
      FULL_LEAD.volume,
      CONSENT_TEXT_FIXTURE,
    ]
    const rows = [
      vi.fn().mockResolvedValue({ ok: false, status: 500 }),
      vi.fn().mockResolvedValue({ ok: false, status: 404 }),
      vi.fn().mockRejectedValue(new TypeError('Failed to fetch')),
    ]
    for (const fetchMock of rows) {
      h.captureApiFailure.mockReset()
      vi.stubGlobal('fetch', fetchMock)
      await submit().catch(() => undefined)

      expect(h.captureApiFailure.mock.calls.length).toBeGreaterThan(0)
      for (const [arg] of h.captureApiFailure.mock.calls) {
        const a = arg as { error: unknown }
        const text = JSON.stringify({ ...a, error: a.error instanceof Error ? a.error.message : a.error })
        expect(text).toContain('submit')
        for (const v of leadValues) expect(text, v).not.toContain(v)
      }
    }
  })

  it('submitDemoLead_aThrowingReporterChangesNothing', async () => {
    h.captureApiFailure.mockImplementation(() => {
      throw new Error('reporter down')
    })

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500 }))
    expect(((await rejection(submit)) as Error).message).toBe('hubspot 500')
    // Control: the throwing reporter was reached.
    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 404 }))
    expect(((await rejection(submit)) as Error).message).toBe('hubspot 404')
    expect(h.captureApiFailure).toHaveBeenCalledTimes(2)

    const failure = new TypeError('Failed to fetch')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(failure))
    expect(await rejection(submit)).toBe(failure)
    expect(h.captureApiFailure).toHaveBeenCalledTimes(3)
  })
})
