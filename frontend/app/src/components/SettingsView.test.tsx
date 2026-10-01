// @vitest-environment jsdom
// A real (hand-off) session's Settings shows empty states, not the demo tenant. ctx-cast idiom of Sidebar.test.tsx.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CERTS, ENDPOINTS, WEBHOOKS } from '../data'
import { initialConnectors } from '../lib/connectors'
import type { PlatformCtx, SettingsTab } from '../types'
import { SettingsView } from './SettingsView'

function settingsCtx(tab: SettingsTab, handoff: boolean): PlatformCtx {
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
