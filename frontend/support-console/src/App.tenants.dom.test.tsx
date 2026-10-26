// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { fetchJobs } from './jobsApi'
import { fetchCodeListSummary, fetchRules, fetchVersions } from './rulesApi'
import { cancelTenantDeletion, deleteMemberAccount, fetchStaffTenant, fetchStaffTenants, previewMemberDeletion, purgeTenant, startTenantDeletion, type StaffTenant, type StaffTenantDetail } from './tenantsApi'

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
const seen: { selectedId: string | null; status: string; id?: string }[] = []
vi.mock('./components/Tenants', async (importOriginal) => {
  const m = await importOriginal<typeof import('./components/Tenants')>()
  return { ...m, Tenants: (p: Parameters<typeof m.Tenants>[0]) => (seen.push({ selectedId: p.selectedId, status: p.detailStatus, id: p.detail?.tenant.id }), m.Tenants(p)) }
})
vi.mock('./jobsApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./jobsApi')>()), fetchJobs: vi.fn() }))
vi.mock('./tenantsApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./tenantsApi')>()),
  fetchStaffTenants: vi.fn(),
  fetchStaffTenant: vi.fn(),
  startTenantDeletion: vi.fn(),
  cancelTenantDeletion: vi.fn(),
  purgeTenant: vi.fn(),
  previewMemberDeletion: vi.fn(),
  deleteMemberAccount: vi.fn(),
}))

const rulesRead = vi.mocked(fetchRules)
const versionsRead = vi.mocked(fetchVersions)
const summary = vi.mocked(fetchCodeListSummary)
const list = vi.mocked(fetchStaffTenants)
const one = vi.mocked(fetchStaffTenant)
const jobsRead = vi.mocked(fetchJobs)
const start = vi.mocked(startTenantDeletion)
const cancel = vi.mocked(cancelTenantDeletion)
const purge = vi.mocked(purgeTenant)
const preview = vi.mocked(previewMemberDeletion)
const delAccount = vi.mocked(deleteMemberAccount)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null, deletion_due_at: null, deletion_purge_started_at: null,
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
  jobsRead.mockResolvedValue([])
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

const DUE = '2026-11-25T10:00:00Z'
const STARTED = '2026-10-26T10:00:00Z'
const SCHEDULED = { ...X, deletion_due_at: DUE }
const PURGING = { ...X, deletion_due_at: DUE, deletion_purge_started_at: STARTED }
const openX = async (t: StaffTenant = X, names = ['Ada One', 'Bola Two', 'Chi Three']) => {
  list.mockResolvedValue([t, Y, H])
  one.mockImplementation(async (id) => (id === 'X' ? detail(t, names) : detail(Y, ['Yemi Only'])))
  render(<App />)
  await openTenants()
  await waitRow('X')
  fireEvent.click(rowOf('X'))
  await screen.findByText(names[0])
}
const btn = (name: string) => screen.getByRole('button', { name }) as HTMLButtonElement
const dialog = () => screen.getByRole('dialog')
const typeReason = (v: string) => fireEvent.change(within(dialog()).getByLabelText('Reason'), { target: { value: v } })
const headerButtons = () => Array.from((screen.getByTestId('tenant-actions').parentElement as HTMLElement).querySelectorAll('button')).map((b) => b.textContent)

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

  const removedDetail = (): StaffTenantDetail => ({
    tenant: X,
    members: [
      { user_id: 'r1', role: 'admin', status: 'removed', display_name: null, email: null },
      { user_id: 'r2', role: 'admin', status: 'removed', display_name: 'Ada Gone', email: null },
    ],
    invoices: [],
  })
  const openRemoved = async () => {
    one.mockResolvedValueOnce(removedDetail())
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada Gone')
  }

  it('tenant detail: a removed member has a REMOVED chip', async () => {
    await openRemoved()
    expect(screen.getAllByText('REMOVED')).toHaveLength(2)
  })

  it('tenant detail: a removed member shows no email', async () => {
    await openRemoved()
    expect(screen.getByText('Deleted member')).toBeTruthy()
    expect(document.querySelectorAll('.mono[style*="font-size: 10.5px"]')).toHaveLength(0)
    expect(screen.queryByText('r1')).toBeNull()
  })

  it('tenant detail: a removed row among active and suspended rows keeps its muted chip and its place', async () => {
    one.mockResolvedValueOnce({
      tenant: X,
      members: [
        { user_id: 'a1', role: 'admin', status: 'active', display_name: 'Ada One', email: 'ada@x.test' },
        { user_id: 'r1', role: 'preparer', status: 'removed', display_name: '   ', email: null },
        { user_id: 's1', role: 'admin', status: 'suspended', display_name: 'Sue Three', email: 'sue@x.test' },
      ],
      invoices: [],
    })
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    const chip = (label: string) => screen.getByText(label) as HTMLElement
    const chips = ['ACTIVE', 'REMOVED', 'SUSPENDED'].map(chip)
    expect(chips).toHaveLength(3)
    expect(chip('REMOVED').style.color).toBe(chip('ACTIVE').style.color)
    expect(chip('REMOVED').style.color).toContain('muted')
    expect(chip('REMOVED').parentElement!.style.background).toBe(chip('ACTIVE').parentElement!.style.background)
    expect(chip('SUSPENDED').style.color).not.toBe(chip('REMOVED').style.color)
    expect(screen.getByText('ada@x.test')).toBeTruthy()
    expect(screen.getByText('sue@x.test')).toBeTruthy()
  })

  it('a tenant with no members and no invoices says so and offers View jobs, Delete workspace and Purge now', async () => {
    one.mockResolvedValueOnce({ tenant: X, members: [], invoices: [] })
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('No members.')
    expect(screen.getByText('No invoices yet.')).toBeTruthy()
    const card = screen.getByRole('heading', { level: 2 }).closest('div[style*="overflow"]') as HTMLElement
    const buttons = Array.from(card.querySelectorAll('button'))
    expect(buttons.map((b) => b.textContent)).toEqual(['View jobs', 'Delete workspace', 'Purge now'])
  })

  it('tenant detail: a scheduled tenant shows its deletion date', async () => {
    await openX(SCHEDULED)
    expect(screen.getByText('Scheduled for deletion on 2026-11-25')).toBeTruthy()
    expect(screen.queryByText(/Purge in progress/)).toBeNull()
    cleanup()
    await openX(PURGING)
    expect(screen.getByText('Purge in progress since 2026-10-26 10:00 UTC')).toBeTruthy()
    expect(screen.queryByText(/Scheduled for deletion/)).toBeNull()
    cleanup()
    await openX(X)
    expect(screen.queryByText(/Scheduled for deletion|Purge in progress/)).toBeNull()
  })

  it('tenant detail: the actions follow the deletion state', async () => {
    const cases: [StaffTenant, string[], number][] = [
      [X, ['View jobs', 'Delete workspace', 'Purge now'], 3],
      [SCHEDULED, ['View jobs', 'Cancel deletion', 'Purge now'], 0],
      [PURGING, ['View jobs'], 0],
    ]
    for (const [t, names, rows] of cases) {
      await openX(t)
      expect(headerButtons(), t.deletion_due_at ?? 'unscheduled').toEqual(names)
      expect(screen.queryAllByRole('button', { name: 'Delete account' }), `${t.deletion_due_at} rows`).toHaveLength(rows)
      cleanup()
    }
    one.mockImplementation(async () => ({ tenant: X, members: [{ user_id: 'r1', role: 'preparer', status: 'removed', display_name: null, email: null }, { user_id: 'a1', role: 'admin', status: 'active', display_name: 'Ada One', email: null }], invoices: [] }))
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    await screen.findByText('Ada One')
    expect(screen.getAllByRole('button', { name: 'Delete account' })).toHaveLength(1)
  })

  it('purge now: the confirm waits for the exact name and a reason', async () => {
    await openX()
    fireEvent.click(btn('Purge now'))
    const confirm = () => within(dialog()).getByRole('button', { name: 'Purge now' }) as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    const name = within(dialog()).getByLabelText('Workspace name')
    fireEvent.change(name, { target: { value: 'okafor & partners' } })
    typeReason('')
    expect(confirm().disabled).toBe(true)
    fireEvent.change(name, { target: { value: 'Okafor & Partners' } })
    expect(confirm().disabled).toBe(true)
    typeReason('  customer wrote in  ')
    expect(confirm().disabled).toBe(false)
    fireEvent.change(name, { target: { value: 'Okafor & Partners ' } })
    expect(confirm().disabled).toBe(true)
    fireEvent.change(name, { target: { value: 'Okafor & Partners' } })
    purge.mockResolvedValue({ deletion_purge_started_at: STARTED })
    fireEvent.click(confirm())
    await waitFor(() => expect(purge).toHaveBeenCalledWith('X', 'Okafor & Partners', 'customer wrote in'))
    await screen.findByText('Purge started')
  })

  it('start deletion: posts the reason, toasts and re-reads', async () => {
    await openX()
    start.mockResolvedValue({ deletion_due_at: DUE })
    fireEvent.click(btn('Delete workspace'))
    expect(within(dialog()).getByText('Start deleting Okafor & Partners?')).toBeTruthy()
    expect(within(dialog()).queryByLabelText('Workspace name')).toBeNull()
    const confirm = within(dialog()).getByRole('button', { name: 'Start deletion' }) as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    typeReason(' by email ')
    const lists = list.mock.calls.length
    const reads = one.mock.calls.length
    fireEvent.click(confirm)
    await waitFor(() => expect(start).toHaveBeenCalledWith('X', 'by email'))
    await screen.findByText('Deletion scheduled')
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => {
      expect(list.mock.calls.length).toBe(lists + 1)
      expect(one.mock.calls.length).toBe(reads + 1)
    })
  })

  it('a 409 shows the server sentence and re-reads', async () => {
    await openX(SCHEDULED)
    cancel.mockRejectedValue(new ApiError('http', 'this workspace is already being deleted', 409))
    fireEvent.click(btn('Cancel deletion'))
    typeReason('changed mind')
    const reads = one.mock.calls.length
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Cancel deletion' }))
    await screen.findByText('this workspace is already being deleted')
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => expect(one.mock.calls.length).toBe(reads + 1))
    expect(cancel).toHaveBeenCalledWith('X', 'changed mind')
  })

  it('a 500 keeps the modal open and a 401 expires the session', async () => {
    await openX()
    start.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    fireEvent.click(btn('Delete workspace'))
    typeReason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Start deletion' }))
    await screen.findByText('boom')
    expect(screen.getByRole('dialog')).toBeTruthy()
    start.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Start deletion' }))
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })

  it('delete account: the modal names the heir, and the last member gets no confirm', async () => {
    await openX()
    preview.mockResolvedValue({ outcome: 'hand_on', heir_name: 'Bola', heir_user_id: 'X1' })
    delAccount.mockResolvedValue({ outcome: 'hand_on' })
    fireEvent.click(screen.getAllByRole('button', { name: 'Delete account' })[0])
    await within(dialog()).findByText(/Admin moves to Bola/)
    expect(preview).toHaveBeenCalledWith('X', 'X0')
    expect(within(dialog()).getByText("Delete Ada One's account?")).toBeTruthy()
    typeReason('asked')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Delete account' }))
    await waitFor(() => expect(delAccount).toHaveBeenCalledWith('X', 'X0', { outcome: 'hand_on', heir_user_id: 'X1', reason: 'asked' }))
    await screen.findByText('Account deleted')
    preview.mockResolvedValue({ outcome: 'delete_workspace', heir_name: null, heir_user_id: null })
    fireEvent.click(screen.getAllByRole('button', { name: 'Delete account' })[0])
    await within(dialog()).findByText('Ada One is the last active member. Start a workspace deletion instead.')
    expect(within(dialog()).queryByRole('button', { name: 'Delete account' })).toBeNull()
    expect(within(dialog()).getByRole('button', { name: 'Back' })).toBeTruthy()
  })

  it('delete account: the confirm waits for the preview', async () => {
    await openX()
    preview.mockReturnValue(new Promise(() => {}))
    fireEvent.click(screen.getAllByRole('button', { name: 'Delete account' })[0])
    expect(within(dialog()).getByText('Reading…')).toBeTruthy()
    typeReason('r')
    expect((within(dialog()).getByRole('button', { name: 'Delete account' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('an action in flight disables every action', async () => {
    await openX()
    let release!: (v: { deletion_due_at: string }) => void
    start.mockReturnValue(new Promise((res) => (release = res)))
    fireEvent.click(btn('Delete workspace'))
    typeReason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Start deletion' }))
    await waitFor(() => expect(start).toHaveBeenCalledTimes(1))
    expect((within(dialog()).getByRole('button', { name: 'Start deletion' }) as HTMLButtonElement).disabled).toBe(true)
    const behind = Array.from((screen.getByTestId('tenant-actions').parentElement as HTMLElement).querySelectorAll('button')).filter((b) => b.textContent !== 'View jobs')
    expect(behind.length).toBe(2)
    expect(behind.every((b) => b.disabled)).toBe(true)
    expect((screen.getAllByRole('button', { name: 'Delete account' }) as HTMLButtonElement[]).every((b) => b.disabled)).toBe(true)
    release({ deletion_due_at: DUE })
    await screen.findByText('Deletion scheduled')
  })

  it('View jobs opens Submissions filtered to the tenant', async () => {
    render(<App />)
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    fireEvent.click(await screen.findByRole('button', { name: 'View jobs' }))
    await screen.findByRole('heading', { level: 1, name: 'Submissions ops' })
    await waitFor(() => expect(jobsRead).toHaveBeenCalledWith(undefined, 'X'))
    expect((screen.getByLabelText('Tenant') as HTMLSelectElement).value).toBe('X')
    expect(screen.getByRole('button', { name: 'ALL' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('View jobs replaces an earlier state and tenant filter on Submissions', async () => {
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Submissions/ }))
    await screen.findByRole('heading', { level: 1, name: 'Submissions ops' })
    await waitFor(() => expect(screen.getByRole('option', { name: 'Yaba Freight' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'FAILED' }))
    fireEvent.change(screen.getByLabelText('Tenant'), { target: { value: 'Y' } })
    await waitFor(() => expect(jobsRead).toHaveBeenLastCalledWith('failed', 'Y'))
    await openTenants()
    await waitRow('X')
    fireEvent.click(rowOf('X'))
    fireEvent.click(await screen.findByRole('button', { name: 'View jobs' }))
    await screen.findByRole('heading', { level: 1, name: 'Submissions ops' })
    await waitFor(() => expect(jobsRead).toHaveBeenLastCalledWith(undefined, 'X'))
    expect((screen.getByLabelText('Tenant') as HTMLSelectElement).value).toBe('X')
    expect(screen.getByRole('button', { name: 'ALL' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'FAILED' }).getAttribute('aria-pressed')).toBe('false')
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
