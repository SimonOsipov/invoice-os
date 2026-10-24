// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { fetchStaffHealth, type StaffHealth } from './healthApi'
import { fetchStaffMetrics, type StaffMetrics } from './metricsApi'
import { fetchCodeListSummary, fetchRules, fetchVersions } from './rulesApi'
import { fetchStaffTenants } from './tenantsApi'

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
vi.mock('./tenantsApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./tenantsApi')>()),
  fetchStaffTenants: vi.fn(),
  fetchStaffTenant: vi.fn(),
}))
vi.mock('./healthApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./healthApi')>()),
  fetchStaffHealth: vi.fn(),
}))
vi.mock('./metricsApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./metricsApi')>()),
  fetchStaffMetrics: vi.fn(),
}))

const rulesRead = vi.mocked(fetchRules)
const versionsRead = vi.mocked(fetchVersions)
const health = vi.mocked(fetchStaffHealth)
const metrics = vi.mocked(fetchStaffMetrics)

const METRICS: StaffMetrics = {
  range: '1h',
  service: '',
  start: '2026-10-14T10:00:00Z',
  end: '2026-10-14T11:00:00Z',
  step_seconds: 60,
  connection_budget: 80,
  panels: [],
  limits: { available: true, services: [] },
  deploys: { available: true, revisions: [] },
}

const HEALTH: StaffHealth = {
  checked_at: '2026-10-14T10:05:31Z',
  fleet: { status: 'ok', services: [{ name: 'gateway', status: 'up', build: '541ac755c7a1' }] },
  jobs: { dead_lettered: 0 },
  queue_depth: 4,
  trend: [],
}

beforeEach(() => {
  rulesRead.mockResolvedValue({ version: 7, rules: [] })
  versionsRead.mockResolvedValue({ today: '2026-10-11', versions: [] })
  vi.mocked(fetchCodeListSummary).mockResolvedValue([])
  vi.mocked(fetchStaffTenants).mockResolvedValue([])
  health.mockResolvedValue(HEALTH)
  metrics.mockResolvedValue(METRICS)
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const openHealth = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
  await screen.findByRole('heading', { level: 1, name: 'System health' })
}

describe('Console system health wiring', () => {
  it('System health opens from the nav and reads once', async () => {
    render(<App />)
    expect(health).not.toHaveBeenCalled()
    await openHealth()
    await screen.findByText('1/1')
    expect(health).toHaveBeenCalledTimes(1)
  })

  it('a failed read retries and a 401 expires the session', async () => {
    health.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openHealth()
    await screen.findByText('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByText('1/1')
    expect(health).toHaveBeenCalledTimes(2)
    expect(expireSession).not.toHaveBeenCalled()
    cleanup()
    health.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openHealth()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })

  it('a 403 shows the staff line without Retry', async () => {
    health.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openHealth()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('re-entering System health re-reads; Rules re-reads on return', async () => {
    render(<App />)
    await waitFor(() => expect(rulesRead).toHaveBeenCalledTimes(1))
    await openHealth()
    await screen.findByText('1/1')
    expect(health).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await waitFor(() => expect(rulesRead).toHaveBeenCalledTimes(2))
    expect(versionsRead).toHaveBeenCalledTimes(2)
    await openHealth()
    await waitFor(() => expect(health).toHaveBeenCalledTimes(2))
  })

  it('a range and a service narrow the metrics read', async () => {
    render(<App />)
    await openHealth()
    await screen.findByText('1/1')
    await waitFor(() => expect(metrics).toHaveBeenLastCalledWith('1h', ''))
    fireEvent.click(screen.getByRole('button', { name: '6h' }))
    await waitFor(() => expect(metrics).toHaveBeenLastCalledWith('6h', ''))
    fireEvent.change(screen.getByLabelText('Service'), { target: { value: 'submission' } })
    await waitFor(() => expect(metrics).toHaveBeenLastCalledWith('6h', 'submission'))
    expect(health).toHaveBeenCalledTimes(1)
  })

  it('a failed metrics read leaves the health blocks and retries', async () => {
    metrics.mockRejectedValueOnce(new ApiError('http', 'metrics boom', 500))
    render(<App />)
    await openHealth()
    const note = await screen.findByText('metrics boom')
    expect(screen.getByText('1/1')).toBeTruthy()
    fireEvent.click(within(note.closest('[data-panels]') as HTMLElement).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(metrics).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByText('metrics boom')).toBeNull())
    expect(screen.getByText('1/1')).toBeTruthy()
  })

  it('a 403 on the metrics read shows the staff line in the panels and keeps the health blocks', async () => {
    metrics.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openHealth()
    const note = await screen.findByText('Your account is not ASComply staff.')
    expect(within(note.closest('[data-panels]') as HTMLElement).queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.getByText('1/1')).toBeTruthy()
  })

  it('a 401 on the metrics read expires the session', async () => {
    metrics.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openHealth()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })
})
