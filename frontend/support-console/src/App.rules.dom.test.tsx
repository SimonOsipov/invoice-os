// @vitest-environment jsdom
import { ApiError } from '@invoice-os/api-client'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { KillConfirm } from './components/KillConfirm'
import { signOutConsole } from '@invoice-os/console-session'
import { fetchRules, fetchVersions, switchRule, type RuleVersion } from './rulesApi'
import type { Rule } from './types'

vi.mock('@invoice-os/console-session', () => ({
  StaffGate: ({ children }: { children: ReactNode }) => children,
  signOutConsole: vi.fn(),
}))
vi.mock('./rulesApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./rulesApi')>()),
  fetchRules: vi.fn(),
  fetchVersions: vi.fn(),
  switchRule: vi.fn(),
}))

const read = vi.mocked(fetchRules)
const versionsRead = vi.mocked(fetchVersions)
const sw = vi.mocked(switchRule)
const rule = (key: string, enabled: boolean): Rule => ({ key, type: 'cel', field: 'invoice.total', severity: 'error', scope: 'global', enabled, message: 'm', params: {}, when: null })
const ver = (version: number, state: RuleVersion['state'], effective_from: string | null = '2026-01-01'): RuleVersion => ({ rule_set_version_id: `id${version}`, version, state, effective_from, opened_at: null, rule_count: 2 })
const VERSIONS = { today: '2026-10-11', versions: [ver(8, 'draft', null), ver(7, 'in_force'), ver(3, 'retired', null)] }
const LIST = { version: 7, rules: [rule('real.on', true), rule('real.off', false)] }

beforeEach(() => {
  read.mockResolvedValue(LIST)
  versionsRead.mockResolvedValue(VERSIONS)
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

  it('reads the list when Rules opens and on each re-entry, never on mount', async () => {
    render(<App />)
    expect(read).not.toHaveBeenCalled()
    await openRules()
    expect(read).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: /^Submissions/ }))
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
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

  it('a 403 on the list shows the no-role line with no switches', async () => {
    read.mockRejectedValue(new ApiError('http', 'forbidden', 403))
    render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    expect(await screen.findByText('Your account has no rules role.')).toBeTruthy()
    expect(screen.queryAllByRole('switch')).toHaveLength(0)
  })

  it('a 401 on the list signs the console out; a 403 does not', async () => {
    read.mockRejectedValue(new ApiError('http', 'expired', 403))
    const { unmount } = render(<App />)
    fireEvent.click(screen.getByRole('button', { name: /^Rules/ }))
    await screen.findByText('Your account has no rules role.')
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
