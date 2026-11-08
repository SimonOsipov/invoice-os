// A readiness check posted to the deployed notifications intake: a PR preview delivers in fake mode,
// the nightly reaches the test HubSpot portal. Fresh @example.com addresses; the nightly sweep deletes them.
import { test, expect } from '@playwright/test'
import { expectedContactsMode, nightlyTarget } from '../targets'
import { fleetHeaders, type ContactMe } from './client'
import { googleIdToken, previewSecret, serviceUrl } from './preview'

const CONSENT = 'Yes, send me product news and offers.'
const POLL = { timeout: nightlyTarget() ? 120_000 : 30_000, intervals: [500, 1000, 2000] }

// The tag contacts/me returns (internal/notifications/store.go); HubSpot's option is readiness_check (dealSourceReadinessCheck).
const TAG = 'readiness check'
const HS_OPTION = 'readiness_check'
// internal/notifications/deal.go: the fallback deal name.
const DEAL_NAME = 'Readiness check'
// cmd/notifications/main.go route; internal/notifications/intake.go ReadinessChecksHandler error.
const INTAKE = '/internal/contacts/readiness-checks'
const CONSENT_REQUIRED = 'marketing_consent_text is required'

// The intake is internal: the gateway has no route, so call notifications with the service identity.
async function direct(path: string, init: { method?: string; headers?: Record<string, string>; body?: unknown } = {}) {
  const base = serviceUrl('notifications')
  const res = await fetch(`${base}${path}`, {
    method: init.method,
    headers: {
      'X-Serverless-Authorization': `Bearer ${await googleIdToken(base)}`,
      ...fleetHeaders(),
      ...init.headers,
      ...(init.body !== undefined && { 'Content-Type': 'application/json' }),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

const readiness = (email: string, consent?: string) =>
  direct(INTAKE, { method: 'POST', body: { email, ...(consent !== undefined && { marketing_consent_text: consent }) } })

async function me(email: string): Promise<ContactMe | null> {
  const res = await direct('/v1/contacts/me', { headers: { 'X-User-Email': email } })
  if (res.status === 404) return null
  if (res.status !== 200) throw new Error(`contacts/me answered ${res.status}`)
  return res.body as ContactMe
}

const freshEmail = () => `readiness-check-${crypto.randomUUID()}@example.com`

test.describe('readiness check contact (API E2E, notifications intake)', () => {
  if (nightlyTarget()) test.describe.configure({ timeout: 240_000 })

  test('a readiness check makes a consented contact tagged readiness check', async () => {
    const email = freshEmail()
    const res = await readiness(email, CONSENT)
    expect(res.status, JSON.stringify(res.body)).toBe(202)

    let row: ContactMe | null = null
    await expect
      .poll(
        async () => {
          row = await me(email)
          return row?.hubspot.delivered_at != null
        },
        POLL,
      )
      .toBe(true)
    expect(row!.tags).toEqual([TAG])
    expect(row!.marketing_eligible).toBe(false)
    expect(row!.resend.applies).toBe(false)
    expect(row!.resend.delivered_at).toBeNull()
    expect(row!.mode).toBe(expectedContactsMode())
  })

  test('a readiness check without consent is refused and stores nothing', async () => {
    const email = freshEmail()
    const res = await readiness(email)
    expect(res.status).toBe(400)
    expect(res.body).toEqual({ error: CONSENT_REQUIRED })
    expect(await me(email)).toBeNull()
  })

  test('nightly: the contact and its readiness-check deal reach the test HubSpot portal', async () => {
    test.skip(!nightlyTarget(), 'the test HubSpot portal is reached on the nightly only')
    const version = process.env.NIGHTLY_HUBSPOT_TOKEN_VERSION ?? ''
    expect(/^\d+$/.test(version), 'NIGHTLY_HUBSPOT_TOKEN_VERSION must be digits, never latest').toBe(true)
    // Memory only: never log or write the token.
    const token = previewSecret('hubspot-token', version).trim()
    const hs = async (path: string): Promise<{ status: number; body: any }> => {
      const res = await fetch(`https://api.hubapi.com${path}`, { headers: { Authorization: `Bearer ${token}` } })
      return { status: res.status, body: await res.json().catch(() => undefined) }
    }

    const email = freshEmail()
    const res = await readiness(email, CONSENT)
    expect(res.status, JSON.stringify(res.body)).toBe(202)

    let contactId = ''
    await expect
      .poll(
        async () => {
          const c = await hs(`/crm/v3/objects/contacts/${encodeURIComponent(email)}?idProperty=email&properties=ascomply_contact_tags`)
          if (c.status !== 200) return [`HubSpot answered ${c.status}`]
          contactId = String(c.body.id)
          return String(c.body.properties?.ascomply_contact_tags ?? '').split(';')
        },
        { ...POLL, message: `is option ${HS_OPTION} on ascomply_contact_tags in the test portal?` },
      )
      .toContain(HS_OPTION)

    await expect
      .poll(
        async () => {
          const assoc = await hs(`/crm/v4/objects/contacts/${contactId}/associations/deals`)
          if (assoc.status !== 200) return [`HubSpot answered ${assoc.status}`]
          const ids = (assoc.body.results as { toObjectId: number | string }[]).map((r) => String(r.toObjectId))
          return Promise.all(ids.map(async (id) => String((await hs(`/crm/v3/objects/deals/${id}?properties=dealname`)).body?.properties?.dealname)))
        },
        POLL,
      )
      .toEqual([DEAL_NAME])
  })
})
