// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { fetchJobs, type StaffJob } from './jobsApi'
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
vi.mock('./jobsApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./jobsApi')>()), fetchJobs: vi.fn() }))

const jobsRead = vi.mocked(fetchJobs)
const tenantsRead = vi.mocked(fetchStaffTenants)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const job = (id: string, over: Partial<StaffJob> = {}): StaffJob => ({
  id, tenant_id: 'T1', tenant_name: 'Okafor & Partners', invoice_id: `${id}i`, invoice_number: `INV-${id}`, invoice_status: 'failed',
  state: 'dead_lettered', attempts: 5, max_attempts: 5, last_error: 'APP timeout', acknowledged: false,
  created_at: new Date(Date.now() - 3 * 3600 * 1000).toISOString(), updated_at: '2026-10-14T08:00:00Z', ...over,
})

beforeEach(() => {
  vi.mocked(fetchRules).mockResolvedValue({ version: 7, rules: [] })
  vi.mocked(fetchVersions).mockResolvedValue({ today: '2026-10-11', versions: [] })
  vi.mocked(fetchCodeListSummary).mockResolvedValue([])
  tenantsRead.mockResolvedValue([tenant('T1', 'Okafor & Partners'), tenant('T2', 'Yaba Freight')])
  jobsRead.mockResolvedValue([job('a'), job('b', { state: 'queued', attempts: 0, last_error: null })])
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const openSubmissions = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^Submissions/ }))
  await screen.findByRole('heading', { level: 1, name: 'Submissions ops' })
}
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-job-id]'))

describe('Console submissions wiring', () => {
  it('Submissions opens from the nav and reads the list once', async () => {
    render(<App />)
    await openSubmissions()
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(jobsRead).toHaveBeenCalledTimes(1)
    expect(jobsRead.mock.calls[0].slice(0, 2)).toEqual([undefined, undefined])
    const cells = rows()[0].children
    expect(cells).toHaveLength(7)
    expect(Array.from(cells).slice(0, 6).map((c) => c.textContent)).toEqual(['Okafor & Partners', 'INV-a', 'DEAD-LETTER', '5', 'APP timeout', '3h'])
    expect(within(rows()[1] as HTMLElement).getByText('QUEUED')).toBeTruthy()
    expect(within(rows()[1] as HTMLElement).getByText('—')).toBeTruthy()
  })

  it('a state chip and the tenant select re-read the list', async () => {
    render(<App />)
    await openSubmissions()
    await waitFor(() => expect(rows()).toHaveLength(2))
    const chip = screen.getByRole('button', { name: 'DEAD-LETTER' })
    expect(chip.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(chip)
    await waitFor(() => expect(jobsRead).toHaveBeenCalledTimes(2))
    expect(jobsRead.mock.calls[1].slice(0, 2)).toEqual(['dead_lettered', undefined])
    expect(screen.getByRole('button', { name: 'DEAD-LETTER' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'ALL' }).getAttribute('aria-pressed')).toBe('false')
    await waitFor(() => expect(rows()).toHaveLength(2))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    fireEvent.change(screen.getByLabelText('Tenant'), { target: { value: 'T2' } })
    await waitFor(() => expect(jobsRead).toHaveBeenCalledTimes(3))
    expect(jobsRead.mock.calls[2].slice(0, 2)).toEqual(['dead_lettered', 'T2'])
    fireEvent.click(screen.getByRole('button', { name: 'ALL' }))
    await waitFor(() => expect(jobsRead).toHaveBeenCalledTimes(4))
    expect(jobsRead.mock.calls[3].slice(0, 2)).toEqual([undefined, 'T2'])
  })

  it('empty, forbidden, error and expired states', async () => {
    jobsRead.mockResolvedValueOnce([])
    render(<App />)
    await openSubmissions()
    await screen.findByText('No jobs in this state.')
    expect(screen.getByRole('button', { name: 'ALL' })).toBeTruthy()
    cleanup()

    jobsRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openSubmissions()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    cleanup()

    jobsRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openSubmissions()
    await screen.findByText('boom')
    const before = jobsRead.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(jobsRead).toHaveBeenCalledTimes(before + 1)
    cleanup()

    jobsRead.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openSubmissions()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })
})
