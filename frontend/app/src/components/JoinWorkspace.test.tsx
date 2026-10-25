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

  const nameBox = () => screen.getByTestId('join-name') as HTMLInputElement
  const edit = (v: string) => fireEvent.change(nameBox(), { target: { value: v } })
  const joinBtn = () => screen.getByRole('button', { name: 'Join Obi Partners' }) as HTMLButtonElement
  const errText = () => screen.queryByTestId('join-name-error')?.textContent ?? null

  it('JoinWorkspace: a blank name says Enter your name', () => {
    show(ONE)
    edit('  ')
    expect(errText()).toBe('Enter your name.')
    expect(nameBox().getAttribute('aria-invalid')).toBe('true')
    expect(nameBox().getAttribute('aria-describedby')).toBe('join-name-error')
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace: an over-long name says the limit', () => {
    show(ONE)
    edit('a'.repeat(201))
    expect(errText()).toBe('Use 200 characters or fewer.')
    expect(joinBtn().disabled).toBe(true)
    edit('a'.repeat(200))
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(false)
    edit(`  ${'a'.repeat(200)}  `)
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(false)
  })

  it('JoinWorkspace: a NUL name disables Join with no message', () => {
    show(ONE)
    for (const v of ['Bo\0b', 'a'.repeat(201) + '\0', ' \0 ']) {
      edit(v)
      expect(errText()).toBeNull()
      expect(joinBtn().disabled).toBe(true)
    }
  })

  it('JoinWorkspace: no message for a pristine empty or a prefilled valid name', () => {
    show(ONE)
    expect(errText()).toBeNull()
    cleanup()
    show(ONE, { defaultName: '' })
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace: a prefilled 201-character name shows the limit with no edit', () => {
    show(ONE, { defaultName: 'a'.repeat(201) })
    expect(errText()).toBe('Use 200 characters or fewer.')
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace: a prefilled U+0085 name shows Enter your name with no edit', () => {
    show(ONE, { defaultName: '\u0085' })
    expect(errText()).toBe('Enter your name.')
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace: a prefilled NUL name shows no message', () => {
    show(ONE, { defaultName: 'Bo\0b' })
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace: clearing the prefilled name says Enter your name', () => {
    show(ONE)
    edit('')
    expect(errText()).toBe('Enter your name.')
  })

  it('JoinWorkspace: the message clears when the name turns valid', () => {
    const { onJoin } = show(ONE)
    edit('  ')
    expect(errText()).toBe('Enter your name.')
    edit('Bo')
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(false)
    fireEvent.click(joinBtn())
    expect(onJoin).toHaveBeenCalledWith('a', 'Bo')
  })

  it('JoinWorkspace: several invites show one message above the rows', () => {
    show(THREE)
    edit('  ')
    const msgs = screen.getAllByTestId('join-name-error')
    expect(msgs).toHaveLength(1)
    expect(msgs[0].compareDocumentPosition(screen.getAllByTestId('join-invite')[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    for (const b of screen.getAllByRole('button', { name: /^Join / })) expect((b as HTMLButtonElement).disabled).toBe(true)
  })

  it('JoinWorkspace: Join passes the trimmed name', () => {
    const { onJoin } = show(ONE)
    edit('\u0085 Bo \u0085')
    fireEvent.click(joinBtn())
    expect(onJoin).toHaveBeenCalledWith('a', 'Bo')
  })

  it('JoinWorkspace: the no-workspace screen shows the name message only', () => {
    const onCreate = vi.fn()
    show([], { onCreate, defaultName: '' })
    const ws = screen.getByTestId('join-workspace-name')
    const create = screen.getByTestId('join-create') as HTMLButtonElement
    fireEvent.change(ws, { target: { value: 'Acme' } })
    edit('  ')
    expect(errText()).toBe('Enter your name.')
    expect(create.disabled).toBe(true)
    fireEvent.change(ws, { target: { value: 'a'.repeat(201) } })
    edit('Bo')
    expect(screen.queryByTestId('join-name-error')).toBeNull()
    expect(create.disabled).toBe(true)
    fireEvent.change(ws, { target: { value: 'Acme' } })
    expect(create.disabled).toBe(false)
  })
  it('JoinWorkspace QA: the message matches the EntityFormModal sibling and clears the aria state', () => {
    show(ONE)
    edit(' ')
    const msg = screen.getByTestId('join-name-error')
    expect(msg.id).toBe('join-name-error')
    expect(msg.tagName).toBe('DIV')
    expect(msg.style.fontSize).toBe('11.5px')
    expect(msg.style.color).toBe('var(--status-red-text)')
    expect(msg.style.marginTop === '6px' || msg.parentElement?.style.gap === '6px').toBe(true)
    edit('Bo')
    expect(nameBox().getAttribute('aria-invalid')).toBeNull()
    expect(nameBox().getAttribute('aria-describedby')).toBeNull()
  })

  it('JoinWorkspace QA: spaces-only, U+0085-only and U+FEFF-only names say Enter your name, prefilled or edited', () => {
    for (const v of ['   ', '\u0085', '\u0085 \u0085 ', '\uFEFF']) {
      show(ONE, { defaultName: v })
      expect(errText(), JSON.stringify(v)).toBe('Enter your name.')
      expect(joinBtn().disabled).toBe(true)
      cleanup()
    }
    show(ONE)
    edit('\uFEFF')
    expect(errText()).toBe('Enter your name.')
  })

  it('JoinWorkspace QA: the limit counts code points, not UTF-16 units', () => {
    show(ONE)
    edit('😀'.repeat(200))
    expect(errText()).toBeNull()
    expect(joinBtn().disabled).toBe(false)
    edit('😀'.repeat(201))
    expect(errText()).toBe('Use 200 characters or fewer.')
    expect(joinBtn().disabled).toBe(true)
  })

  it('JoinWorkspace QA: several invites with a prefilled over-long name show one message and every Join is off', () => {
    show(THREE, { defaultName: 'a'.repeat(201) })
    expect(screen.getAllByTestId('join-name-error')).toHaveLength(1)
    expect(nameBox().getAttribute('aria-describedby')).toBe('join-name-error')
    const buttons = screen.getAllByRole('button', { name: /^Join / })
    expect(buttons).toHaveLength(3)
    for (const b of buttons) expect((b as HTMLButtonElement).disabled).toBe(true)
    edit('Bo')
    expect(screen.queryByTestId('join-name-error')).toBeNull()
    for (const b of screen.getAllByRole('button', { name: /^Join / })) expect((b as HTMLButtonElement).disabled).toBe(false)
  })

  it('JoinWorkspace QA: Create a workspace passes both trimmed values; Workspace name never gets aria state or a message', () => {
    const onCreate = vi.fn()
    show([], { onCreate, defaultName: '' })
    const ws = screen.getByTestId('join-workspace-name')
    fireEvent.change(ws, { target: { value: '  Acme  ' } })
    edit('\u0085 Bo \u0085')
    expect(errText()).toBeNull()
    fireEvent.click(screen.getByTestId('join-create'))
    expect(onCreate).toHaveBeenCalledWith('Acme', 'Bo')
    fireEvent.change(ws, { target: { value: 'a'.repeat(201) } })
    expect(ws.getAttribute('aria-invalid')).toBeNull()
    expect(ws.getAttribute('aria-describedby')).toBeNull()
    expect(screen.queryByTestId('join-name-error')).toBeNull()
  })

  it('JoinWorkspace QA: an edited-then-cleared name says Enter your name in the no-workspace screen', () => {
    show([], { onCreate: vi.fn(), defaultName: '' })
    expect(errText()).toBeNull()
    edit('x')
    edit('')
    expect(errText()).toBe('Enter your name.')
  })
})
