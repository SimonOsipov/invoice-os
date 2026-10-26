import { describe, expect, it, vi } from 'vitest'

import {
  DELETE_ACCOUNT_PHRASE,
  accountDeletionBody,
  deleteMyAccount,
  phraseMatches,
  readAccountDeletion,
} from './accountDeletion'

describe('accountDeletion: the two calls hit their routes', () => {
  it('read sends GET and maps a missing or null heir_name to null', async () => {
    const f = vi
      .fn()
      .mockResolvedValueOnce({ outcome: 'leave' })
      .mockResolvedValueOnce({ outcome: 'hand_on', heir_name: null })
      .mockResolvedValueOnce({ outcome: 'hand_on', heir_name: 'Chidi Eze' })
    expect(await readAccountDeletion(f, 'B')).toEqual({ outcome: 'leave', heirName: null })
    expect(await readAccountDeletion(f, 'B')).toEqual({ outcome: 'hand_on', heirName: null })
    expect(await readAccountDeletion(f, 'B')).toEqual({ outcome: 'hand_on', heirName: 'Chidi Eze' })
    expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/me/deletion')
  })

  it('delete posts exactly {outcome} and maps the answer', async () => {
    for (const outcome of ['leave', 'hand_on'] as const) {
      const f = vi.fn().mockResolvedValue({ outcome, extra: 1 })
      expect(await deleteMyAccount(f, 'B', outcome)).toEqual({ outcome })
      expect(f).toHaveBeenCalledWith('B/api/tenancy/v1/me/deletion', { method: 'POST', body: { outcome } })
    }
  })
})

describe('accountDeletion: the body names the workspace or the heir', () => {
  it('leave names the workspace', () => {
    const p = accountDeletionBody({ outcome: 'leave', heirName: null }, 'Okafor & Co')
    expect(p[1]).toBe('Okafor & Co')
    expect(p.join('')).toBe('Your account goes at once. Your name stays in the history of Okafor & Co, marked (deleted).')
  })

  it('hand_on names the heir, or the no-name sentence', () => {
    const named = accountDeletionBody({ outcome: 'hand_on', heirName: 'Chidi Eze' }, 'W')
    expect(named[1]).toBe('Chidi Eze')
    expect(named.join('')).toBe('You are the last admin. Admin moves to Chidi Eze, who joined first.')
    expect(accountDeletionBody({ outcome: 'hand_on', heirName: null }, 'W')).toEqual([
      'You are the last admin. Admin moves to the member who joined first.',
    ])
  })

  it('delete_workspace names the workspace, else "this workspace"', () => {
    const p = accountDeletionBody({ outcome: 'delete_workspace', heirName: null }, null)
    expect(p[1]).toBe('this workspace')
    expect(p.join('')).toBe('You are the last member. this workspace will be deleted in 30 days.')
    expect(accountDeletionBody({ outcome: 'delete_workspace', heirName: null }, 'Okafor & Co')[1]).toBe('Okafor & Co')
  })
})

describe('accountDeletion: the phrase must be exact', () => {
  it('only the exact phrase passes', () => {
    expect(phraseMatches(DELETE_ACCOUNT_PHRASE)).toBe(true)
    expect(phraseMatches('delete my account')).toBe(true)
    for (const typed of ['Delete my account', 'delete my account ', ' delete my account', '']) {
      expect(phraseMatches(typed)).toBe(false)
    }
  })
})
