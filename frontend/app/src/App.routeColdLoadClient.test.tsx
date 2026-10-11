// @vitest-environment jsdom
// A cold load of a review path must adopt the batch's client. Real <App/>, Sidebar and
// ReviewBatch; only fetch is stubbed. Harness: App.routeReviewHash.test.tsx.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const BATCH_ID = 'bbbbbbbb-1111-4111-8111-111111111111'
const ENTITY_FIRST = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_SECOND = 'aaaaaaaa-0000-4000-8000-000000000002'
const ENTITY_UNKNOWN = 'aaaaaaaa-0000-4000-8000-0000000000ff'
const BATCH_ID_2 = 'bbbbbbbb-2222-4222-8222-222222222222'

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
type FetchOpts = { batchEntities?: Record<string, string>; portfolioGate?: Promise<void> }

function routeFetch(opts: FetchOpts = {}) {
  const batchEntities = opts.batchEntities ?? { [BATCH_ID]: ENTITY_SECOND }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/portfolio/v1/entities')) {
        const body = {
          entities: [entityRow(ENTITY_FIRST, 'Alpha Ltd', '11111111-0001'), entityRow(ENTITY_SECOND, 'Zulu Ltd', '22222222-0001')],
          pagination: { limit: 200, offset: 0, total: 2 },
        }
        return opts.portfolioGate ? opts.portfolioGate.then(() => ok(body)) : ok(body)
      }
      const batchId = Object.keys(batchEntities).find((id) => url.includes(`/imports/${id}`))
      if (batchId) {
        return ok({
          id: batchId,
          entity_id: batchEntities[batchId],
          filename: 'june.csv',
          document_id: null,
          status: 'completed',
          rows_total: 0,
          rows_valid: 0,
          rows_invalid: 0,
          errors: [],
          rule_set_version: null,
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

async function coldLoadReview(opts: FetchOpts = {}, path = `/imports/${BATCH_ID}/review`) {
  routeFetch(opts)
  window.history.replaceState(null, '', path)
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

  it('coldLoad_aBatchOfAnUnknownEntityFallsBackAndStampsNoForeignId', async () => {
    await coldLoadReview({ batchEntities: { [BATCH_ID]: ENTITY_UNKNOWN } })
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    expect(screen.getByTestId('company-switcher').textContent).toContain('Alpha')
    expect(window.history.state?.e).toBe(ENTITY_FIRST)
  })

  it('coldLoad_aLatePortfolioStillAdoptsTheBatchsClient', async () => {
    let release!: () => void
    const portfolioGate = new Promise<void>((r) => (release = r))
    routeFetch({ portfolioGate })
    window.history.replaceState(null, '', `/imports/${BATCH_ID}/review`)
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.resetModules()
    const { default: App } = await import('./App')
    render(<App />)
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    await act(async () => release())
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
    await waitFor(() => expect(window.history.state?.e).toBe(ENTITY_SECOND))
  })

  it('coldLoad_aMultiBatchRunAdoptsTheFirstBatchsClient', async () => {
    await coldLoadReview(
      { batchEntities: { [BATCH_ID]: ENTITY_SECOND, [BATCH_ID_2]: ENTITY_FIRST } },
      `/imports/${BATCH_ID},${BATCH_ID_2}/review`,
    )
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
    expect(window.location.pathname).toBe(`/imports/${BATCH_ID},${BATCH_ID_2}/review`)
  })

  it('coldLoad_stampsHistoryOnceAndNeverRestampsOnLaterRenders', async () => {
    const spy = vi.spyOn(window.history, 'replaceState')
    await coldLoadReview()
    await waitFor(() => expect(window.history.state?.e).toBe(ENTITY_SECOND))
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    const stamps = () => spy.mock.calls.filter(([s]) => (s as { e?: string } | null)?.e === ENTITY_SECOND).length
    const settled = stamps()
    expect(settled).toBe(1)
    fireEvent.click(screen.getByTestId('company-switcher'))
    fireEvent.click(screen.getByTestId('company-switcher'))
    await act(async () => {})
    expect(stamps()).toBe(settled)
    expect(window.history.state?.e).toBe(ENTITY_SECOND)
  })

  it('pickedClient_isNotOverriddenByABatchOfAnotherClient', async () => {
    await coldLoadReview({}, '/dashboard')
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Alpha'))
    fireEvent.click(screen.getByTestId('company-switcher'))
    const options = await screen.findAllByTestId('company-switcher-option')
    fireEvent.click(options[0])
    await waitFor(() => expect(window.history.state?.e).toBe(ENTITY_FIRST))
    window.history.pushState({ e: ENTITY_FIRST }, '', `/imports/${BATCH_ID}/review`)
    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate', { state: { e: ENTITY_FIRST } }))
    })
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    expect(screen.getByTestId('company-switcher').textContent).toContain('Alpha')
    expect(window.history.state?.e).toBe(ENTITY_FIRST)
  })

  it('popstate_intoAnUnstampedReviewEntryAdoptsTheBatchsClientAndStaysOnReview', async () => {
    await coldLoadReview({}, '/dashboard')
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Alpha'))
    window.history.pushState(null, '', `/imports/${BATCH_ID}/review`)
    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate', { state: null }))
    })
    await waitFor(() => expect(screen.getByTestId('review-table')).toBeTruthy())
    expect(window.location.pathname).toBe(`/imports/${BATCH_ID}/review`)
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
  })

  it('coldLoad_aPreLoadEntryResolvesToTheAdoptedCompany', async () => {
    let release!: () => void
    const portfolioGate = new Promise<void>((r) => (release = r))
    await coldLoadReview({ portfolioGate })
    const traverse = async (delta: number) => {
      await act(async () => {
        const popped = new Promise<void>((r) => window.addEventListener('popstate', () => r(), { once: true }))
        window.history.go(delta)
        await popped
      })
    }
    const invoicesNav = [...document.querySelectorAll('button.pf-nav')].find((b) => b.textContent?.trim().endsWith('Invoices'))
    expect(invoicesNav, 'the Invoices nav button').toBeTruthy()
    await act(async () => (invoicesNav as HTMLButtonElement).click())
    await traverse(-1)
    expect(window.location.pathname).toBe(`/imports/${BATCH_ID}/review`)
    expect(window.history.state?.e, 'floor: no company resolved yet').toBeNull()
    await act(async () => release())
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await traverse(1)
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(window.location.pathname).toBe('/invoices')
    expect(window.history.state?.e).toBeNull()
  })
  const traverseBy = async (delta: number) => {
    await act(async () => {
      const popped = new Promise<void>((r) => window.addEventListener('popstate', () => r(), { once: true }))
      window.history.go(delta)
      await popped
    })
  }
  const goInvoices = async () => {
    const nav = [...document.querySelectorAll('button.pf-nav')].find((b) => b.textContent?.trim().endsWith('Invoices'))
    expect(nav, 'the Invoices nav button').toBeTruthy()
    await act(async () => (nav as HTMLButtonElement).click())
  }

  // KNOWN DEFECT (QA-2 F1): the review mirror re-stamps the boot entry {e: fallback, m} before adoption, so it is not a window entry and the ref stays on the fallback. Flip to `it` with the fix.
  it.fails('coldLoad_aBackfilledEntryAndAReviewAdoptionShareOneWindow', async () => {
    let release!: () => void
    const portfolioGate = new Promise<void>((r) => (release = r))
    await coldLoadReview({ portfolioGate })
    await goInvoices()
    await act(async () => release())
    await waitFor(() => expect(window.history.state?.p, 'floor: the backfill stamped the Invoices entry').toBe(true))
    await traverseBy(-1)
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await traverseBy(1)
    expect(replaceSpy, 'the backfilled entry resolves to the adopted company: no clamp').not.toHaveBeenCalled()
    expect(window.location.pathname).toBe('/invoices')
  })

  it('coldLoad_anAdoptionFromAnEntryOutsideTheWindowNeverRepointsTheBootEntry', async () => {
    let release!: () => void
    const portfolioGate = new Promise<void>((r) => (release = r))
    await coldLoadReview({ portfolioGate }, '/invoices?q=a')
    await act(async () => release())
    await waitFor(() => expect(window.history.state?.p, 'floor: the backfill stamped the boot entry').toBe(true))
    window.history.pushState({ scroll: 0 }, '', `/imports/${BATCH_ID}/review`)
    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate', { state: { scroll: 0 } }))
    })
    await waitFor(() => expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu'))
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await traverseBy(-1)
    expect(replaceSpy, 'the boot entry names Alpha, the active company is Zulu: clamp').toHaveBeenCalledWith(
      { e: ENTITY_SECOND },
      '',
      '/invoices',
    )
  })
})
