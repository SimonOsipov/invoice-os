import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  DELETE_ADMIN_ONLY,
  DELETE_WORKSPACE,
  DELETION_NOTICE,
  CANCEL_LINK_KEY,
  cancelWorkspaceDeletion,
  cancelWorkspaceDeletionByLink,
  holdCancelLink,
  peekCancelLink,
  readCancelLinkFragment,
  deletionNoticeBody,
  formatDueDate,
  nameMatches,
  readWorkspaceDeletion,
  scheduleWorkspaceDeletion,
} from './workspaceDeletion'

const me = (role: string, extra: object = {}) => ({
  tenant: { id: 't', name: 'Okafor & Co', kind: 'firm', ...extra },
  user: { id: 'u', role, display_name: null, email: null },
})

describe('workspaceDeletion: the three calls hit their routes', () => {
  it('schedule posts {workspace_name} and answers the due date', async () => {
    const f = vi.fn().mockResolvedValue({ deletion_due_at: '2026-11-24T18:10:44Z' })
    const due = await scheduleWorkspaceDeletion(f, 'B', 'Okafor & Co')
    expect(due).toBe('2026-11-24T18:10:44Z')
    expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/workspace/deletion', {
      method: 'POST',
      body: { workspace_name: 'Okafor & Co' },
    })
  })

  it('cancel posts to the cancel route', async () => {
    const f = vi.fn().mockResolvedValue({ deletion_due_at: null })
    await cancelWorkspaceDeletion(f, 'B')
    expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/workspace/deletion/cancel', { method: 'POST' })
  })

  it('read maps deletion_due_at and the role', async () => {
    const f = vi
      .fn()
      .mockResolvedValueOnce(me('admin'))
      .mockResolvedValueOnce(me('preparer', { deletion_due_at: null }))
      .mockResolvedValueOnce(me('admin', { deletion_due_at: '2026-11-24T18:10:44Z' }))
    expect(await readWorkspaceDeletion(f, 'B')).toEqual({ dueAt: null, name: 'Okafor & Co', isAdmin: true })
    expect(await readWorkspaceDeletion(f, 'B')).toEqual({ dueAt: null, name: 'Okafor & Co', isAdmin: false })
    expect((await readWorkspaceDeletion(f, 'B')).dueAt).toBe('2026-11-24T18:10:44Z')
    expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/me')
  })

  it('read: only the admin role is an admin', async () => {
    for (const role of ['approver', 'reviewer', 'viewer', 'preparer', 'Admin', '']) {
      const f = vi.fn().mockResolvedValue(me(role))
      expect((await readWorkspaceDeletion(f, 'B')).isAdmin, role).toBe(false)
    }
  })
})

describe('workspaceDeletion: formatDueDate and nameMatches', () => {
  it('formats the date in the given zone', () => {
    expect(formatDueDate('2026-11-24T18:10:44Z', 'UTC')).toBe('24 November 2026')
  })

  it('formats the date in a zone ahead of UTC', () => {
    expect(formatDueDate('2026-11-24T18:10:44Z', 'Pacific/Auckland')).toBe('25 November 2026')
  })

  it('matches exactly', () => {
    expect(nameMatches('Okafor & Co', 'Okafor & Co')).toBe(true)
    for (const typed of ['okafor & co', 'Okafor & Co ', '']) expect(nameMatches(typed, 'Okafor & Co')).toBe(false)
    const long = 'W' + 'x'.repeat(199)
    expect(nameMatches(long, long)).toBe(true)
    expect(nameMatches(long.slice(0, -1), long)).toBe(false)
  })
})

describe('workspaceDeletion: the copy', () => {
  it('is the mock copy', () => {
    expect(DELETE_WORKSPACE).toEqual({
      title: 'Delete this workspace',
      body: 'The workspace locks for 30 days. Then we delete every invoice, file and member.',
      button: 'Delete workspace',
      label: 'Type the workspace name to confirm',
      cancel: 'Cancel',
    })
    expect(DELETE_ADMIN_ONLY).toBe('Only an admin can delete the workspace.')
    expect(DELETION_NOTICE).toEqual({
      title: 'This workspace is scheduled for deletion',
      cancel: 'Cancel deletion',
      signOut: 'Sign out',
    })
    expect(deletionNoticeBody('24 November 2026', 'Okafor & Co').join('')).toBe(
      'On 24 November 2026 we delete every invoice, file and member of Okafor & Co.',
    )
  })
})

const T = 'A'.repeat(43)

describe('workspaceDeletion: the cancel link', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })
  const memory = () => {
    const m = new Map<string, string>()
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
      removeItem: (k: string) => void m.delete(k),
    })
    return m
  }

  it('workspaceDeletion: readCancelLinkFragment takes exactly one 43-character token', () => {
    expect(readCancelLinkFragment(`#cancel-deletion=${T}`)).toBe(T)
    expect(readCancelLinkFragment(`#cancel-deletion=${T}&cancel-deletion=${'B'.repeat(43)}`)).toBeNull()
    expect(readCancelLinkFragment(`#cancel-deletion=${'A'.repeat(42)}`)).toBeNull()
    expect(readCancelLinkFragment(`#cancel-deletion=${'A'.repeat(44)}`)).toBeNull()
    expect(readCancelLinkFragment(`#invite=${T}`)).toBeNull()
    expect(readCancelLinkFragment('')).toBeNull()
  })

  it('workspaceDeletion: a held cancel link lives ten minutes in this tab', () => {
    const m = memory()
    const t = 1_000_000
    const ten = 10 * 60 * 1000
    holdCancelLink(T, t)
    expect(peekCancelLink(t + ten - 60_000)).toBe(T)
    expect(peekCancelLink(t + ten)).toBeNull()
    expect(peekCancelLink(t - 1)).toBeNull()
    holdCancelLink(null)
    expect(peekCancelLink(t)).toBeNull()
    expect(m.has(CANCEL_LINK_KEY)).toBe(false)

    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    m.set(CANCEL_LINK_KEY, '{not json')
    expect(peekCancelLink(t)).toBeNull()
    expect(warn).toHaveBeenCalled()
  })

  it('workspaceDeletion: cancelWorkspaceDeletionByLink posts the token', async () => {
    const f = vi.fn().mockResolvedValue({})
    await cancelWorkspaceDeletionByLink(f, 'B', T)
    expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/workspace/deletion/cancel-by-link', {
      method: 'POST',
      body: { token: T },
    })
  })
})
