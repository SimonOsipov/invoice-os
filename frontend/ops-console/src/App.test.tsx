import type { ReactElement, ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { StaffGate } from '@invoice-os/console-session'

import App from './App'
import { NAV_ITEMS } from './data'

vi.mock('@invoice-os/console-session', () => ({
  StaffGate: function StaffGate({ children }: { children: ReactNode }) {
    return children
  },
  signOutConsole: vi.fn(),
}))

// the key the console already stores under (moves from session.ts to auth.ts)
const OPS_SESSION_KEY = 'invoice-os.ops-session'

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
    expect(el.props.storageKey).toBe(OPS_SESSION_KEY)
    expect(el.props.target).toBe('ops')

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

  it('App_rendersTheSameConsoleWhenTheGateOpens', () => {
    // React hoists the logo preload <link> ahead of the root.
    const html = renderToStaticMarkup(<App />).replace(/^(<link[^>]*>)+/, '').replaceAll('&amp;', '&')

    // The gate adds no wrapper: the console's own root is the document root.
    expect(html.startsWith('<div class="asc-app"')).toBe(true)
    expect(html.match(/class="asc-app"/g)).toHaveLength(1)
    expect(html).toContain('<aside')
    expect(html).toContain('<main')
    expect(html).toMatch(/<h1[^>]*>Overview<\/h1>/)
    expect(NAV_ITEMS.length).toBeGreaterThan(0)
    for (const n of NAV_ITEMS) expect(html, `nav item ${n.label}`).toContain(n.label)
    expect(html.match(/aria-label="Sign out"/g)).toHaveLength(1)
  })
})
