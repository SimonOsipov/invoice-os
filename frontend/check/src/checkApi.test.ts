import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const captureApiFailure = vi.hoisted(() => vi.fn())
vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure }))

import { runCheck, runForm, suggestMapping } from './checkApi'
import { CONSENT_TEXT } from './analytics'

const file = (name: string) => new File(['a,b'], name)
const sheet = (fields: Record<string, string | null>) => ({ email: 'a@example.com', file: file('a.csv'), mapping: { headerRow: 2, fields } })
const reply = (status: number, body: string) => new Response(body, { status })

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://api.example.test/')
  captureApiFailure.mockClear()
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('runForm', () => {
  it('runForm_writesThePartsInTheGatewayOrder', () => {
    expect([...runForm(sheet({ invoice_number: 'Inv' })).keys()]).toEqual(['email', 'consent_text', 'header_row', 'mapping', 'file'])
  })

  it('runForm_aDocumentSendsNoMapping', () => {
    expect([...runForm({ email: 'a@example.com', file: file('a.pdf') }).keys()]).toEqual(['email', 'consent_text', 'file'])
  })

  it('runForm_mappingCarriesNoUnplacedField', () => {
    const f = runForm(sheet({ total: null, vat: '', invoice_number: 'Inv' }))
    expect(JSON.parse(f.get('mapping') as string)).toEqual({ invoice_number: 'Inv' })
  })

  it('runForm_sendsTheConsentSentenceAndTheTrimmedEmail', () => {
    const f = runForm({ ...sheet({}), email: '  a@example.com ' })
    expect(f.get('email')).toBe('a@example.com')
    expect(f.get('consent_text')).toBe(CONSENT_TEXT)
    expect(CONSENT_TEXT.length).toBeLessThanOrEqual(500)
  })
})

describe('calls', () => {
  it('suggestMapping_sendsOnlyTheFilePart', async () => {
    const fetchMock = vi.fn().mockResolvedValue(reply(200, '{"columns":[]}'))
    vi.stubGlobal('fetch', fetchMock)
    await suggestMapping(file('a.csv'))
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://api.example.test/readiness-check/mapping')
    expect(init.method).toBe('POST')
    expect([...(init.body as FormData).keys()]).toEqual(['file'])
    expect(init.headers).toBeUndefined()
  })

  it('runCheck_readsTheErrorWordOfARefusal', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(reply(409, '{"error":"This address used its checks."}')))
    expect(await runCheck(sheet({}))).toEqual({ ok: false, status: 409, error: 'This address used its checks.' })
  })

  it('runCheck_aBodylessRefusalHasNoError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(reply(429, '<html>Too many</html>')))
    expect(await runCheck(sheet({}))).toEqual({ ok: false, status: 429, error: null })
  })

  it('runCheck_reportsANetworkFailureAndA503Once', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    expect(await runCheck(sheet({}))).toEqual({ ok: false, status: null, error: null })
    expect(captureApiFailure).toHaveBeenCalledTimes(1)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(reply(503, '{"error":"The check is unavailable."}')))
    await runCheck(sheet({}))
    expect(captureApiFailure).toHaveBeenCalledTimes(2)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(reply(400, '{"error":"email is invalid"}')))
    await runCheck(sheet({}))
    expect(captureApiFailure).toHaveBeenCalledTimes(2)
  })
})
