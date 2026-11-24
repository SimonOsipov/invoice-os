// @vitest-environment jsdom
// The real SettingsView, ConnectorDetail and FieldMappingModal: another connection must not inherit an open modal.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { PlatformCtx } from '../types'
import { SettingsView } from './SettingsView'

const AT = '2026-03-04T10:00:00Z'
const KEYS: Record<string, string> = { c1: 'co-one', c2: 'co-two' }
const SKU: Record<string, string> = { c1: 'sku-one', c2: 'sku-two' }

function route(url: string) {
  const id = /erp-connections\/(c\d)\/overview$/.exec(url)?.[1]
  if (id) {
    const connection = { id, entity_id: 'e1', system: 'fake', external_key: KEYS[id], status: 'active', last_sync_at: AT, created_at: '', updated_at: '' }
    return { connection, activity: { since: AT, counts: { pulled: 0, pending: 0, drafted: 0, linked: 0, held: 0, failed: 0, deleted: 0 }, documents: [] }, writebacks: [], held: [] }
  }
  if (url.includes('/erp-mappings/')) {
    return { system: 'fake', fields: [{ target: 'invoice_number', label: 'INVOICE_NUMBER', line: false, source_path: 'a.no', default_path: 'a.no', origin: 'default' }] }
  }
  const key = /external_key=([^&]+)/.exec(url)?.[1]
  const id2 = Object.keys(KEYS).find((k) => KEYS[k] === key)
  if (id2) {
    const lookup = { kind: 'item', erp_key: SKU[id2], tax_category: null, exemption_code: null, hsn_code: '1234', service_code: null, tin: null, state: null, lga: null }
    return { system: 'fake', external_key: key, lookups: [lookup] }
  }
  throw new Error('unexpected ' + url)
}

function ctx(connectionId: string, entityId = 'e1'): PlatformCtx {
  return {
    mode: 'firm',
    handoff: false,
    settingsTab: 'connectors',
    sandbox: true,
    activeEntity: { id: entityId, name: 'Acme Ltd' },
    entities: [],
    entitiesState: 'ready',
    entitiesError: null,
    refetchEntities: vi.fn(),
    members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
    membersState: 'ready',
    refetchMembers: vi.fn(),
    setSettingsTab: vi.fn(),
    openErpConnection: vi.fn(),
    openImportedInvoice: vi.fn(),
    adoptBatchClient: vi.fn(),
    erpConnectionId: connectionId,
    authedFetch: vi.fn(async (url: string) => route(url)),
  } as unknown as PlatformCtx
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('Settings > ERP connectors, switching connection', () => {
  it('settingsView_switchingConnectionClosesTheOpenMappingModal', async () => {
    const { rerender } = render(<SettingsView ctx={ctx('c1')} />)
    await screen.findByText(/HSN code 1234/)
    await waitFor(() => expect(screen.getByTestId('erp-detail-mapping-edit')).toBeTruthy())
    await act(async () => {
      fireEvent.click(screen.getByTestId('erp-detail-mapping-edit'))
    })
    expect(screen.getByTestId('field-mapping-modal')).toBeTruthy()
    expect(screen.getByDisplayValue('sku-one')).toBeTruthy()

    rerender(<SettingsView ctx={ctx('c2')} />)
    await waitFor(() => expect(screen.getByTestId('erp-detail-header').textContent).toContain('co-two'))
    expect(screen.queryByTestId('field-mapping-modal'), 'the modal of the first connection is gone').toBeNull()
    expect(screen.queryByDisplayValue('sku-one')).toBeNull()
  })

  it('settingsView_switchingClientClosesTheOpenMappingModal', async () => {
    const { rerender } = render(<SettingsView ctx={ctx('c1')} />)
    await screen.findByText(/HSN code 1234/)
    await waitFor(() => expect(screen.getByTestId('erp-detail-mapping-edit')).toBeTruthy())
    await act(async () => {
      fireEvent.click(screen.getByTestId('erp-detail-mapping-edit'))
    })
    expect(screen.getByTestId('field-mapping-modal')).toBeTruthy()
    rerender(<SettingsView ctx={ctx('c1', 'e2')} />)
    await waitFor(() => expect(screen.getByTestId('erp-detail-header')).toBeTruthy())
    expect(screen.queryByTestId('field-mapping-modal')).toBeNull()
  })
})
