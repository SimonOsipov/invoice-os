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

  it('coldLoad_aBackfilledEntryAndAReviewAdoptionShareOneWindow', async () => {
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

// The overview answers per client, and the client is chosen by the real App: a stub that answers
// every client alike (as the drill-down suite's does) could not tell the default from the owner.
describe('cold load of /settings/connectors/<id>', () => {
  const CONN_ID = 'cccccccc-3333-4333-8333-333333333333'
  const conn = { id: CONN_ID, entity_id: ENTITY_SECOND, system: 'fake', external_key: null, status: 'active', last_sync_at: null, created_at: '', updated_at: '' }
  const missing = () => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'not found' }) })

  type ListOpts = { listGate?: Promise<void>; listFails?: boolean; failList?: string }
  function routeConnectorFetch(ownerEntity: string | null, o: ListOpts = {}) {
    const base = routeFetchImpl()
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        const m = /entities\/([^/]+)\/erp-connections(\/[^/]+\/overview)?$/.exec(url)
        if (m && m[2]) {
          return m[1] === ownerEntity
            ? ok({ connection: conn, activity: { since: '', counts: { pulled: 0, pending: 0, drafted: 0, linked: 0, held: 0, failed: 0, deleted: 0 }, documents: [] }, writebacks: [], held: [] })
            : missing()
        }
        if (m) {
          if (o.listFails || m[1] === o.failList) return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
          const list = ok({ connections: m[1] === ownerEntity ? [conn] : [], systems: [] })
          return o.listGate ? o.listGate.then(() => list) : list
        }
        // The catch-all body has no `fields`; the real mapping read always carries it.
        if (/\/erp-mappings\//.test(url)) return ok({ fields: [{ target: 'invoice_no', label: 'Invoice no', source_path: 'DocNum', origin: 'platform' }] })
        if (/\/erp-lookups\//.test(url)) return ok({ lookups: [] })
        return base(url)
      }),
    )
  }
  function routeFetchImpl() {
    routeFetch()
    return (globalThis.fetch as unknown as (u: string) => Promise<unknown>).bind(globalThis)
  }

  async function boot(state: unknown = null) {
    window.history.replaceState(state, '', `/settings/connectors/${CONN_ID}`)
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.resetModules()
    const { default: App } = await import('./App')
    render(<App />)
  }

  it('coldLoad_aConnectionOfANonDefaultClientOpensUnderItsOwnClient', async () => {
    routeConnectorFetch(ENTITY_SECOND)
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    // The mapping read lands in every run, not only a loaded one: the test no longer races it.
    await waitFor(() => expect(screen.getAllByTestId('erp-mapping-row')).toHaveLength(1))
    expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu')
    expect(screen.queryByTestId('erp-detail-not-found')).toBeNull()
    expect(window.location.pathname).toBe(`/settings/connectors/${CONN_ID}`)
  })

  const urls = () => (fetch as unknown as { mock: { calls: string[][] } }).mock.calls.map((c) => c[0])
  const overviewReads = (entity: string) => urls().filter((u) => u.includes(`entities/${entity}/erp-connections/${CONN_ID}/overview`))
  const listReads = () => urls().filter((u) => /entities\/[^/]+\/erp-connections$/.test(u))

  // A reload keeps the stamp {e, m} of the entry: the boot starts on that client, so no search and no read under the default.
  it('coldLoad_aReloadOfTheDetailKeepsItUnderItsClient', async () => {
    routeConnectorFetch(ENTITY_SECOND)
    await boot({ e: ENTITY_SECOND, m: 'an-earlier-mount' })
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu')
    expect(window.location.pathname).toBe(`/settings/connectors/${CONN_ID}`)
    expect(overviewReads(ENTITY_FIRST), 'no overview read under the default client').toHaveLength(0)
    expect(listReads(), 'no search').toHaveLength(0)
    expect(overviewReads(ENTITY_SECOND)).toHaveLength(1)
    await waitFor(() => expect(window.history.state?.e).toBe(ENTITY_SECOND))
  })

  it('coldLoad_aLinkWithNoStampSearchesThenReadsOnceUnderTheOwner', async () => {
    routeConnectorFetch(ENTITY_SECOND)
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    expect(listReads().length).toBeGreaterThan(0)
    expect(overviewReads(ENTITY_SECOND)).toHaveLength(1)
    expect(overviewReads(ENTITY_FIRST), 'no overview read, so no 404, under the default client').toHaveLength(0)
  })

  it('coldLoad_aStampNamingAClientTheMemberCannotSeeIsIgnored', async () => {
    routeConnectorFetch(ENTITY_SECOND)
    await boot({ e: ENTITY_UNKNOWN, m: 'an-earlier-mount' })
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu')
    expect(overviewReads(ENTITY_UNKNOWN)).toHaveLength(0)
    expect(overviewReads(ENTITY_FIRST)).toHaveLength(0)
    expect(listReads().length).toBeGreaterThan(0)
    expect(urls().filter((u) => u.includes(ENTITY_UNKNOWN)), 'no request names a client outside the portfolio').toHaveLength(0)
  })

  it('coldLoad_theOwnerLoadsWithoutAFrameOfNotInTheClient', async () => {
    routeConnectorFetch(ENTITY_SECOND)
    let flashed = false
    const watch = new MutationObserver((records) => {
      for (const r of records)
        for (const n of r.addedNodes)
          if (n instanceof Element && (n.matches('[data-testid="erp-detail-not-found"]') || n.querySelector('[data-testid="erp-detail-not-found"]'))) flashed = true
    })
    watch.observe(document.body, { childList: true, subtree: true })
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    watch.disconnect()
    expect(flashed, 'a committed "This connection is not in ..." between the adoption and the owner overview').toBe(false)
  })

  it('coldLoad_theNotInSentenceWaitsForTheSearch', async () => {
    let release!: () => void
    const listGate = new Promise<void>((r) => (release = r))
    routeConnectorFetch(null, { listGate })
    await boot()
    await waitFor(() => expect((fetch as unknown as { mock: { calls: string[][] } }).mock.calls.some((c) => /entities\/[^/]+\/erp-connections$/.test(c[0])), 'the search read a list').toBe(true))
    expect(screen.queryByTestId('erp-detail-not-found')).toBeNull()
    await act(async () => release())
    await waitFor(() => expect(screen.getByTestId('erp-detail-not-found')).toBeTruthy())
  })

  it('coldLoad_aListThatFailsEndsTheSearchAsNotFound', async () => {
    routeConnectorFetch(ENTITY_SECOND, { listFails: true })
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-not-found').textContent).toContain('Alpha'))
  })

  it('coldLoad_aListThatFailsForOneClientDoesNotHideTheOwner', async () => {
    routeConnectorFetch(ENTITY_SECOND, { failList: ENTITY_FIRST })
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    expect(screen.getByTestId('company-switcher').textContent).toContain('Zulu')
  })

  it('coldLoad_leavingTheLinkBeforeTheSearchEndsAdoptsNothing', async () => {
    let release!: () => void
    const listGate = new Promise<void>((r) => (release = r))
    routeConnectorFetch(ENTITY_SECOND, { listGate })
    await boot()
    await waitFor(() => expect(listReads().length, 'the search read a list').toBeGreaterThan(0))
    fireEvent.click(screen.getByTestId('erp-detail-back'))
    await act(async () => {
      release()
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(screen.getByTestId('company-switcher').textContent).toContain('Alpha')
    expect(screen.queryByTestId('erp-detail-header')).toBeNull()
    expect(window.history.state?.e ?? null, 'no adoption stamped the owner').not.toBe(ENTITY_SECOND)
  })

  it('coldLoad_aConnectionOfNoClientStaysNotFound', async () => {
    routeConnectorFetch(null)
    await boot()
    await waitFor(() => expect(screen.getByTestId('erp-detail-not-found').textContent).toContain('Alpha'))
  })
})
