// @vitest-environment jsdom
// A cold load of a review path must adopt the batch's client. Real <App/>, Sidebar and
// ReviewBatch; only fetch is stubbed. Harness: App.routeReviewHash.test.tsx.

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const BATCH_ID = 'bbbbbbbb-1111-4111-8111-111111111111'
const ENTITY_FIRST = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_SECOND = 'aaaaaaaa-0000-4000-8000-000000000002'

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => void store.set(key, value)),
    removeItem: vi.fn((key: string) => void store.delete(key)),
    clear: vi.fn(() => store.clear()),
  }
}

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

// The batch belongs to the SECOND client by name; the portfolio sorts "Alpha" first.
function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/portfolio/v1/entities')) {
        return ok({
          entities: [entityRow(ENTITY_FIRST, 'Alpha Ltd', '11111111-0001'), entityRow(ENTITY_SECOND, 'Zulu Ltd', '22222222-0001')],
          pagination: { limit: 200, offset: 0, total: 2 },
        })
      }
      if (url.includes(`/imports/${BATCH_ID}`)) {
        return ok({
          id: BATCH_ID,
          entity_id: ENTITY_SECOND,
          filename: 'june.csv',
          document_id: null,
          status: 'completed',
          rows_total: 0,
          rows_valid: 0,
          rows_invalid: 0,
          errors: [],
          rule_set_version: 1,
          created_at: '2026-08-01T00:00:00Z',
        })
      }
      if (url.includes('/dashboard/v1/rollup')) return ok({ totals: {}, clients: [], top_violations: [] })
      if (url.includes('/violation-summary')) return ok({ rules: [] })
      if (url.includes('/api/invoice/v1/invoices')) {
        return ok({ invoices: [], pagination: { limit: 1, offset: 0, total: 0 } })
      }
      return ok({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 })
    }),
  )
}

beforeEach(() => {
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function coldLoadReview() {
  routeFetch()
  window.history.replaceState(null, '', `/imports/${BATCH_ID}/review`)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  render(<App />)
  // Both clients loaded: the switcher is the only place the active client shows.
  await waitFor(() => expect(screen.getByTestId('company-switcher')).toBeTruthy())
}

describe('cold load of /imports/<id>/review', () => {
  it('coldLoad_sidebarShowsTheBatchsClientNotTheFirst', async () => {
    await coldLoadReview()
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
  })

  it('coldLoad_historyStateCarriesTheBatchsEntity', async () => {
    await coldLoadReview()
    await waitFor(() => expect(window.history.state?.e).toBe(ENTITY_SECOND))
  })

  it('coldLoad_staysOnTheReviewPath', async () => {
    await coldLoadReview()
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    expect(window.location.pathname).toBe(`/imports/${BATCH_ID}/review`)
  })
})
