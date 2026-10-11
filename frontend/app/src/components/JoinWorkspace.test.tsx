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
  const props = { invites, joining: null, onJoin: vi.fn(), onSignOut: vi.fn(), ...over }
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
    expect(onJoin).toHaveBeenCalledWith('b')
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
})
