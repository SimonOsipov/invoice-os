// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { fmtDateTime } from '../lib/format'
import {
  CONNECTORS_ROLE_FAILED,
  type ErpConnectionWire,
  type ErpListing,
} from '../lib/erpConnections'
import type { PlatformCtx } from '../types'
import { ConnectorsView } from './ConnectorsView'

const BASE = 'https://gw.test/api/portfolio/v1/entities'
const CONSENT = 'https://gw/erp/fake/consent?state=s'
const ADMIN_ONLY = 'Only an admin can connect or disconnect an ERP.'

const conn = (o: Partial<ErpConnectionWire> = {}): ErpConnectionWire => ({
  id: 'c1', entity_id: 'e1', system: 'fake', external_key: 'k', status: 'active', last_sync_at: null, created_at: '', updated_at: '', ...o,
})
const listing = (connections: ErpConnectionWire[] = [], systems: string[] = ['fake']): ErpListing => ({ connections, systems })
const me = (role = 'admin', status = 'active') => [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status, isYou: true }]

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((a, b) => {
    resolve = a
    reject = b
  })
  return { promise, resolve, reject }
}

type Init = { method?: string; body?: unknown }
function mkFetch(o: { get?: (url: string) => unknown; post?: (url: string, body: unknown) => unknown }) {
  return vi.fn(async (url: string, init?: Init) => {
    if (init?.method === 'POST') {
      if (!o.post) throw new Error('unexpected POST')
      return o.post(url, init.body)
    }
    return o.get!(url)
  })
}
type F = ReturnType<typeof mkFetch>
const gets = (f: F, entity?: string) => f.mock.calls.filter((c) => c[1] === undefined && (!entity || c[0].includes('/' + entity + '/')))
const posts = (f: F) => f.mock.calls.filter((c) => c[1]?.method === 'POST')

function mkCtx(f: F, over: Record<string, unknown> = {}, entity: string | null = 'e1'): PlatformCtx {
  return {
    activeEntity: entity ? { id: entity } : null,
    members: me(),
    membersState: 'ready',
    refetchMembers: vi.fn(),
    authedFetch: f,
    ...over,
  } as unknown as PlatformCtx
}

const mkTab = () => ({ opener: {} as unknown, location: { href: '' }, close: vi.fn() })
const spyOpen = (tab: ReturnType<typeof mkTab> | null = mkTab()) => ({ tab, open: vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window) })
const rows = () => screen.queryAllByTestId('erp-connector-row')
const rowOf = (system: string) => rows().find((r) => r.dataset.system === system)!
const connectBtn = (r: HTMLElement) => within(r).queryByTestId('erp-connector-connect')
const disconnectBtn = (r: HTMLElement) => within(r).queryByTestId('erp-connector-disconnect')
const none = () => {
  expect(screen.queryAllByTestId('erp-connector-connect')).toHaveLength(0)
  expect(screen.queryAllByTestId('erp-connector-disconnect')).toHaveLength(0)
}
const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const click = (el: HTMLElement) => act(async () => { fireEvent.click(el) })

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('rows', () => {
  it('connectorsTab_noMockStringsRemain', async () => {
    render(<ConnectorsView ctx={mkCtx(mkFetch({ get: () => listing([], ['fake']) }))} />)
    expect(await screen.findByText('Fake ERP')).toBeTruthy()
    expect(screen.getByText('NOT CONNECTED')).toBeTruthy()
    expect(screen.queryByText(/\/ 6 CONNECTED/)).toBeNull()
    expect(screen.queryByText('Synced 2 min ago')).toBeNull()
    for (const n of ['SAP S/4HANA', 'Oracle NetSuite', 'QuickBooks Online', 'Sage 300', 'Odoo', 'Microsoft Dynamics 365']) expect(screen.queryByText(n)).toBeNull()
  })

  it('connectorsTab_fetchesTheActiveClientsConnections', async () => {
    const at = '2026-03-04T10:00:00Z'
    const f = mkFetch({ get: () => listing([conn({ last_sync_at: at })]) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    expect(await screen.findByText('Fake ERP')).toBeTruthy()
    expect(gets(f)).toHaveLength(1)
    expect(gets(f)[0][0]).toBe(BASE + '/e1/erp-connections')
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    expect(screen.getByText('Synced ' + fmtDateTime(at))).toBeTruthy()
  })

  it('connectorsTab_refetchesOnClientSwitch', async () => {
    const e2 = deferred<ErpListing>()
    const f = mkFetch({ get: (u) => (u.includes('/e2/') ? e2.promise : listing([conn()])) })
    const { rerender } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    rerender(<ConnectorsView ctx={mkCtx(f, {}, 'e2')} />)
    expect(gets(f, 'e2')).toHaveLength(1)
    expect(rows()).toHaveLength(0)
    none()
    expect(screen.getByText('Loading ERP connectors…')).toBeTruthy()
    await act(async () => e2.resolve(listing([conn({ id: 'c2', system: 'beta' })], ['beta'])))
    expect(await screen.findByText('beta')).toBeTruthy()
    expect(rows()[0].dataset.connectionId).toBe('c2')
  })

  it('connectorsTab_aLateAnswerForThePreviousClientNeverRenders', async () => {
    const e1 = deferred<ErpListing>()
    const f = mkFetch({ get: (u) => (u.includes('/e1/') ? e1.promise : listing([], ['beta'])) })
    const { rerender } = render(<ConnectorsView ctx={mkCtx(f)} />)
    rerender(<ConnectorsView ctx={mkCtx(f, {}, 'e2')} />)
    expect(await screen.findByText('beta')).toBeTruthy()
    await act(async () => e1.resolve(listing([], ['alpha'])))
    expect(screen.queryByText('alpha')).toBeNull()
    expect(rows().map((r) => r.dataset.system)).toEqual(['beta'])
  })

  it('connectorsTab_aRefetchKeepsTheRows', async () => {
    vi.useFakeTimers()
    const held = deferred<ErpListing>()
    let n = 0
    const f = mkFetch({ get: () => (++n === 1 ? listing([conn({ status: 'pending' })]) : held.promise) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await advance(3000)
    expect(gets(f)).toHaveLength(2)
    expect(rows()).toHaveLength(1)
    expect(screen.getByText('WAITING FOR SIGN-IN')).toBeTruthy()
    await act(async () => held.resolve(listing([conn()])))
    expect(screen.getByText('CONNECTED')).toBeTruthy()
  })
})

describe('states', () => {
  it('connectorsTab_productionShowsTheEmptyState', async () => {
    render(<ConnectorsView ctx={mkCtx(mkFetch({ get: () => listing([], []) }))} />)
    expect(await screen.findByText('No connectors are available yet.')).toBeTruthy()
    expect(rows()).toHaveLength(0)
    none()
  })

  it('connectorsTab_noActiveClientSendsNothing', () => {
    const f = mkFetch({ get: () => listing() })
    render(<ConnectorsView ctx={mkCtx(f, {}, null)} />)
    expect(screen.getByText('No company selected')).toBeTruthy()
    expect(f).not.toHaveBeenCalled()
  })

  it('connectorsTab_aFailedLoadOffersRetry', async () => {
    let n = 0
    const f = mkFetch({ get: () => (++n === 1 ? Promise.reject(new ApiError('http', 'boom', 500)) : listing()) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    fireEvent.click(await screen.findByRole('button', { name: /retry/i }))
    expect(await screen.findByText('Fake ERP')).toBeTruthy()
    expect(gets(f)).toHaveLength(2)
  })
})

describe('roles', () => {
  const four = () =>
    listing(
      [conn({ id: 'p', system: 'a', status: 'pending', external_key: null }), conn({ id: 'act', system: 'b' }), conn({ id: 'd', system: 'c', status: 'disconnected' })],
      ['fake', 'a', 'b', 'c'],
    )

  it('connectorsTab_adminSeesControlsPerStatus', async () => {
    render(<ConnectorsView ctx={mkCtx(mkFetch({ get: four }))} />)
    await screen.findByText('Fake ERP')
    const has = (s: string) => [!!connectBtn(rowOf(s)), !!disconnectBtn(rowOf(s))]
    expect(has('fake')).toEqual([true, false])
    expect(has('a')).toEqual([true, true])
    expect(has('b')).toEqual([false, true])
    expect(has('c')).toEqual([true, false])
    expect(screen.queryByTestId('erp-connectors-admin-only')).toBeNull()
  })

  it('connectorsTab_nonAdminSeesNoControls', async () => {
    const f = mkFetch({ get: four })
    render(<ConnectorsView ctx={mkCtx(f, { members: me('preparer') })} />)
    await screen.findByText('Fake ERP')
    expect(rows()).toHaveLength(4)
    none()
    expect(screen.getByTestId('erp-connectors-admin-only').textContent).toBe(ADMIN_ONLY)
    cleanup()
    // control: an admin whose membership is suspended is no admin (viewerIsAdmin's status clause)
    render(<ConnectorsView ctx={mkCtx(f, { members: me('admin', 'suspended') })} />)
    await screen.findByText('Fake ERP')
    none()
    expect(screen.getByTestId('erp-connectors-admin-only')).toBeTruthy()
  })

  it('connectorsTab_loadingRosterShowsNeitherControlsNorNote', async () => {
    render(<ConnectorsView ctx={mkCtx(mkFetch({ get: four }), { members: [], membersState: 'loading' })} />)
    await screen.findByText('Fake ERP')
    none()
    expect(screen.queryByTestId('erp-connectors-admin-only')).toBeNull()
    expect(screen.queryByTestId('erp-connectors-role-unknown')).toBeNull()
  })

  it('connectorsTab_aFailedRosterSaysWhyAndRetries', async () => {
    const refetchMembers = vi.fn()
    render(<ConnectorsView ctx={mkCtx(mkFetch({ get: four }), { members: [], membersState: 'error', refetchMembers })} />)
    await screen.findByText('Fake ERP')
    none()
    const line = screen.getByTestId('erp-connectors-role-unknown')
    expect(line.textContent).toContain(CONNECTORS_ROLE_FAILED)
    expect(CONNECTORS_ROLE_FAILED).toBe('Your role could not be checked. Connect and disconnect show once it loads.')
    fireEvent.click(within(line).getByRole('button', { name: 'Retry' }))
    expect(refetchMembers).toHaveBeenCalledTimes(1)
  })
})

describe('connect', () => {
  const pendingAnswer = () => ({ ...conn({ status: 'pending', external_key: null }), consent_url: CONSENT })

  it('connectorsTab_connectOpensTheConsentTab', async () => {
    const post = deferred<unknown>()
    const f = mkFetch({ get: () => listing(), post: () => post.promise })
    const { tab, open } = spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    fireEvent.click(connectBtn(rowOf('fake'))!)
    expect(open).toHaveBeenCalledWith('', '_blank')
    expect(posts(f)).toHaveLength(1)
    expect(tab!.location.href).toBe('')
    await act(async () => post.resolve(pendingAnswer()))
    expect(tab!.location.href).toBe(CONSENT)
    expect(tab!.opener).toBeNull()
    expect(posts(f)[0][0]).toBe(BASE + '/e1/erp-connections')
    expect(posts(f)[0][1]?.body).toEqual({ system: 'fake' })
  })

  it('connectorsTab_pollsUntilTheConnectionLeavesPending', async () => {
    vi.useFakeTimers()
    const seq = [listing(), listing([conn({ status: 'pending' })]), listing([conn()])]
    let n = 0
    const f = mkFetch({ get: () => seq[Math.min(n++, 2)], post: () => pendingAnswer() })
    spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await click(connectBtn(rowOf('fake'))!)
    expect(gets(f)).toHaveLength(1)
    await advance(3000)
    expect(gets(f)).toHaveLength(2)
    expect(screen.getByText('WAITING FOR SIGN-IN')).toBeTruthy()
    await advance(3000)
    expect(gets(f)).toHaveLength(3)
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    await advance(9000)
    expect(gets(f)).toHaveLength(3)
  })

  it('connectorsTab_aPendingRowLoadedOnMountIsPolled', async () => {
    vi.useFakeTimers()
    let n = 0
    const f = mkFetch({ get: () => (++n < 2 ? listing([conn({ status: 'pending' })]) : listing([conn()])) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    expect(gets(f)).toHaveLength(1)
    await advance(3000)
    expect(gets(f)).toHaveLength(2)
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    await advance(9000)
    expect(gets(f)).toHaveLength(2)
  })

  it('connectorsTab_pollStopsAfterTenMinutes', async () => {
    vi.useFakeTimers()
    const f = mkFetch({ get: () => listing([conn({ status: 'pending' })]) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await advance(10 * 60 * 1000)
    const n = gets(f).length
    expect(n).toBeGreaterThan(100)
    await advance(60 * 1000)
    expect(gets(f)).toHaveLength(n)
    expect(screen.getByText('WAITING FOR SIGN-IN')).toBeTruthy()
  })

  it('connectorsTab_aRefusedConnectClosesTheTab', async () => {
    const msg = 'only an admin can manage ERP connectors'
    const f = mkFetch({ get: () => listing(), post: () => Promise.reject(new ApiError('http', msg, 403)) })
    const { tab } = spyOpen()
    const ctx = mkCtx(f)
    render(<ConnectorsView ctx={ctx} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    expect(tab!.close).toHaveBeenCalled()
    expect(tab!.location.href).toBe('')
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe(msg)
    expect(ctx.refetchMembers).toHaveBeenCalledTimes(1)
    expect(gets(f)).toHaveLength(1)
  })

  it('connectorsTab_anUnauthorisedWriteRefetchesNothing', async () => {
    const f = mkFetch({ get: () => listing([conn()], ['fake', 'other']), post: () => Promise.reject(new ApiError('http', 'unauthorized', 401)) })
    const { tab } = spyOpen()
    const ctx = mkCtx(f)
    render(<ConnectorsView ctx={ctx} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('other'))!)
    expect(tab!.close).toHaveBeenCalled()
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('unauthorized')
    expect(gets(f)).toHaveLength(1)
    expect(ctx.refetchMembers).not.toHaveBeenCalled()
    // the same for a disconnect
    await click(disconnectBtn(rowOf('fake'))!)
    await click(screen.getByTestId('erp-disconnect-confirmed'))
    expect(posts(f)).toHaveLength(2)
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    expect(gets(f)).toHaveLength(1)
    expect(ctx.refetchMembers).not.toHaveBeenCalled()
  })

  it('connectorsTab_anUnsafeConsentUrlIsNeverOpened', async () => {
    const f = mkFetch({ get: () => listing(), post: () => ({ ...pendingAnswer(), consent_url: 'javascript:alert(1)' }) })
    const { tab } = spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    expect(tab!.close).toHaveBeenCalled()
    expect(tab!.location.href).toBe('')
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('The sign-in link was not valid.')
  })

  it('connectorsTab_aBlockedTabShowsTheLink', async () => {
    const f = mkFetch({ get: () => listing(), post: () => pendingAnswer() })
    spyOpen(null)
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    const a = screen.getByTestId('erp-consent-link') as HTMLAnchorElement
    expect(a.getAttribute('href')).toBe(CONSENT)
    expect(a.target).toBe('_blank')
    expect(a.rel).toContain('noopener')
  })

  it('connectorsTab_reconnectSendsTheConnectionId', async () => {
    const f = mkFetch({ get: () => listing([conn({ status: 'disconnected' })]), post: () => pendingAnswer() })
    spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('DISCONNECTED')
    await click(connectBtn(rowOf('fake'))!)
    expect(posts(f)[0][1]?.body).toEqual({ system: 'fake', connection_id: 'c1' })
  })

  it('connectorsTab_aWriteInFlightDisablesItsRow', async () => {
    const post = deferred<unknown>()
    const f = mkFetch({ get: () => listing([], ['fake', 'other']), post: () => post.promise })
    spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    const mine = connectBtn(rowOf('fake'))! as HTMLButtonElement
    expect(mine.disabled).toBe(true)
    expect(mine.style.opacity).toBe('0.45')
    expect((connectBtn(rowOf('other'))! as HTMLButtonElement).disabled).toBe(false)
  })

  it('connectorsTab_aDoubleClickConnectsOnce', async () => {
    const post = deferred<unknown>()
    const f = mkFetch({ get: () => listing(), post: () => post.promise })
    const { open } = spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    const b = connectBtn(rowOf('fake'))!
    act(() => {
      b.click()
      b.click()
    })
    expect(open).toHaveBeenCalledTimes(1)
    expect(posts(f)).toHaveLength(1)
  })
})

describe('disconnect', () => {
  it('connectorsTab_disconnectAsksOnce', async () => {
    const f = mkFetch({ get: () => listing([conn()]), post: () => conn({ status: 'disconnected' }) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('CONNECTED')
    await click(disconnectBtn(rowOf('fake'))!)
    expect(screen.getByTestId('erp-disconnect-confirm').textContent).toContain('Disconnect Fake ERP? Sync stops and its sign-in is deleted. Invoices already pulled stay.')
    expect(posts(f)).toHaveLength(0)
    await click(screen.getByTestId('erp-disconnect-keep'))
    expect(screen.queryByTestId('erp-disconnect-confirm')).toBeNull()
    expect(posts(f)).toHaveLength(0)
    await click(disconnectBtn(rowOf('fake'))!)
    await click(screen.getByTestId('erp-disconnect-confirmed'))
    expect(posts(f)).toHaveLength(1)
    expect(posts(f)[0][0]).toBe(BASE + '/e1/erp-connections/c1/disconnect')
    await waitFor(() => expect(gets(f)).toHaveLength(2))
  })

  it('connectorsTab_aDoubleClickDisconnectsOnce', async () => {
    const post = deferred<unknown>()
    const f = mkFetch({ get: () => listing([conn()]), post: () => post.promise })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('CONNECTED')
    await click(disconnectBtn(rowOf('fake'))!)
    const b = screen.getByTestId('erp-disconnect-confirmed')
    act(() => {
      b.click()
      b.click()
    })
    expect(posts(f)).toHaveLength(1)
  })

  it('connectorsTab_aRefusedDisconnectRefreshesTheRole', async () => {
    const msg = 'only an admin can manage ERP connectors'
    const f = mkFetch({ get: () => listing([conn()]), post: () => Promise.reject(new ApiError('http', msg, 403)) })
    const ctx = mkCtx(f)
    render(<ConnectorsView ctx={ctx} />)
    await screen.findByText('CONNECTED')
    await click(disconnectBtn(rowOf('fake'))!)
    await click(screen.getByTestId('erp-disconnect-confirmed'))
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe(msg)
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    expect(ctx.refetchMembers).toHaveBeenCalledTimes(1)
    expect(gets(f)).toHaveLength(1)
  })
})

describe('write failures refetch', () => {
  it('connectorsTab_a409Refreshes', async () => {
    const f = mkFetch({ get: () => listing([conn()]), post: () => Promise.reject(new ApiError('http', 'already disconnected', 409)) })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('CONNECTED')
    await click(disconnectBtn(rowOf('fake'))!)
    await click(screen.getByTestId('erp-disconnect-confirmed'))
    await waitFor(() => expect(gets(f)).toHaveLength(2))
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('already disconnected')
    cleanup()

    const g = mkFetch({ get: () => listing(), post: () => Promise.reject(new ApiError('http', 'already connected', 409)) })
    const { tab } = spyOpen()
    render(<ConnectorsView ctx={mkCtx(g)} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    await waitFor(() => expect(gets(g)).toHaveLength(2))
    expect(tab!.close).toHaveBeenCalled()
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('already connected')
  })

  it('connectorsTab_aFailureAfterCommitRefetches (connect)', async () => {
    let n = 0
    const f = mkFetch({
      get: () => (++n === 1 ? listing() : listing([conn({ status: 'pending', external_key: null })])),
      post: () => Promise.reject(new ApiError('http', 'internal', 500)),
    })
    const { tab } = spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('Fake ERP')
    await click(connectBtn(rowOf('fake'))!)
    expect(tab!.close).toHaveBeenCalled()
    expect(await screen.findByText('WAITING FOR SIGN-IN')).toBeTruthy()
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('internal')
  })

  it('connectorsTab_aFailureAfterCommitRefetches (disconnect)', async () => {
    let n = 0
    const f = mkFetch({
      get: () => (++n === 1 ? listing([conn()]) : listing([conn({ status: 'disconnected' })])),
      post: () => Promise.reject(new ApiError('network', 'Failed to fetch')),
    })
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await screen.findByText('CONNECTED')
    await click(disconnectBtn(rowOf('fake'))!)
    await click(screen.getByTestId('erp-disconnect-confirmed'))
    expect(await screen.findByText('DISCONNECTED')).toBeTruthy()
    expect(screen.getByTestId('erp-connectors-flash').textContent).toBe('Failed to fetch')
  })
})

describe('leaving mid-write or mid-poll', () => {
  const pendingAnswer = () => ({ ...conn({ status: 'pending', external_key: null }), consent_url: CONSENT })

  it('connectorsTab_unmountDuringConnectStartsNoPoll', async () => {
    vi.useFakeTimers()
    const post = deferred<unknown>()
    const f = mkFetch({ get: () => listing(), post: () => post.promise })
    const { tab } = spyOpen()
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { unmount } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await click(connectBtn(rowOf('fake'))!)
    unmount()
    await act(async () => post.resolve(pendingAnswer()))
    expect(tab!.location.href).toBe(CONSENT)
    const n = gets(f).length
    await advance(30000)
    expect(gets(f)).toHaveLength(n)
    expect(err).not.toHaveBeenCalled()
  })

  it('connectorsTab_unmountStopsThePoll', async () => {
    vi.useFakeTimers()
    const f = mkFetch({ get: () => listing([conn({ status: 'pending' })]) })
    const { unmount } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await advance(3000)
    expect(gets(f)).toHaveLength(2)
    unmount()
    await advance(30000)
    expect(gets(f)).toHaveLength(2)
  })

  it('connectorsTab_aClientSwitchStopsThePoll', async () => {
    vi.useFakeTimers()
    const f = mkFetch({ get: (u) => (u.includes('/e1/') ? listing([conn({ status: 'pending' })]) : listing([], ['beta'])) })
    const { rerender } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await advance(3000)
    rerender(<ConnectorsView ctx={mkCtx(f, {}, 'e2')} />)
    await flush()
    const n = gets(f, 'e1').length
    await advance(30000)
    expect(gets(f, 'e1')).toHaveLength(n)
    expect(rows().map((r) => r.dataset.system)).toEqual(['beta'])
  })

  it('connectorsTab_aClientSwitchDuringConnectStartsNoPoll', async () => {
    vi.useFakeTimers()
    const post = deferred<unknown>()
    const f = mkFetch({ get: (u) => listing([], [u.includes('/e1/') ? 'fake' : 'beta']), post: () => post.promise })
    const { tab } = spyOpen()
    const { rerender } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await click(connectBtn(rowOf('fake'))!)
    rerender(<ConnectorsView ctx={mkCtx(f, {}, 'e2')} />)
    await flush()
    await act(async () => post.resolve(pendingAnswer()))
    expect(tab!.location.href).toBe(CONSENT)
    expect(screen.queryByTestId('erp-connectors-flash')).toBeNull()
    const n = gets(f, 'e1').length
    await advance(30000)
    expect(gets(f, 'e1')).toHaveLength(n)
    expect(gets(f, 'e2')).toHaveLength(1)
  })
})
describe('poll edges (QA)', () => {
  const pendingAnswer = () => ({ ...conn({ status: 'pending', external_key: null }), consent_url: CONSENT })

  it('connectorsTab_aConnectThatLandsActiveAtOnceStopsThePoll', async () => {
    vi.useFakeTimers()
    let n = 0
    const f = mkFetch({ get: () => (++n === 1 ? listing() : listing([conn()])), post: () => pendingAnswer() })
    spyOpen()
    render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await click(connectBtn(rowOf('fake'))!)
    await advance(3000)
    expect(gets(f)).toHaveLength(2)
    expect(screen.getByText('CONNECTED')).toBeTruthy()
    await advance(60000)
    expect(gets(f)).toHaveLength(2)
  })

  it('connectorsTab_aClientSwitchStopsThePollForEveryClient', async () => {
    vi.useFakeTimers()
    const f = mkFetch({ get: (u) => (u.includes('/e1/') ? listing([conn({ status: 'pending' })]) : listing([], ['beta'])) })
    const { rerender } = render(<ConnectorsView ctx={mkCtx(f)} />)
    await flush()
    await advance(3000)
    expect(gets(f, 'e1').length, 'the poll ran before the switch').toBeGreaterThanOrEqual(2)
    rerender(<ConnectorsView ctx={mkCtx(f, {}, 'e2')} />)
    await flush()
    const total = gets(f).length
    await advance(30000)
    expect(gets(f)).toHaveLength(total)
  })
})
