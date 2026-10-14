// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { fetchAiCalls, type AiCall, type AiCallsPage } from './aiUsageApi'
import { expireSession } from './auth'
import { fetchCodeListSummary, fetchRules, fetchVersions } from './rulesApi'
import { fetchStaffTenants, type StaffTenant } from './tenantsApi'

vi.mock('@invoice-os/console-session', () => ({
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
vi.mock('./aiUsageApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./aiUsageApi')>()), fetchAiCalls: vi.fn() }))

const aiRead = vi.mocked(fetchAiCalls)
const tenantsRead = vi.mocked(fetchStaffTenants)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const call = (id: string, over: Partial<AiCall> = {}): AiCall => ({
  id, tenant_id: 'T1', tenant_name: 'Okafor & Partners', created_at: '2026-10-14T09:12:03.123456Z', client: 'ai', purpose: 'document', model: 'google/gemini',
  input_tokens: 3447, output_tokens: 120, cost: '0.0014741', latency_ms: 40000, attempts: 2, outcome: 'ok', ...over,
})
const page = (calls: AiCall[], next: string | null = null): AiCallsPage => ({ calls, page: { limit: 50, has_more: next !== null, next_cursor: next } })

beforeEach(() => {
  vi.mocked(fetchRules).mockResolvedValue({ version: 7, rules: [] })
  vi.mocked(fetchVersions).mockResolvedValue({ today: '2026-10-11', versions: [] })
  vi.mocked(fetchCodeListSummary).mockResolvedValue([])
  tenantsRead.mockResolvedValue([tenant('T1', 'Okafor & Partners'), tenant('T2', 'Yaba Freight')])
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
})
