// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { expireSession } from './auth'
import { cancelJob, fetchJob, fetchJobs, JOB_STATES, redriveJob, type JobDetail, type StaffJob } from './jobsApi'
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
vi.mock('./jobsApi', async (importOriginal) => ({ ...(await importOriginal<typeof import('./jobsApi')>()), fetchJobs: vi.fn(), fetchJob: vi.fn(), redriveJob: vi.fn(), cancelJob: vi.fn() }))

const jobsRead = vi.mocked(fetchJobs)
const tenantsRead = vi.mocked(fetchStaffTenants)
const jobRead = vi.mocked(fetchJob)
const redrive = vi.mocked(redriveJob)
const cancel = vi.mocked(cancelJob)

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const job = (id: string, over: Partial<StaffJob> = {}): StaffJob => ({
  id, tenant_id: 'T1', tenant_name: 'Okafor & Partners', invoice_id: `${id}i`, invoice_number: `INV-${id}`, invoice_status: 'failed',
  state: 'dead_lettered', attempts: 5, max_attempts: 5, last_error: 'APP timeout', acknowledged: false,
  created_at: new Date(Date.now() - 3 * 3600 * 1000).toISOString(), updated_at: '2026-10-14T08:00:00Z', ...over,
})

const detail = (j: StaffJob): JobDetail => ({
  job: j,
  history: [
    { from_status: null, to_status: 'queued', actor: 'system', changed_at: '2026-10-14T07:00:00Z' },
    { from_status: 'queued', to_status: 'submitting', actor: 'system', changed_at: '2026-10-14T07:01:00Z' },
    { from_status: 'submitting', to_status: 'failed', actor: 'system', changed_at: '2026-10-14T07:02:00Z' },
  ],
})

beforeEach(() => {
  jobRead.mockImplementation(async (id) => detail(id === 'a' ? job('a', { attempts: 8, max_attempts: 8 }) : job('b', { state: 'queued', attempts: 0, last_error: null })))
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

  it('a tenant-select load failure shows the error with a retry', async () => {
    tenantsRead.mockRejectedValueOnce(new ApiError('http', 'tenants down', 500))
    render(<App />)
    await openSubmissions()
    await screen.findByText('tenants down')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByRole('option', { name: 'Yaba Freight' })
    expect(screen.queryByText('tenants down')).toBeNull()
  })
})

const openDrawer = async (id: string) => {
  render(<App />)
  await openSubmissions()
  await waitFor(() => expect(rows()).toHaveLength(2))
  fireEvent.click(rows().find((r) => r.dataset.jobId === id) as HTMLElement)
  return screen.findByRole('dialog')
}
const NRS = 'The Access Point acknowledged this submission. Confirm with NRS before re-sending.'

describe('Console job drawer', () => {
  it('a row opens the job drawer', async () => {
    jobRead.mockResolvedValue(detail(job('a', { attempts: 8, max_attempts: 8, created_at: '2026-10-14T05:30:00Z' })))
    const d = await openDrawer('a')
    await within(d).findByText('8 / 8')
    expect(jobRead).toHaveBeenCalledWith('a')
    expect(within(d).getByText('APP timeout')).toBeTruthy()
    const cell = (label: string) => within(d).getByText(label).nextElementSibling?.textContent
    expect([cell('Tenant'), cell('Invoice'), cell('Invoice status'), cell('Attempts'), cell('Created'), cell('Updated')]).toEqual([
      'Okafor & Partners', 'INV-a', 'failed', '8 / 8', '2026-10-14 05:30 UTC', '2026-10-14 08:00 UTC',
    ])
    const steps = within(within(d).getByRole('list', { name: 'Invoice timeline' })).getAllByRole('listitem')
    expect(steps.map((li) => li.textContent)).toEqual([
      'queuedsystem · 2026-10-14 07:00 UTC', 'queued → submittingsystem · 2026-10-14 07:01 UTC', 'submitting → failedsystem · 2026-10-14 07:02 UTC',
    ])
  })

  it('the drawer offers only the action its state allows', async () => {
    const want = (state: string, ack: boolean): [string[], boolean] =>
      state === 'dead_lettered' ? (ack ? [[], true] : [['Re-drive'], false]) : state === 'queued' ? [['Cancel job'], false] : [[], false]
    for (const state of JOB_STATES) {
      for (const ack of state === 'dead_lettered' ? [false, true] : [false]) {
        jobRead.mockResolvedValue(detail(job('a', { state, acknowledged: ack })))
        const d = await openDrawer('a')
        await within(d).findByText('APP timeout')
        const [buttons, nrs] = want(state, ack)
        expect(within(d).queryAllByRole('button').map((b) => b.textContent?.trim()).filter((t) => t === 'Re-drive' || t === 'Cancel job'), `${state} ${ack}`).toEqual(buttons)
        expect(within(d).queryByText(NRS) !== null, `${state} ${ack} NRS`).toBe(nrs)
        cleanup()
      }
    }
  })

  const openRedrive = async () => {
    const d = await openDrawer('a')
    fireEvent.click(await within(d).findByRole('button', { name: /Re-drive/ }))
    return screen.findByLabelText('Reason')
  }
  const confirmBtn = () => screen.getByRole('button', { name: 'Re-drive job' }) as HTMLButtonElement

  it('the confirm blocks until the reason is valid', async () => {
    const reason = await openRedrive()
    expect(confirmBtn().disabled).toBe(true)
    fireEvent.change(reason, { target: { value: '  ' } })
    expect(confirmBtn().disabled).toBe(true)
    fireEvent.change(reason, { target: { value: 'x'.repeat(501) } })
    expect(confirmBtn().disabled).toBe(true)
    fireEvent.change(reason, { target: { value: 'x' } })
    expect(confirmBtn().disabled).toBe(false)
    expect(screen.getByText('The customer sees this reason in their audit log, from ASComply support. Leave out names, emails and internal notes.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Back' })).toBeTruthy()
  })

  it('the confirm sends the trimmed reason', async () => {
    redrive.mockResolvedValue(detail(job('a')))
    fireEvent.change(await openRedrive(), { target: { value: '  APP back up  ' } })
    fireEvent.click(confirmBtn())
    await waitFor(() => expect(redrive).toHaveBeenCalledWith('a', 'APP back up'))
  })

  it('a successful action toasts and re-reads', async () => {
    let release: (v: JobDetail) => void = () => {}
    redrive.mockReturnValue(new Promise<JobDetail>((r) => (release = r)))
    fireEvent.change(await openRedrive(), { target: { value: 'APP back up' } })
    const listCalls = jobsRead.mock.calls.length
    const oneCalls = jobRead.mock.calls.length
    fireEvent.click(confirmBtn())
    await waitFor(() => expect(confirmBtn().disabled).toBe(true))
    fireEvent.click(confirmBtn())
    expect(redrive).toHaveBeenCalledTimes(1)
    release(detail(job('a')))
    await screen.findByText('Job re-driven')
    await waitFor(() => expect(jobsRead.mock.calls.length).toBe(listCalls + 1))
    await waitFor(() => expect(jobRead.mock.calls.length).toBe(oneCalls + 1))
    expect(screen.queryByLabelText('Reason')).toBeNull()
  })

  it('a refused action shows the server message', async () => {
    cancel.mockRejectedValue(new ApiError('http', 'only a queued job can be cancelled', 409))
    const d = await openDrawer('b')
    const listCalls = jobsRead.mock.calls.length
    const oneCalls = jobRead.mock.calls.length
    fireEvent.click(await within(d).findByRole('button', { name: 'Cancel job' }))
    fireEvent.change(await screen.findByLabelText('Reason'), { target: { value: 'duplicate' } })
    const dlg = screen.getAllByRole('dialog').at(-1) as HTMLElement
    fireEvent.click(within(dlg).getByRole('button', { name: 'Cancel job' }))
    await screen.findByText('only a queued job can be cancelled')
    await waitFor(() => expect(screen.queryByLabelText('Reason')).toBeNull())
    expect(within(screen.getByRole('dialog')).getAllByText('INV-b')).toHaveLength(2)
    await waitFor(() => expect(jobsRead.mock.calls.length).toBe(listCalls + 1))
    await waitFor(() => expect(jobRead.mock.calls.length).toBe(oneCalls + 1))
  })

  it('a cancel sends the trimmed reason and toasts; a 404 shows its message and re-reads; a 401 expires the session', async () => {
    cancel.mockResolvedValueOnce(detail(job('b')))
    const d = await openDrawer('b')
    const start = (reason: string) => async () => {
      fireEvent.click(await within(d).findByRole('button', { name: 'Cancel job' }))
      fireEvent.change(await screen.findByLabelText('Reason'), { target: { value: reason } })
      const dlg = screen.getAllByRole('dialog').at(-1) as HTMLElement
      fireEvent.click(within(dlg).getByRole('button', { name: 'Cancel job' }))
    }
    await start('  dup  ')()
    await screen.findByText('Job cancelled')
    expect(cancel).toHaveBeenLastCalledWith('b', 'dup')
    await waitFor(() => expect(screen.queryByLabelText('Reason')).toBeNull())

    cancel.mockRejectedValueOnce(new ApiError('http', 'job not found', 404))
    const listCalls = jobsRead.mock.calls.length
    await start('gone')()
    await screen.findByText('job not found')
    await waitFor(() => expect(screen.queryByLabelText('Reason')).toBeNull())
    await waitFor(() => expect(jobsRead.mock.calls.length).toBeGreaterThan(listCalls))

    cancel.mockRejectedValueOnce(new ApiError('http', 'expired', 401))
    await start('late')()
    await waitFor(() => expect(expireSession).toHaveBeenCalledTimes(1))
  })
})
