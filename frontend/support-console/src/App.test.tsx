import type { ReactElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { StaffGate } from '@invoice-os/console-session'

import App from './App'

vi.mock('@invoice-os/console-session', () => ({
  StaffGate: function StaffGate() {
    return null
  },
  signOutConsole: vi.fn(),
}))

// the key the console already stores under (moves from session.ts to auth.ts)
const SUPPORT_SESSION_KEY = 'invoice-os.support-session'

type GateProps = {
  storageKey: string
  target: string
  gateway: string | null
  landing: string | null
  children: ReactElement | ReactElement[]
}

// App must be a hookless element factory so the gate owns all boot state.
function renderApp(): ReactElement<GateProps> {
  let el: ReactElement<GateProps> | undefined
  expect(() => {
    el = App() as ReactElement<GateProps>
  }, 'App() returns the StaffGate element without running hooks').not.toThrow()
  return el as ReactElement<GateProps>
}

describe('App', () => {
  afterEach(() => vi.unstubAllEnvs())

  it('App_wrapsTheConsoleInTheSharedStaffGate', () => {
    const el = renderApp()

    expect(el.type).toBe(StaffGate)
    expect(el.props.storageKey).toBe(SUPPORT_SESSION_KEY)
    expect(el.props.target).toBe('support')

    const kids = Array.isArray(el.props.children) ? el.props.children : [el.props.children]
    expect(kids).toHaveLength(1)
    expect(typeof kids[0].type === 'function' && kids[0].type.name).toBe('Console')
  })

  it('App_passesTheBuildsGatewayAndLanding', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.example/')
    vi.stubEnv('VITE_LANDING_URL', 'https://land.example/')
    const set = renderApp()
    expect(set.type).toBe(StaffGate)
    expect(set.props.gateway).toBe('https://gw.example')
    expect(set.props.landing).toBe('https://land.example')

    vi.stubEnv('VITE_GATEWAY_URL', '')
    vi.stubEnv('VITE_LANDING_URL', '')
    const unset = renderApp()
    expect(unset.type).toBe(StaffGate)
    expect(unset.props.gateway).toBeNull()
    expect(unset.props.landing).toBeNull()
  })
})
