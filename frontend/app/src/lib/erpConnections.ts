// ERP connections (Settings > ERP connectors): the wire and the pure row logic. Server truth.

import type { AuthedFetch } from './portfolio'
import { fmtDateTime } from './format'

export const POLL_MS = 3000
// A consent state lives 10 minutes, so a run is 200 ticks of 3 s.
export const POLL_MAX_TICKS = 200

export type ErpStatus = 'pending' | 'active' | 'reconsent' | 'disconnected'

export type ErpConnectionWire = {
  id: string
  entity_id: string
  system: string
  external_key: string | null
  status: ErpStatus
  last_sync_at: string | null
  created_at: string
  updated_at: string
}

export type ErpListing = { connections: ErpConnectionWire[]; systems: string[] }
export type ErpConnectAnswer = ErpConnectionWire & { consent_url: string }

/** One screen row: a connection, or a placeholder (`connection` null) for a system with none. */
export type ConnectorRow = { key: string; system: string; connection: ErpConnectionWire | null }

export const CONNECTORS_INTRO =
  "Connect a client's ERP or accounting system. Its invoices arrive as drafts and go through validation and approval."
export const CONNECTORS_ADMIN_ONLY = 'Only an admin can connect or disconnect an ERP.'
export const CONNECTORS_NO_CLIENT_TITLE = 'No company selected'
export const CONNECTORS_NO_CLIENT_MESSAGE = 'An ERP connection belongs to one company. Pick or add one first.'
export const CONNECTORS_POPUP_BLOCKED = 'Your browser blocked the sign-in tab.'
export const CONNECTORS_POPUP_LINK = 'Open the sign-in page'
export const CONNECTORS_EMPTY_TITLE = 'No connectors are available yet.'
export const CONNECTORS_BAD_CONSENT_URL = 'The sign-in link was not valid.'
export const CONNECTORS_LOADING = 'Loading ERP connectors…'
export const CONNECTORS_ROLE_FAILED = 'Your role could not be checked. Connect and disconnect show once it loads.'
export const CONNECTORS_RETRY = 'Retry'
export const DISCONNECT_KEEP = 'Keep connected'
export const DISCONNECT_CONFIRM = 'Disconnect'

export function disconnectQuestion(name: string): string {
  return `Disconnect ${name}? Sync stops and its sign-in is deleted. Invoices already pulled stay.`
}

const connectionsPath = (base: string, entityId: string) =>
  `${base}/api/portfolio/v1/entities/${encodeURIComponent(entityId)}/erp-connections`

export function listErpConnections(f: AuthedFetch, base: string, entityId: string): Promise<ErpListing> {
  return f<ErpListing>(connectionsPath(base, entityId))
}

export function connectErp(
  f: AuthedFetch,
  base: string,
  entityId: string,
  body: { system: string; connection_id?: string },
): Promise<ErpConnectAnswer> {
  return f<ErpConnectAnswer>(connectionsPath(base, entityId), { method: 'POST', body })
}

export function disconnectErp(
  f: AuthedFetch,
  base: string,
  entityId: string,
  connectionId: string,
): Promise<ErpConnectionWire> {
  return f<ErpConnectionWire>(`${connectionsPath(base, entityId)}/${encodeURIComponent(connectionId)}/disconnect`, {
    method: 'POST',
  })
}

/** Connections in server order, then a placeholder per listed system with no connection at all. */
export function connectorRows(listing: ErpListing): ConnectorRow[] {
  const rows: ConnectorRow[] = listing.connections.map((c) => ({ key: c.id, system: c.system, connection: c }))
  const have = new Set(listing.connections.map((c) => c.system))
  for (const s of listing.systems) if (!have.has(s)) rows.push({ key: `system:${s}`, system: s, connection: null })
  return rows
}

export function connectorsEmpty(listing: ErpListing): boolean {
  return listing.connections.length === 0 && listing.systems.length === 0
}

/** A keyless pending row is reused by the server from `{system}` alone; other non-active rows keep their id. */
export function connectBody(row: ConnectorRow): { system: string; connection_id?: string } {
  const c = row.connection
  if (c === null || (c.status === 'pending' && c.external_key === null)) return { system: row.system }
  return { system: row.system, connection_id: c.id }
}

export function canConnect(row: ConnectorRow, systems: readonly string[]): boolean {
  return row.connection?.status !== 'active' && systems.includes(row.system)
}

export function canDisconnect(row: ConnectorRow): boolean {
  return row.connection !== null && row.connection.status !== 'disconnected'
}

export function systemLabel(system: string): { name: string; mono: string } {
  if (system === 'fake') return { name: 'Fake ERP', mono: 'FAKE' }
  return { name: system, mono: system.slice(0, 3).toUpperCase() }
}

export function statusPill(row: ConnectorRow): { text: string; tone: 'green' | 'amber' | 'neutral' } {
  switch (row.connection?.status) {
    case 'active':
      return { text: 'CONNECTED', tone: 'green' }
    case 'pending':
      return { text: 'WAITING FOR SIGN-IN', tone: 'neutral' }
    case 'reconsent':
      return { text: 'SIGN IN AGAIN', tone: 'amber' }
    case 'disconnected':
      return { text: 'DISCONNECTED', tone: 'neutral' }
    default:
      return { text: 'NOT CONNECTED', tone: 'neutral' }
  }
}

export function syncLine(row: ConnectorRow): string {
  const at = row.connection?.last_sync_at
  return at ? `Synced ${fmtDateTime(at)}` : 'No sync yet'
}

/** The URL string only for an absolute http(s) URL; anything else must never reach window.location. */
export function safeConsentUrl(raw: string): string | null {
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' || u.protocol === 'http:' ? raw : null
  } catch {
    return null
  }
}

/** True while any connection is waiting for consent: the screen polls until it lands. */
export function anyPending(listing: ErpListing): boolean {
  return listing.connections.some((c) => c.status === 'pending')
}
