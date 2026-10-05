import { afterEach, describe, expect, it, vi } from 'vitest'

import { signOutConsole } from '@invoice-os/console-session'

import { signOut } from './auth'

vi.mock('@invoice-os/console-session', () => ({
  StaffGate: () => null,
  signOutConsole: vi.fn(),
}))

// the key the console already stores under (moves from session.ts to auth.ts)
const SUPPORT_SESSION_KEY = 'invoice-os.support-session'

describe('auth', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.mocked(signOutConsole).mockClear()
  })

  it('signOut_callsTheSharedSignOutWithThisConsolesKey', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.example/')
    vi.stubEnv('VITE_LANDING_URL', 'https://land.example/')
    signOut()
    expect(signOutConsole).toHaveBeenCalledTimes(1)
    expect(signOutConsole).toHaveBeenCalledWith({
      storageKey: SUPPORT_SESSION_KEY,
      gateway: 'https://gw.example',
      landing: 'https://land.example',
    })

    // The env is read per call, not frozen at module load.
    vi.stubEnv('VITE_GATEWAY_URL', '')
    vi.stubEnv('VITE_LANDING_URL', '')
    signOut()
    expect(signOutConsole).toHaveBeenCalledTimes(2)
    expect(signOutConsole).toHaveBeenLastCalledWith({ storageKey: SUPPORT_SESSION_KEY, gateway: null, landing: null })
  })
})
