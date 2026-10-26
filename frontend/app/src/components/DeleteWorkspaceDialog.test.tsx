// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { PlatformCtx } from '../types'
import { DeleteWorkspaceDialog } from './DeleteWorkspaceDialog'

afterEach(cleanup)

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
const input = () => screen.getByTestId('delete-workspace-name') as HTMLInputElement

describe('DeleteWorkspaceDialog', () => {
  it('confirm needs the exact name', async () => {
    setup(vi.fn())
    expect(confirm().disabled).toBe(true)
    for (const [typed, enabled] of [['okafor & co', false], [`${NAME} `, false], [NAME, true]] as const) {
      fireEvent.change(input(), { target: { value: typed } })
      expect(confirm().disabled, typed).toBe(!enabled)
    }
  })

  it('Escape and Cancel close only while idle', async () => {
    const pending = new Promise(() => {})
    const { onClose } = setup(vi.fn().mockReturnValue(pending))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)

    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(2)

    fireEvent.change(input(), { target: { value: NAME } })
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
    await userEvent.click(confirm())
    await waitFor(() => expect(ok.lockWorkspace).toHaveBeenCalledWith('2026-11-25T00:00:00Z'))
    cleanup()

    const bad = setup(vi.fn().mockRejectedValue(new ApiError('http', MISMATCH, 400)))
    fireEvent.change(input(), { target: { value: NAME } })
    await userEvent.click(confirm())
    expect((await screen.findByTestId('delete-workspace-error')).textContent).toBe(MISMATCH)
    expect(bad.lockWorkspace).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })
})
