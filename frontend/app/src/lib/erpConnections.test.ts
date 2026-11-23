import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { fmtDateTime } from './format'
import type { AuthedFetch } from './portfolio'
import {
  anyPending,
  canConnect,
  canDisconnect,
  connectBody,
  connectErp,
  connectorRows,
  connectorsEmpty,
  disconnectErp,
  listErpConnections,
  safeConsentUrl,
  statusPill,
  syncLine,
  systemLabel,
  type ConnectorRow,
  type ErpConnectionWire,
  type ErpStatus,
} from './erpConnections'

const conn = (over: Partial<ErpConnectionWire> = {}): ErpConnectionWire => ({
  id: 'c1',
  entity_id: 'e1',
  system: 'fake',
  external_key: 'K1',
  status: 'active',
  last_sync_at: null,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  ...over,
})
const rowOf = (c: ErpConnectionWire | null, system = 'fake'): ConnectorRow => ({
  key: c ? c.id : `system:${system}`,
  system,
  connection: c,
})

const base = 'https://gw'
const stub = (answer: unknown) => vi.fn(async () => answer) as unknown as AuthedFetch & ReturnType<typeof vi.fn>

describe('erp wire', () => {
  it('erpConnections_listGetsTheClientsConnections', async () => {
    const body = { connections: [conn()], systems: ['fake'] }
    const f = stub(body)
    expect(await listErpConnections(f, base, 'e 1')).toEqual(body)
    expect(f).toHaveBeenCalledWith('https://gw/api/portfolio/v1/entities/e%201/erp-connections')
  })

  it('erpConnections_connectPostsTheBody', async () => {
    const answer = { ...conn(), consent_url: 'https://gw/erp/fake/consent?state=s' }
    const f = stub(answer)
    const body = { system: 'fake', connection_id: 'c1' }
    expect(await connectErp(f, base, 'e1', body)).toEqual(answer)
    expect(f).toHaveBeenCalledWith('https://gw/api/portfolio/v1/entities/e1/erp-connections', { method: 'POST', body })
  })

  it('erpConnections_disconnectPostsToTheConnection', async () => {
    const f = stub(conn({ status: 'disconnected' }))
    await disconnectErp(f, base, 'e1', 'c/1')
    expect(f).toHaveBeenCalledWith('https://gw/api/portfolio/v1/entities/e1/erp-connections/c%2F1/disconnect', {
      method: 'POST',
    })
  })

  it('erpConnections_anApiErrorPassesThrough', async () => {
    const err = new ApiError('http', 'already connected', 409)
    const f = vi.fn(async () => {
      throw err
    }) as unknown as AuthedFetch
    await expect(listErpConnections(f, base, 'e1')).rejects.toBe(err)
    await expect(connectErp(f, base, 'e1', { system: 'fake' })).rejects.toBe(err)
    await expect(disconnectErp(f, base, 'e1', 'c1')).rejects.toBe(err)
  })
})

describe('row logic', () => {
  it('connectorRows_connectionsInServerOrderThenPlaceholders', () => {
    const a = conn({ id: 'a', system: 'fake', status: 'disconnected' })
    const b = conn({ id: 'b', system: 'fake', status: 'active' })
    const c = conn({ id: 'c', system: 'legacy', status: 'active' })
    const rows = connectorRows({ connections: [a, b, c], systems: ['fake', 'other'] })
    expect(rows.map((r) => [r.key, r.system, r.connection])).toEqual([
      ['a', 'fake', a],
      ['b', 'fake', b],
      ['c', 'legacy', c],
      ['system:other', 'other', null],
    ])
  })

  it('connectorRows_aDisconnectedOnlySystemGetsNoPlaceholder', () => {
    const a = conn({ status: 'disconnected' })
    const rows = connectorRows({ connections: [a], systems: ['fake'] })
    expect(rows).toHaveLength(1)
    expect(rows[0].connection).toBe(a)
  })

  it('connectorsEmpty_onlyWhenBothAreEmpty', () => {
    expect(connectorsEmpty({ connections: [], systems: [] })).toBe(true)
    expect(connectorsEmpty({ connections: [], systems: ['fake'] })).toBe(false)
    expect(connectorsEmpty({ connections: [conn()], systems: [] })).toBe(false)
  })

  it('connectBody_perRowKind', () => {
    expect(connectBody(rowOf(null))).toEqual({ system: 'fake' })
    expect(connectBody(rowOf(conn({ status: 'pending', external_key: null })))).toEqual({ system: 'fake' })
    for (const status of ['pending', 'reconsent', 'disconnected'] as const) {
      expect(connectBody(rowOf(conn({ id: 'x9', status })))).toEqual({ system: 'fake', connection_id: 'x9' })
    }
  })

  it('canConnect_andCanDisconnect_perStatus', () => {
    const cases: [string, ConnectorRow, boolean, boolean][] = [
      ['placeholder', rowOf(null), true, false],
      ['pending', rowOf(conn({ status: 'pending' })), true, true],
      ['active', rowOf(conn({ status: 'active' })), false, true],
      ['reconsent', rowOf(conn({ status: 'reconsent' })), true, true],
      ['disconnected', rowOf(conn({ status: 'disconnected' })), true, false],
    ]
    for (const [name, row, connect, disconnect] of cases) {
      expect([name, canConnect(row, ['fake'])]).toEqual([name, connect])
      expect([name, canDisconnect(row)]).toEqual([name, disconnect])
    }
  })

  it('canConnect_falseForASystemTheServerNoLongerOffers', () => {
    const row = rowOf(conn({ status: 'reconsent', system: 'legacy' }), 'legacy')
    expect(canConnect(row, ['fake'])).toBe(false)
    expect(canConnect(row, ['fake', 'legacy'])).toBe(true)
  })

  it('anyPending_whileAnyConnectionIsPending', () => {
    const l = (...cs: ErpConnectionWire[]) => ({ connections: cs, systems: ['fake'] })
    const st = (id: string, status: ErpStatus) => conn({ id, status })
    expect(anyPending(l(st('c1', 'pending')))).toBe(true)
    expect(anyPending(l(st('c1', 'active')))).toBe(false)
    expect(anyPending(l())).toBe(false)
    expect(anyPending(l(st('c1', 'active'), st('c2', 'pending')))).toBe(true)
    expect(anyPending(l(st('c1', 'reconsent'), st('c2', 'disconnected')))).toBe(false)
  })
})

describe('copy', () => {
  it('labels_perStatusAndSystem', () => {
    expect(systemLabel('fake')).toEqual({ name: 'Fake ERP', mono: 'FAKE' })
    expect(systemLabel('xero')).toEqual({ name: 'xero', mono: 'XER' })
    const pill = (s: ErpStatus | null) => statusPill(rowOf(s ? conn({ status: s }) : null))
    expect(pill('active')).toEqual({ text: 'CONNECTED', tone: 'green' })
    expect(pill(null)).toEqual({ text: 'NOT CONNECTED', tone: 'neutral' })
    expect(pill('pending')).toEqual({ text: 'WAITING FOR SIGN-IN', tone: 'neutral' })
    expect(pill('reconsent')).toEqual({ text: 'SIGN IN AGAIN', tone: 'amber' })
    expect(pill('disconnected')).toEqual({ text: 'DISCONNECTED', tone: 'neutral' })
    expect(syncLine(rowOf(conn({ last_sync_at: null })))).toBe('No sync yet')
    expect(syncLine(rowOf(null))).toBe('No sync yet')
    const at = '2026-03-04T10:20:30Z'
    expect(syncLine(rowOf(conn({ last_sync_at: at })))).toBe('Synced ' + fmtDateTime(at))
  })

  it('safeConsentUrl_acceptsOnlyAbsoluteHttp', () => {
    for (const ok of ['https://gw/erp/fake/consent?state=s', 'http://localhost:8080/x']) {
      expect(safeConsentUrl(ok)).toBe(ok)
    }
    for (const bad of ['javascript:alert(1)', 'data:text/html,x', '/erp/fake/consent', '', '::']) {
      expect(safeConsentUrl(bad)).toBeNull()
    }
  })
})
