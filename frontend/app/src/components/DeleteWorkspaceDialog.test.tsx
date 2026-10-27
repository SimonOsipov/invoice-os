// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { PlatformCtx } from '../types'
import { EXPORT_COPY } from '../lib/workspaceExport'
import { DeleteWorkspaceDialog } from './DeleteWorkspaceDialog'

const startExport = vi.hoisted(() => vi.fn())
vi.mock('../lib/workspaceExport', async (orig) => ({ ...(await orig<typeof import('../lib/workspaceExport')>()), startWorkspaceExport: startExport }))

afterEach(() => {
  cleanup()
  startExport.mockReset()
})

const NAME = 'Okafor & Co'
const MISMATCH = 'the workspace name does not match'

function setup(authedFetch: unknown) {
  const lockWorkspace = vi.fn()
  const onClose = vi.fn()
  const ctx = { authedFetch, lockWorkspace } as unknown as PlatformCtx
  render(<DeleteWorkspaceDialog ctx={ctx} base="https://gw.test" name={NAME} onClose={onClose} />)
  return { lockWorkspace, onClose }
}

const confirm = () => screen.getByTestId('delete-workspace-confirm') as HTMLButtonElement
const tick = () => screen.getByTestId('delete-workspace-exported') as HTMLInputElement
const exportBtn = () => screen.getByTestId('delete-workspace-export') as HTMLButtonElement
const input = () => screen.getByTestId('delete-workspace-name') as HTMLInputElement

describe('DeleteWorkspaceDialog', () => {
  it('Delete needs the tick and the exact name', async () => {
    setup(vi.fn())
    fireEvent.change(input(), { target: { value: NAME } })
    expect(confirm().disabled).toBe(true)
    fireEvent.click(tick())
    expect(confirm().disabled).toBe(false)
    fireEvent.click(tick())
    expect(confirm().disabled).toBe(true)
    fireEvent.click(tick())
    fireEvent.change(input(), { target: { value: 'okafor & co' } })
    expect(confirm().disabled).toBe(true)
  })

  it('Download export starts one export and shows it started', async () => {
    let done: () => void = () => {}
    startExport.mockReturnValue(new Promise<void>((r) => (done = r)))
    setup(vi.fn())
    fireEvent.click(exportBtn())
    fireEvent.click(exportBtn())
    expect(startExport).toHaveBeenCalledTimes(1)
    expect(exportBtn().disabled).toBe(true)
    done()
    expect((await screen.findByTestId('delete-workspace-export-status')).textContent).toBe(EXPORT_COPY.started)
    expect(exportBtn().disabled).toBe(false)
  })

  it('a running delete disables Download export and the tick', async () => {
    setup(vi.fn().mockReturnValue(new Promise(() => {})))
    fireEvent.change(input(), { target: { value: NAME } })
    fireEvent.click(tick())
    await userEvent.click(confirm())
    await waitFor(() => expect(confirm().disabled).toBe(true))
    expect(exportBtn().disabled).toBe(true)
    expect(tick().disabled).toBe(true)
  })

  it('a failed mint shows the sentence', async () => {
    startExport.mockRejectedValue(new ApiError('http', 'unauthorized', 401))
    setup(vi.fn())
    fireEvent.click(exportBtn())
    const status = await screen.findByTestId('delete-workspace-export-status')
    expect(status.textContent).toBe('unauthorized')
    expect(status.style.color).toBe('var(--status-red-text)')
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('a running export shows the sentence', async () => {
    const sentence = 'an export of this workspace is already running'
    startExport.mockRejectedValue(new ApiError('http', sentence, 409))
    setup(vi.fn())
    fireEvent.click(exportBtn())
    const status = await screen.findByTestId('delete-workspace-export-status')
    expect(status.textContent).toBe(sentence)
    expect(status.style.color).toBe('var(--status-red-text)')
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(tick().disabled).toBe(false)
  })

  it('Escape and Cancel close only while idle', async () => {
    const pending = new Promise(() => {})
    const { onClose } = setup(vi.fn().mockReturnValue(pending))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)

    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(2)

    fireEvent.change(input(), { target: { value: NAME } })
    fireEvent.click(tick())
    await userEvent.click(confirm())
    await waitFor(() => expect(confirm().disabled).toBe(true))
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByTestId('delete-workspace-dialog'))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('success locks, failure explains', async () => {
    const ok = setup(vi.fn().mockResolvedValue({ deletion_due_at: '2026-11-25T00:00:00Z' }))
    fireEvent.change(input(), { target: { value: NAME } })
    fireEvent.click(tick())
    await userEvent.click(confirm())
    await waitFor(() => expect(ok.lockWorkspace).toHaveBeenCalledWith('2026-11-25T00:00:00Z'))
    cleanup()

    const bad = setup(vi.fn().mockRejectedValue(new ApiError('http', MISMATCH, 400)))
    fireEvent.change(input(), { target: { value: NAME } })
    fireEvent.click(tick())
    await userEvent.click(confirm())
    expect((await screen.findByTestId('delete-workspace-error')).textContent).toBe(MISMATCH)
    expect(bad.lockWorkspace).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it.each([
    [409, 'the house workspace cannot be deleted'],
    [403, 'only an admin can delete the workspace'],
  ])('a %i refusal shows the server sentence and keeps the dialog', async (status, sentence) => {
    const { lockWorkspace } = setup(vi.fn().mockRejectedValue(new ApiError('http', sentence, status)))
    fireEvent.change(input(), { target: { value: NAME } })
    fireEvent.click(tick())
    await userEvent.click(confirm())
    expect((await screen.findByTestId('delete-workspace-error')).textContent).toBe(sentence)
    expect(lockWorkspace).not.toHaveBeenCalled()
    expect(confirm().disabled).toBe(false)
  })
})
