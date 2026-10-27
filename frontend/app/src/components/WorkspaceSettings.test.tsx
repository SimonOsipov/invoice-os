// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Member } from '../lib/members'
import { DELETE_ADMIN_ONLY } from '../lib/workspaceDeletion'
import type { PlatformCtx } from '../types'
import { WorkspaceSettings } from './WorkspaceSettings'

const member = (role: Member['role']): Member => ({ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status: 'active', isYou: true })

function wsCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    members: [member('admin')],
    membersState: 'ready',
    user: { name: 'Ada', initials: 'A', tenantName: 'Okafor & Co', verified: true },
    ...over,
  } as unknown as PlatformCtx
}

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
})

const button = () => screen.getByTestId('settings-workspace-delete') as HTMLButtonElement
const reason = () => screen.queryByTestId('settings-workspace-delete-reason')

describe('WorkspaceSettings', () => {
  it('an admin gets an enabled Delete workspace', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<WorkspaceSettings ctx={wsCtx()} />)
    expect(button().disabled).toBe(false)
    expect(button().title).toBe('')
    expect(reason()).toBeNull()
  })

  it('a non-admin gets the sibling disabled treatment', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<WorkspaceSettings ctx={wsCtx({ members: [member('preparer')] })} />)
    expect(button().disabled).toBe(true)
    expect(button().title).toBe(DELETE_ADMIN_ONLY)
    expect(button().style.opacity).toBe('0.45')
    const line = reason()
    expect(line?.textContent).toBe(DELETE_ADMIN_ONLY)
    expect(button().getAttribute('aria-describedby')).toBe(line?.id)
  })

  it('no gateway or no verified name disables it without a reason line', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    render(<WorkspaceSettings ctx={wsCtx()} />)
    expect(button().disabled).toBe(true)
    expect(reason()).toBeNull()
    cleanup()

    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<WorkspaceSettings ctx={wsCtx({ user: { name: 'Ada', initials: 'A', tenantName: null, verified: false } })} />)
    expect(button().disabled).toBe(true)
    expect(reason()).toBeNull()
  })

  it('a roster that has not loaded disables it without a reason line', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<WorkspaceSettings ctx={wsCtx({ members: [], membersState: 'loading' })} />)
    expect(button().disabled).toBe(true)
    expect(reason()).toBeNull()
  })
})
