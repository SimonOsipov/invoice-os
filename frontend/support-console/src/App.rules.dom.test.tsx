// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { KillConfirm } from './components/KillConfirm'
import { signOutConsole } from '@invoice-os/console-session'
import { SAMPLE_INVOICE, addDraftRule, editDraftRule, fetchCodeListSummary, fetchCodeListSyncs, fetchRules, fetchVersions, openDraft, publishDraft, removeDraftRule, switchRule, testDraft, type RuleVersion } from './rulesApi'
import type { Rule } from './types'

vi.mock('@invoice-os/console-session', () => ({
  StaffGate: ({ children }: { children: ReactNode }) => children,
  signOutConsole: vi.fn(),
}))
vi.mock('./rulesApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./rulesApi')>()),
  fetchRules: vi.fn(),
  fetchCodeListSummary: vi.fn(),
  fetchCodeListSyncs: vi.fn(),
  fetchVersions: vi.fn(),
  switchRule: vi.fn(),
  openDraft: vi.fn(),
  addDraftRule: vi.fn(),
  editDraftRule: vi.fn(),
  removeDraftRule: vi.fn(),
  testDraft: vi.fn(),
  publishDraft: vi.fn(),
}))

const read = vi.mocked(fetchRules)
const versionsRead = vi.mocked(fetchVersions)
const summary = vi.mocked(fetchCodeListSummary)
const syncsRead = vi.mocked(fetchCodeListSyncs)
const sw = vi.mocked(switchRule)
const open = vi.mocked(openDraft)
const add = vi.mocked(addDraftRule)
const edit = vi.mocked(editDraftRule)
const remove = vi.mocked(removeDraftRule)
const test = vi.mocked(testDraft)
const publish = vi.mocked(publishDraft)
const rule = (key: string, enabled: boolean): Rule => ({ key, type: 'cel', field: 'invoice.total', severity: 'error', scope: 'global', enabled, message: 'm', params: {}, when: null })
const ver = (version: number, state: RuleVersion['state'], effective_from: string | null = '2026-01-01'): RuleVersion => ({ rule_set_version_id: `id${version}`, version, state, effective_from, opened_at: null, rule_count: 2 })
const VERSIONS = { today: '2026-10-11', versions: [ver(8, 'draft', null), ver(7, 'in_force'), ver(3, 'retired', null)] }
const LIST = { version: 7, rules: [rule('real.on', true), rule('real.off', false)] }

beforeEach(() => {
  read.mockResolvedValue(LIST)
  versionsRead.mockResolvedValue(VERSIONS)
  summary.mockResolvedValue([])
  syncsRead.mockResolvedValue([])
  sw.mockResolvedValue({ key: 'real.on', enabled: false })
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const openRules = async () => {
  fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
  await screen.findByRole('switch', { name: 'Disable real.on' })
}
const dialog = () => screen.getAllByRole('dialog').at(-1) as HTMLElement
const reason = (text: string) => fireEvent.change(within(dialog()).getByLabelText('Reason'), { target: { value: text } })
const toast = () => screen.getByRole('status')

describe('KillConfirm', () => {
  it('stays disabled while busy even with a valid reason', () => {
    const props = { ruleKey: 'k', action: 'disable' as const, onClose: () => undefined, onConfirm: () => undefined }
    const { rerender } = render(<KillConfirm {...props} busy={false} />)
    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'why' } })
    expect((screen.getByRole('button', { name: 'Disable rule' }) as HTMLButtonElement).disabled).toBe(false)
    rerender(<KillConfirm {...props} busy />)
    expect((screen.getByRole('button', { name: 'Disabling…' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('Console rules wiring', () => {
  it('selecting a version reads its rules', async () => {
    render(<App />)
    await openRules()
    expect(read).toHaveBeenLastCalledWith(undefined)
    read.mockResolvedValue({ version: 3, rules: [rule('old.key', true)] })
    fireEvent.click(await screen.findByRole('button', { name: /^v3/ }))
    await screen.findByText('old.key')
    expect(read).toHaveBeenLastCalledWith(3)
    expect(screen.getByText('RETIRED v3')).toBeTruthy()
    const sw = screen.getByRole('switch', { name: 'Disable old.key' }) as HTMLButtonElement
    expect(sw.disabled).toBe(true)
    fireEvent.click(sw)
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByText('old.key'))
    expect(screen.queryByRole('button', { name: /Kill-switch/ })).toBeNull()
    read.mockResolvedValue(LIST)
    fireEvent.click(screen.getByRole('button', { name: /^v7/ }))
    await screen.findByRole('switch', { name: 'Disable real.on' })
    expect(read).toHaveBeenLastCalledWith(undefined)
    expect(screen.getByText('IN FORCE v7')).toBeTruthy()
  })

  it('the version in force is selected on open and a failed versions read keeps the table live', async () => {
    render(<App />)
    await openRules()
    const pressed = (name: RegExp) => screen.getByRole('button', { name }).getAttribute('aria-pressed')
    expect([pressed(/^v7/), pressed(/^v8/), pressed(/^v3/)]).toEqual(['true', 'false', 'false'])
    cleanup()
    versionsRead.mockRejectedValue(new ApiError('http', 'versions down', 500))
    render(<App />)
    await openRules()
    expect(await screen.findByText('versions down')).toBeTruthy()
    expect(screen.queryAllByRole('button', { pressed: true })).toHaveLength(0)
    expect((screen.getByRole('switch', { name: 'Disable real.on' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.getByText('IN FORCE v7')).toBeTruthy()
  })

  it('a 401 on the versions read signs the console out', async () => {
    versionsRead.mockRejectedValue(new ApiError('http', 'expired', 401))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await waitFor(() => expect(signOutConsole).toHaveBeenCalledTimes(1))
  })

  it('opens on Rules and reads the list, versions and code lists once', async () => {
    render(<App />)
    await screen.findByRole('switch', { name: 'Disable real.on' })
    const counts = () => [read.mock.calls.length, versionsRead.mock.calls.length, summary.mock.calls.length]
    expect(counts()).toEqual([1, 1, 1])
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    expect(counts()).toEqual([1, 1, 1])
  })

  it('a non-staff account lands on the not-staff line', async () => {
    read.mockRejectedValue(new ApiError('http', 'forbidden', 403))
    render(<App />)
    expect(await screen.findAllByText('Your account is not ASComply staff.')).not.toHaveLength(0)
    expect(screen.queryByRole('switch')).toBeNull()
    expect(signOutConsole).not.toHaveBeenCalled()
  })

  it('shows the real enabled state and version, and none of the mock keys', async () => {
    render(<App />)
    await openRules()
    expect(screen.getByText('IN FORCE v7')).toBeTruthy()
    expect(screen.getByRole('switch', { name: 'Disable real.on' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('switch', { name: 'Enable real.off' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.queryByText('vat.rate.taxmath')).toBeNull()
  })

  it('opens the confirm for a switch in either direction', async () => {
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    expect(within(dialog()).getByText('Disable a live rule?')).toBeTruthy()
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Enable real.off' }))
    expect(within(dialog()).getByText('Enable this rule?')).toBeTruthy()
  })

  it('keeps the confirm disabled until the reason is 1..500 code points', async () => {
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    const btn = within(dialog()).getByRole('button', { name: 'Disable rule' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    reason('   ')
    expect(btn.disabled).toBe(true)
    reason('😀'.repeat(500))
    expect(btn.disabled).toBe(false)
    reason('😀'.repeat(501))
    expect(btn.disabled).toBe(true)
  })

  it('confirming PATCHes once with the trimmed reason, toasts AUDITED red, and re-reads', async () => {
    let done!: () => void
    sw.mockReturnValue(new Promise((r) => (done = () => r({ key: 'real.on', enabled: false }))))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('  bad rule  ')
    const btn = within(dialog()).getByRole('button', { name: 'Disable rule' })
    fireEvent.click(btn)
    expect(sw).toHaveBeenCalledWith('real.on', false, 'bad rule')
    const busy = await within(dialog()).findByRole('button', { name: 'Disabling…' })
    fireEvent.click(busy)
    fireEvent.click(screen.getByRole('switch', { name: 'Enable real.off' }))
    expect(sw).toHaveBeenCalledTimes(1)
    expect((screen.getByRole('switch', { name: 'Enable real.off' }) as HTMLButtonElement).disabled).toBe(true)
    read.mockResolvedValue({ version: 7, rules: [rule('real.on', false), rule('real.off', false)] })
    await act(async () => done())
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(within(toast()).getByText('Kill-switch · real.on disabled')).toBeTruthy()
    expect(within(toast()).getByText('AUDITED')).toBeTruthy()
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    expect((await screen.findByRole('switch', { name: 'Enable real.on' })).getAttribute('aria-checked')).toBe('false')
  })

  it('confirming an enable sends enabled=true and toasts AUDITED green', async () => {
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Enable real.off' }))
    reason('ok again')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Enable rule' }))
    await waitFor(() => expect(sw).toHaveBeenCalledWith('real.off', true, 'ok again'))
    expect(await within(toast()).findByText('Re-enabled real.off')).toBeTruthy()
    expect(within(toast()).getByText('AUDITED')).toBeTruthy()
    expect(toast().innerHTML).not.toContain('var(--status-red-text)')
  })

  it.each([404, 409])('a %i on the switch toasts the server text in red, closes the confirm and re-reads', async (status) => {
    sw.mockRejectedValue(new ApiError('http', 'rule is already disabled', status))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Disable rule' }))
    await screen.findByText('rule is already disabled')
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
  })

  it('any other switch failure toasts red, keeps the list and the confirm, and does not re-read', async () => {
    sw.mockRejectedValue(new ApiError('http', 'boom', 500))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Disable rule' }))
    await screen.findByText('boom')
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    expect(read).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('switch', { name: 'Disable real.on' })).toBeTruthy()
    expect((within(dialog()).getByRole('button', { name: 'Disable rule' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('a 403 on the list shows the not-staff line with no switches', async () => {
    read.mockRejectedValue(new ApiError('http', 'forbidden', 403))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    expect(await screen.findByText('Your account is not ASComply staff.')).toBeTruthy()
    expect(screen.queryAllByRole('switch')).toHaveLength(0)
  })

  it('a 500 on the list shows the server text and Retry, not the not-staff line', async () => {
    read.mockRejectedValue(new ApiError('http', 'db down', 500))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    expect(await screen.findByText('db down')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
    expect(screen.queryByText('Your account is not ASComply staff.')).toBeNull()
    expect(signOutConsole).not.toHaveBeenCalled()
  })

  it('a 401 on the list signs the console out; a 403 does not', async () => {
    read.mockRejectedValue(new ApiError('http', 'expired', 403))
    const { unmount } = render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await screen.findByText('Your account is not ASComply staff.')
    expect(signOutConsole).not.toHaveBeenCalled()
    unmount()
    read.mockRejectedValue(new ApiError('http', 'expired', 401))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await waitFor(() => expect(signOutConsole).toHaveBeenCalledTimes(1))
  })

  it('a 401 on the switch signs the console out without a red toast', async () => {
    sw.mockRejectedValue(new ApiError('http', 'expired', 401))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Disable rule' }))
    await waitFor(() => expect(signOutConsole).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('two synchronous confirm clicks send one PATCH', async () => {
    sw.mockReturnValue(new Promise(() => undefined))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('r')
    const btn = within(dialog()).getByRole('button', { name: 'Disable rule' })
    act(() => {
      btn.click()
      btn.click()
    })
    expect(sw).toHaveBeenCalledTimes(1)
  })

  it('the drawer Kill-switch is disabled while a switch is pending', async () => {
    sw.mockReturnValue(new Promise(() => undefined))
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByText('real.on'))
    const kill = (await screen.findByRole('button', { name: /Kill-switch/ })) as HTMLButtonElement
    expect(kill.disabled).toBe(false)
    fireEvent.click(screen.getByRole('switch', { name: 'Disable real.on' }))
    reason('r')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Disable rule' }))
    await waitFor(() => expect((screen.getByRole('button', { name: /Kill-switch/ }) as HTMLButtonElement).disabled).toBe(true))
    const style = screen.getByRole('button', { name: /Kill-switch/ }).getAttribute('style') ?? ''
    expect(style).toContain('opacity: 0.45')
    expect(style).toContain('not-allowed')
  })

  it('the drawer Kill-switch button opens the disable confirm', async () => {
    render(<App />)
    await openRules()
    fireEvent.click(screen.getByText('real.on'))
    fireEvent.click(await screen.findByRole('button', { name: /Kill-switch/ }))
    expect(within(dialog()).getByText('Disable a live rule?')).toBeTruthy()
  })
})

const DRAFT = { rule_set_version: 5, rule_set_version_id: 'id5' }
const DRAFT_VERSIONS = { today: '2026-10-11', versions: [ver(5, 'draft', null), ver(4, 'in_force'), ver(3, 'retired', null)] }
const NO_DRAFT_VERSIONS = { today: '2026-10-11', versions: [ver(4, 'in_force'), ver(3, 'retired', null)] }
const IN_FORCE = { version: 4, rules: [rule('real-on', true), rule('real-off', false)] }
const DRAFT_LIST = { version: 5, rules: [rule('real-on', true), rule('d-new', true)] }
// A read by version: the draft, the version in force, or an older one.
const byVersion = (inForce = IN_FORCE) => async (v?: number) => (v === 5 ? DRAFT_LIST : v === 3 ? { version: 3, rules: [rule('old-key', true)] } : inForce)
const btn = (name: string | RegExp) => screen.getByRole('button', { name }) as HTMLButtonElement
const field = (label: string) => within(dialog()).getByLabelText(label) as HTMLInputElement
const type = (label: string, value: string) => fireEvent.change(field(label), { target: { value } })

const openDraftView = async () => {
  versionsRead.mockResolvedValue(DRAFT_VERSIONS)
  read.mockImplementation(byVersion())
  render(<App />)
  fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
  fireEvent.click(await screen.findByRole('button', { name: /^v5/ }))
  await screen.findByText('d-new')
}

describe('Console code-list wiring', () => {
  const latest = (list: string, status: string) => ({ list, synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added_count: 1, removed_count: 1, changed_count: 0, status })

  it('opening a list reads its syncs', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'applied'), latest('currencies', 'held')])
    syncsRead.mockResolvedValue([{ status: 'applied', synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added: ['ZZ-ADDED'], removed: [], changed: [], added_count: 1, removed_count: 0, changed_count: 0 }])
    render(<App />)
    await openRules()
    expect(syncsRead).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: /currencies/ }))
    await waitFor(() => expect(syncsRead).toHaveBeenCalledWith('currencies'))
    expect((await screen.findByRole('dialog')).textContent).toContain('ZZ-ADDED')
  })

  it('a held sync shows Would remove in the drawer', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'held')])
    syncsRead.mockResolvedValue([{ status: 'held', synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added: [], removed: ['GONE'], changed: [], added_count: 0, removed_count: 1, changed_count: 0 }])
    render(<App />)
    await openRules()
    fireEvent.click(await screen.findByRole('button', { name: /hs-codes/ }))
    const d = await screen.findByRole('dialog')
    await waitFor(() => expect(d.textContent).toContain('Would remove'))
    expect(d.textContent).toContain('GONE')
  })

  it('the code-list panel reads once on open and shows the empty state', async () => {
    render(<App />)
    expect(await screen.findByText('No sync recorded yet.')).toBeTruthy()
    expect(summary).toHaveBeenCalledTimes(1)
  })

  it('a 403 on the summary uses the not-staff wording', async () => {
    summary.mockRejectedValue(new ApiError('http', 'forbidden', 403))
    render(<App />)
    await openRules()
    await waitFor(() => expect(screen.getAllByText('Your account is not ASComply staff.').length).toBeGreaterThan(0))
  })

  const syncOf = (added: string[], status = 'applied') => ({ status, synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added, removed: [], changed: [], added_count: added.length, removed_count: 0, changed_count: 0 })

  it('a 500 on the summary shows the server text, not the not-staff wording', async () => {
    summary.mockRejectedValue(new ApiError('http', 'db down', 500))
    render(<App />)
    await openRules()
    expect(await screen.findByText('db down')).toBeTruthy()
    expect(screen.queryByText('Your account is not ASComply staff.')).toBeNull()
    expect(screen.queryByText('No sync recorded yet.')).toBeNull()
  })

  it('a 403 and a 500 on the syncs read show in the drawer; a list with no syncs says so', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'applied')])
    render(<App />)
    await openRules()
    const row = await screen.findByRole('button', { name: /hs-codes/ })
    syncsRead.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    fireEvent.click(row)
    await waitFor(() => expect(dialog().textContent).toContain('Your account is not ASComply staff.'))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Close' }))
    syncsRead.mockRejectedValueOnce(new ApiError('http', 'db down', 500))
    fireEvent.click(row)
    await waitFor(() => expect(dialog().textContent).toContain('db down'))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Close' }))
    syncsRead.mockResolvedValueOnce([])
    fireEvent.click(row)
    await waitFor(() => expect(dialog().textContent).toContain('No sync recorded yet.'))
  })

  it('a 401 on the syncs read signs out', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'applied')])
    syncsRead.mockRejectedValue(new ApiError('http', 'expired', 401))
    render(<App />)
    await openRules()
    fireEvent.click(await screen.findByRole('button', { name: /hs-codes/ }))
    await waitFor(() => expect(vi.mocked(signOutConsole)).toHaveBeenCalled())
  })

  it('a late reply for a closed list does not paint on the next list', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'applied'), latest('currencies', 'applied')])
    let lateA!: (v: ReturnType<typeof syncOf>[]) => void
    syncsRead.mockImplementation((l) => (l === 'hs-codes' ? new Promise((r) => (lateA = r)) : Promise.resolve([syncOf(['B-ONLY'])])))
    render(<App />)
    await openRules()
    fireEvent.click(await screen.findByRole('button', { name: /hs-codes/ }))
    await waitFor(() => expect(syncsRead).toHaveBeenCalledWith('hs-codes'))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Close' }))
    fireEvent.click(screen.getByRole('button', { name: /currencies/ }))
    await waitFor(() => expect(dialog().textContent).toContain('B-ONLY'))
    await act(async () => lateA([syncOf(['A-STALE'])]))
    expect(dialog().textContent).toContain('currencies')
    expect(dialog().textContent).toContain('B-ONLY')
    expect(dialog().textContent).not.toContain('A-STALE')
  })

  it('a late reply after close paints nothing when the same list reopens pending', async () => {
    summary.mockResolvedValue([latest('hs-codes', 'applied')])
    const late: ((v: ReturnType<typeof syncOf>[]) => void)[] = []
    syncsRead.mockImplementation(() => new Promise((r) => late.push(r)))
    render(<App />)
    await openRules()
    const row = await screen.findByRole('button', { name: /hs-codes/ })
    fireEvent.click(row)
    await waitFor(() => expect(late).toHaveLength(1))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Close' }))
    fireEvent.click(row)
    await waitFor(() => expect(late).toHaveLength(2))
    await act(async () => late[0]([syncOf(['FIRST-STALE'])]))
    expect(dialog().textContent).not.toContain('FIRST-STALE')
    await act(async () => late[1]([syncOf(['SECOND'])]))
    expect(dialog().textContent).toContain('SECOND')
  })

  it('panel rows keep the served order', async () => {
    summary.mockResolvedValue([latest('zz-last', 'applied'), latest('aa-first', 'applied')])
    render(<App />)
    await openRules()
    await screen.findByRole('button', { name: /zz-last/ })
    const html = document.body.innerHTML
    expect(html.indexOf('zz-last')).toBeGreaterThan(-1)
    expect(html.indexOf('zz-last')).toBeLessThan(html.indexOf('aa-first'))
  })
})

describe('Console draft wiring', () => {
  it('New draft opens and selects the draft', async () => {
    versionsRead.mockResolvedValue(NO_DRAFT_VERSIONS)
    read.mockImplementation(byVersion())
    open.mockResolvedValue({ version: 5, from_version: 4 })
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await screen.findByRole('switch', { name: 'Disable real-on' })
    expect(btn('Publish draft').disabled).toBe(true)
    versionsRead.mockResolvedValue(DRAFT_VERSIONS)
    fireEvent.click(btn('New draft'))
    await screen.findByText('DRAFT v5')
    expect(open).toHaveBeenCalledTimes(1)
    expect(versionsRead).toHaveBeenCalledTimes(2)
    expect(read).toHaveBeenLastCalledWith(5)
    expect(btn('New draft').disabled).toBe(true)
    expect(btn('Publish draft v5').disabled).toBe(false)
  })

  it('New draft is disabled while a draft exists', async () => {
    await openDraftView()
    expect(btn('New draft').disabled).toBe(true)
    fireEvent.click(btn('New draft'))
    expect(open).not.toHaveBeenCalled()
  })

  it('a 409 on New draft shows the server text and re-reads', async () => {
    versionsRead.mockResolvedValue(NO_DRAFT_VERSIONS)
    read.mockImplementation(byVersion())
    open.mockRejectedValue(new ApiError('http', 'a draft already exists', 409))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await screen.findByRole('switch', { name: 'Disable real-on' })
    versionsRead.mockResolvedValue(DRAFT_VERSIONS)
    fireEvent.click(btn('New draft'))
    expect(await screen.findByText('a draft already exists')).toBeTruthy()
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    await waitFor(() => expect(versionsRead).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(btn('New draft').disabled).toBe(true))
  })

  it('Save to draft and Remove from draft', async () => {
    edit.mockResolvedValue({ ...DRAFT, key: 'd-new', changed: true })
    add.mockResolvedValue({ ...DRAFT, key: 'fresh-key' })
    remove.mockResolvedValue({ ...DRAFT, key: 'd-new' })
    await openDraftView()
    const reads = read.mock.calls.length

    fireEvent.click(screen.getByText('d-new'))
    type('Message', 'edited text')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Save to draft' }))
    await waitFor(() => expect(edit).toHaveBeenCalledWith('d-new', { type: 'cel', target: 'invoice.total', params: {}, severity: 'error', when: null, message: 'edited text', enabled: true }))
    await waitFor(() => expect(read.mock.calls.length).toBeGreaterThan(reads))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(add).not.toHaveBeenCalled()

    fireEvent.click(btn('Add rule'))
    const save = () => within(dialog()).getByRole('button', { name: 'Save to draft' }) as HTMLButtonElement
    expect(save().disabled).toBe(true)
    type('Key', 'd-new')
    type('Message', 'm')
    expect(save().disabled, 'a key already in the draft').toBe(true)
    expect(within(dialog()).getByText('Key already in the draft.')).toBeTruthy()
    fireEvent.click(save())
    expect(add).not.toHaveBeenCalled()
    type('Key', 'fresh-key')
    type('Params', '[1]')
    expect(save().disabled, 'params that are not an object').toBe(true)
    fireEvent.click(save())
    expect(add).not.toHaveBeenCalled()
    type('Params', '{"a": 1}')
    fireEvent.click(save())
    await waitFor(() => expect(add).toHaveBeenCalledWith({ key: 'fresh-key', type: 'required', target: '', params: { a: 1 }, severity: 'error', when: null, message: 'm', enabled: true }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    fireEvent.click(screen.getByText('d-new'))
    fireEvent.click(within(dialog()).getByRole('button', { name: /Remove from draft/ }))
    await waitFor(() => expect(remove).toHaveBeenCalledWith('d-new'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('a 409 on add shows the server text and keeps the form', async () => {
    add.mockRejectedValue(new ApiError('http', 'rule already in the draft', 409))
    await openDraftView()
    fireEvent.click(btn('Add rule'))
    type('Key', 'racing-key')
    type('Message', 'm')
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Save to draft' }))
    expect(await screen.findByText('rule already in the draft')).toBeTruthy()
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    expect(field('Key').value).toBe('racing-key')
  })

  it('one draft request is in flight at a time', async () => {
    add.mockReturnValue(new Promise(() => undefined))
    await openDraftView()
    fireEvent.click(btn('Add rule'))
    type('Key', 'slow-key')
    type('Message', 'm')
    const save = within(dialog()).getByRole('button', { name: 'Save to draft' }) as HTMLButtonElement
    act(() => {
      save.click()
      save.click()
    })
    expect(add).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(save.disabled).toBe(true))
  })

  it('Add rule and Test draft show on the draft only', async () => {
    await openDraftView()
    expect(btn('Add rule')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /^v4/ }))
    await screen.findByRole('switch', { name: 'Disable real-on' })
    expect(screen.queryByRole('button', { name: 'Add rule' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Test draft' })).toBeNull()
  })

  it('Test draft is prefilled with the sample, gates bad JSON and shows both columns', async () => {
    const v = (rule_key: string) => ({ rule_key, severity: 'error' as const, message: 'bad', path: 'invoice.total' })
    test.mockResolvedValue({ draft: { rule_set_version: 5, violations: [v('from-draft')], error: null }, in_force: { rule_set_version: 4, violations: [v('from-force')] } })
    await openDraftView()
    fireEvent.click(btn('Test draft'))
    const area = field('Sample invoice') as unknown as HTMLTextAreaElement
    expect(area.value).toBe(SAMPLE_INVOICE)
    const run = within(dialog()).getByRole('button', { name: 'Run test' }) as HTMLButtonElement
    fireEvent.change(area, { target: { value: '{"invoice": ' } })
    expect(run.disabled).toBe(true)
    expect(within(dialog()).getByText('Invoice JSON does not parse.')).toBeTruthy()
    fireEvent.change(area, { target: { value: SAMPLE_INVOICE } })
    expect(run.disabled).toBe(false)
    fireEvent.click(run)
    await within(dialog()).findByText('Draft v5')
    expect(test).toHaveBeenCalledWith(JSON.parse(SAMPLE_INVOICE))
    expect(within(dialog()).getByText('In force v4')).toBeTruthy()
    expect(within(dialog()).getByText(/from-draft/)).toBeTruthy()
    expect(within(dialog()).getByText(/from-force/)).toBeTruthy()
  })

  it('Publish reads the draft and the version in force whatever is selected', async () => {
    await openDraftView()
    read.mockClear()
    fireEvent.click(screen.getByRole('button', { name: /^v3/ }))
    await screen.findByText('old-key')
    read.mockClear()
    fireEvent.click(btn('Publish draft v5'))
    await within(dialog()).findByText('real-off')
    expect(read).toHaveBeenCalledWith(5)
    expect(read.mock.calls.some((c) => c.length === 0)).toBe(true)
    const modal = within(dialog())
    expect(modal.getByText('d-new')).toBeTruthy()
    expect(modal.getByText('real-off')).toBeTruthy()
    expect(modal.queryByText('old-key')).toBeNull()
    expect(modal.getByText('1 ADDED · 1 REMOVED')).toBeTruthy()
  })

  it('a failed in-force read shows its text in the modal and keeps Publish disabled', async () => {
    await openDraftView()
    read.mockImplementation(async (v?: number) => {
      if (v === undefined) throw new ApiError('http', 'no rule set in force', 503)
      return DRAFT_LIST
    })
    fireEvent.click(btn('Publish draft v5'))
    expect(await within(dialog()).findByText('no rule set in force')).toBeTruthy()
    fireEvent.change(within(dialog()).getByLabelText('Start date'), { target: { value: '3001-01-01' } })
    expect((within(dialog()).getByRole('button', { name: 'Publish v5' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('the publish modal shows Loading diff and enables Publish only once both reads resolve', async () => {
    await openDraftView()
    const pending: Array<() => void> = []
    read.mockImplementation((v?: number) => new Promise((r) => pending.push(() => r(v === 5 ? DRAFT_LIST : IN_FORCE))) as never)
    fireEvent.click(btn('Publish draft v5'))
    const modal = within(dialog())
    expect(modal.getByText('Loading diff…')).toBeTruthy()
    fireEvent.change(modal.getByLabelText('Start date'), { target: { value: '2026-10-11' } })
    const confirm = () => modal.getByRole('button', { name: 'Publish v5' }) as HTMLButtonElement
    expect(confirm().disabled, 'both reads pending').toBe(true)
    await act(async () => pending[0]())
    expect(confirm().disabled, 'one read pending').toBe(true)
    await act(async () => pending[1]())
    await modal.findByText('real-off')
    expect(confirm().disabled).toBe(false)
  })

  it('PublishModal date gate', async () => {
    publish.mockReturnValue(new Promise(() => undefined))
    await openDraftView()
    fireEvent.click(btn('Publish draft v5'))
    const modal = within(dialog())
    await modal.findByText('real-off')
    const date = modal.getByLabelText('Start date') as HTMLInputElement
    const disabled = () => (modal.getByRole('button', { name: 'Publish v5' }) as HTMLButtonElement).disabled
    expect(date.min).toBe('2026-10-11')
    expect(disabled(), 'no date').toBe(true)
    fireEvent.change(date, { target: { value: '2026-10-10' } })
    expect(disabled(), 'yesterday').toBe(true)
    fireEvent.change(date, { target: { value: '2026-10-11' } })
    expect(disabled(), 'today').toBe(false)
    fireEvent.click(modal.getByRole('button', { name: 'Publish v5' }))
    await waitFor(() => expect(disabled(), 'in flight').toBe(true))
    expect(publish).toHaveBeenCalledTimes(1)
  })

  it('publish re-reads and selects the version in force', async () => {
    publish.mockResolvedValue({ version: 5, effective_from: '3001-01-01', rule_count: 2 })
    await openDraftView()
    fireEvent.click(btn('Publish draft v5'))
    const modal = within(dialog())
    await modal.findByText('real-off')
    fireEvent.change(modal.getByLabelText('Start date'), { target: { value: '3001-01-01' } })
    versionsRead.mockClear()
    read.mockClear()
    fireEvent.click(modal.getByRole('button', { name: 'Publish v5' }))
    await waitFor(() => expect(publish).toHaveBeenCalledWith('3001-01-01'))
    expect(await within(toast()).findByText('Published v5 · starts 3001-01-01')).toBeTruthy()
    expect(within(toast()).getByText('AUDITED')).toBeTruthy()
    await waitFor(() => expect(versionsRead).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(read).toHaveBeenLastCalledWith(undefined))
    expect(screen.getByRole('button', { name: /^v4/ }).getAttribute('aria-pressed')).toBe('true')
  })

  it('a stale draft shows its warning lines in the modal', async () => {
    versionsRead.mockResolvedValue({
      today: '2026-10-11',
      versions: [ver(6, 'scheduled', '2027-01-01'), { ...ver(5, 'draft', null), opened_at: '2026-10-01T09:00:00Z' }, ver(4, 'in_force', '2026-10-05')],
    })
    read.mockImplementation(byVersion())
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    fireEvent.click(await screen.findByRole('button', { name: /^v5/ }))
    await screen.findByText('d-new')
    fireEvent.click(btn('Publish draft v5'))
    const modal = within(dialog())
    expect(await modal.findByText(/v4 took effect after this draft was opened/)).toBeTruthy()
    expect(modal.getByText(/v6 is scheduled from 2027-01-01/)).toBeTruthy()
  })

  it('the modal warns that publish re-enables a rule switched off in the version in force', async () => {
    versionsRead.mockResolvedValue(DRAFT_VERSIONS)
    read.mockImplementation(byVersion({ version: 4, rules: [rule('real-on', false)] }))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    fireEvent.click(await screen.findByRole('button', { name: /^v5/ }))
    await screen.findByText('d-new')
    fireEvent.click(btn('Publish draft v5'))
    expect(await within(dialog()).findByText('real-on is switched off in v4 but enabled in this draft. Publishing switches it back on.')).toBeTruthy()
  })

  const publishReady = async (date = '2026-10-11') => {
    await openDraftView()
    fireEvent.click(btn('Publish draft v5'))
    const modal = within(dialog())
    await modal.findByText('real-off')
    fireEvent.change(modal.getByLabelText('Start date'), { target: { value: date } })
    return { modal, confirm: modal.getByRole('button', { name: 'Publish v5' }) as HTMLButtonElement }
  }

  it('two synchronous Publish clicks send one request', async () => {
    publish.mockReturnValue(new Promise(() => undefined))
    const { confirm } = await publishReady()
    act(() => {
      confirm.click()
      confirm.click()
    })
    expect(publish).toHaveBeenCalledTimes(1)
  })

  it('a refused publish shows the server text, keeps the modal and re-reads on a 409', async () => {
    publish.mockRejectedValue(new ApiError('http', 'the draft has no rules', 409))
    const { confirm } = await publishReady()
    versionsRead.mockClear()
    fireEvent.click(confirm)
    expect(await screen.findByText('the draft has no rules')).toBeTruthy()
    expect(toast().innerHTML).toContain('var(--status-red-text)')
    expect(within(dialog()).getByRole('button', { name: 'Publish v5' })).toBeTruthy()
    await waitFor(() => expect(versionsRead).toHaveBeenCalledTimes(1))
  })

  it('an empty diff says so and leaves the date as the only gate', async () => {
    read.mockImplementation(async () => IN_FORCE)
    const { modal, confirm } = await (async () => {
      versionsRead.mockResolvedValue(DRAFT_VERSIONS)
      render(<App />)
      fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
      fireEvent.click(await screen.findByRole('button', { name: /^v5/ }))
      await screen.findByText('real-on')
      fireEvent.click(btn('Publish draft v5'))
      const m = within(dialog())
      await m.findByText('No changes against the version in force.')
      return { modal: m, confirm: m.getByRole('button', { name: 'Publish v5' }) as HTMLButtonElement }
    })()
    expect(confirm.disabled, 'no date').toBe(true)
    fireEvent.change(modal.getByLabelText('Start date'), { target: { value: '2026-10-11' } })
    expect(confirm.disabled).toBe(false)
  })

  it('a draft opened after the version in force shows no warning lines', async () => {
    const { modal } = await publishReady()
    expect(modal.queryByText(/took effect after/)).toBeNull()
    expect(modal.queryByText(/is scheduled from/)).toBeNull()
  })

  it('a 401 on a draft write signs the console out without a red toast', async () => {
    open.mockRejectedValue(new ApiError('http', 'expired', 401))
    versionsRead.mockResolvedValue(NO_DRAFT_VERSIONS)
    read.mockImplementation(byVersion())
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await screen.findByRole('switch', { name: 'Disable real-on' })
    fireEvent.click(btn('New draft'))
    await waitFor(() => expect(signOutConsole).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('a 404 on Remove shows the server text, keeps the drawer and re-reads', async () => {
    remove.mockRejectedValue(new ApiError('http', 'no such rule in the draft', 404))
    await openDraftView()
    fireEvent.click(screen.getByText('d-new'))
    versionsRead.mockClear()
    fireEvent.click(within(dialog()).getByRole('button', { name: /Remove from draft/ }))
    expect(await screen.findByText('no such rule in the draft')).toBeTruthy()
    expect(within(dialog()).getByRole('button', { name: 'Save to draft' })).toBeTruthy()
    await waitFor(() => expect(versionsRead).toHaveBeenCalledTimes(1))
  })

  it('a new rule has no Remove from draft', async () => {
    await openDraftView()
    fireEvent.click(btn('Add rule'))
    expect(within(dialog()).queryByRole('button', { name: /Remove from draft/ })).toBeNull()
    expect(within(dialog()).getByLabelText('Key')).toBeTruthy()
  })

  it('an in-force rule opens no draft form', async () => {
    await openDraftView()
    fireEvent.click(screen.getByRole('button', { name: /^v4/ }))
    await screen.findByRole('switch', { name: 'Disable real-on' })
    fireEvent.click(screen.getByText('real-on'))
    expect(within(dialog()).queryByRole('button', { name: 'Save to draft' })).toBeNull()
    expect(within(dialog()).queryByLabelText('Message')).toBeNull()
  })

  it('Run test refuses JSON that is not an object and sends one request on a double click', async () => {
    test.mockReturnValue(new Promise(() => undefined))
    await openDraftView()
    fireEvent.click(btn('Test draft'))
    const area = field('Sample invoice') as unknown as HTMLTextAreaElement
    const run = within(dialog()).getByRole('button', { name: 'Run test' }) as HTMLButtonElement
    for (const bad of ['[1]', 'null', '"x"', '']) {
      fireEvent.change(area, { target: { value: bad } })
      expect(run.disabled, bad).toBe(true)
      expect(within(dialog()).getByText('Invoice JSON does not parse.')).toBeTruthy()
    }
    fireEvent.change(area, { target: { value: '{}' } })
    act(() => {
      run.click()
      run.click()
    })
    expect(test).toHaveBeenCalledTimes(1)
    expect(test).toHaveBeenCalledWith({})
  })

  it('a draft fault shows with the in-force column and no draft violations', async () => {
    test.mockResolvedValue({ draft: { rule_set_version: 5, violations: [], error: 'the draft does not evaluate' }, in_force: { rule_set_version: 4, violations: [{ rule_key: 'from-force', severity: 'error', message: 'bad' }] } })
    await openDraftView()
    fireEvent.click(btn('Test draft'))
    fireEvent.click(within(dialog()).getByRole('button', { name: 'Run test' }))
    expect((await within(dialog()).findByRole('alert')).textContent).toBe('the draft does not evaluate')
    expect(within(dialog()).getByText('No violations.')).toBeTruthy()
    expect(within(dialog()).getByText(/from-force/)).toBeTruthy()
  })
})
