// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { fetchCodeListSummary, fetchRules, fetchVersions } from './rulesApi'
import { fetchStaffTenant, fetchStaffTenants, type StaffTenant, type StaffTenantDetail } from './tenantsApi'

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
const seen: { selectedId: string | null; status: string; id?: string }[] = []
vi.mock('./components/Tenants', async (importOriginal) => {
  const m = await importOriginal<typeof import('./components/Tenants')>()
  return { ...m, Tenants: (p: Parameters<typeof m.Tenants>[0]) => (seen.push({ selectedId: p.selectedId, status: p.detailStatus, id: p.detail?.tenant.id }), m.Tenants(p)) }
})
vi.mock('./tenantsApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./tenantsApi')>()),
  fetchStaffTenants: vi.fn(),
  fetchStaffTenant: vi.fn(),
}))

const rulesRead = vi.mocked(fetchRules)
const versionsRead = vi.mocked(fetchVersions)
const summary = vi.mocked(fetchCodeListSummary)
const list = vi.mocked(fetchStaffTenants)
const one = vi.mocked(fetchStaffTenant)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const X = tenant('X', 'Okafor & Partners')
const Y = tenant('Y', 'Yaba Freight')
const H = tenant('H', 'Honeywell Ltd')
const detail = (t: StaffTenant, names: string[]): StaffTenantDetail => ({
  tenant: t,
  members: names.map((n, i) => ({ user_id: `${t.id}${i}`, role: 'admin', status: (['active', 'invited', 'suspended'] as const)[i % 3], display_name: n, email: `${n.toLowerCase().replace(/\s/g, '.')}@x.test` })),
  invoices: [{ id: `${t.id}i`, invoice_number: `INV-${t.id}`, status: 'validated', created_at: '2026-10-13T08:00:00Z' }],
})

beforeEach(() => {
  rulesRead.mockResolvedValue({ version: 7, rules: [] })
  versionsRead.mockResolvedValue({ today: '2026-10-11', versions: [] })
  summary.mockResolvedValue([])
  list.mockResolvedValue([X, Y, H])
  one.mockImplementation(async (id) => detail([X, Y, H].find((t) => t.id === id)!, id === 'X' ? ['Ada One', 'Bola Two', 'Chi Three'] : ['Yemi Only']))
})
afterEach(() => {
  seen.length = 0
  cleanup()
  vi.clearAllMocks()
})

const openTenants = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^Tenants/ }))
  await screen.findByRole('heading', { level: 1, name: 'Tenants' })
}
const rowOf = (id: string) => document.querySelector(`[data-tenant-id="${id}"]`) as HTMLButtonElement
const waitRow = async (id: string) => waitFor(() => expect(rowOf(id)).toBeTruthy())

describe('Console tenants wiring', () => {
  it('Tenants opens from the nav and reads the list once', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    expect(list).toHaveBeenCalledTimes(1)
    expect(one).not.toHaveBeenCalled()
    expect(screen.getByText('Select a tenant to see its members and recent invoices.')).toBeTruthy()
  })

  it('selecting a tenant reads and shows its detail', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    expect(one).toHaveBeenCalledTimes(1)
    expect(list).toHaveBeenCalledTimes(1)
    expect(one.mock.calls[0][0]).toBe('X')
    expect(rowOf('X').getAttribute('aria-pressed')).toBe('true')
    expect(rowOf('Y').getAttribute('aria-pressed')).toBe('false')
    expect(screen.getByRole('heading', { level: 2, name: 'Okafor & Partners' })).toBeTruthy()
    for (const n of ['Ada One', 'Bola Two', 'Chi Three']) expect(screen.getByText(n)).toBeTruthy()
    for (const s of ['ACTIVE', 'INVITED', 'SUSPENDED']) expect(screen.getByText(s)).toBeTruthy()
    expect(screen.getByText('INV-X')).toBeTruthy()
    expect(screen.getByText('2026-10-13')).toBeTruthy()
  })

  it('search narrows by name and keeps the selection', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    fireEvent.change(screen.getByLabelText('Search tenants'), { target: { value: 'hon' } })
    expect(document.querySelectorAll('[data-tenant-id]')).toHaveLength(1)
    expect(rowOf('H')).toBeTruthy()
    expect(screen.getByText('Ada One')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Search tenants'), { target: { value: 'zzz' } })
    expect(screen.getByText('No tenant matches.')).toBeTruthy()
    expect(screen.getByText('Ada One')).toBeTruthy()
  })

  it('switching tenants shows loading, never the previous detail', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    let release!: (d: StaffTenantDetail) => void
    one.mockImplementationOnce(() => new Promise((res) => (release = res)))
    fireEvent.click(rowOf('Y'))
    expect(screen.queryByText('Ada One')).toBeNull()
    expect(screen.getByText('Loading tenant…')).toBeTruthy()
    expect(one.mock.calls.at(-1)![0]).toBe('Y')
    release(detail(Y, ['Yemi Only']))
    await screen.findByText('Yemi Only')
    expect(screen.queryByText('Loading tenant…')).toBeNull()
    const forY = seen.filter((r) => r.selectedId === 'Y')
    expect(forY.length).toBeGreaterThan(0)
    expect(forY.filter((r) => r.status === 'ready' && r.id !== 'Y')).toEqual([])
  })

  it.each([[500, 'down'], [404, 'tenant not found'], [403, 'forbidden']])('after a %i detail failure for X, no render for Y shows an error state', async (code, msg) => {
    one.mockRejectedValueOnce(new ApiError('http', msg, code))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await waitFor(() => expect(seen.some((r) => r.selectedId === 'X' && ['error', 'not-found', 'forbidden'].includes(r.status))).toBe(true))
    seen.length = 0
    one.mockResolvedValueOnce(detail(Y, ['Yemi Only']))
    fireEvent.click(rowOf('Y'))
    await screen.findByText('Yemi Only')
    const forY = seen.filter((r) => r.selectedId === 'Y')
    expect(forY.length).toBeGreaterThan(0)
    expect(forY.filter((r) => ['error', 'not-found', 'forbidden'].includes(r.status))).toEqual([])
  })

  it('a failed list read retries; an empty list says so', async () => {
    list.mockRejectedValueOnce(new ApiError('http', 'boom', 500)).mockResolvedValueOnce([])
    render(<App />)
    await openTenants()
    await screen.findByText('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByText('No tenants yet.')
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('a 403 shows the staff line and a 404 detail says not found', async () => {
    list.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openTenants()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    cleanup()
    one.mockRejectedValueOnce(new ApiError('http', 'tenant not found', 404))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Tenant not found.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('a 401 on either read expires the session', async () => {
    list.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openTenants()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
    cleanup()
    list.mockResolvedValue([X])
    one.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(2))
  })

  it('leaving Rules for Tenants and back re-reads the rules', async () => {
    render(<App />)
    await waitFor(() => expect(rulesRead).toHaveBeenCalledTimes(1))
    expect(versionsRead).toHaveBeenCalledTimes(1)
    await openTenants()
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await waitFor(() => expect(rulesRead).toHaveBeenCalledTimes(2))
    expect(versionsRead).toHaveBeenCalledTimes(2)
  })

  it('re-entering Tenants re-reads the list and the selected tenant', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    fireEvent.click(screen.getByRole('button', { name: /^Tenants/ }))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(one).toHaveBeenCalledTimes(2))
  })

  it('a failed detail read retries and a late reply for an abandoned tenant is dropped', async () => {
    one.mockRejectedValueOnce(new ApiError('http', 'down', 500))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('down')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByText('Ada One')
    expect(one).toHaveBeenCalledTimes(2)
    expect(one.mock.calls[1][0]).toBe('X')

    let releaseY!: (d: StaffTenantDetail) => void
    one.mockImplementationOnce(() => new Promise((res) => (releaseY = res)))
    fireEvent.click(rowOf('Y'))
    fireEvent.click(rowOf('H'))
    await screen.findByRole('heading', { level: 2, name: 'Honeywell Ltd' })
    releaseY(detail(Y, ['Yemi Only']))
    await Promise.resolve()
    expect(screen.getByRole('heading', { level: 2, name: 'Honeywell Ltd' })).toBeTruthy()
    expect(screen.queryByRole('heading', { level: 2, name: 'Yaba Freight' })).toBeNull()
    expect(rowOf('H').getAttribute('aria-pressed')).toBe('true')
    expect(rowOf('Y').getAttribute('aria-pressed')).toBe('false')
  })

  it('a late failure for an abandoned tenant never hides the current tenant error', async () => {
    let failX!: (e: Error) => void
    one.mockImplementationOnce(() => new Promise((_, rej) => (failX = rej)))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    one.mockRejectedValueOnce(new ApiError('http', 'y down', 500))
    fireEvent.click(rowOf('Y'))
    await screen.findByText('y down')
    failX(new ApiError('http', 'x down', 500))
    await new Promise((r) => setTimeout(r, 20))
    fireEvent.change(screen.getByLabelText('Search tenants'), { target: { value: 'a' } })
    expect(screen.getByText('y down')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('a tenant with no members and no invoices says so, offers no action and no View jobs', async () => {
    one.mockResolvedValueOnce({ tenant: X, members: [], invoices: [] })
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('No members.')
    expect(screen.getByText('No invoices yet.')).toBeTruthy()
    expect(screen.queryByText('View jobs')).toBeNull()
    const card = screen.getByRole('heading', { level: 2 }).closest('div[style*="overflow"]') as HTMLElement
    expect(card.querySelectorAll('button')).toHaveLength(0)
  })

  it('an empty list shows its line, no rows, no search field and the detail prompt', async () => {
    list.mockResolvedValueOnce([])
    render(<App />)
    await openTenants()
    await screen.findByText('No tenants yet.')
    expect(document.querySelectorAll('[data-tenant-id]')).toHaveLength(0)
    expect(screen.queryByLabelText('Search tenants')).toBeNull()
    expect(screen.getByText('Select a tenant to see its members and recent invoices.')).toBeTruthy()
    expect(one).not.toHaveBeenCalled()
  })
})
