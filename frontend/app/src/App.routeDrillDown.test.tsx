// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// ROUTE-02-05: a nonexistent id and a cross-tenant id must render the SAME 404 surface --
// the backend is already indistinguishable (RLS + statusForErr, see task-918); this file
// proves the frontend doesn't reintroduce a leak on top of that. No production change is
// expected; this is a test-only subtask.

import { StrictMode } from 'react'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'

const ABSENT_INVOICE_ID = 'a0000000-0000-4000-8000-000000000001'
const TENANT_B_INVOICE_ID = 'b0000000-0000-4000-8000-000000000002'
const MALFORMED_INVOICE_ID = 'not-a-uuid'
const ABSENT_JOB_ID = 'c0000000-0000-4000-8000-000000000003'
const TENANT_B_JOB_ID = 'd0000000-0000-4000-8000-000000000004'

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string, opts: { strict?: boolean } = {}) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(opts.strict ? (
    <StrictMode>
      <App />
    </StrictMode>
  ) : (
    <App />
  ))
}

type StubResponse = { ok: boolean; status: number; json: () => Promise<unknown> }

// URL-substring dispatch (App.auditPrefilter.test.tsx's routeFetch idiom): the invoice/
// extraction endpoint under test gets `resp`, everything else (list/dashboard bootstrap
// calls this story doesn't care about) gets a bland 200 empty payload.
function stubFetch(matchSubstring: string, resp: StubResponse | (() => Promise<never>)) {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes(matchSubstring)) {
        return typeof resp === 'function' ? resp() : Promise.resolve(resp)
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
      })
    }),
  )
}

const notFound404: StubResponse = { ok: false, status: 404, json: () => Promise.resolve({ error: 'not found' }) }
const malformed400: StubResponse = { ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid invoice id' }) }
const neverResolves = () => new Promise<never>(() => {})

describe('E-1/E-2: invoice not-found and cross-tenant render identical text', () => {
  it('drillDown_notFoundAndCrossTenantInvoiceRenderIdenticalText', async () => {
    stubFetch('/api/invoice/v1/invoices/', notFound404)
    await bootAt('/invoices/' + ABSENT_INVOICE_ID)
    await waitFor(() => screen.getByText('not found'))
    const absentText = document.body.textContent ?? ''
    cleanup()

    stubFetch('/api/invoice/v1/invoices/', notFound404)
    await bootAt('/invoices/' + TENANT_B_INVOICE_ID)
    await waitFor(() => screen.getByText('not found'))
    const tenantBText = document.body.textContent ?? ''

    expect(absentText, 'absent-id and cross-tenant-id 404 surfaces must be byte-identical').toBe(tenantBText)
    expect(absentText).toContain('not found')
    expect(absentText).toContain('HTTP 404')
    expect(absentText, 'the 404 surface must not name the absent id').not.toContain(ABSENT_INVOICE_ID)
    expect(absentText, 'the 404 surface must not name the cross-tenant id').not.toContain(TENANT_B_INVOICE_ID)
  })
})

describe('E-3: same equality for extraction', () => {
  it('drillDown_notFoundIsIdenticalForExtractionToo', async () => {
    stubFetch('/api/submission/v1/extractions/', notFound404)
    await bootAt('/extraction/' + ABSENT_JOB_ID)
    await waitFor(() => screen.getByText('not found'))
    const absentText = screen.getByTestId('extraction-review').textContent ?? ''
    cleanup()

    stubFetch('/api/submission/v1/extractions/', notFound404)
    await bootAt('/extraction/' + TENANT_B_JOB_ID)
    await waitFor(() => screen.getByText('not found'))
    const tenantBText = screen.getByTestId('extraction-review').textContent ?? ''

    expect(absentText, 'absent-job and cross-tenant-job 404 surfaces must be byte-identical').toBe(tenantBText)
    expect(absentText).toContain('not found')
    expect(absentText).toContain('HTTP 404')
    expect(absentText).not.toContain(ABSENT_JOB_ID)
    expect(absentText).not.toContain(TENANT_B_JOB_ID)
  })
})

describe('E-4: malformed id settles into an honest ErrorState', () => {
  it('drillDown_malformedIdRendersErrorStateAndSettles', async () => {
    stubFetch('/api/invoice/v1/invoices/', malformed400)
    await bootAt('/invoices/' + MALFORMED_INVOICE_ID)
    await waitFor(() => screen.getByText('HTTP 400'))

    expect(screen.queryByText('Loading invoice…'), 'no invoice spinner should survive settling').toBeNull()
    expect(document.querySelector('.apic-loading-spin'), 'no spinner node should survive settling').toBeNull()
  })
})

describe('E-5: positive control -- a pending fetch mounts Loading', () => {
  it('drillDown_pendingFetchRendersLoadingInvoice', async () => {
    stubFetch('/api/invoice/v1/invoices/', neverResolves)
    await bootAt('/invoices/' + ABSENT_INVOICE_ID)
    expect(screen.getByText('Loading invoice…')).toBeTruthy()
  })

  it('drillDown_pendingFetchRendersLoadingExtraction', async () => {
    stubFetch('/api/submission/v1/extractions/', neverResolves)
    await bootAt('/extraction/' + ABSENT_JOB_ID)
    expect(document.querySelector('.apic-loading-spin')).toBeTruthy()
  })
})

describe('E-6: a malformed URL segment boots the dashboard, not a crash', () => {
  it('drillDown_malformedUrlSegmentBootsDashboardNoUncaughtError', async () => {
    const onError = vi.fn()
    window.addEventListener('error', onError)
    try {
      stubFetch('/api/invoice/v1/invoices/', notFound404)
      await bootAt('/invoices/%zz')
      expect(capturedCtx?.view, 'an unparseable path must fall back to the dashboard').toBe('dashboard')
      expect(onError, 'booting an unparseable path must not raise an uncaught error').not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('error', onError)
    }
  })
})
