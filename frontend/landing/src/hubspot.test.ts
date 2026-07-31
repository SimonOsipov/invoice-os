// RED-then-GREEN spec (LAND-02-01, U10-U27) — pins the hostname gate, the payload
// builder and the POST contract before the implementation lands. The env-driven
// specs (U15-U17) copy the vi.stubEnv/vi.unstubAllEnvs idiom from
// packages/api-client/src/client.test.ts:54-56,145-158 — NOT from auth.test.ts,
// which never calls vi.stubEnv (Explore-stage correction, task-313).
//
// isProductionHost/hubspotTarget/resolveSubmitTarget/submissionUrl/buildSubmission
// are synchronous stubs that throw 'not implemented' when called directly, so
// U10-U24 RED via the thrown error. U25/U26 wrap submitDemoLead's rejection in
// captureRejection() and assert on the *shape* of the rejection (the exact
// 'hubspot <status>' message, or object identity with the original network error)
// — a generic "not implemented" Error satisfies neither, so both still RED via a
// genuine assertion failure rather than a vacuous pass. U27 calls submitDemoLead
// directly (uncaught), so it RED via the thrown error before any fetch assertion
// runs.
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  buildSubmission,
  hubspotTarget,
  isProductionHost,
  resolveSubmitTarget,
  submissionUrl,
  submitDemoLead,
  type DemoLead,
  type HubSpotTarget,
} from './hubspot'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const FULL_LEAD: DemoLead = {
  name: 'Ada Okafor',
  email: 'ada@okafor.ng',
  company: 'Okafor & Partners',
  role: 'Finance or Accounting lead',
  size: 'Medium ₦1bn–₦5bn',
  volume: '1k–10k',
  consent: true,
}
const CONSENT_TEXT_FIXTURE =
  'I agree to ASComply Africa storing and processing my details so a compliance specialist can contact me about this demo request.'

// Calls a (currently throwing) submitDemoLead and returns the caught rejection
// reason, tolerating both today's synchronous 'not implemented' throw and the
// eventual real async rejection (mirrors client.test.ts's captureRejection).
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected submitDemoLead to reject, but it resolved')
}

function spyOnConsole() {
  return {
    error: vi.spyOn(console, 'error').mockImplementation(() => undefined),
    warn: vi.spyOn(console, 'warn').mockImplementation(() => undefined),
    log: vi.spyOn(console, 'log').mockImplementation(() => undefined),
    info: vi.spyOn(console, 'info').mockImplementation(() => undefined),
  }
}

function expectNoConsoleCalls(spies: ReturnType<typeof spyOnConsole>) {
  expect(spies.error).not.toHaveBeenCalled()
  expect(spies.warn).not.toHaveBeenCalled()
  expect(spies.log).not.toHaveBeenCalled()
  expect(spies.info).not.toHaveBeenCalled()
}

describe('isProductionHost', () => {
  it('U10: the real production hostname matches', () => {
    expect(isProductionHost('www.ascomply.com')).toBe(true)
  })

  it('U11: matching is case-insensitive and whitespace-trimmed', () => {
    expect(isProductionHost('WWW.ASCOMPLY.COM')).toBe(true)
    expect(isProductionHost(' www.ascomply.com ')).toBe(true)
  })

  it('U12: PR/dev/local hostnames all fail', () => {
    for (const h of ['landing-pr-142-a3f9.up.railway.app', 'localhost', '127.0.0.1', '']) {
      expect(isProductionHost(h), h).toBe(false)
    }
  })

  it('U13: suffix impostor, substring impostor, and the un-allowlisted apex all fail — exact match only', () => {
    for (const h of ['www.ascomply.com.attacker.example', 'evil-www.ascomply.com', 'ascomply.com']) {
      expect(isProductionHost(h), h).toBe(false)
    }
  })
})

describe('hubspotTarget', () => {
  it('U14: returns null when both VITE_HUBSPOT_* vars are unset', () => {
    expect(hubspotTarget()).toBeNull()
  })

  it('U15: a single blank variable closes the gate, in either direction', () => {
    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', '   ')
    expect(hubspotTarget()).toBeNull()

    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '   ')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
    expect(hubspotTarget()).toBeNull()
  })

  it('U16: both vars set with surrounding whitespace come back trimmed', () => {
    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '  148915098  ')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', '  abc-123  ')
    expect(hubspotTarget()).toEqual({ portalId: '148915098', formGuid: 'abc-123' })
  })
})

describe('resolveSubmitTarget', () => {
  it('U17: needs BOTH the build ids and a production hostname', () => {
    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
    expect(resolveSubmitTarget('landing-pr-142-a3f9.up.railway.app')).toBeNull()

    vi.unstubAllEnvs()
    expect(resolveSubmitTarget('www.ascomply.com')).toBeNull()

    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
    expect(resolveSubmitTarget('www.ascomply.com')).toEqual({ portalId: '148915098', formGuid: 'abc-123' })
  })
})

describe('submissionUrl', () => {
  it('U18: builds the EU Forms submit URL from the target', () => {
    expect(submissionUrl({ portalId: '148915098', formGuid: 'abc-123' })).toBe(
      'https://api-eu1.hsforms.com/submissions/v3/integration/submit/148915098/abc-123',
    )
  })
})

describe('buildSubmission', () => {
  it('U19: emits exactly the seven mapped fields, in field-mapping-table order', () => {
    const result = buildSubmission(FULL_LEAD, CONSENT_TEXT_FIXTURE)
    expect(result.fields).toEqual([
      { objectTypeId: '0-1', name: 'firstname', value: 'Ada' },
      { objectTypeId: '0-1', name: 'lastname', value: 'Okafor' },
      { objectTypeId: '0-1', name: 'email', value: 'ada@okafor.ng' },
      { objectTypeId: '0-1', name: 'company', value: 'Okafor & Partners' },
      { objectTypeId: '0-1', name: 'jobtitle', value: 'Finance or Accounting lead' },
      { objectTypeId: '0-1', name: 'company_size', value: 'Medium ₦1bn–₦5bn' },
      { objectTypeId: '0-1', name: 'monthly_invoice_volume', value: '1k–10k' },
    ])
  })

  it('U20: a single-token name emits no lastname field at all (not an empty-valued one)', () => {
    const lead: DemoLead = { ...FULL_LEAD, name: 'Ada' }
    const result = buildSubmission(lead, CONSENT_TEXT_FIXTURE)
    expect(result.fields.some((f) => f.name === 'lastname')).toBe(false)
  })

  it('U21: an empty role emits no jobtitle field', () => {
    const lead: DemoLead = { ...FULL_LEAD, role: '' }
    const result = buildSubmission(lead, CONSENT_TEXT_FIXTURE)
    expect(result.fields.some((f) => f.name === 'jobtitle')).toBe(false)
  })

  it('U22: an extra property on the lead never leaks into the emitted fields (honeypot guard)', () => {
    const lead: DemoLead & { website: string } = { ...FULL_LEAD, website: 'spam.example' }
    const result = buildSubmission(lead, CONSENT_TEXT_FIXTURE)
    const names = result.fields.map((f) => f.name)
    expect(names).toEqual(['firstname', 'lastname', 'email', 'company', 'jobtitle', 'company_size', 'monthly_invoice_volume'])
    expect(names).not.toContain('website')
  })

  it('U23: legalConsentOptions.consent is structured exactly, and no context key is emitted', () => {
    const result = buildSubmission(FULL_LEAD, CONSENT_TEXT_FIXTURE)
    expect(result.legalConsentOptions.consent).toEqual({
      consentToProcess: true,
      text: CONSENT_TEXT_FIXTURE,
      communications: [],
    })
    expect(result).not.toHaveProperty('context')
  })

  it('U24: the emitted consent text is byte-identical to the argument — no truncation, no interpolation', () => {
    const result = buildSubmission(FULL_LEAD, CONSENT_TEXT_FIXTURE)
    expect(result.legalConsentOptions.consent.text).toBe(CONSENT_TEXT_FIXTURE)
  })
})

describe('submitDemoLead', () => {
  const TARGET: HubSpotTarget = { portalId: '148915098', formGuid: 'abc-123' }

  it('U25: a non-2xx response rejects with a status-only message, without logging, and carries no field value', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 400 }))
    const spies = spyOnConsole()

    const err = await captureRejection(() => submitDemoLead(TARGET, FULL_LEAD, CONSENT_TEXT_FIXTURE))

    expect(err).toBeInstanceOf(Error)
    const message = (err as Error).message
    // A stub's generic "not implemented" message fails this — genuine RED, not a
    // vacuous pass — and it pins AC #6's exact 'hubspot ' + status contract.
    expect(message).toBe('hubspot 400')
    expect(message).not.toContain(FULL_LEAD.email)
    expect(message).not.toContain(FULL_LEAD.company)
    expectNoConsoleCalls(spies)
  })

  it('U26: a network failure propagates as the native rejection (same object), and logs nothing', async () => {
    const networkError = new TypeError('Failed to fetch')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(networkError))
    const spies = spyOnConsole()

    const err = await captureRejection(() => submitDemoLead(TARGET, FULL_LEAD, CONSENT_TEXT_FIXTURE))

    // Object identity, not just instanceof — a stub's own Error is a different
    // instance, so this fails genuinely pre-implementation.
    expect(err).toBe(networkError)
    expectNoConsoleCalls(spies)
  })

  it('U27: a 2xx response resolves; fetch is called once with the EU URL, POST, JSON headers, a signal, and a body matching buildSubmission — nothing logged', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    const spies = spyOnConsole()

    await submitDemoLead(TARGET, FULL_LEAD, CONSENT_TEXT_FIXTURE)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const call = fetchMock.mock.calls[0]
    const url = call?.[0] as string | undefined
    const init = call?.[1] as RequestInit | undefined
    expect(url).toBe('https://api-eu1.hsforms.com/submissions/v3/integration/submit/148915098/abc-123')
    expect(init?.method).toBe('POST')
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(init?.signal).toBeInstanceOf(AbortSignal)
    expect(JSON.parse(init?.body as string)).toEqual(buildSubmission(FULL_LEAD, CONSENT_TEXT_FIXTURE))
    expectNoConsoleCalls(spies)
  })
})
