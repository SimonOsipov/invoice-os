// A signed fake webhook is accepted and an unsigned one refused, over the deployed gateway (the preview runs the fake).
// Status codes only; the connected case acts on a client this run created.
import { createHmac } from 'node:crypto'
import { test, expect } from '@playwright/test'
import { login, createEntity, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { connectFake } from './erpFake'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go
const FAKE_KEY = 'fake-co-1' // fakeSeed key in internal/erp/fake_seed.go
const FAKE_WEBHOOK_KEY = 'erpf-fake-webhook-key' // fakeWebhookKey in internal/erp/fake_webhook.go
const SIGNATURE_HEADER = 'X-Fake-Signature' // fakeSignatureHeader in internal/erp/fake_webhook.go
const MAX_BODY = 64 << 10 // erpWebhookMaxBody in internal/gateway/erp_webhook.go

const sign = (body: string) => 'sha256=' + createHmac('sha256', FAKE_WEBHOOK_KEY).update(body).digest('hex') // FakeWebhookSignature
const event = (company: string) => JSON.stringify({ events: [{ company, invoice: 'FK1-0001' }] })
const post = (system: string, body: string, signature?: string) =>
  fetch(`${apiBase()}/erp/webhooks/${system}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(signature ? { [SIGNATURE_HEADER]: signature } : {}),
    },
    body,
  })

test.describe('ERP webhooks (API E2E, over the deployed gateway)', () => {
  test('an unsigned POST and a wrong signature answer 401', async () => {
    const body = event(FAKE_KEY)
    expect((await post(FAKE, body)).status).toBe(401)
    expect((await post(FAKE, body, 'sha256=' + '0'.repeat(64))).status).toBe(401)
  })

  test('a signed POST naming an unknown company answers 200', async () => {
    const body = event('no-such-company')
    expect((await post(FAKE, body, sign(body))).status).toBe(200)
  })

  test('a signed POST naming a company connected by this run answers 200', async () => {
    const admin = await login(PERSONAS.A)
    const tin = freshTin()
    const entity = await createEntity(admin, {
      name: `ERP webhook ${tin}`,
      tin,
    })
    await connectFake(admin, entity.id, FAKE_KEY)
    const body = event(FAKE_KEY)
    expect((await post(FAKE, body, sign(body))).status).toBe(200)
  })

  test('an oversized body answers 413, another vendor 404, a GET 405', async () => {
    const big = JSON.stringify({ events: [], pad: 'x'.repeat(MAX_BODY) })
    expect((await post(FAKE, big, sign(big))).status).toBe(413)
    const body = event(FAKE_KEY)
    expect((await post('quickbooks', body, sign(body))).status).toBe(404)
    expect((await fetch(`${apiBase()}/erp/webhooks/fake`)).status).toBe(405)
  })
})
