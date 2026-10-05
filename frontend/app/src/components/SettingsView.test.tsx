// @vitest-environment jsdom
// A real (hand-off) session's Settings shows empty states, not the demo tenant. ctx-cast idiom of Sidebar.test.tsx.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CERTS, ENDPOINTS, WEBHOOKS } from '../data'
import { initialConnectors } from '../lib/connectors'
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

  // D21: API keys and Endpoints stay in a hand-off session. Control: green before and after.
  it('hand-off: the API keys and Endpoints cards are unchanged', () => {
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
const inhouse = (over: Record<string, unknown> = {}) => settingsCtx('company', false, { mode: 'inhouse', ...over })
const radii = (c: HTMLElement) => [...c.querySelectorAll<HTMLElement>('*')].map((e) => e.style.borderRadius).filter(Boolean)

describe('Settings > shell', () => {
  it('the h1 carries no inline weight and the tab strip is still the pf-tab buttons', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    expect(screen.getByRole('heading', { name: 'Settings' }).style.fontWeight).toBe('')
    const tabs = screen.getAllByRole('button').filter((b) => b.className === 'pf-tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Members', 'Roles', 'ERP connectors', 'API & webhooks', 'Signing & certificates'])
  })

  it.each(['connectors', 'api', 'signing'] as const)('%s: no 99 or 999 radius anywhere', (tab) => {
    const { container } = render(<SettingsView ctx={settingsCtx(tab, false)} />)
    expect(radii(container).length).toBeGreaterThan(5)
    expect(radii(container).filter((r) => /^(99|999)(px)?$/.test(r))).toHaveLength(0)
  })
})

describe('Settings > Company card', () => {
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
    const labels = ['Name', 'TIN', 'Sector', 'Registration', 'Address'].map((l) => screen.getByText(l, { exact: true }))
    labels.forEach((l) => expect(l.className).toBe('label'))
    const rows = labels.map((l) => l.parentElement as HTMLElement)
    rows.forEach((r) => {
      expect(r.style.gridTemplateColumns).toBe('140px 1fr')
      expect(r.style.padding).toBe('10px 0px')
    })
    rows.slice(0, 4).forEach((r) => expect(r.style.borderBottom).toBe('1px solid var(--line-1)'))
    expect(rows[4].style.borderBottom).toMatch(/^(0|0px|none)/)
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
    expect(copies).toHaveLength(3)
    copies.forEach((b) => expect(b.style.borderRadius).toBe('var(--radius-btn)'))
    expect((copies[0].previousElementSibling as HTMLElement).style.borderRadius).toBe('var(--radius-md)')
    expect(screen.getByRole('button', { name: /Add endpoint/ }).style.borderRadius).toBe('var(--radius-btn)')
    for (const b of [...copies, screen.getByRole('button', { name: /Add endpoint/ })]) expect(b.style.fontFamily, 'bare buttons take the app face').toBe('var(--font-sans)')
  })

  it('key env pills and method pills are radius-md', () => {
    render(<SettingsView ctx={settingsCtx('api', false)} />)
    for (const t of ['LIVE', 'TEST', 'GET', 'POST']) {
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

  it('the key for the other environment dims to 0.45 and the active one stays 1', () => {
    const { rerender } = render(<SettingsView ctx={settingsCtx('api', false)} />)
    const row = (env: string) => screen.getByText(env, { exact: true }).parentElement!.parentElement as HTMLElement
    expect(row('LIVE').style.opacity).toBe('0.45')
    expect(row('TEST').style.opacity).toBe('1')
    rerender(<SettingsView ctx={settingsCtx('api', false, { sandbox: false })} />)
    expect(row('LIVE').style.opacity).toBe('1')
    expect(row('TEST').style.opacity).toBe('0.45')
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
    cleanup()
    render(<SettingsView ctx={settingsCtx('signing', true)} />)
    const s = screen.getByText('No signing certificate yet').parentElement as HTMLElement
    expect(s.style.border).toContain('dashed')
    expect(s.querySelector('svg')).toBeTruthy()
  })
})
