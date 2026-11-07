// @vitest-environment jsdom
// A real (hand-off) session's Settings shows empty states, not the demo tenant. ctx-cast idiom of Sidebar.test.tsx.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CERTS, ENDPOINTS, WEBHOOKS } from '../data'
import { initialConnectors } from '../lib/connectors'
import { COMPANY_ADMIN_ONLY } from '../lib/members'
import type { PlatformCtx, SettingsTab } from '../types'
import { SettingsView } from './SettingsView'

function settingsCtx(tab: SettingsTab, handoff: boolean, over: Record<string, unknown> = {}): PlatformCtx {
  const ctx = {
    mode: 'firm',
    handoff,
    settingsTab: tab,
    sandbox: true,
    connectors: initialConnectors(handoff),
    connectorMappings: {},
    activeEntity: null,
    entitiesState: 'ready',
    entitiesError: null,
    refetchEntities: vi.fn(),
    members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
    membersState: 'ready',
    setSettingsTab: vi.fn(),
    toggleConnector: vi.fn(),
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

afterEach(cleanup)

describe('Settings > ERP connectors', () => {
  it('hand-off: ERP connectors are all disconnected', () => {
    render(<SettingsView ctx={settingsCtx('connectors', true)} />)
    expect(screen.getByText('0 / 6 CONNECTED')).toBeTruthy()
    expect(screen.getAllByText('NOT CONNECTED')).toHaveLength(6)
    expect(screen.getAllByText('No sync yet')).toHaveLength(6)
    expect(screen.queryByText('Synced 2 min ago')).toBeNull()
  })

  // Control: green before and after.
  it('persona: ERP connectors keep the demo connections', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    expect(screen.getByText('2 / 6 CONNECTED')).toBeTruthy()
    expect(screen.getAllByText('Synced 2 min ago')).toHaveLength(2)
    expect(screen.getAllByText('NOT CONNECTED')).toHaveLength(4)
  })
})

// The detail panel is reachable only through Manage, which only a connected row shows.
describe('Settings > ERP connectors > detail panel', () => {
  it('hand-off: no Manage entry and no mock ERP host render', () => {
    render(<SettingsView ctx={settingsCtx('connectors', true)} />)
    expect(screen.getAllByText('Connect')).toHaveLength(6)
    expect(screen.queryAllByText('Manage')).toHaveLength(0)
    expect(screen.queryByText(/honeywell\.ng/)).toBeNull()
  })

  // Control: Manage opens the panel that carries the mock host.
  it('persona: Manage opens the detail panel with the mock ERP host', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    const manage = screen.getAllByText('Manage')
    expect(manage).toHaveLength(2)
    fireEvent.click(manage[0])
    expect(screen.getAllByText(/erp\.honeywell\.ng:44300/).length).toBeGreaterThan(0)
  })
})

describe('Settings > API & webhooks', () => {
  it('hand-off: no demo webhooks', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    expect(screen.getByText('No webhooks yet')).toBeTruthy()
    expect(WEBHOOKS.length).toBeGreaterThan(0)
    WEBHOOKS.forEach((w) => expect(screen.queryByText(w.event)).toBeNull())
    expect(screen.queryByText(/honeywell\.ng/)).toBeNull()
  })

  // F23: the title alone, no message line.
  it('hand-off: the webhooks empty state carries a title only', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    const title = screen.getByText('No webhooks yet')
    expect(title.parentElement?.textContent).toBe('No webhooks yet')
  })

  it('hand-off: the API keys and Endpoints card headers render', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    expect(screen.getByText('API keys')).toBeTruthy()
    expect(screen.getByText('Endpoints')).toBeTruthy()
    expect(ENDPOINTS.length).toBeGreaterThan(0)
    ENDPOINTS.forEach((e) => expect(screen.getByText(e.path)).toBeTruthy())
  })

  // Control: green before and after.
  it('persona: the demo webhooks still render', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    expect(screen.getAllByText(/honeywell\.ng/)).toHaveLength(3)
    expect(screen.queryByText('No webhooks yet')).toBeNull()
  })
})

describe('Settings > Signing', () => {
  it('hand-off: no demo signing certificate', () => {
    render(<SettingsView ctx={settingsCtx('signing', true)} />)
    expect(screen.getByText('No signing certificate yet')).toBeTruthy()
    expect(screen.getByText('This workspace has no signing certificate.')).toBeTruthy()
    expect(CERTS.length).toBeGreaterThan(0)
    CERTS.forEach((c) => expect(screen.queryByText(c.name)).toBeNull())
    expect(screen.queryByText(/O=Okafor & Partners/)).toBeNull()
    expect(screen.queryByText('ACTIVE')).toBeNull()
  })

  // Control: green before and after.
  it('persona: the demo certificates still render', () => {
    render(<SettingsView ctx={settingsCtx('signing', false)} />)
    expect(screen.getByText('CN=ASComply SI · O=Okafor & Partners')).toBeTruthy()
    expect(screen.getAllByText('ACTIVE')).toHaveLength(2)
    expect(screen.queryByText('No signing certificate yet')).toBeNull()
  })
})

// Resolved paint against the prototype Settings view (radius, dots, rows, titles).
const ENTITY = { id: 'e1', name: 'Honeywell Group', tin: '12345678-0001', sector: 'Food', registration: null, address: '1 Marina Rd', status: 'active', created_at: '2026-01-01T00:00:00.000Z' }
const NRS_LABELS = ['Email', 'Phone', 'Street', 'City', 'Postal zone', 'Country', 'State', 'LGA']
const inhouse = (over: Record<string, unknown> = {}) => settingsCtx('company', false, { mode: 'inhouse', ...over })
const radii = (c: HTMLElement) => [...c.querySelectorAll<HTMLElement>('*')].map((e) => e.style.borderRadius).filter(Boolean)

describe('Settings > shell', () => {
  it('the h1 carries no inline weight and the tab strip is still the pf-tab buttons', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    expect(screen.getByRole('heading', { name: 'Settings' }).style.fontWeight).toBe('')
    const tabs = screen.getAllByRole('button').filter((b) => b.className === 'pf-tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['01Members', '02Roles', '03ERP connectors', '04API & webhooks', '05Signing & certificates', '06Workspace'])
  })

  it('the workspace tab renders the deletion card', () => {
    render(<SettingsView ctx={settingsCtx('workspace', false, { user: { name: 'Ada', initials: 'A', tenantName: 'Okafor & Co', verified: true } })} />)
    expect(screen.getByTestId('settings-workspace-card')).toBeTruthy()
  })

  it('in-house tab strip lists Company first', () => {
    render(<SettingsView ctx={inhouse()} />)
    const tabs = screen.getAllByRole('button').filter((b) => b.className === 'pf-tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['01Company', '02Members', '03Roles', '04ERP connectors', '05API & webhooks', '06Signing & certificates', '07Workspace'])
  })

  it.each(['connectors', 'api', 'signing'] as const)('%s: no 99 or 999 radius anywhere', (tab) => {
    const { container } = render(<SettingsView ctx={settingsCtx(tab, false)} />)
    expect(radii(container).length).toBeGreaterThan(5)
    expect(radii(container).filter((r) => /^(99|999)(px)?$/.test(r))).toHaveLength(0)
  })
})

const USER = { name: 'Ada', initials: 'A', tenantName: 'Okafor & Co', verified: true }
const FIRM_TAB_IDS = ['members', 'roles', 'connectors', 'api', 'signing', 'workspace'] as const
const stripButtons = () => screen.getAllByRole('button').filter((b) => b.className === 'pf-tab')
const tabByLabel = (label: string) => screen.getByRole('button', { name: label })

describe('Settings > tab strip paint', () => {
  it('active tab carries the blog paint', () => {
    render(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    const s = tabByLabel('Roles').style
    expect(s.background).toBe('var(--sage-panel)')
    expect(s.color).toBe('var(--tab-active-text)')
    expect(s.borderBottom).toBe('2px solid var(--tab-active-border)')
    expect(s.marginBottom).toBe('')
    expect(s.borderRadius).toBe('6px 6px 0 0')
    expect(s.fontSize).toBe('13.5px')
    expect(s.fontWeight).toBe('700')
    expect(s.padding).toBe('10px 16px')
    expect(s.gap).toBe('8px')
    expect(s.whiteSpace).toBe('nowrap')
    expect(s.outlineOffset).toBe('-2px')
  })

  it('inactive tab is transparent with a transparent underline', () => {
    render(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    const s = tabByLabel('Members').style
    expect(s.background).toBe('transparent')
    expect(s.color).toBe('var(--fg-3)')
    expect(s.borderBottom).toBe('2px solid transparent')
    expect(s.borderRadius).toBe('6px 6px 0 0')
    expect(s.padding).toBe('10px 16px')
    expect(s.fontSize).toBe('13.5px')
    expect(s.fontWeight).toBe('700')
  })

  it('strip has no gap, a 2px tab-border rule and scrolls sideways', () => {
    render(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    const s = tabByLabel('Members').parentElement!.style
    expect(s.gap).toBe('0')
    expect(s.boxShadow).toBe('inset 0 -2px 0 var(--tab-border)')
    expect(s.overflowX).toBe('auto')
    expect(s.overflowY).toBe('hidden')
    expect(s.borderBottom).toBe('')
  })

  it('tab class stays pf-tab alone (radius survives .pf-btn !important)', () => {
    render(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    const tabs = stripButtons()
    expect(tabs).toHaveLength(6)
    for (const b of tabs) {
      expect(b.className).toBe('pf-tab')
      expect(b.className).not.toContain('pf-btn')
    }
  })

  it('number span is mono, 10px, 0.7, hidden; label is the second and last child', () => {
    render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    const first = stripButtons()[0]
    const num = first.children[0] as HTMLElement
    expect(first.children).toHaveLength(2)
    expect(num.className).toBe('mono')
    expect(num.style.fontSize).toBe('10px')
    expect(num.style.opacity).toBe('0.7')
    expect(num.getAttribute('aria-hidden')).toBe('true')
    expect(first.children[1].textContent).toBe('Members')
  })

  it('in-house: Company is numbered 01 and carries the active paint', () => {
    render(<SettingsView ctx={inhouse()} />)
    const company = stripButtons()[0]
    expect(company.textContent).toBe('01Company')
    expect(company.style.background).toBe('var(--sage-panel)')
  })

  it('clicking a tab selects it', () => {
    const ctx = settingsCtx('members', false, { user: USER, roles: [] })
    render(<SettingsView ctx={ctx} />)
    fireEvent.click(screen.getByRole('button', { name: 'Roles' }))
    expect(ctx.setSettingsTab).toHaveBeenCalledTimes(1)
    expect(ctx.setSettingsTab).toHaveBeenCalledWith('roles')
  })

  it('accessible name equals the label', () => {
    render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    expect(screen.getByRole('button', { name: 'API & webhooks' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Signing & certificates' })).toBeTruthy()
  })

  it.each(FIRM_TAB_IDS)('only the %s tab is active', (id) => {
    render(<SettingsView ctx={settingsCtx(id, false, { user: USER, roles: [] })} />)
    const active = stripButtons().filter((b) => b.style.background === 'var(--sage-panel)')
    expect(active).toHaveLength(1)
    expect(active[0]).toBe(stripButtons()[FIRM_TAB_IDS.indexOf(id)])
  })
})

describe('Settings > active tab scrolled into the strip', () => {
  let scrollIntoView: ReturnType<typeof vi.fn>
  let original: unknown
  beforeEach(() => {
    original = Element.prototype.scrollIntoView
    scrollIntoView = vi.fn()
    Element.prototype.scrollIntoView = scrollIntoView as unknown as typeof Element.prototype.scrollIntoView
  })
  afterEach(() => {
    if (original === undefined) delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
    else Element.prototype.scrollIntoView = original as typeof Element.prototype.scrollIntoView
    vi.restoreAllMocks()
  })

  // jsdom does no layout: stub rects per tab label and give the strip a backed scrollLeft.
  function stub(rects: Record<string, [number, number]>) {
    let scrollLeft = 0
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      let l = 0
      let r = 0
      if (this.style.overflowX === 'auto') r = 300
      else if (this.tagName === 'BUTTON') [l, r] = rects[this.children[1]?.textContent ?? ''] ?? [0, 0]
      return { left: l, right: r, top: 0, bottom: 0, width: r - l, height: 0, x: l, y: 0, toJSON() {} } as DOMRect
    })
    return (strip: HTMLElement) => {
      Object.defineProperty(strip, 'scrollLeft', { get: () => scrollLeft, set: (v: number) => { scrollLeft = v }, configurable: true })
    }
  }

  it('on mount', () => {
    let scrollLeft = 0
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const [l, r] = this.style.overflowX === 'auto' ? [0, 300] : this.tagName === 'BUTTON' && this.children[1]?.textContent === 'Workspace' ? [700, 900] : [0, 100]
      return { left: l, right: r } as DOMRect
    })
    Object.defineProperty(HTMLElement.prototype, 'scrollLeft', { get: () => scrollLeft, set: (v: number) => { scrollLeft = v }, configurable: true })
    try {
      const { container } = render(<SettingsView ctx={settingsCtx('workspace', false, { user: USER, roles: [] })} />)
      expect((container.querySelector('.pf-tab')!.parentElement as HTMLElement).scrollLeft).toBe(600)
      expect(scrollIntoView).not.toHaveBeenCalled()
    } finally {
      delete (HTMLElement.prototype as { scrollLeft?: number }).scrollLeft
    }
  })

  it('on selection change, moving only scrollLeft', () => {
    const rects: Record<string, [number, number]> = { Members: [0, 150], Workspace: [700, 900] }
    const back = stub(rects)
    const { container, rerender } = render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    const strip = container.querySelector('.pf-tab')!.parentElement as HTMLElement
    back(strip)
    expect(strip.scrollLeft).toBe(0)
    rerender(<SettingsView ctx={settingsCtx('workspace', false, { user: USER, roles: [] })} />)
    expect(strip.scrollLeft).toBe(600)
    rects.Members = [-400, -250]
    rerender(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    expect(strip.scrollLeft).toBe(200)
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  it('a tab already inside the strip leaves scrollLeft alone, edges included', () => {
    const rects: Record<string, [number, number]> = { Members: [0, 150], Roles: [100, 250] }
    const back = stub(rects)
    const { container, rerender } = render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    const strip = container.querySelector('.pf-tab')!.parentElement as HTMLElement
    back(strip)
    rerender(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    expect(strip.scrollLeft).toBe(0)
  })

  it('a tab clipped on the right scrolls by the overhang only', () => {
    const rects: Record<string, [number, number]> = { Members: [0, 150], Roles: [200, 350] }
    const back = stub(rects)
    const { container, rerender } = render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
    const strip = container.querySelector('.pf-tab')!.parentElement as HTMLElement
    back(strip)
    rerender(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
    expect(strip.scrollLeft).toBe(50)
  })

  it('re-reveals the active tab when the observed sizes change', () => {
    const callbacks: Array<() => void> = []
    const disconnect = vi.fn()
    class RO {
      constructor(cb: () => void) {
        callbacks.push(cb)
      }
      observe() {}
      unobserve() {}
      disconnect = disconnect
    }
    vi.stubGlobal('ResizeObserver', RO)
    try {
      const rects: Record<string, [number, number]> = { Members: [0, 150], Roles: [100, 250] }
      const back = stub(rects)
      const { container, rerender, unmount } = render(<SettingsView ctx={settingsCtx('members', false, { user: USER, roles: [] })} />)
      const strip = container.querySelector('.pf-tab')!.parentElement as HTMLElement
      back(strip)
      rerender(<SettingsView ctx={settingsCtx('roles', false, { user: USER, roles: [] })} />)
      expect(strip.scrollLeft).toBe(0)
      rects.Roles = [200, 400]
      callbacks[callbacks.length - 1]()
      expect(strip.scrollLeft).toBe(100)
      expect(scrollIntoView).not.toHaveBeenCalled()
      unmount()
      expect(disconnect).toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe('Settings > Company card', () => {
  it('lists the NRS rows with — for unset', () => {
    render(<SettingsView ctx={inhouse({ activeEntity: { ...ENTITY, state: 'NG-LA' } })} />)
    const value = (l: string) => screen.getByText(l, { exact: true }).parentElement!.lastElementChild!.textContent
    expect(value('State')).toBe('NG-LA')
    for (const l of NRS_LABELS.filter((l) => l !== 'State')) expect(value(l), l).toBe('—')
  })

  it('each NRS row shows its own entity value', () => {
    const entity = { ...ENTITY, email: 'a@x.ng', telephone: '080', street: 'S1', city: 'C1', postal_zone: 'P1', country: 'NG', state: 'NG-LA', lga: 'NG-LA-001' }
    render(<SettingsView ctx={inhouse({ activeEntity: entity })} />)
    const value = (l: string) => screen.getByText(l, { exact: true }).parentElement!.lastElementChild!.textContent
    const want: Record<string, string> = { Email: 'a@x.ng', Phone: '080', Street: 'S1', City: 'C1', 'Postal zone': 'P1', Country: 'NG', State: 'NG-LA', LGA: 'NG-LA-001' }
    expect(Object.keys(want)).toHaveLength(8)
    for (const [l, v] of Object.entries(want)) expect(value(l), l).toBe(v)
  })

  it('NRS rows read — when the keys are null or absent', () => {
    render(<SettingsView ctx={inhouse({ activeEntity: { ...ENTITY, email: null, lga: null } })} />)
    const value = (l: string) => screen.getByText(l, { exact: true }).parentElement!.lastElementChild!.textContent
    for (const l of NRS_LABELS) expect(value(l), l).toBe('—')
  })

  it('the header pads 14px 20px with a 15/700 title and a 32-tall ghost Edit company', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY })} />)
    const title = screen.getByText('Your company', { exact: true })
    expect(title.style.fontSize).toBe('15px')
    expect(title.style.fontWeight).toBe('700')
    expect(title.parentElement!.style.padding).toBe('14px 20px')
    const edit = screen.getByRole('button', { name: 'Edit company' })
    expect(edit.className).toBe('v2-btn v2-btn-ghost pf-btn')
    expect(edit.style.height).toBe('32px')
    expect(edit.style.fontSize).toBe('13px')
    vi.unstubAllEnvs()
  })

  it('rows are 140px 1fr with a divider between them and none under the last; a null value reads —', () => {
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY })} />)
    const labels = ['Name', 'TIN', 'Sector', 'Registration', 'Address', ...NRS_LABELS].map((l) => screen.getByText(l, { exact: true }))
    labels.forEach((l) => expect(l.className).toBe('label'))
    const rows = labels.map((l) => l.parentElement as HTMLElement)
    rows.forEach((r) => {
      expect(r.style.gridTemplateColumns).toBe('140px 1fr')
      expect(r.style.padding).toBe('10px 0px')
    })
    rows.slice(0, 12).forEach((r) => expect(r.style.borderBottom).toBe('1px solid var(--line-1)'))
    expect(rows[12].style.borderBottom).toMatch(/^(0|0px|none)/)
    expect(rows[3].lastElementChild!.textContent).toBe('—')
    expect(rows[1].lastElementChild!.className).toBe('mono')
    expect((rows[1].lastElementChild as HTMLElement).style.fontSize).toBe('12.5px')
    expect((rows[0].lastElementChild as HTMLElement).style.fontSize).toBe('13px')
  })

  it('with no company the Add company primary shows over the EmptyState and no ghost Edit', () => {
    render(<SettingsView ctx={inhouse()} />)
    expect(screen.getByRole('button', { name: 'Add company' }).className).toBe('v2-btn v2-btn-primary pf-btn')
    expect(screen.queryByRole('button', { name: 'Edit company' })).toBeNull()
    expect(screen.getByText('No company set up yet')).toBeTruthy()
  })

  it.each(['preparer', 'reviewer'])('Company tab: a non-admin with no company sees No company created and no Add company (%s)', (role) => {
    render(<SettingsView ctx={inhouse({ members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status: 'active', isYou: true }] })} />)
    expect(screen.queryByRole('button', { name: 'Add company' })).toBeNull()
    expect(screen.getByText('No company created')).toBeTruthy()
    expect(screen.getByText('Your workspace admin adds the company. You can start when it exists.')).toBeTruthy()
    expect(screen.queryByText('No company set up yet')).toBeNull()
  })

  it.each(['preparer', 'reviewer'])('Company tab: Edit company is disabled with the reason for a non-admin (%s)', (role) => {
    const members = [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status: 'active', isYou: true }]
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY, members })} />)
    const edit = screen.getByRole('button', { name: 'Edit company' }) as HTMLButtonElement
    expect(edit.disabled).toBe(true)
    expect(edit.title).toBe(COMPANY_ADMIN_ONLY)
    expect(document.getElementById(edit.getAttribute('aria-describedby') as string)?.textContent).toBe(COMPANY_ADMIN_ONLY)
    expect(edit.style.opacity).toBe('0.45')
    fireEvent.click(edit)
    expect(screen.queryByRole('dialog')).toBeNull()
    vi.unstubAllEnvs()
  })

  it('Company tab: an admin keeps Edit company enabled, with no note', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY })} />)
    expect((screen.getByRole('button', { name: 'Edit company' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('settings-company-admin-only')).toBeNull()
    vi.unstubAllEnvs()
  })

  it.each([
    ['loading', []],
    ['error', []],
  ])('Company tab: Edit company stays enabled while the roster is %s', (membersState, members) => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY, membersState, members })} />)
    expect((screen.getByRole('button', { name: 'Edit company' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('settings-company-admin-only')).toBeNull()
    vi.unstubAllEnvs()
  })

  it.each([
    ['loading', []],
    ['error', []],
  ])('Company tab: no Add company while the roster is %s', (membersState, members) => {
    render(<SettingsView ctx={inhouse({ membersState, members })} />)
    expect(screen.queryByRole('button', { name: 'Add company' })).toBeNull()
    expect(screen.getByText('No company set up yet')).toBeTruthy()
  })

  it('with no gateway the action is disabled with the D-3 paint and keeps its fill class', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    render(<SettingsView ctx={inhouse({ activeEntity: ENTITY })} />)
    const edit = screen.getByRole('button', { name: 'Edit company' }) as HTMLButtonElement
    expect(edit.disabled).toBe(true)
    expect(edit.style.opacity).toBe('0.45')
    expect(edit.style.cursor).toBe('not-allowed')
    expect(edit.style.filter).toBe('none')
    vi.unstubAllEnvs()
  })
})

describe('Settings > connector list paint', () => {
  it('status pills are radius-sm with a 50% dot; the category chip is radius-md', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    const pills = screen.getAllByText(/^(NOT )?CONNECTED$/)
    expect(pills).toHaveLength(6)
    pills.forEach((p) => {
      expect(p.parentElement!.style.borderRadius).toBe('var(--radius-sm)')
      expect((p.previousElementSibling as HTMLElement).style.borderRadius).toBe('50%')
    })
    const chips = screen.getAllByText(/^(ERP|ACCOUNTING)$/)
    expect(chips.length).toBeGreaterThan(0)
    chips.forEach((c) => expect(c.style.borderRadius).toBe('var(--radius-md)'))
  })

  it('Manage is a bordered --bg-2 --fg-1 button padded 0 15px', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    const [m] = screen.getAllByText('Manage')
    expect(m.style.border).toContain('var(--line-2)')
    expect(m.style.background).toBe('var(--bg-2)')
    expect(m.style.color).toBe('var(--fg-1)')
    expect(m.style.padding).toBe('0px 15px')
    expect(m.style.borderRadius).toBe('var(--radius-btn)')
  })

  it('Connect text is --primary-foreground; Disconnect keeps --fg-2 on a transparent fill', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    const connect = screen.getAllByText('Connect')
    const disconnect = screen.getAllByText('Disconnect')
    expect(connect.length).toBeGreaterThan(0)
    expect(disconnect.length).toBeGreaterThan(0)
    connect.forEach((b) => expect(b.style.color).toBe('var(--primary-foreground)'))
    disconnect.forEach((b) => {
      expect(b.style.color).toBe('var(--fg-2)')
      expect(b.style.background).toBe('transparent')
    })
  })

  it('the monogram tile text is white, as the prototype #fff', () => {
    render(<SettingsView ctx={settingsCtx('connectors', false)} />)
    expect(screen.getByText('SAP', { exact: true }).style.color).toBe('var(--primary-foreground)')
  })
})

describe('Settings > API & webhooks paint', () => {
  it('the base URL box is radius-md and the copy buttons are radius-btn', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    const copies = screen.getAllByText('Copy', { exact: false }).map((c) => c.closest('button') as HTMLElement)
    expect(copies).toHaveLength(1)
    copies.forEach((b) => expect(b.style.borderRadius).toBe('var(--radius-btn)'))
    expect((copies[0].previousElementSibling as HTMLElement).style.borderRadius).toBe('var(--radius-md)')
    expect(screen.getByRole('button', { name: /Add endpoint/ }).style.borderRadius).toBe('var(--radius-btn)')
    for (const b of [...copies, screen.getByRole('button', { name: /Add endpoint/ })]) expect(b.style.fontFamily, 'bare buttons take the app face').toBe('var(--font-sans)')
  })

  it('method pills are radius-md', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    for (const t of ['GET', 'POST']) {
      const el = screen.getAllByText(t, { exact: true })[0]
      expect(el.parentElement!.style.borderRadius).toBe('var(--radius-md)')
    }
  })

  it('webhook status pills are radius-sm with a 50% dot', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    const pills = WEBHOOKS.map((w) => screen.getAllByText(w.st, { exact: true })).flat()
    expect(pills.length).toBeGreaterThan(0)
    pills.forEach((p) => {
      expect(p.parentElement!.style.borderRadius).toBe('var(--radius-sm)')
      expect((p.previousElementSibling as HTMLElement).style.borderRadius).toBe('50%')
    })
  })

  // The prototype draws these three heads as inline 14/600; .card-title would render 14/700.
  it('the API keys, Endpoints and Webhooks card titles are inline 14/600', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    for (const t of ['API keys', 'Endpoints', 'Webhooks']) {
      const el = screen.getByText(t, { exact: true })
      expect(el.className).not.toContain('card-title')
      expect(el.style.fontSize).toBe('14px')
      expect(el.style.fontWeight).toBe('600')
    }
  })
})

describe('Settings > API keys', () => {
  const TITLE = 'No API keys yet'
  const MESSAGE = 'API access opens after NRS accreditation.'

  it('hand-off: the API keys card shows the empty state', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    expect(screen.getByText(TITLE)).toBeTruthy()
    expect(screen.getByText(MESSAGE)).toBeTruthy()
  })

  it('persona: the API keys card shows the same empty state', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    expect(screen.getByText(TITLE)).toBeTruthy()
    expect(screen.getByText(MESSAGE)).toBeTruthy()
  })

  it('the empty state does not follow the sandbox flag', () => {
    render(<SettingsView ctx={settingsCtx('api', false, { sandbox: false })} />)
    expect(screen.getByText(TITLE)).toBeTruthy()
    expect(screen.getByText(MESSAGE)).toBeTruthy()
  })

  it('the API keys empty state sits in the Webhooks wrapper', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    const wrap = (t: string) => screen.getByText(t).parentElement!.parentElement as HTMLElement
    expect(wrap(TITLE).style.padding).toBe('20px')
    expect(wrap(TITLE).style.padding).toBe(wrap('No webhooks yet').style.padding)
  })

  it.each([true, false])('the API keys card has no Copy button and no env pill (handoff=%s)', (handoff) => {
    render(<SettingsView ctx={settingsCtx('api', handoff)} />)
    const card = screen.getByText('API keys').closest('div[style*="overflow"]') as HTMLElement | null
    expect(card).not.toBeNull()
    expect(card!.textContent).toContain(TITLE)
    expect(within(card!).queryAllByRole('button')).toHaveLength(0)
    expect(screen.queryByText('LIVE')).toBeNull()
    expect(screen.queryByText('TEST')).toBeNull()
    expect(screen.getAllByText('Copy', { exact: false })).toHaveLength(1)
  })

  it.each([
    [true, true],
    [true, false],
    [false, true],
    [false, false],
  ])('no invented API key text renders (handoff=%s, sandbox=%s)', (handoff, sandbox) => {
    const { container } = render(<SettingsView ctx={settingsCtx('api', handoff, { sandbox })} />)
    expect(container.textContent).not.toContain('sk_live_')
    expect(container.textContent).not.toContain('sk_test_')
  })
})

describe('Settings > Signing paint', () => {
  it('ACTIVE pills are radius-sm with a 50% dot; the expiry track and fill are radius-md', () => {
    render(<SettingsView ctx={settingsCtx('signing', false)} />)
    const pills = screen.getAllByText('ACTIVE', { exact: true })
    expect(pills).toHaveLength(CERTS.length)
    pills.forEach((p) => {
      expect(p.parentElement!.style.borderRadius).toBe('var(--radius-sm)')
      expect((p.previousElementSibling as HTMLElement).style.borderRadius).toBe('50%')
    })
    const track = screen.getAllByText(/^Expires /)[0].parentElement!.nextElementSibling as HTMLElement
    expect(track.style.borderRadius).toBe('var(--radius-md)')
    expect((track.firstElementChild as HTMLElement).style.borderRadius).toBe('var(--radius-md)')
  })
})

describe('Settings > empty states stay EmptyState', () => {
  it('the webhooks and signing hand-off blocks are the dashed EmptyState card', () => {
    render(<SettingsView ctx={settingsCtx('api', true)} />)
    const w = screen.getByText('No webhooks yet').parentElement as HTMLElement
    expect(w.style.border).toContain('dashed')
    expect(w.querySelector('svg')).toBeTruthy()
    const k = screen.getByText('No API keys yet').parentElement as HTMLElement
    expect(k.style.border).toContain('dashed')
    expect(k.querySelector('svg')).toBeTruthy()
    cleanup()
    render(<SettingsView ctx={settingsCtx('signing', true)} />)
    const s = screen.getByText('No signing certificate yet').parentElement as HTMLElement
    expect(s.style.border).toContain('dashed')
    expect(s.querySelector('svg')).toBeTruthy()
  })
})
