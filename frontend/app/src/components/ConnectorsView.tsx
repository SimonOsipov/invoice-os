// Settings > ERP connectors: the active client's real connections, with Connect (consent in a
// new tab) and Disconnect for admins. Server truth; the rows come from lib/erpConnections.

import { useEffect, useRef, useState } from 'react'

import { ApiError, EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import {
  anyPending,
  CONNECTORS_ADMIN_ONLY,
  CONNECTORS_BAD_CONSENT_URL,
  CONNECTORS_EMPTY_TITLE,
  CONNECTORS_INTRO,
  CONNECTORS_LOADING,
  CONNECTORS_NO_CLIENT_MESSAGE,
  CONNECTORS_NO_CLIENT_TITLE,
  CONNECTORS_POPUP_BLOCKED,
  CONNECTORS_POPUP_LINK,
  CONNECTORS_RETRY,
  CONNECTORS_ROLE_FAILED,
  canConnect,
  canDisconnect,
  connectBody,
  connectErp,
  connectorRows,
  connectorsEmpty,
  disconnectErp,
  DISCONNECT_CONFIRM,
  DISCONNECT_KEEP,
  disconnectQuestion,
  listErpConnections,
  POLL_MAX_TICKS,
  POLL_MS,
  statusPill,
  syncLine,
  systemLabel,
  type ConnectorRow,
  type ErpListing,
} from '../lib/erpConnections'
import { closeConsentTab, openConsentTab, sendToConsent } from '../lib/erpConsent'
import { viewerIsAdmin } from '../lib/members'
import type { PlatformCtx } from '../types'

// The repo has no :disabled rule; filter:none stops .v2-btn-primary:hover repainting a dead button.
const DISABLED: React.CSSProperties = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }

const PILL = {
  green: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  neutral: { bg: 'var(--bg-3)', border: 'var(--line-2)', text: 'var(--fg-3)' },
} as const

type Landed = { entityId: string; listing: ErpListing }

export function ConnectorsView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const entityId = ctx.activeEntity?.id ?? null
  const admin = viewerIsAdmin(ctx.members)
  const rosterLanded = ctx.membersState === 'ready' || ctx.membersState === 'empty'

  const list = useAsync<Landed>(
    () =>
      base && entityId
        ? listErpConnections(ctx.authedFetch, base, entityId).then((listing) => ({ entityId, listing }))
        : Promise.reject(new Error('no gateway configured')),
    { immediate: entityId != null, deps: [entityId] },
  )
  // The last landed listing: a refetch nulls list.data, and the rows must stay put meanwhile.
  const [mirror, setMirror] = useState<Landed | null>(null)
  const [seen, setSeen] = useState<Landed | null>(null)
  if (list.data && list.data !== seen) {
    setSeen(list.data)
    setMirror(list.data)
  }
  const shown = mirror && mirror.entityId === entityId ? mirror.listing : null
  const pendingNow = shown !== null && anyPending(shown)

  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [confirmKey, setConfirmKey] = useState<string | null>(null)
  const [blocked, setBlocked] = useState<{ entityId: string; system: string; url: string } | null>(null)
  const [flash, setFlash] = useState<string | null>(null)
  useEffect(() => {
    if (!flash) return
    const t = window.setTimeout(() => setFlash(null), 3000)
    return () => window.clearTimeout(t)
  }, [flash])

  const busyRef = useRef(false)
  const aliveRef = useRef(true)
  const entityRef = useRef(entityId)
  entityRef.current = entityId
  const runRef = useRef(list.run)
  runRef.current = list.run
  const pendingRef = useRef(pendingNow)
  pendingRef.current = pendingNow
  const pollRef = useRef<number | null>(null)
  const loadingRef = useRef(false)
  loadingRef.current = list.status === 'loading'

  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  function stopPoll() {
    if (pollRef.current !== null) window.clearInterval(pollRef.current)
    pollRef.current = null
  }
  function startPoll() {
    stopPoll()
    let ticks = 0
    pollRef.current = window.setInterval(() => {
      // After the first refetch, a listing with nothing pending ends the run.
      if (ticks >= POLL_MAX_TICKS || (ticks > 0 && !pendingRef.current)) return stopPoll()
      // A slow list answer must land: a new run would discard it.
      if (loadingRef.current) return
      ticks++
      runRef.current()
    }, POLL_MS)
  }
  // A run starts when a landed listing holds a pending row; it ends with the row, the client or the view.
  useEffect(() => {
    if (pendingNow) startPoll()
    return stopPoll
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [entityId, pendingNow])

  // A refetch that fails after rows landed keeps them and says why.
  useEffect(() => {
    if (list.status === 'error' && shown && list.error) setFlash(list.error.message)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list.status])

  if (entityId === null) return <EmptyState title={CONNECTORS_NO_CLIENT_TITLE} message={CONNECTORS_NO_CLIENT_MESSAGE} />

  // The write failed: 401/403 committed nothing; anything else may have, so the list is refetched.
  function failed(err: unknown) {
    const status = err instanceof ApiError ? err.status : null
    setFlash(err instanceof Error ? err.message : String(err))
    if (status === 403) ctx.refetchMembers()
    if (status !== 401 && status !== 403) runRef.current()
  }

  async function connect(row: ConnectorRow) {
    if (busyRef.current || !base || entityId === null) return
    busyRef.current = true
    const eid = entityId
    const tab = openConsentTab()
    setBusyKey(row.key)
    setBlocked(null)
    try {
      const answer = await connectErp(ctx.authedFetch, base, eid, connectBody(row))
      // The user asked for this consent, so it opens even when the view is gone.
      const url = sendToConsent(tab, answer)
      if (url === null) throw new ApiError('malformed', CONNECTORS_BAD_CONSENT_URL)
      if (!aliveRef.current || entityRef.current !== eid) return
      if (!tab) setBlocked({ entityId: eid, system: row.system, url })
      // The row shows the answered connection now, so it reads pending and the poll has a pending listing to read.
      const { consent_url: _consent, ...landed } = answer
      setMirror((m) =>
        m && m.entityId === eid
          ? { ...m, listing: { ...m.listing, connections: [...m.listing.connections.filter((c) => c.id !== landed.id), landed] } }
          : m,
      )
      startPoll()
    } catch (err) {
      closeConsentTab(tab)
      if (aliveRef.current && entityRef.current === eid) failed(err)
    } finally {
      busyRef.current = false
      if (aliveRef.current) setBusyKey(null)
    }
  }

  async function disconnect(row: ConnectorRow) {
    if (busyRef.current || !base || entityId === null || !row.connection) return
    busyRef.current = true
    const eid = entityId
    setConfirmKey(null)
    setBusyKey(row.key)
    try {
      await disconnectErp(ctx.authedFetch, base, eid, row.connection.id)
      if (aliveRef.current && entityRef.current === eid) runRef.current()
    } catch (err) {
      if (aliveRef.current && entityRef.current === eid) failed(err)
    } finally {
      busyRef.current = false
      if (aliveRef.current) setBusyKey(null)
    }
  }

  let body: React.ReactNode
  if (shown === null) {
    body =
      list.status === 'error' && list.error ? (
        <ErrorState error={list.error} onRetry={list.run} />
      ) : (
        <Loading label={CONNECTORS_LOADING} />
      )
  } else if (connectorsEmpty(shown)) {
    body = <EmptyState title={CONNECTORS_EMPTY_TITLE} />
  } else {
    body = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {connectorRows(shown).map((row) => {
          const label = systemLabel(row.system)
          const pill = statusPill(row)
          const tone = PILL[pill.tone]
          const busy = busyKey === row.key
          const link = blocked && blocked.entityId === entityId && blocked.system === row.system && row.connection?.status !== 'active' ? blocked : null
          return (
            <div key={row.key} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div
                data-testid="erp-connector-row"
                data-system={row.system}
                data-connection-id={row.connection?.id}
                style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 15, padding: '16px 18px' }}
              >
                <span style={{ flex: 'none', width: 42, height: 42, borderRadius: 'var(--radius-md)', background: 'var(--slate-800)', color: 'var(--primary-foreground)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, letterSpacing: '0.02em' }}>{label.mono}</span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <span style={{ fontSize: 14.5, fontWeight: 600 }}>{label.name}</span>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                    {syncLine(row)}
                  </div>
                  {link && (
                    <div style={{ fontSize: 12.5, color: 'var(--fg-2)', marginTop: 4 }}>
                      {CONNECTORS_POPUP_BLOCKED}{' '}
                      <a data-testid="erp-consent-link" href={link.url} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--action)' }}>
                        {CONNECTORS_POPUP_LINK}
                      </a>
                    </div>
                  )}
                </div>
                <span data-testid="erp-connector-status" style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '4px 10px' }}>
                  <span style={{ width: 5, height: 5, borderRadius: '50%', background: tone.text }} />
                  <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: tone.text, letterSpacing: '0.04em' }}>{pill.text}</span>
                </span>
                {row.connection && (
                  <button
                    type="button"
                    data-testid="erp-connector-manage"
                    disabled={busy}
                    onClick={() => ctx.openErpConnection(row.connection!.id)}
                    className="pf-btn"
                    style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 'var(--radius-btn)', border: '1px solid var(--line-2)', background: 'transparent', color: 'var(--fg-2)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500, ...(busy ? DISABLED : null) }}
                  >
                    Manage
                  </button>
                )}
                {admin && canConnect(row, shown.systems) && (
                  <button
                    type="button"
                    data-testid="erp-connector-connect"
                    disabled={busy}
                    onClick={() => void connect(row)}
                    className="pf-btn"
                    style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 'var(--radius-btn)', border: '1px solid var(--action)', background: 'var(--action)', color: 'var(--primary-foreground)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500, ...(busy ? DISABLED : null) }}
                  >
                    Connect
                  </button>
                )}
                {admin && canDisconnect(row) && (
                  <button
                    type="button"
                    data-testid="erp-connector-disconnect"
                    disabled={busy}
                    onClick={() => setConfirmKey(row.key)}
                    className="pf-btn"
                    style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 'var(--radius-btn)', border: '1px solid var(--line-2)', background: 'transparent', color: 'var(--fg-2)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500, ...(busy ? DISABLED : null) }}
                  >
                    Disconnect
                  </button>
                )}
              </div>
              {admin && confirmKey === row.key && (
                <div data-testid="erp-disconnect-confirm" style={{ padding: '9px 16px', background: 'var(--status-red-bg)', borderTop: '1px solid var(--status-red-border)' }}>
                  <p style={{ margin: '0 0 8px', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>{disconnectQuestion(label.name)}</p>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button type="button" onClick={() => setConfirmKey(null)} className="v2-btn v2-btn-ghost pf-btn" data-testid="erp-disconnect-keep" style={{ height: 32, fontSize: 12.5 }}>
                      {DISCONNECT_KEEP}
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => void disconnect(row)}
                      className="v2-btn pf-btn"
                      data-testid="erp-disconnect-confirmed"
                      style={{ height: 32, fontSize: 12.5, background: 'var(--status-red-text)', color: 'var(--primary-foreground)' }}
                    >
                      {DISCONNECT_CONFIRM}
                    </button>
                  </div>
                </div>
              )}
            </div>
          )
        })}
      </div>
    )
  }

  const noteStyle: React.CSSProperties = { margin: '0 0 14px', fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'right' }
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 10, marginBottom: 16 }}>
        <p style={{ flex: '1 1 240px', fontSize: 13.5, color: 'var(--fg-2)', margin: 0, maxWidth: 560, lineHeight: 1.55 }}>{CONNECTORS_INTRO}</p>
        {flash && (
          <span data-testid="erp-connectors-flash" title={flash} style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12.5, color: 'var(--status-red-text)' }}>
            {flash}
          </span>
        )}
      </div>
      {!admin && rosterLanded && (
        <div data-testid="erp-connectors-admin-only" style={noteStyle}>
          {CONNECTORS_ADMIN_ONLY}
        </div>
      )}
      {ctx.membersState === 'error' && (
        <div data-testid="erp-connectors-role-unknown" style={{ ...noteStyle, display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 10 }}>
          <span>{CONNECTORS_ROLE_FAILED}</span>
          <button type="button" onClick={() => ctx.refetchMembers()} className="v2-btn pf-btn" style={{ flex: 'none', height: 28, padding: '0 12px', fontSize: 12.5 }}>
            {CONNECTORS_RETRY}
          </button>
        </div>
      )}
      {body}
    </>
  )
}
