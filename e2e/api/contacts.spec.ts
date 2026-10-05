// Contact sync over the deployed fork: a pr-<N> fork's notifications runs in fake mode, so
// nothing reaches HubSpot or Resend. Every run uses fresh addresses; contacts rows survive the reset.
import { test, expect } from '@playwright/test'
import { apiBase, claimsOf, contactsMe, demoRequest, rawFetch, registerFormAccount, type ContactMe } from './client'

const CONSENT = 'Yes, send me product news and offers.'
const POLL = { timeout: 30_000, intervals: [500, 1000, 2000] }

// The tags contacts/me returns (internal/notifications/store.go).
const REGISTERED = 'registered'
const DEMO_REQUEST = 'demo request'

function demo(email: string, extra: Record<string, unknown> = {}) {
  return { email, name: 'Contacts E2E', company: 'Contacts E2E Ltd', ...extra }
}

// Waits until both destinations have a delivery time and returns the row.
async function delivered(token: string): Promise<ContactMe> {
  let row: ContactMe | null = null
  await expect
    .poll(
      async () => {
        row = await contactsMe(token)
        return row !== null && row.hubspot.delivered_at !== null && row.resend.delivered_at !== null
      },
      POLL,
    )
    .toBe(true)
  return row!
}

test.describe('contact sync (API E2E, over the deployed gateway)', () => {
  test('/healthz/fleet reports notifications contacts "fake" on the fork', async () => {
    const res = await fetch(`${apiBase()}/healthz/fleet`)
    const body = (await res.json()) as { services: { name: string; contacts?: string }[] }
    expect(body.services.length).toBeGreaterThan(0)
    const notifications = body.services.find((s) => s.name === 'notifications')
    expect(notifications, JSON.stringify(body)).toBeDefined()
    expect(notifications!.contacts).toBe('fake')
  })

  test('a form registrant who signs in is delivered, tagged registered and not marketing-eligible', async () => {
    const { account, token } = await registerFormAccount('contacts')
    // contacts/me reads the row by the token's email claim; without it the read 404s for good.
    expect(claimsOf(token).email).toBe(account.email)
    const row = await delivered(token)
    expect(row.email).toBe(account.email)
    expect(row.tags).toEqual([REGISTERED])
    expect(row.marketing_eligible).toBe(false)
    expect(row.hubspot.delivered_at).not.toBeNull()
    expect(row.resend.delivered_at).not.toBeNull()
    expect(row.mode).toBe('fake')
  })

  test('a registrant who ticked at register is marketing-eligible', async () => {
    const { token } = await registerFormAccount('contacts-ticked', { marketingConsentText: CONSENT })
    const row = await delivered(token)
    expect(row.tags).toEqual([REGISTERED])
    expect(row.marketing_eligible).toBe(true)
  })

  test('a demo request with the same email in other case joins the contact; a tick sticks', async () => {
    test.setTimeout(120_000)
    const { account, token } = await registerFormAccount('contacts-demo')
    const first = await delivered(token)
    expect(first.marketing_eligible).toBe(false)

    const upper = account.email.toUpperCase()
    expect(upper).not.toBe(account.email)

    await test.step('an unticked demo request adds the tag only', async () => {
      const res = await demoRequest(demo(upper))
      expect(res.status, JSON.stringify(res.body)).toBe(202)
      await expect.poll(async () => (await contactsMe(token))?.tags, POLL).toEqual([REGISTERED, DEMO_REQUEST])
      expect((await contactsMe(token))!.marketing_eligible).toBe(false)
    })

    await test.step('a ticked demo request makes the contact eligible', async () => {
      const res = await demoRequest(demo(upper, { marketing_consent_text: CONSENT }))
      expect(res.status, JSON.stringify(res.body)).toBe(202)
      await expect.poll(async () => (await contactsMe(token))?.marketing_eligible, POLL).toBe(true)
    })

    await test.step('a later unticked one leaves it eligible', async () => {
      const res = await demoRequest(demo(upper))
      expect(res.status, JSON.stringify(res.body)).toBe(202)
      const row = await delivered(token)
      expect(row.marketing_eligible).toBe(true)
      expect(row.tags).toEqual([REGISTERED, DEMO_REQUEST])
    })
  })

  test('a demo request with an invalid field is 400 and a valid one for a fresh address is 202', async () => {
    const fresh = `contacts-demo-${crypto.randomUUID()}@example.com`
    // internal/gateway/contacts.go validateDemoRequest: each body trips its own clause.
    const invalid = [
      { body: demo('not-an-email'), error: 'email is invalid' },
      { body: { email: fresh, name: '', company: 'Contacts E2E Ltd' }, error: 'name must be 1 to 200 characters' },
      { body: demo(fresh, { marketing_consent_text: '   ' }), error: 'marketing_consent_text must be 1 to 500 characters' },
    ]
    expect(invalid.length).toBeGreaterThan(0)
    for (const { body, error } of invalid) {
      const res = await demoRequest(body)
      expect(res.status, JSON.stringify(body)).toBe(400)
      expect(res.body, JSON.stringify(body)).toEqual({ error })
    }
    const ok = await demoRequest(demo(fresh))
    expect(ok.status, JSON.stringify(ok.body)).toBe(202)
  })

  test('the internal registrants path answers 404 to a tenant token', async () => {
    const { account, token } = await registerFormAccount('contacts-internal')
    const res = await rawFetch('/api/notifications/internal/contacts/registrants', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { email: account.email },
    })
    expect(res.status, JSON.stringify(res.body)).toBe(404)
  })
})
