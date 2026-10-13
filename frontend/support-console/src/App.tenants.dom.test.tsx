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
    release(detail(Y, ['Yemi Only']))
    await screen.findByText('Yemi Only')
    expect(screen.queryByText('Loading tenant…')).toBeNull()
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
})
