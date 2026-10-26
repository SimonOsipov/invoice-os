// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { PlatformCtx } from '../types'
import { DeleteAccountDialog } from './DeleteAccountDialog'

afterEach(cleanup)

function setup(authedFetch: unknown, outcome: 'leave' | 'hand_on' = 'leave') {
  const signOut = vi.fn()
  const accountDeleted = vi.fn()
  const lockWorkspace = vi.fn()
  const onClose = vi.fn()
  const ctx = { authedFetch, signOut, accountDeleted, lockWorkspace, user: { tenantName: 'Okafor & Co' } } as unknown as PlatformCtx
  render(<DeleteAccountDialog ctx={ctx} base="https://gw.test" preview={{ outcome, heirName: 'Chidi Eze', heirUserId: 'u-heir' }} outcome={outcome} onClose={onClose} />)
  return { signOut, accountDeleted, lockWorkspace, onClose }
}

const confirm = () => screen.getByTestId('delete-account-confirm') as HTMLButtonElement
const input = () => screen.getByTestId('delete-account-typed') as HTMLInputElement

describe('DeleteAccountDialog', () => {
  it('DeleteAccountDialog: confirm needs the exact phrase', () => {
    setup(vi.fn())
    expect(confirm().disabled).toBe(true)
    for (const [typed, enabled] of [['Delete my account', false], ['delete my account ', false], ['delete my account', true]] as const) {
      fireEvent.change(input(), { target: { value: typed } })
      expect(confirm().disabled, typed).toBe(!enabled)
    }
  })

  it('DeleteAccountDialog: Escape and Cancel close only while idle', async () => {
    const { onClose } = setup(vi.fn().mockReturnValue(new Promise(() => {})))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(2)

    fireEvent.change(input(), { target: { value: 'delete my account' } })
    await userEvent.click(confirm())
    await waitFor(() => expect(confirm().disabled).toBe(true))
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByTestId('delete-account-dialog'))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it.each(['leave', 'hand_on'] as const)('DeleteAccountDialog: a 200 leaves without a sign-out request (%s)', async (outcome) => {
    const f = vi.fn().mockResolvedValue({ outcome })
    const { signOut, accountDeleted, lockWorkspace } = setup(f, outcome)
    fireEvent.change(input(), { target: { value: 'delete my account' } })
    await userEvent.click(confirm())
    await waitFor(() => expect(accountDeleted).toHaveBeenCalledTimes(1))
    expect(signOut).not.toHaveBeenCalled()
    expect(lockWorkspace).not.toHaveBeenCalled()
    expect(f).toHaveBeenCalledWith('https://gw.test/api/tenancy/v1/me/deletion', { method: 'POST', body: outcome === 'hand_on' ? { outcome, heir_user_id: 'u-heir' } : { outcome } })
  })

  it('DeleteAccountDialog: the body names the heir in bold', () => {
    setup(vi.fn(), 'hand_on')
    const b = screen.getByRole('dialog').querySelector('b')!
    expect(b.textContent).toBe('Chidi Eze')
  })
})
