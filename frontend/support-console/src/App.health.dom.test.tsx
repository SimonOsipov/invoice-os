// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { fetchStaffHealth, type StaffHealth } from './healthApi'
import { fetchStaffMetrics, HEALTH_REFRESH_MS, type StaffMetrics } from './metricsApi'
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
  vi.useRealTimers()
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

describe('Console system health refresh', () => {
  const WITH_REQ: StaffMetrics = { ...METRICS, panels: [{ key: 'req_per_min', unit: 'req/min', available: true, series: [{ name: 'gateway', points: [[1, 42]] }] }] }
  const tick = (ms = HEALTH_REFRESH_MS) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
  const reqTile = () => (document.querySelector('[data-tile="req"]') as HTMLElement).textContent ?? ''
  const open = async () => {
    vi.useFakeTimers()
    metrics.mockResolvedValue(WITH_REQ)
    render(<App />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(0)
    expect(screen.getByText('1/1')).toBeTruthy()
    expect(reqTile()).toContain('42')
  }
  const pending = () => new Promise<never>(() => {})

  it('System health re-reads both routes every 60 s', async () => {
    await open()
    await tick()
    await tick()
    expect(health).toHaveBeenCalledTimes(3)
    expect(metrics).toHaveBeenCalledTimes(3)
  })

  it('no re-read before 60 s', async () => {
    await open()
    await tick(59_000)
    expect(health).toHaveBeenCalledTimes(1)
    expect(metrics).toHaveBeenCalledTimes(1)
  })

  it('a refresh keeps the last values on screen', async () => {
    await open()
    health.mockImplementation(pending)
    metrics.mockImplementation(pending)
    await tick()
    expect(health).toHaveBeenCalledTimes(2)
    expect(screen.getByText('1/1')).toBeTruthy()
    expect(reqTile()).toContain('42')
    expect(screen.queryByText(/Loading/)).toBeNull()
    expect(screen.queryByText('REFRESH FAILED')).toBeNull()
  })

  it('leaving System health stops the refresh', async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await tick(0)
    await tick(180_000)
    expect(health).toHaveBeenCalledTimes(1)
    expect(metrics).toHaveBeenCalledTimes(1)
  })

  it('re-entering System health shows Loading, not the old visit', async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await tick(0)
    health.mockImplementation(pending)
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(0)
    expect(screen.getByText('Loading system health…')).toBeTruthy()
    expect(screen.queryByText('1/1')).toBeNull()
  })

  it('a range change loads its metrics and keeps the health data', async () => {
    await open()
    metrics.mockImplementation(pending)
    fireEvent.click(screen.getByRole('button', { name: '6h' }))
    await tick(0)
    expect(screen.getByText('Loading metrics…')).toBeTruthy()
    expect(screen.getByText('1/1')).toBeTruthy()
    expect(reqTile()).not.toContain('42')
  })

  it('a failed refresh tick keeps the page and marks it', async () => {
    await open()
    health.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    metrics.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await tick()
    expect(screen.getByText('1/1')).toBeTruthy()
    expect(reqTile()).toContain('42')
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    expect(screen.queryByText('boom')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    await tick()
    expect(screen.queryByText('REFRESH FAILED')).toBeNull()
    expect(screen.getByText('1/1')).toBeTruthy()
  })

  it('REFRESH FAILED stays while the next tick is in flight and goes when it succeeds', async () => {
    await open()
    health.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await tick()
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    health.mockImplementationOnce(pending)
    await tick()
    expect(health).toHaveBeenCalledTimes(3)
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    expect(screen.queryByText(/Loading/)).toBeNull()
    await tick()
    expect(screen.queryByText('REFRESH FAILED')).toBeNull()
  })

  it('a range change clears REFRESH FAILED for the new key', async () => {
    await open()
    metrics.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await tick()
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    metrics.mockImplementation(pending)
    fireEvent.click(screen.getByRole('button', { name: '6h' }))
    await tick(0)
    expect(screen.queryByText('REFRESH FAILED')).toBeNull()
    expect(screen.getByText('Loading metrics…')).toBeTruthy()
  })

  it('a failed metrics tick alone marks the page', async () => {
    await open()
    metrics.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await tick()
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    expect(reqTile()).toContain('42')
  })

  it('a failed health tick alone marks the page', async () => {
    await open()
    health.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    await tick()
    expect(screen.getByText('REFRESH FAILED')).toBeTruthy()
    expect(screen.getByText('1/1')).toBeTruthy()
  })

  it('a 401 on a refresh tick expires the session', async () => {
    await open()
    metrics.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    await tick()
    expect(expireSession).toHaveBeenCalledTimes(1)
    expect(reqTile()).not.toContain('42')
    expect(screen.queryByText('REFRESH FAILED')).toBeNull()
  })

  it('re-entering System health does not show the old metrics while they load', async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await tick(0)
    metrics.mockImplementation(pending)
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(0)
    expect(screen.getByText('Loading metrics…')).toBeTruthy()
    expect(reqTile()).not.toContain('42')
  })

  it('an older range answer that lands late does not overwrite the newer range', async () => {
    await open()
    const later = (v: StaffMetrics) => new Promise<StaffMetrics>((res) => setTimeout(() => res(v), 5_000))
    const OLD: StaffMetrics = { ...WITH_REQ, panels: [{ ...WITH_REQ.panels[0], series: [{ name: 'gateway', points: [[1, 111]] }] }] }
    const NEW: StaffMetrics = { ...WITH_REQ, panels: [{ ...WITH_REQ.panels[0], series: [{ name: 'gateway', points: [[1, 222]] }] }] }
    metrics.mockImplementationOnce(() => later(OLD)).mockImplementationOnce(() => Promise.resolve(NEW))
    fireEvent.click(screen.getByRole('button', { name: '6h' }))
    await tick(0)
    fireEvent.click(screen.getByRole('button', { name: '24h' }))
    await tick(10_000)
    expect(reqTile()).toContain('222')
    expect(reqTile()).not.toContain('111')
  })

  it('leaving and re-entering before the first read answers shows no data from the first visit', async () => {
    vi.useFakeTimers()
    const first = new Promise<StaffHealth>((res) => setTimeout(() => res(HEALTH), 5_000))
    health.mockImplementationOnce(() => first).mockImplementation(pending)
    render(<App />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(0)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await tick(0)
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(10_000)
    expect(screen.getByText('Loading system health…')).toBeTruthy()
    expect(screen.queryByText('1/1')).toBeNull()
  })

  it('under StrictMode the refresh keeps the last values and does not loop', async () => {
    vi.useFakeTimers()
    metrics.mockResolvedValue(WITH_REQ)
    render(<StrictMode><App /></StrictMode>)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: /^System health$/ }))
    await tick(0)
    const reads = health.mock.calls.length
    expect(reads).toBeLessThanOrEqual(2)
    health.mockImplementation(pending)
    await tick()
    expect(health).toHaveBeenCalledTimes(reads + 1)
    expect(screen.getByText('1/1')).toBeTruthy()
    expect(screen.queryByText(/Loading/)).toBeNull()
  })

  const setHidden = (hidden: boolean) => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
    document.dispatchEvent(new Event('visibilitychange'))
  }
  afterEach(() => { Reflect.deleteProperty(document, 'hidden') })

  it('a hidden tab skips the refresh ticks and reads once when it is visible again', async () => {
    await open()
    setHidden(true)
    await tick()
    await tick()
    await tick()
    expect(health).toHaveBeenCalledTimes(1)
    expect(metrics).toHaveBeenCalledTimes(1)
    setHidden(false)
    await tick(0)
    expect(health).toHaveBeenCalledTimes(2)
    expect(metrics).toHaveBeenCalledTimes(2)
    setHidden(false)
    await tick(0)
    expect(health).toHaveBeenCalledTimes(2)
  })

  it('a 403 stops the refresh', async () => {
    await open()
    health.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await tick()
    expect(health).toHaveBeenCalledTimes(2)
    await tick()
    await tick()
    expect(health).toHaveBeenCalledTimes(2)
    expect(metrics).toHaveBeenCalledTimes(2)
  })

  it('leaving System health drops the visibility listener', async () => {
    await open()
    setHidden(true)
    await tick()
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await tick(0)
    setHidden(false)
    await tick(0)
    expect(health).toHaveBeenCalledTimes(1)
    expect(metrics).toHaveBeenCalledTimes(1)
  })

  it('a 401 or a metrics-only 403 stops the refresh of both reads', async () => {
    for (const status of [401, 403]) {
      cleanup()
      vi.clearAllMocks()
      health.mockResolvedValue(HEALTH)
      metrics.mockResolvedValue(WITH_REQ)
      await open()
      metrics.mockRejectedValueOnce(new ApiError('http', 'denied', status))
      await tick()
      expect(metrics, `${status}`).toHaveBeenCalledTimes(2)
      await tick()
      await tick()
      expect(metrics, `${status}`).toHaveBeenCalledTimes(2)
      expect(health, `${status}`).toHaveBeenCalledTimes(2)
    }
  })

  it('a metrics 403 stops polling and a range change restarts it', async () => {
    await open()
    metrics.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await tick()
    await tick()
    expect(metrics).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('button', { name: '6h' }))
    await tick(0)
    expect(metrics).toHaveBeenCalledTimes(3)
    await tick()
    expect(metrics).toHaveBeenCalledTimes(4)
  })

  it('a 403 on a refresh tick shows the staff line', async () => {
    await open()
    health.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await tick()
    expect(screen.getByText('Your account is not ASComply staff.')).toBeTruthy()
    expect(screen.queryByText('1/1')).toBeNull()
  })
})
