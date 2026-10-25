// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { PendingInvite } from '../lib/sessionHandoff'
import { CREATING_OWN, JoinWorkspace } from './JoinWorkspace'

afterEach(cleanup)

const inv = (id: string, workspace: string, role: string, inviter: string | null): PendingInvite => ({
  id,
  workspace,
  role,
  inviter,
  expires_at: '2026-10-20T00:00:00Z',
})
const ONE = [inv('a', 'Obi Partners', 'reviewer', 'Ada Obi')]
const THREE = [inv('a', 'Obi Partners', 'reviewer', 'Ada Obi'), inv('b', 'Zulu Books', 'admin', null), inv('c', 'Kano Tax', 'preparer', 'Musa K')]

function show(invites: PendingInvite[], over: Partial<Parameters<typeof JoinWorkspace>[0]> = {}) {
  const props = { invites, joining: null, defaultName: 'Ada', onJoin: vi.fn(), onSignOut: vi.fn(), ...over }
  render(<JoinWorkspace {...props} />)
  return props
}

describe('JoinWorkspace', () => {
  it('joinWorkspace_oneInviteNamesWorkspaceInviterAndRole', () => {
    show(ONE)
    expect(screen.getByRole('heading').textContent).toBe('Join Obi Partners')
    expect(screen.getByText('Ada Obi invited you as Reviewer.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Join Obi Partners' }).textContent).toBe('Join')
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeTruthy()
    expect(screen.queryAllByTestId('join-invite')).toHaveLength(0)
  })

  it('joinWorkspace_nullInviterUsesTheRoleLine', () => {
    show([inv('a', 'Obi Partners', 'preparer', null)])
    expect(screen.getByText('You are invited as Preparer.')).toBeTruthy()
  })

  it('joinWorkspace_severalInvitesShowAChooserInOrder', () => {
    show(THREE)
    expect(screen.getByRole('heading').textContent).toBe('Choose a workspace to join')
    expect(screen.getByText('You are invited to 3 workspaces. An account can belong to only one.')).toBeTruthy()
    const rows = screen.getAllByTestId('join-invite')
    expect(rows.map((r) => r.textContent?.replace(/Join$/, ''))).toEqual([
      'Obi PartnersAda Obi invited you as Reviewer.',
      'Zulu BooksYou are invited as Admin.',
      'Kano TaxMusa K invited you as Preparer.',
    ])
    expect(screen.getAllByRole('button', { name: 'Sign out' })).toHaveLength(1)
  })

  it('joinWorkspace_joinPassesTheRowId', () => {
    const { onJoin } = show(THREE)
    fireEvent.click(within(screen.getAllByTestId('join-invite')[1]).getByRole('button', { name: 'Join Zulu Books' }))
    expect(onJoin).toHaveBeenCalledTimes(1)
    expect(onJoin).toHaveBeenCalledWith('b', 'Ada')
  })

  it('joinWorkspace_joiningDisablesEveryButton', () => {
    show(THREE, { joining: 'b', onCreateOwn: vi.fn() })
    const buttons = screen.getAllByRole('button') as HTMLButtonElement[]
    expect(buttons).toHaveLength(5)
    expect(buttons.every((b) => b.disabled)).toBe(true)
    const rows = screen.getAllByTestId('join-invite')
    expect(within(rows[1]).getByRole('button').textContent).toBe('Joining…')
    expect(within(rows[0]).getByRole('button').textContent).toBe('Join')
  })

  it('joinWorkspace_buttonsCarryTheCardChrome', () => {
    show(ONE)
    const join = screen.getByRole('button', { name: 'Join Obi Partners' })
    const out = screen.getByRole('button', { name: 'Sign out' })
    expect(join.className).toBe('v2-btn v2-btn-primary pf-btn')
    expect(out.className).toBe('v2-btn v2-btn-ghost pf-btn')
    for (const b of [join, out]) {
      expect(b.style.height).toBe('34px')
      expect(b.style.padding).toBe('0px 12px')
      expect(b.style.fontSize).toBe('13px')
      expect(b.style.alignSelf).toBe('flex-start')
    }
  })

  it('joinWorkspace_createOwnShownOnlyWithAnswers', () => {
    const { onCreateOwn } = show(ONE, { onCreateOwn: vi.fn() })
    const create = screen.getByRole('button', { name: 'Create my own workspace' })
    expect(create.className).toBe('v2-btn v2-btn-ghost pf-btn')
    const order = screen.getAllByRole('button').map((b) => b.textContent)
    expect(order).toEqual(['Join', 'Create my own workspace', 'Sign out'])
    fireEvent.click(create)
    expect(onCreateOwn).toHaveBeenCalledTimes(1)
    cleanup()
    show(ONE)
    expect(screen.queryByRole('button', { name: 'Create my own workspace' })).toBeNull()
  })

  it('joinWorkspace_createOwnReadsCreatingWhileItRuns', () => {
    show(ONE, { joining: CREATING_OWN, onCreateOwn: vi.fn() })
    expect(screen.getByRole('button', { name: 'Creating…' })).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Join Obi Partners' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('JoinWorkspace: the name is prefilled and gates Join', () => {
    show(ONE)
    const input = screen.getByTestId('join-name') as HTMLInputElement
    const join = screen.getByRole('button', { name: 'Join Obi Partners' }) as HTMLButtonElement
    expect(input.value).toBe('Ada')
    expect(join.disabled).toBe(false)
    expect(input.style.width).toBe('100%')
    fireEvent.change(input, { target: { value: '  ' } })
    expect(join.disabled).toBe(true)
    fireEvent.change(input, { target: { value: 'a'.repeat(201) } })
    expect(join.disabled).toBe(true)
    fireEvent.change(input, { target: { value: 'Bo\0b' } })
    expect(join.disabled).toBe(true)
    fireEvent.change(input, { target: { value: ' Bo ' } })
    expect(join.disabled).toBe(false)
  })

  it('JoinWorkspace: no invites renders the no-workspace screen', () => {
    const { onCreate, onJoin } = show([], { defaultName: '', onCreate: vi.fn() })
    expect(screen.getByRole('heading').textContent).toBe('No workspace yet')
    expect(screen.getByText('This account has no workspace yet. If you were invited, open the invite link in your email.')).toBeTruthy()
    const ws = screen.getByTestId('join-workspace-name') as HTMLInputElement
    const nm = screen.getByTestId('join-name') as HTMLInputElement
    const create = screen.getByTestId('join-create') as HTMLButtonElement
    expect([ws.value, nm.value]).toEqual(['', ''])
    expect(create.disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^Join/ })).toBeNull()
    fireEvent.change(ws, { target: { value: 'Acme' } })
    expect(create.disabled).toBe(true)
    fireEvent.change(nm, { target: { value: 'Ada' } })
    expect(create.disabled).toBe(false)
    fireEvent.change(ws, { target: { value: 'a'.repeat(201) } })
    expect(create.disabled).toBe(true)
    fireEvent.change(ws, { target: { value: 'Ac\0me' } })
    expect(create.disabled, 'a NUL in the workspace name').toBe(true)
    fireEvent.change(ws, { target: { value: ' Acme ' } })
    expect(create.disabled).toBe(false)
    fireEvent.change(nm, { target: { value: 'A\0da' } })
    expect(create.disabled, 'a NUL in your name').toBe(true)
    fireEvent.change(nm, { target: { value: 'Ada' } })
    fireEvent.click(create)
    expect(onCreate).toHaveBeenCalledWith('Acme', 'Ada')
    expect(onJoin).not.toHaveBeenCalled()
  })

  it('JoinWorkspace: the name limit is 200 characters, counted as characters', () => {
    show(ONE)
    const input = screen.getByTestId('join-name')
    const join = screen.getByRole('button', { name: 'Join Obi Partners' }) as HTMLButtonElement
    fireEvent.change(input, { target: { value: 'a'.repeat(200) } })
    expect(join.disabled).toBe(false)
    fireEvent.change(input, { target: { value: '\u{1F600}'.repeat(200) } })
    expect(join.disabled, '200 astral characters fit').toBe(false)
    fireEvent.change(input, { target: { value: '\u{1F600}'.repeat(201) } })
    expect(join.disabled).toBe(true)
  })

  it('JoinWorkspace: Create stays off for a blank name or a blank workspace alone', () => {
    show([], { defaultName: 'Ada', defaultWorkspace: '  ', onCreate: vi.fn() })
    const create = screen.getByTestId('join-create') as HTMLButtonElement
    expect(create.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('join-workspace-name'), { target: { value: 'a'.repeat(200) } })
    expect(create.disabled).toBe(false)
    fireEvent.change(screen.getByTestId('join-name'), { target: { value: ' ' } })
    expect(create.disabled).toBe(true)
  })

  it('JoinWorkspace: the no-workspace screen offers Create a workspace and not Create my own', () => {
    show([], { onCreate: vi.fn(), onCreateOwn: vi.fn() })
    expect(screen.getByTestId('join-create')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Create my own workspace' })).toBeNull()
  })

  it('JoinWorkspace: the no-workspace inputs are prefilled from the answers', () => {
    show([], { defaultName: 'Ada', defaultWorkspace: 'Acme', onCreate: vi.fn() })
    expect((screen.getByTestId('join-workspace-name') as HTMLInputElement).value).toBe('Acme')
    expect((screen.getByTestId('join-name') as HTMLInputElement).value).toBe('Ada')
    expect((screen.getByTestId('join-create') as HTMLButtonElement).disabled).toBe(false)
  })
})
