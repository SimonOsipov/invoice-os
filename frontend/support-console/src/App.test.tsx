import type { ReactElement, ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { StaffGate } from '@invoice-os/console-session'

import App from './App'
import { Sidebar } from './components/Sidebar'
import { TopBar } from './components/TopBar'
import { KEPT_KEY } from './blog'
import { GLYPHS } from './glyphs'
import { NAV_ITEMS } from './nav'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  StaffGate: function StaffGate({ children }: { children: ReactNode }) {
    return children
  },
  signOutConsole: vi.fn(),
  expireConsole: vi.fn(),
}))

// the key the console already stores under (moves from session.ts to auth.ts)
const SUPPORT_SESSION_KEY = 'invoice-os.support-session'

type GateProps = {
  storageKey: string
  target: string
  gateway: string | null
  landing: string | null
  app: string | null
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
    const unset = renderApp()
    expect(unset.type).toBe(StaffGate)
    expect(unset.props.gateway).toBeNull()
    expect(unset.props.landing).toBe('https://land.example')
  })

  it('the console renders nothing without a landing URL', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://land.example/')
    expect(renderToStaticMarkup(<App />)).toContain('asc-app')

    for (const unset of ['', '   ', '/', undefined]) {
      vi.stubEnv('VITE_LANDING_URL', unset)
      expect(renderToStaticMarkup(<App />), JSON.stringify(unset)).toBe('')
    }
  })

  it('App_passesTheAppBaseToTheGate', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example/')
    expect(renderApp().props.app).toBe('https://app.example')

    vi.stubEnv('VITE_APP_URL', '')
    expect(renderApp().props.app).toBeNull()
  })

  it('App_rendersTheSameConsoleWhenTheGateOpens', () => {
    // React hoists the logo preload <link> ahead of the root.
    const html = renderToStaticMarkup(<App />).replace(/^(<link[^>]*>)+/, '').replaceAll('&amp;', '&')

    // The gate adds no wrapper: the console's own root is the document root.
    expect(html.startsWith('<div class="asc-app"')).toBe(true)
    expect(html.match(/class="asc-app"/g)).toHaveLength(1)
    expect(html).toContain('<aside')
    expect(html).toContain('<main')
    expect(html).toMatch(/<h1[^>]*>Rules admin<\/h1>/)
    expect(NAV_ITEMS.map((n) => n.key)).toEqual(['submissions', 'rules', 'audit', 'tenants', 'health', 'ai', 'blog'])
    for (const n of NAV_ITEMS) expect(html, `nav item ${n.label}`).toContain(n.label)
    expect(html.match(/aria-label="Sign out"/g)).toHaveLength(1)
  })

  it('the Blog crumb is Blog', () => {
    const html = renderToStaticMarkup(<TopBar screen="blog" env="sandbox" onSetEnv={() => undefined} />)
    expect(html).toContain('>Blog<')
  })

  it('the Blog nav row carries the file-text glyph at size 17', () => {
    const blog = NAV_ITEMS.find((n) => n.key === 'blog')
    expect(blog).toBeDefined()
    const html = renderToStaticMarkup(<>{blog!.glyph}</>)
    expect(html).toContain('width="17"')
    expect(html).toContain('height="17"')
    expect(html).toContain(GLYPHS['file-text'][0])
  })

  it('the sidebar names the Blog row Blog', () => {
    const html = renderToStaticMarkup(<Sidebar screen="blog" onNavigate={() => undefined} />)
    expect(html).toContain('>Blog</span>')
  })
})

describe('boot screen', () => {
  const jwt = (c: object) => `${btoa('{"alg":"x"}')}.${btoa(JSON.stringify(c)).replace(/=+$/, '')}.s`
  const landing = (sub: string) => JSON.stringify({ v: 2, token: jwt({ sub }), refresh_token: 'R' })
  const store = (session: string | null, owner: string | null) => {
    const map = new Map<string, string>()
    if (session !== null) map.set(SUPPORT_SESSION_KEY, session)
    if (owner) map.set(KEPT_KEY, JSON.stringify({ v: 1, owner, draft: { id: '', status: 'draft', title: '', summary: '', featured: false, serverHasCover: false, cover: { kind: 'none' }, body: [] } }))
    vi.stubGlobal('localStorage', { getItem: (k: string) => map.get(k) ?? null })
    vi.stubEnv('VITE_LANDING_URL', 'https://land.example/')
  }
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  it('starts on Blog with a kept copy of the signed-in member', () => {
    store(landing('S'), 'S')
    expect(renderToStaticMarkup(<App />)).toContain('07 — CONTENT')
  })

  it('starts on Blog for a borrowed session with a kept copy', () => {
    store(JSON.stringify({ v: 2, token: jwt({ sub: 'S' }), from: 'app' }), 'S')
    expect(renderToStaticMarkup(<App />)).toContain('07 — CONTENT')
  })

  it('starts on Rules for another owner or with no kept copy', () => {
    store(landing('S'), 'T')
    expect(renderToStaticMarkup(<App />)).toMatch(/<h1[^>]*>Rules admin<\/h1>/)
    store(landing('S'), null)
    expect(renderToStaticMarkup(<App />)).toMatch(/<h1[^>]*>Rules admin<\/h1>/)
  })

  it('starts on Rules when a kept copy exists but no session is stored', () => {
    store(null, 'S')
    expect(renderToStaticMarkup(<App />)).toMatch(/<h1[^>]*>Rules admin<\/h1>/)
  })
})
