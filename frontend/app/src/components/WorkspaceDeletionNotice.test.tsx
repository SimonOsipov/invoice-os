// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { AuthedFetch } from '../lib/portfolio'
import { EXPORT_COPY } from '../lib/workspaceExport'
import { WorkspaceDeletionNotice } from './WorkspaceDeletionNotice'

const startExport = vi.hoisted(() => vi.fn())
vi.mock('../lib/workspaceExport', async (orig) => ({ ...(await orig<typeof import('../lib/workspaceExport')>()), startWorkspaceExport: startExport }))

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
  startExport.mockReset()
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

  it('under StrictMode two reads overlap and a stale answer arriving last does not overwrite the newer one', async () => {
    const c = controlled()
    render(
      <StrictMode>
        <WorkspaceDeletionNotice authedFetch={c.fetch} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />
      </StrictMode>,
    )
    expect(c.pending).toHaveLength(2)
    await act(async () => c.pending[1].resolve(me('2026-11-24T12:00:00Z', 'Newer Co')))
    await act(async () => c.pending[0].resolve(me('2026-11-20T12:00:00Z', 'Stale Co')))
    const text = screen.getByTestId('workspace-deletion-notice').textContent
    expect(text).toContain('Newer Co')
    expect(text).not.toContain('Stale Co')
  })

  it('under StrictMode a stale read failing after the newer read started does not replace Loading with the error', async () => {
    const c = controlled()
    render(
      <StrictMode>
        <WorkspaceDeletionNotice authedFetch={c.fetch} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />
      </StrictMode>,
    )
    expect(c.pending).toHaveLength(2)
    await act(async () => c.pending[0].reject(new ApiError('network', 'down', 0, null)))
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
    await act(async () => c.pending[1].resolve(me('2026-11-24T12:00:00Z', 'Newer Co')))
    expect(screen.getByTestId('workspace-deletion-notice').textContent).toContain('Newer Co')
  })

  it('Retry after the authedFetch changed reads through the newest fetch', async () => {
    const a = controlled()
    const view = mount(a.fetch)
    await act(async () => a.pending[0].reject(new ApiError('network', 'down', 0, null)))
    const b = controlled()
    view.rerender(
      <WorkspaceDeletionNotice authedFetch={b.fetch} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />,
    )
    fireEvent.click(await screen.findByRole('button', { name: /retry/i }))
    expect(b.fetch).toHaveBeenCalledTimes(1)
    expect(a.fetch).toHaveBeenCalledTimes(1)
  })

  it('bolds the date and the workspace name and leaves the sentence unchanged', async () => {
    const c = controlled()
    mount(c.fetch)
    await act(async () => c.pending[0].resolve(me('2026-11-24T12:00:00Z', 'Okafor & Co')))
    const bold = Array.from(screen.getByTestId('workspace-deletion-notice').querySelectorAll('b')).map((b) => b.textContent)
    expect(bold).toEqual(['24 November 2026', 'Okafor & Co'])
    expect(screen.getByText(/we delete every invoice/).textContent).toBe(
      'On 24 November 2026 we delete every invoice, file and member of Okafor & Co.',
    )
  })
})

describe('WorkspaceDeletionNotice export', () => {
  const buttons = () => screen.getAllByRole('button').map((b) => b.textContent)
  const exportBtn = () => screen.getByTestId('deletion-notice-export') as HTMLButtonElement

  it('an admin sees Download export first, a preparer sees neither', async () => {
    const c = controlled()
    mount(c.fetch)
    await act(async () => c.pending[0].resolve(me('2026-11-24T12:00:00Z')))
    expect(buttons()).toEqual(['Download export', 'Cancel deletion', 'Sign out'])
    cleanup()

    const p = controlled()
    mount(p.fetch)
    await act(async () => p.pending[0].resolve({ ...me('2026-11-24T12:00:00Z'), user: { id: 'u2', role: 'preparer', display_name: null, email: null } }))
    expect(buttons()).toEqual(['Sign out'])
  })

  it('Download export starts the export and reports it', async () => {
    const c = controlled()
    mount(c.fetch)
    await act(async () => c.pending[0].resolve(me('2026-11-24T12:00:00Z')))
    startExport.mockResolvedValueOnce(undefined)
    fireEvent.click(exportBtn())
    expect((await screen.findByTestId('deletion-notice-export-status')).textContent).toBe(EXPORT_COPY.started)
    expect(startExport).toHaveBeenCalledTimes(1)

    startExport.mockRejectedValueOnce(new ApiError('http', 'unauthorized', 401))
    fireEvent.click(exportBtn())
    await waitFor(() => expect(screen.getByTestId('deletion-notice-export-status').textContent).toBe('unauthorized'))

    const sentence = 'an export of this workspace is already running'
    startExport.mockRejectedValueOnce(new ApiError('http', sentence, 409))
    fireEvent.click(exportBtn())
    await waitFor(() => expect(screen.getByTestId('deletion-notice-export-status').textContent).toBe(sentence))
    expect(screen.getByTestId('deletion-notice-export-status').style.color).toBe('var(--status-red-text)')

    fireEvent.click(screen.getByTestId('deletion-notice-cancel'))
    expect(exportBtn().disabled).toBe(true)
  })

  it('the export adds no read and uses the latest fetch', async () => {
    const { startWorkspaceExport } = await vi.importActual<typeof import('../lib/workspaceExport')>('../lib/workspaceExport')
    startExport.mockImplementation(startWorkspaceExport)
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    const a = controlled()
    const view = mount(a.fetch)
    await act(async () => a.pending[0].resolve(me('2026-11-24T12:00:00Z')))
    const b = controlled()
    view.rerender(<WorkspaceDeletionNotice authedFetch={b.fetch} base="" known={KNOWN} onUnlocked={() => {}} onSignOut={() => {}} />)
    expect(b.fetch).not.toHaveBeenCalled()
    expect(a.fetch).toHaveBeenCalledTimes(1)

    fireEvent.click(exportBtn())
    await waitFor(() => expect(b.fetch).toHaveBeenCalledTimes(1))
    await act(async () => b.pending[0].resolve(undefined))
    await waitFor(() => expect(b.fetch).toHaveBeenCalledTimes(2))
    expect(a.fetch).toHaveBeenCalledTimes(1)
    await act(async () => b.pending[1].resolve({ code: 'c' }))
    expect(submit).toHaveBeenCalledTimes(1)
    submit.mockRestore()
  })
})
