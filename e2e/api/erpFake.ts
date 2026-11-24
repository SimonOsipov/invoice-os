// The fake ERP's connect step for the API specs: create the pending fake connection, then complete
// its consent through the gateway's /erp/callback with the company code, as the consent page does.
import { expect } from '@playwright/test'
import { rawFetch, apiBase } from './client'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go

export async function connectFake(token: string, entityId: string, key: string): Promise<string> {
  const res = await rawFetch(`/api/portfolio/v1/entities/${entityId}/erp-connections`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: { system: FAKE },
  })
  expect(res.status).toBe(201)
  const conn = res.body as { id: string; consent_url: string }
  const state = new URL(conn.consent_url).searchParams.get('state') ?? ''
  const done = await fetch(`${apiBase()}/erp/callback?${new URLSearchParams({ state, code: key })}`, { redirect: 'manual' })
  expect(done.status).toBe(200)
  return conn.id
}
