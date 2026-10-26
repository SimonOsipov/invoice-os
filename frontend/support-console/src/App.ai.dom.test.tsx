// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { fetchAiCalls, fetchAiTotals, type AiCall, type AiCallsPage, type AiTotal, type AiTotalsPage } from './aiUsageApi'
import { expireSession } from './auth'
import { fetchCodeListSummary, fetchRules, fetchVersions } from './rulesApi'
import { fetchStaffTenants, type StaffTenant } from './tenantsApi'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  StaffGate: ({ children }: { children: ReactNode }) => children,
  signOutConsole: vi.fn(),
  expireConsole: vi.fn(),
}))
vi.mock('./auth', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./auth')>()),
  expireSession: vi.fn(),
  signOut: vi.fn(),
}))
vi.mock('./rulesApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./rulesApi')>()),
  fetchRules: vi.fn(),
  fetchCodeListSummary: vi.fn(),
  fetchCodeListSyncs: vi.fn(),
  fetchVersions: vi.fn(),
}))
vi.mock('./tenantsApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./tenantsApi')>()), fetchStaffTenants: vi.fn() }))
vi.mock('./aiUsageApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./aiUsageApi')>()), fetchAiCalls: vi.fn(), fetchAiTotals: vi.fn() }))

const aiRead = vi.mocked(fetchAiCalls)
const totalsRead = vi.mocked(fetchAiTotals)
const tenantsRead = vi.mocked(fetchStaffTenants)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null, deletion_due_at: null, deletion_purge_started_at: null,
})
const call = (id: string, over: Partial<AiCall> = {}): AiCall => ({
  id, tenant_id: 'T1', tenant_name: 'Okafor & Partners', created_at: '2026-10-14T09:12:03.123456Z', client: 'ai', purpose: 'document', model: 'google/gemini',
  input_tokens: 3447, output_tokens: 120, cost: '0.0014741', latency_ms: 40000, attempts: 2, outcome: 'ok', ...over,
})
const total = (day: string, over: Partial<AiTotal> = {}): AiTotal => ({
  day, tenant_id: 'T1', tenant_name: 'Okafor & Partners', calls: 2, input_tokens: 1000, output_tokens: 0, cost: '0.0010054', failed: 2, ...over,
})
const totalsPage = (totals: AiTotal[], next: string | null = null): AiTotalsPage => ({ totals, page: { limit: 50, has_more: next !== null, next_cursor: next } })
const page = (calls: AiCall[], next: string | null = null): AiCallsPage => ({ calls, page: { limit: 50, has_more: next !== null, next_cursor: next } })

beforeEach(() => {
  vi.mocked(fetchRules).mockResolvedValue({ version: 7, rules: [] })
  vi.mocked(fetchVersions).mockResolvedValue({ today: '2026-10-11', versions: [] })
  vi.mocked(fetchCodeListSummary).mockResolvedValue([])
  tenantsRead.mockResolvedValue([tenant('T1', 'Okafor & Partners'), tenant('T2', 'Yaba Freight')])
  totalsRead.mockResolvedValue(totalsPage([total('2026-10-15'), total('2026-10-14', { tenant_id: 'T2', tenant_name: 'Yaba Freight', calls: 5, failed: 0 })]))
  aiRead.mockResolvedValue(page([call('c1'), call('c2', { client: 'jev', purpose: 'mapping_check', model: '', outcome: 'unavailable' })]))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const openAi = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^AI usage/ }))
  await screen.findByRole('heading', { level: 1, name: 'AI usage' })
}
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-call-id]'))
const ids = () => rows().map((r) => r.dataset.callId)
const totalRows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-total-key]'))
const totalKeys = () => totalRows().map((r) => r.dataset.totalKey)
const cells = (r: HTMLElement) => Array.from(r.children).map((c) => c.textContent)
const typeAndEnter = (label: string, value: string) => {
  const input = screen.getByLabelText(label)
  fireEvent.change(input, { target: { value } })
  fireEvent.keyDown(input, { key: 'Enter' })
}

describe('Console AI usage wiring', () => {
  it('AI usage opens from the nav and reads the first page once', async () => {
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(aiRead).toHaveBeenCalledTimes(1)
    expect(aiRead.mock.calls[0][0]).toEqual({})
    expect(aiRead.mock.calls[0][1]).toBe(false)
    expect(aiRead.mock.calls[0][2]).toBeUndefined()
    expect(cells(rows()[0])).toEqual(['2026-10-14 09:12:03 UTC', 'Okafor & Partners', 'ai', 'document', 'google/gemini', '3,447', '120', '$0.0014741', '40,000 ms', '2', 'ok'])
    expect(cells(rows()[1])).toEqual(['2026-10-14 09:12:03 UTC', 'Okafor & Partners', 'jev', 'mapping_check', '—', '3,447', '120', '$0.0014741', '40,000 ms', '2', 'unavailable'])
  })

  it('the filters re-read with only the set values', async () => {
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(2))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    const last = () => aiRead.mock.calls.at(-1)![0]

    fireEvent.change(screen.getByLabelText('Tenant'), { target: { value: 'T2' } })
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(2))
    expect(last()).toEqual({ tenant: 'T2' })

    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(3))
    expect(last()).toEqual({ tenant: 'T2', purpose: 'document' })

    fireEvent.change(screen.getByLabelText('Outcome'), { target: { value: ' unavailable ' } })
    expect(aiRead).toHaveBeenCalledTimes(3)
    fireEvent.blur(screen.getByLabelText('Outcome'))
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(4))
    expect(last()).toEqual({ tenant: 'T2', purpose: 'document', outcome: 'unavailable' })

    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-10-01' } })
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(5))
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-03' } })
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(6))
    expect(last()).toEqual({ tenant: 'T2', purpose: 'document', outcome: 'unavailable', from: '2026-10-01', to: '2026-10-03' })

    fireEvent.change(screen.getByLabelText('Purpose'), { target: { value: 'x' } })
    await new Promise((r) => setTimeout(r, 30))
    expect(aiRead).toHaveBeenCalledTimes(6)
  })

  it('an inverted range sends no read', async () => {
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-10-05' } })
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(2))
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-01' } })
    await screen.findByText('End date must be on or after the start date.')
    await new Promise((r) => setTimeout(r, 30))
    expect(aiRead).toHaveBeenCalledTimes(2)
    expect(rows()).toHaveLength(0)
  })

  it('Load more appends, hides at the end and resets on a filter change', async () => {
    aiRead.mockResolvedValueOnce(page([call('a1'), call('a2')], 'CUR1'))
    aiRead.mockResolvedValueOnce(page([call('a3')], null))
    render(<App />)
    await openAi()
    await waitFor(() => expect(ids()).toEqual(['a1', 'a2']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(ids()).toEqual(['a1', 'a2', 'a3']))
    expect(aiRead.mock.calls[1]).toEqual([{}, false, 'CUR1'])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()

    aiRead.mockResolvedValueOnce(page([call('b1')], null))
    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(ids()).toEqual(['b1']))
    expect(aiRead).toHaveBeenCalledTimes(3)
    expect(aiRead.mock.calls[2]).toEqual([{ purpose: 'document' }, false])
  })

  it('a late Load more page is dropped after a filter change', async () => {
    let release: (p: AiCallsPage) => void = () => {}
    aiRead.mockResolvedValueOnce(page([call('a1')], 'CUR1'))
    aiRead.mockReturnValueOnce(new Promise<AiCallsPage>((r) => (release = r)))
    aiRead.mockResolvedValueOnce(page([call('b1')], null))
    render(<App />)
    await openAi()
    await waitFor(() => expect(ids()).toEqual(['a1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(2))
    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(ids()).toEqual(['b1']))
    release(page([call('late')], null))
    await new Promise((r) => setTimeout(r, 30))
    expect(ids()).toEqual(['b1'])
  })

  it('FAILURES is one click and shows each outcome', async () => {
    aiRead.mockResolvedValueOnce(page([call('c1')]))
    aiRead.mockResolvedValueOnce(page([call('f1', { outcome: 'unavailable' }), call('f2', { outcome: 'fake' })]))
    render(<App />)
    await openAi()
    await waitFor(() => expect(ids()).toEqual(['c1']))
    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(2))

    aiRead.mockResolvedValueOnce(page([call('f1', { outcome: 'unavailable' }), call('f2', { outcome: 'fake' })]))
    fireEvent.click(screen.getByRole('button', { name: 'FAILURES' }))
    await waitFor(() => expect(ids()).toEqual(['f1', 'f2']))
    expect(screen.getByRole('button', { name: 'FAILURES' }).getAttribute('aria-pressed')).toBe('true')
    expect(aiRead.mock.calls.at(-1)).toEqual([{ purpose: 'document' }, true])
    const outcome = (r: HTMLElement) => r.lastElementChild as HTMLElement
    expect(rows().map((r) => [outcome(r).textContent, outcome(r).style.color])).toEqual([
      ['unavailable', 'var(--status-red-text)'],
      ['fake', 'var(--status-red-text)'],
    ])

    fireEvent.click(screen.getByRole('button', { name: 'CALLS' }))
    await waitFor(() => expect(aiRead.mock.calls.at(-1)).toEqual([{ purpose: 'document' }, false]))
  })

  it('cost and an empty model render as sent', async () => {
    aiRead.mockResolvedValue(page([call('z1', { cost: '0.0010054', model: '' })]))
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(cells(rows()[0]).slice(4, 8)).toEqual(['—', '3,447', '120', '$0.0010054'])
  })

  it('the screen offers no control that changes AI behaviour', async () => {
    const allowed = ['CALLS', 'FAILURES', 'DAILY TOTALS', 'Load more', 'Retry']
    const audit = () => {
      expect(document.querySelectorAll('[role="switch"], input[type="checkbox"], input[type="range"], input[type="number"]')).toHaveLength(0)
      const root = document.querySelector('.ops-screen-pad') as HTMLElement
      for (const b of Array.from(root.querySelectorAll('button'))) expect(allowed, `button "${b.textContent}"`).toContain(b.textContent?.trim())
    }
    aiRead.mockResolvedValueOnce(page([call('a1')], 'CUR1'))
    render(<App />)
    await openAi()
    await screen.findByRole('button', { name: 'Load more' })
    audit()
    cleanup()

    aiRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openAi()
    await screen.findByRole('button', { name: 'Retry' })
    audit()
  })

  it('empty, forbidden, error and expired states', async () => {
    aiRead.mockResolvedValueOnce(page([]))
    render(<App />)
    await openAi()
    await screen.findByText('No AI calls match these filters.')
    aiRead.mockResolvedValueOnce(page([]))
    fireEvent.click(screen.getByRole('button', { name: 'FAILURES' }))
    await screen.findByText('No failed calls match these filters.')
    cleanup()

    aiRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openAi()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    cleanup()

    aiRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openAi()
    await screen.findByText('boom')
    const before = aiRead.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(aiRead).toHaveBeenCalledTimes(before + 1)
    cleanup()

    aiRead.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openAi()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })

  it('a failed tenant read leaves the list usable', async () => {
    tenantsRead.mockRejectedValueOnce(new ApiError('http', 'tenants down', 500))
    render(<App />)
    await openAi()
    await screen.findByText('tenants down')
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    expect(screen.queryByText('tenants down')).toBeNull()
  })

  it("DAILY TOTALS lists the server's totals", async () => {
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(2))
    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(aiRead).toHaveBeenCalledTimes(2))
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalRows()).toHaveLength(2))
    expect(totalsRead).toHaveBeenCalledTimes(1)
    expect(totalsRead.mock.calls[0]).toEqual([{ purpose: 'document' }])
    expect(aiRead).toHaveBeenCalledTimes(2)
    expect(totalKeys()).toEqual(['2026-10-15|T1', '2026-10-14|T2'])
    expect(cells(totalRows()[0])).toEqual(['2026-10-15', 'Okafor & Partners', '2', '1,000', '0', '$0.0010054', '2'])
    const failed = (r: HTMLElement) => (r.lastElementChild as HTMLElement).style.color
    expect(failed(totalRows()[0])).toBe('var(--status-red-text)')
    expect(failed(totalRows()[1])).not.toBe('var(--status-red-text)')
    expect(cells(totalRows()[1])).toEqual(['2026-10-14', 'Yaba Freight', '5', '1,000', '0', '$0.0010054', '0'])
  })

  it('totals re-read on a filter change and not on an inverted range', async () => {
    render(<App />)
    await openAi()
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalRows()).toHaveLength(2))
    typeAndEnter('Outcome', 'unavailable')
    await waitFor(() => expect(totalsRead).toHaveBeenCalledTimes(2))
    expect(totalsRead.mock.calls[1]).toEqual([{ outcome: 'unavailable' }])

    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-10-05' } })
    await waitFor(() => expect(totalsRead).toHaveBeenCalledTimes(3))
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-01' } })
    await screen.findByText('End date must be on or after the start date.')
    await new Promise((r) => setTimeout(r, 30))
    expect(totalsRead).toHaveBeenCalledTimes(3)
    expect(totalRows()).toHaveLength(0)
  })

  it("totals cells are the server's values, not sums of the call list", async () => {
    aiRead.mockResolvedValue(page([call('c1', { input_tokens: 7 }), call('c2', { input_tokens: 5 })]))
    render(<App />)
    await openAi()
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalRows()).toHaveLength(2))
    expect(cells(totalRows()[0])).toEqual(['2026-10-15', 'Okafor & Partners', '2', '1,000', '0', '$0.0010054', '2'])
  })

  it('totals Load more appends, hides at the end and drops a late page', async () => {
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-15')], 'TC1'))
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-14')], null))
    render(<App />)
    await openAi()
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalKeys()).toEqual(['2026-10-15|T1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(totalKeys()).toEqual(['2026-10-15|T1', '2026-10-14|T1']))
    expect(totalsRead.mock.calls[1]).toEqual([{}, 'TC1'])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()

    cleanup()
    vi.clearAllMocks()
    let release: (p: AiTotalsPage) => void = () => {}
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-15')], 'TC1'))
    totalsRead.mockReturnValueOnce(new Promise<AiTotalsPage>((r) => (release = r)))
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-09-01')], null))
    render(<App />)
    await openAi()
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalKeys()).toEqual(['2026-10-15|T1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(totalsRead).toHaveBeenCalledTimes(2))
    typeAndEnter('Purpose', 'document')
    await waitFor(() => expect(totalKeys()).toEqual(['2026-09-01|T1']))
    release(totalsPage([total('2026-08-01')], null))
    await new Promise((r) => setTimeout(r, 30))
    expect(totalKeys()).toEqual(['2026-09-01|T1'])
  })

  it('totals empty, forbidden, error and expired', async () => {
    const openTotals = async () => {
      render(<App />)
      await openAi()
      fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    }
    totalsRead.mockResolvedValueOnce(totalsPage([]))
    await openTotals()
    await screen.findByText('No AI calls match these filters.')
    cleanup()

    totalsRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await openTotals()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    cleanup()

    totalsRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await openTotals()
    await screen.findByText('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(totalRows()).toHaveLength(2))
    cleanup()

    totalsRead.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    await openTotals()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })

  it('totals show Loading while the read is pending', async () => {
    totalsRead.mockReturnValueOnce(new Promise<AiTotalsPage>(() => {}))
    render(<App />)
    await openAi()
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await screen.findByText('Loading AI totals…')
    expect(totalRows()).toHaveLength(0)
  })

  it('totals Load more keeps every page across two loads', async () => {
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-15')], 'TC1'))
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-14')], 'TC2'))
    totalsRead.mockResolvedValueOnce(totalsPage([total('2026-10-13')], null))
    render(<App />)
    await openAi()
    fireEvent.click(screen.getByRole('button', { name: 'DAILY TOTALS' }))
    await waitFor(() => expect(totalKeys()).toEqual(['2026-10-15|T1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(totalKeys()).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(totalKeys()).toEqual(['2026-10-15|T1', '2026-10-14|T1', '2026-10-13|T1']))
    expect(totalsRead.mock.calls.map((c) => c[1])).toEqual([undefined, 'TC1', 'TC2'])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })
})
