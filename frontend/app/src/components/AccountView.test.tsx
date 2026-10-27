// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { PlatformCtx } from '../types'
import { AccountView } from './AccountView'

type Preview = { outcome: string; heir_name?: string | null; heir_user_id?: string | null }

beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test'))
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
})

// Each GET answers the next preview; the last one repeats.
function setup(previews: Preview[], over: { tenantName?: string | null; post?: () => Promise<unknown> } = {}) {
  let n = 0
  const gets = vi.fn()
  const authedFetch = vi.fn(async (_url: string, opts?: { method?: string }) => {
    if (opts?.method === 'POST') return over.post ? over.post() : { outcome: 'leave' }
    gets()
    return previews[Math.min(n++, previews.length - 1)]
  })
  const ctx = {
    authedFetch,
    signOut: vi.fn(),
    accountDeleted: vi.fn(),
    lockWorkspace: vi.fn(),
    user: { name: 'Ada', initials: 'A', tenantName: over.tenantName === undefined ? 'Okafor & Co' : over.tenantName },
  } as unknown as PlatformCtx
  render(<AccountView ctx={ctx} />)
  return { gets, ctx }
}

const button = () => screen.getByTestId('account-delete') as HTMLButtonElement
const body = () => screen.getByTestId('account-delete-body')
const ready = () => screen.findByTestId('account-delete-card')

describe('AccountView', () => {
  it("AccountView: the card speaks the preview's outcome", async () => {
    for (const [preview, text, bold, label] of [
      [{ outcome: 'leave' }, 'Your account goes at once. Your name stays in the history of Okafor & Co, marked (deleted).', 'Okafor & Co', 'Delete my account'],
      [{ outcome: 'hand_on', heir_name: 'Chidi Eze', heir_user_id: 'u-chidi' }, 'You are the last admin. Admin moves to Chidi Eze, who joined first.', 'Chidi Eze', 'Delete my account'],
      [{ outcome: 'delete_workspace' }, 'You are the last member. Okafor & Co will be deleted in 30 days.', 'Okafor & Co', 'Delete my account and workspace'],
    ] as const) {
      setup([preview])
      await ready()
      expect(body().textContent).toBe(text)
      expect(body().querySelector('b')!.textContent).toBe(bold)
      expect(button().textContent).toBe(label)
      cleanup()
    }
  })

  it('AccountView: the last member gets the delete-workspace dialog', async () => {
    const { gets } = setup([{ outcome: 'delete_workspace' }])
    await ready()
    await userEvent.click(button())
    expect(await screen.findByTestId('delete-workspace-dialog')).toBeTruthy()
    expect(screen.queryByTestId('delete-account-dialog')).toBeNull()
    expect(screen.getByTestId('delete-workspace-name-shown').textContent).toBe('Okafor & Co')
    expect(gets).toHaveBeenCalledTimes(2)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('delete-workspace-dialog')).toBeNull()
    await waitFor(() => expect(gets).toHaveBeenCalledTimes(3))
  })

  it('AccountView: the button re-reads the outcome before it opens a dialog', async () => {
    const { gets } = setup([{ outcome: 'delete_workspace' }, { outcome: 'hand_on', heir_name: 'Chidi Eze', heir_user_id: 'u-chidi' }])
    await ready()
    await userEvent.click(button())
    await waitFor(() => expect(body().textContent).toContain('Chidi Eze'))
    expect(gets).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('delete-workspace-dialog')).toBeNull()
    expect(screen.queryByTestId('delete-account-dialog')).toBeNull()
    cleanup()

    setup([{ outcome: 'hand_on', heir_name: 'Chidi Eze', heir_user_id: 'u-chidi' }, { outcome: 'hand_on', heir_name: 'Ngozi Bello', heir_user_id: 'u-ngozi' }])
    await ready()
    await userEvent.click(button())
    await waitFor(() => expect(body().textContent).toContain('Ngozi Bello'))
    expect(screen.queryByTestId('delete-account-dialog')).toBeNull()
    cleanup()

    // a different person with the same name, and the same person renamed, are both a changed page
    for (const second of [{ heir_name: 'Chidi Eze', heir_user_id: 'u-other-chidi' }, { heir_name: 'Chidi Okoro', heir_user_id: 'u-chidi' }]) {
      const { gets: pair } = setup([{ outcome: 'hand_on', heir_name: 'Chidi Eze', heir_user_id: 'u-chidi' }, { outcome: 'hand_on', ...second }])
      await ready()
      await userEvent.click(button())
      await waitFor(() => expect(pair).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(button().disabled).toBe(false))
      expect(screen.queryByTestId('delete-account-dialog')).toBeNull()
      cleanup()
    }

    setup([{ outcome: 'leave' }, { outcome: 'leave' }])
    await ready()
    await userEvent.click(button())
    expect(await screen.findByTestId('delete-account-dialog')).toBeTruthy()
  })

  it('AccountView: disabled without a gateway, a preview or a workspace name', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const { gets } = setup([{ outcome: 'leave' }])
    expect(button().disabled).toBe(true)
    expect(button().style.opacity).toBe('0.45')
    expect(gets).not.toHaveBeenCalled()
    cleanup()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')

    const pending = vi.fn(() => new Promise(() => {}))
    render(<AccountView ctx={{ authedFetch: pending, user: { tenantName: 'Okafor & Co' } } as unknown as PlatformCtx} />)
    expect(button().disabled).toBe(true)
    expect(button().style.opacity).toBe('0.45')
    cleanup()

    setup([{ outcome: 'delete_workspace' }], { tenantName: null })
    await ready()
    expect(button().disabled).toBe(true)
    expect(button().style.opacity).toBe('0.45')
  })

  it('AccountView: a failed re-read on click shows the error and opens no dialog', async () => {
    const f = vi.fn().mockResolvedValueOnce({ outcome: 'leave' }).mockRejectedValueOnce(new ApiError('http', 'boom', 500))
    render(<AccountView ctx={{ authedFetch: f, user: { tenantName: 'Okafor & Co' } } as unknown as PlatformCtx} />)
    await ready()
    await userEvent.click(button())
    expect((await screen.findByTestId('account-delete-error')).textContent).toBe('boom')
    expect(screen.queryByTestId('delete-account-dialog')).toBeNull()
    expect(screen.queryByTestId('delete-workspace-dialog')).toBeNull()
  })

  it('AccountView: a failed read shows Retry', async () => {
    const f = vi.fn().mockRejectedValueOnce(new ApiError('http', 'boom', 500)).mockResolvedValue({ outcome: 'leave' })
    render(<AccountView ctx={{ authedFetch: f, user: { tenantName: 'Okafor & Co' } } as unknown as PlatformCtx} />)
    fireEvent.click(await screen.findByRole('button', { name: /retry/i }))
    expect(await ready()).toBeTruthy()
  })

  it('AccountView: a refusal shows why and the page re-reads', async () => {
    const sentence = 'the workspace changed; review the page and confirm again'
    const { gets, ctx } = setup([{ outcome: 'leave' }], { post: () => Promise.reject(new ApiError('http', sentence, 409)) })
    await ready()
    await userEvent.click(button())
    fireEvent.change(await screen.findByTestId('delete-account-typed'), { target: { value: 'delete my account' } })
    await userEvent.click(screen.getByTestId('delete-account-confirm'))
    expect((await screen.findByTestId('delete-account-error')).textContent).toBe(sentence)
    expect(ctx.signOut).not.toHaveBeenCalled()
    expect(ctx.accountDeleted).not.toHaveBeenCalled()
    const before = gets.mock.calls.length
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(gets.mock.calls.length).toBe(before + 1))
  })
})
