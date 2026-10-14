// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { fetchAuditEvents, fetchStaffChanges, type StaffAuditPage, type StaffAuditRow, type StaffChange } from './auditApi'
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
vi.mock('./auditApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./auditApi')>()), fetchAuditEvents: vi.fn(), fetchStaffChanges: vi.fn() }))

const auditRead = vi.mocked(fetchAuditEvents)
const tenantsRead = vi.mocked(fetchStaffTenants)
const changesRead = vi.mocked(fetchStaffChanges)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const row = (id: string, over: Partial<StaffAuditRow> = {}): StaffAuditRow => ({
  id, tenant_id: 'T1', tenant_name: 'Okafor & Partners', created_at: '2026-10-14T09:12:03.123456Z', event: 'invoice.created',
  actor: '5b8f3c1e-0000-4000-8000-000000000001', actor_name: 'Ada Obi', actor_kind: 'person', payload: { invoice: id, lines: [1, 2] }, ...over,
})
const change = (id: string, over: Partial<StaffChange> = {}): StaffChange => ({
  id, actor: '5b8f3c1e-0000-4000-8000-0000000000aa', event: 'tenant.demo_set', rule_set_version_id: null, tenant_id: 'T1', tenant_name: 'Okafor & Partners',
  subject_id: 'S1', payload: { demo: true }, created_at: '2026-10-14T23:30:00.5Z', ...over,
})
const page = (events: StaffAuditRow[], next: string | null = null): StaffAuditPage => ({ events, page: { limit: 50, has_more: next !== null, next_cursor: next } })

beforeEach(() => {
  vi.mocked(fetchRules).mockResolvedValue({ version: 7, rules: [] })
  vi.mocked(fetchVersions).mockResolvedValue({ today: '2026-10-11', versions: [] })
  vi.mocked(fetchCodeListSummary).mockResolvedValue([])
  tenantsRead.mockResolvedValue([tenant('T1', 'Okafor & Partners'), tenant('T2', 'Yaba Freight')])
  changesRead.mockResolvedValue([change('c1'), change('c2', { event: 'rules.published', tenant_id: null, tenant_name: null, subject_id: null, rule_set_version_id: 'V9' })])
  auditRead.mockResolvedValue(page([row('e1'), row('e2', { event: 'invoice.sent', actor: 'system', actor_name: 'System', actor_kind: 'system' })]))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const openAudit = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^Audit/ }))
  await screen.findByRole('heading', { level: 1, name: 'Audit & evidence explorer' })
}
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-audit-id]'))
const ids = () => rows().map((r) => r.dataset.auditId)
const typeAndEnter = (label: string, value: string) => {
  const input = screen.getByLabelText(label)
  fireEvent.change(input, { target: { value } })
  fireEvent.keyDown(input, { key: 'Enter' })
}

describe('Console audit wiring', () => {
  it('Audit opens from the nav and reads the first page once', async () => {
    render(<App />)
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(auditRead).toHaveBeenCalledTimes(1)
    expect(auditRead.mock.calls[0][0]).toEqual({})
    expect(auditRead.mock.calls[0][1]).toBeUndefined()
    const cells = rows()[0].children
    expect(cells).toHaveLength(5)
    expect(Array.from(cells).slice(0, 4).map((c) => c.textContent)).toEqual(['2026-10-14 09:12:03 UTC', 'Okafor & Partners', 'Ada Obi', 'invoice.created'])
    expect(Array.from(rows()[1].children).slice(2, 4).map((c) => c.textContent)).toEqual(['System', 'invoice.sent'])
  })

  it('the filters re-read with only the set values', async () => {
    render(<App />)
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(2))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    const last = () => auditRead.mock.calls.at(-1)![0]

    fireEvent.change(screen.getByLabelText('Tenant'), { target: { value: 'T2' } })
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(2))
    expect(last()).toEqual({ tenant: 'T2' })

    typeAndEnter('Event', 'invoice.created')
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(3))
    expect(last()).toEqual({ tenant: 'T2', event: 'invoice.created' })

    fireEvent.change(screen.getByLabelText('Actor'), { target: { value: ' system ' } })
    expect(auditRead).toHaveBeenCalledTimes(3)
    fireEvent.blur(screen.getByLabelText('Actor'))
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(4))
    expect(last()).toEqual({ tenant: 'T2', event: 'invoice.created', actor: 'system' })

    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-10-01' } })
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(5))
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-03' } })
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(6))
    expect(last()).toEqual({ tenant: 'T2', event: 'invoice.created', actor: 'system', from: '2026-10-01', to: '2026-10-03' })

    fireEvent.change(screen.getByLabelText('Event'), { target: { value: 'x' } })
    await new Promise((r) => setTimeout(r, 30))
    expect(auditRead).toHaveBeenCalledTimes(6)
  })

  it('Load more appends and hides at the end', async () => {
    auditRead.mockResolvedValueOnce(page([row('a1'), row('a2')], 'CUR1'))
    auditRead.mockResolvedValueOnce(page([row('a3')], null))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(ids()).toEqual(['a1', 'a2']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(ids()).toEqual(['a1', 'a2', 'a3']))
    expect(auditRead.mock.calls[1]).toEqual([{}, 'CUR1'])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()

    auditRead.mockResolvedValueOnce(page([row('b1')], null))
    typeAndEnter('Event', 'invoice.sent')
    await waitFor(() => expect(ids()).toEqual(['b1']))
    expect(auditRead).toHaveBeenCalledTimes(3)
    expect(auditRead.mock.calls[2]).toEqual([{ event: 'invoice.sent' }])
  })

  it('a late Load more page is dropped after a filter change', async () => {
    let release: (p: StaffAuditPage) => void = () => {}
    auditRead.mockResolvedValueOnce(page([row('a1')], 'CUR1'))
    auditRead.mockReturnValueOnce(new Promise<StaffAuditPage>((r) => (release = r)))
    auditRead.mockResolvedValueOnce(page([row('b1')], null))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(ids()).toEqual(['a1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(2))
    typeAndEnter('Event', 'invoice.sent')
    await waitFor(() => expect(ids()).toEqual(['b1']))
    release(page([row('late')], null))
    await new Promise((r) => setTimeout(r, 30))
    expect(ids()).toEqual(['b1'])
  })

  // The late success is also hidden by the display's key filter; only a late failure observes the staleness guard.
  it('a late failed Load more raises no toast and no session expiry after a filter change', async () => {
    let fail: (e: unknown) => void = () => {}
    auditRead.mockResolvedValueOnce(page([row('a1')], 'CUR1'))
    auditRead.mockReturnValueOnce(new Promise<StaffAuditPage>((_, rej) => (fail = rej)))
    auditRead.mockResolvedValueOnce(page([row('b1')], null))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(ids()).toEqual(['a1']))
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(2))
    typeAndEnter('Event', 'invoice.sent')
    await waitFor(() => expect(ids()).toEqual(['b1']))
    fail(new ApiError('http', 'late boom', 500))
    await new Promise((r) => setTimeout(r, 30))
    expect(screen.queryByText('late boom')).toBeNull()
    expect(expireSession).not.toHaveBeenCalled()
  })

  it('a row opens the entry drawer', async () => {
    const payload = { invoice: 'e1', lines: [1, 2], note: null }
    auditRead.mockResolvedValue(page([row('e1', { payload })]))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(1))
    fireEvent.click(rows()[0])
    const d = await screen.findByRole('dialog')
    const cell = (label: string) => within(d).getByText(label).nextElementSibling?.textContent
    expect([cell('Tenant'), cell('Actor'), cell('Actor id'), cell('Time')]).toEqual([
      'Okafor & Partners', 'Ada Obi', '5b8f3c1e-0000-4000-8000-000000000001', '2026-10-14 09:12:03 UTC',
    ])
    expect(within(d).getByText('invoice.created')).toBeTruthy()
    expect(within(d).getByText('#e1 · 2026-10-14 09:12:03 UTC')).toBeTruthy()
    expect(d.querySelector('pre')?.textContent).toBe(JSON.stringify(payload, null, 2))
  })

  it('a row whose tenant is gone shows its id', async () => {
    auditRead.mockResolvedValue(page([row('e1', { tenant_name: null, tenant_id: 'TENANT-GONE' })]))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(rows()[0].children[1].textContent).toBe('TENANT-GONE')
  })

  it('empty, forbidden, error and expired states', async () => {
    auditRead.mockResolvedValueOnce(page([]))
    render(<App />)
    await openAudit()
    await screen.findByText('No audit entries match these filters.')
    cleanup()

    auditRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openAudit()
    await screen.findByText('Your account is not ASComply staff.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    cleanup()

    auditRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openAudit()
    await screen.findByText('boom')
    const before = auditRead.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(auditRead).toHaveBeenCalledTimes(before + 1)
    cleanup()

    auditRead.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openAudit()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })

  it('an inverted range sends no read', async () => {
    render(<App />)
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-10-05' } })
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(2))
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-01' } })
    await screen.findByText('End date must be on or after the start date.')
    await new Promise((r) => setTimeout(r, 30))
    expect(auditRead).toHaveBeenCalledTimes(2)
    expect(rows()).toHaveLength(0)
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-06' } })
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(3))
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(screen.queryByText('End date must be on or after the start date.')).toBeNull()
  })

  it('a failed tenant read leaves the log usable', async () => {
    tenantsRead.mockRejectedValueOnce(new ApiError('http', 'tenants down', 500))
    render(<App />)
    await openAudit()
    await screen.findByText('tenants down')
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    expect(screen.queryByText('tenants down')).toBeNull()
  })

  const openStaff = async () => {
    await openAudit()
    await waitFor(() => expect(rows()).toHaveLength(2))
    fireEvent.click(screen.getByRole('button', { name: 'STAFF CHANGES' }))
  }
  const changeRows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-change-id]'))

  it('the staff view lists every change', async () => {
    render(<App />)
    await openStaff()
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    expect(changesRead).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'STAFF CHANGES' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'TENANT LOG' }).getAttribute('aria-pressed')).toBe('false')
    expect(Array.from(changeRows()[0].children).slice(0, 4).map((c) => c.textContent)).toEqual(['2026-10-14 23:30:00 UTC', '5b8f3c1e-0000-4000-8000-0000000000aa', 'tenant.demo_set', 'Okafor & Partners'])
    expect(changeRows()[1].children[3].textContent).toBe('All tenants')
  })

  it('the change drawer offers the link only for a tenant row', async () => {
    render(<App />)
    await openStaff()
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    fireEvent.click(changeRows()[0])
    let d = await screen.findByRole('dialog')
    expect(within(d).getByRole('button', { name: 'Open in tenant log' })).toBeTruthy()
    const cell = (label: string) => within(d).getByText(label).nextElementSibling?.textContent
    expect([cell('Staff member'), cell('Tenant'), cell('Subject'), cell('Rule set version'), cell('Time')]).toEqual([
      '5b8f3c1e-0000-4000-8000-0000000000aa', 'Okafor & Partners', 'S1', '—', '2026-10-14 23:30:00 UTC',
    ])
    expect(d.querySelector('pre')?.textContent).toBe(JSON.stringify({ demo: true }, null, 2))
    cleanup()

    render(<App />)
    await openStaff()
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    fireEvent.click(changeRows()[1])
    d = await screen.findByRole('dialog')
    expect(within(d).queryByRole('button', { name: 'Open in tenant log' })).toBeNull()
    expect(within(d).getByText('Rule set version').nextElementSibling?.textContent).toBe('V9')
    expect(within(d).getByText('Tenant').nextElementSibling?.textContent).toBe('All tenants')
  })

  it('Open in tenant log lands on the matching row', async () => {
    render(<App />)
    await openStaff()
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    auditRead.mockResolvedValueOnce(page([row('x1', { event: 'tenant.demo_set', created_at: '2026-10-14T23:30:00.1Z' }), row('x2', { event: 'tenant.demo_set', created_at: '2026-10-14T23:30:00.5Z' })]))
    fireEvent.click(changeRows()[0])
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Open in tenant log' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'TENANT LOG' }).getAttribute('aria-pressed')).toBe('true'))
    await waitFor(() => expect(auditRead).toHaveBeenCalledTimes(2))
    expect(auditRead.mock.calls[1][0]).toEqual({ tenant: 'T1', event: 'tenant.demo_set', from: '2026-10-14', to: '2026-10-14' })
    const d = await screen.findByRole('dialog')
    expect(within(d).getByText('#x2 · 2026-10-14 23:30:00 UTC')).toBeTruthy()
  })

  it('Open in tenant log with no match keeps the list', async () => {
    render(<App />)
    await openStaff()
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    auditRead.mockResolvedValueOnce(page([row('x1', { event: 'tenant.demo_set', created_at: '2026-10-14T23:30:00.1Z' })]))
    fireEvent.click(changeRows()[0])
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Open in tenant log' }))
    await waitFor(() => expect(ids()).toEqual(['x1']))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('staff view empty, forbidden, error and expired', async () => {
    changesRead.mockResolvedValueOnce([])
    render(<App />)
    await openStaff()
    await screen.findByText('No staff changes yet.')
    cleanup()

    changesRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openStaff()
    await screen.findByText('Your account is not ASComply staff.')
    cleanup()

    changesRead.mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<App />)
    await openStaff()
    await screen.findByText('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(changeRows()).toHaveLength(2))
    cleanup()

    changesRead.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    render(<App />)
    await openStaff()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })
})
