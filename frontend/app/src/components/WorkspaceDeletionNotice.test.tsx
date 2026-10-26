// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { AuthedFetch } from '../lib/portfolio'
import { WorkspaceDeletionNotice } from './WorkspaceDeletionNotice'

const endAppChat = vi.fn()
vi.mock('../chat', () => ({ endAppChat: () => endAppChat() }))

const me = (dueAt: string | null, name = 'Adaeze Ventures') => ({
  tenant: { id: 't1', name, kind: 'firm', deletion_due_at: dueAt },
  user: { id: 'u1', role: 'admin', display_name: null, email: null },
})
const KNOWN = { dueAt: null, name: null, isAdmin: true }

type Deferred = { resolve: (v: unknown) => void; reject: (e: unknown) => void }
function controlled() {
  const pending: Deferred[] = []
  const fetch = vi.fn(
    () => new Promise((resolve, reject) => pending.push({ resolve, reject })),
  ) as unknown as AuthedFetch & ReturnType<typeof vi.fn>
  return { fetch, pending }
}

const mount = (f: AuthedFetch) =>
  render(<WorkspaceDeletionNotice authedFetch={f} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />)

afterEach(() => {
  cleanup()
  endAppChat.mockClear()
})

describe('WorkspaceDeletionNotice reads', () => {
  it('a new authedFetch while the notice is up issues no new /me read and ends the chat once', async () => {
    const a = controlled()
    const view = mount(a.fetch)
    expect(a.fetch).toHaveBeenCalledTimes(1)
    expect(endAppChat).toHaveBeenCalledTimes(1)

    const b = controlled()
    view.rerender(
      <WorkspaceDeletionNotice authedFetch={b.fetch} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />,
    )
    expect(b.fetch).not.toHaveBeenCalled()
    expect(a.fetch).toHaveBeenCalledTimes(1)
    expect(endAppChat).toHaveBeenCalledTimes(1)
  })

  it('Retry after a failed read shows the retry answer', async () => {
    const c = controlled()
    mount(c.fetch)
    await act(async () => c.pending[0].reject(new ApiError('network', 'down', 0, null)))
    fireEvent.click(await screen.findByRole('button', { name: /retry/i }))
    expect(c.fetch).toHaveBeenCalledTimes(2)
    await act(async () => c.pending[1].resolve(me('2026-11-24T12:00:00Z', 'Newer Co')))
    expect(screen.getByTestId('workspace-deletion-notice').textContent).toContain('Newer Co')
  })
})
