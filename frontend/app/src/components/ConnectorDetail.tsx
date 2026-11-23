// The ERP connector detail: one connection of the active client, as the platform holds it.
// Server truth only: the overview read, the field mapping and its lookups.

import { useEffect, useRef, useState } from 'react'

import { ApiError, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { backGlyph, warnTriGlyph } from '../glyphs'
import {
  ACTIVITY_EMPTY,
  ACTIVITY_HEAD,
  ACTIVITY_SUB,
  activitySummary,
  BACK_TO_CONNECTORS,
  clientLine,
  documentOutcome,
  getErpLookups,
  getErpMapping,
  getErpOverview,
  HELD_EMPTY,
  HELD_HEAD,
  HELD_SUB,
  heldReasonText,
  LOADING_CONNECTION,
  LOADING_MAPPING,
  LOOKUPS_EMPTY,
  LOOKUPS_HEAD,
  LOOKUPS_NEED_KEY,
  lookupKindText,
  lookupValueLabel,
  LOOKUP_VALUE_KEYS,
  MAPPING_ADMIN_ONLY,
  MAPPING_CUSTOM,
  MAPPING_EDIT,
  MAPPING_HEAD,
  MAPPING_NOT_MAPPED,
  MAPPING_SUB,
  notFoundTitle,
  OPEN_INVOICE,
  RECONNECT,
  WRITEBACK_EMPTY,
  WRITEBACK_HEAD,
  writebackResultText,
  writebackText,
  type ErpOverview,
  type LookupsView,
  type MappingView,
} from '../lib/erpConnectionDetail'
import {
  CONNECTORS_BAD_CONSENT_URL,
  CONNECTORS_POPUP_BLOCKED,
  CONNECTORS_POPUP_LINK,
  connectBody,
  connectErp,
  POLL_MAX_TICKS,
  POLL_MS,
  statusPill,
  syncLine,
  systemLabel,
  type ConnectorRow,
} from '../lib/erpConnections'
import { closeConsentTab, openConsentTab, sendToConsent } from '../lib/erpConsent'
import { fmtDateTime } from '../lib/format'
import { viewerIsAdmin } from '../lib/members'
import type { PlatformCtx } from '../types'
import { FieldMappingModal } from './FieldMappingModal'

const CARD: React.CSSProperties = { background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)' }
const CARD_HEAD: React.CSSProperties = { padding: '13px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }
const ROW: React.CSSProperties = { display: 'flex', alignItems: 'baseline', flexWrap: 'wrap', gap: 10, padding: '10px 0', borderBottom: '1px solid var(--line-1)' }
const SUB: React.CSSProperties = { fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }
const NOTE: React.CSSProperties = { fontSize: 12, color: 'var(--fg-3)' }
const GHOST: React.CSSProperties = { flex: 'none', height: 30, fontSize: 12.5, padding: '0 12px' }
// The repo has no :disabled rule; filter:none stops .v2-btn-primary:hover repainting a dead button.
const DISABLED: React.CSSProperties = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }

const PILL = {
  green: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  neutral: { bg: 'var(--bg-3)', border: 'var(--line-2)', text: 'var(--fg-3)' },
} as const

const DOT: Record<string, string> = {
  drafted: 'var(--status-green-text)',
  linked: 'var(--status-green-text)',
  held: 'var(--status-amber-text)',
  failed: 'var(--status-red-text)',
  deleted: 'var(--status-red-text)',
}

type Landed = { key: string; overview: ErpOverview }
// A saved view, kept until the next read of its key lands over it.
type Saved<T> = { key: string; view: T }

export function ConnectorDetail({
  ctx,
  entityId,
  connectionId,
  onBack,
}: {
  ctx: PlatformCtx
  entityId: string
  connectionId: string
  onBack: () => void
}) {
  const base = gatewayBase()
  const admin = viewerIsAdmin(ctx.members)
  const rosterLanded = ctx.membersState === 'ready' || ctx.membersState === 'empty'
  const clientName = ctx.activeEntity?.name ?? ''
  const key = `${entityId}/${connectionId}`

  const overview = useAsync<Landed>(
    () =>
      base
        ? getErpOverview(ctx.authedFetch, base, entityId, connectionId).then((o) => ({ key, overview: o }))
        : Promise.reject(new Error('no gateway configured')),
    { deps: [entityId, connectionId] },
  )
  // The last landed overview: a refetch nulls overview.data, and the panels must stay put meanwhile.
  const [mirror, setMirror] = useState<Landed | null>(null)
  const [seen, setSeen] = useState<Landed | null>(null)
  if (overview.data && overview.data !== seen) {
    setSeen(overview.data)
    setMirror(overview.data)
  }
  const shown = mirror && mirror.key === key ? mirror.overview : null
  const conn = shown?.connection ?? null
  const system = conn?.system ?? null
  const externalKey = conn?.external_key ?? null
  const pendingNow = conn?.status === 'pending'

  const mappingRead = useAsync<MappingView>(
    () => (base && system ? getErpMapping(ctx.authedFetch, base, entityId, system) : Promise.reject(new Error('no gateway configured'))),
    { immediate: system !== null, deps: [entityId, system] },
  )
  const lookupsRead = useAsync<LookupsView>(
    () =>
      base && system && externalKey
        ? getErpLookups(ctx.authedFetch, base, entityId, system, externalKey)
        : Promise.reject(new Error('no gateway configured')),
    { immediate: system !== null && externalKey !== null, deps: [entityId, system, externalKey] },
  )
  const mapKey = `${entityId}/${system}`
  const lookKey = `${mapKey}/${externalKey}`
  const [savedMap, setSavedMap] = useState<Saved<MappingView> | null>(null)
  const [savedLook, setSavedLook] = useState<Saved<LookupsView> | null>(null)
  const mapping = savedMap?.key === mapKey ? savedMap.view : mappingRead.data
  const lookups = savedLook?.key === lookKey ? savedLook.view : lookupsRead.data
  const [editing, setEditing] = useState(false)

  const [busy, setBusy] = useState(false)
  const [blocked, setBlocked] = useState<{ key: string; url: string } | null>(null)
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
  const runRef = useRef(overview.run)
  runRef.current = overview.run
  const pendingRef = useRef(pendingNow)
  pendingRef.current = pendingNow
  const loadingRef = useRef(false)
  loadingRef.current = overview.status === 'loading'

  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  // The poll runs while the connection waits for sign-in; it ends with the connection, the view or the cap.
  useEffect(() => {
    if (!pendingNow) return
    let ticks = 0
    const id = window.setInterval(() => {
      if (ticks >= POLL_MAX_TICKS || (ticks > 0 && !pendingRef.current)) return window.clearInterval(id)
      // A slow answer must land: a new run would discard it.
      if (loadingRef.current) return
      ticks++
      runRef.current()
    }, POLL_MS)
    return () => window.clearInterval(id)
  }, [entityId, connectionId, pendingNow])

  // A refetch that fails after the panels landed keeps them and says why.
  useEffect(() => {
    if (overview.status === 'error' && shown && overview.error) setFlash(overview.error.message)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [overview.status])

  // 401/403 committed nothing; anything else may have, so the overview is read again.
  function failed(err: unknown) {
    const status = err instanceof ApiError ? err.status : null
    setFlash(err instanceof Error ? err.message : String(err))
    if (status === 403) ctx.refetchMembers()
    if (status !== 401 && status !== 403) runRef.current()
  }

  async function reconnect() {
    if (busyRef.current || !base || !conn) return
    busyRef.current = true
    const eid = entityId
    const row: ConnectorRow = { key: conn.id, system: conn.system, connection: conn }
    const tab = openConsentTab()
    setBusy(true)
    setBlocked(null)
    try {
      const answer = await connectErp(ctx.authedFetch, base, eid, connectBody(row))
      // The user asked for this consent, so it opens even when the view is gone.
      const url = sendToConsent(tab, answer)
      if (url === null) throw new ApiError('malformed', CONNECTORS_BAD_CONSENT_URL)
      if (!aliveRef.current || entityRef.current !== eid) return
      if (!tab) setBlocked({ key, url })
      // The header reads the answered connection now, so it is pending and the poll starts.
      const { consent_url: _consent, ...landed } = answer
      setMirror((m) => (m && m.key === key ? { ...m, overview: { ...m.overview, connection: landed } } : m))
    } catch (err) {
      closeConsentTab(tab)
      if (aliveRef.current && entityRef.current === eid) failed(err)
    } finally {
      busyRef.current = false
      if (aliveRef.current) setBusy(false)
    }
  }

  const back = (
    <button
      type="button"
      data-testid="erp-detail-back"
      onClick={onBack}
      className="pf-btn"
      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12.5, padding: 0, marginBottom: 14 }}
    >
      {backGlyph} {BACK_TO_CONNECTORS}
    </button>
  )

  if (shown === null || conn === null) {
    let body: React.ReactNode = <Loading label={LOADING_CONNECTION} />
    if (overview.status === 'error' && overview.error) {
      body =
        overview.error.status === 404 ? (
          <div data-testid="erp-detail-not-found" style={{ ...CARD, padding: '20px 22px', fontSize: 14, fontWeight: 600 }}>
            {notFoundTitle(clientName)}
          </div>
        ) : (
          <ErrorState error={overview.error} onRetry={overview.run} />
        )
    }
    return (
      <>
        {back}
        {body}
      </>
    )
  }

  const label = systemLabel(conn.system)
  const row: ConnectorRow = { key: conn.id, system: conn.system, connection: conn }
  const pill = statusPill(row)
  const tone = PILL[pill.tone]
  const link = blocked && blocked.key === key && conn.status !== 'active' ? blocked : null
  const lookupsLoading = externalKey !== null && lookupsRead.status === 'loading' && !lookups
  const canEdit = admin && mapping != null && !lookupsLoading

  let mappingBody: React.ReactNode
  if (mapping == null) {
    mappingBody =
      mappingRead.status === 'error' && mappingRead.error ? (
        <ErrorState error={mappingRead.error} onRetry={mappingRead.run} />
      ) : (
        <Loading label={LOADING_MAPPING} />
      )
  } else {
    mappingBody = (
      <>
        {mapping.fields.map((f) => (
          <div key={f.target} data-testid="erp-mapping-row" data-target={f.target} style={{ ...ROW, display: 'grid', gridTemplateColumns: 'minmax(0, 1.15fr) 12px minmax(0, 0.85fr)', gap: 8 }}>
            <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
              {f.source_path === null ? (
                <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>{MAPPING_NOT_MAPPED}</span>
              ) : (
                <code className="mono" style={{ fontSize: 11.5, color: 'var(--action)' }}>{f.source_path}</code>
              )}
              {f.origin === 'client' && (
                <span className="mono" style={{ marginLeft: 8, fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 5px' }}>{MAPPING_CUSTOM}</span>
              )}
            </span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-4)' }} aria-hidden="true">→</span>
            <span style={{ fontSize: 12, color: 'var(--fg-2)' }}>{f.label}</span>
          </div>
        ))}
        <div className="label" style={{ margin: '14px 0 4px' }}>{LOOKUPS_HEAD}</div>
        {externalKey === null ? (
          <div style={NOTE}>{LOOKUPS_NEED_KEY}</div>
        ) : lookups ? (
          lookups.lookups.length === 0 ? (
            <div style={NOTE}>{LOOKUPS_EMPTY}</div>
          ) : (
            lookups.lookups.map((l) => (
              <div key={`${l.kind}/${l.erp_key}`} data-testid="erp-lookup-row" style={ROW}>
                <span style={{ fontSize: 12, fontWeight: 600 }}>{lookupKindText(l.kind)}</span>
                <code className="mono" style={{ fontSize: 11.5 }}>{l.erp_key}</code>
                <span style={{ fontSize: 11.5, color: 'var(--fg-3)' }}>
                  {LOOKUP_VALUE_KEYS.filter((k) => l[k]).map((k) => `${lookupValueLabel[k]} ${l[k]}`).join(' · ')}
                </span>
              </div>
            ))
          )
        ) : lookupsRead.status === 'error' && lookupsRead.error ? (
          <ErrorState error={lookupsRead.error} onRetry={lookupsRead.run} />
        ) : (
          <Loading label={LOADING_MAPPING} />
        )}
      </>
    )
  }

  const { counts, documents } = shown.activity
  return (
    <>
      {back}

      <div data-testid="erp-detail-header" style={{ ...CARD, padding: '18px 20px', display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 15, marginBottom: 16 }}>
        <span style={{ flex: 'none', width: 42, height: 42, borderRadius: 'var(--radius-md)', background: 'var(--slate-800)', color: 'var(--primary-foreground)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700 }}>{label.mono}</span>
        <div style={{ flex: '1 1 220px', minWidth: 0 }}>
          <span style={{ fontSize: 16, fontWeight: 700 }}>{label.name}</span>
          <div style={{ fontSize: 12.5, color: 'var(--fg-2)', marginTop: 3, overflowWrap: 'anywhere' }}>{clientLine(clientName, conn.external_key)}</div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>{syncLine(row)}</div>
          {link && (
            <div style={{ fontSize: 12.5, color: 'var(--fg-2)', marginTop: 4 }}>
              {CONNECTORS_POPUP_BLOCKED}{' '}
              <a data-testid="erp-consent-link" href={link.url} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--action)' }}>
                {CONNECTORS_POPUP_LINK}
              </a>
            </div>
          )}
          {flash && (
            <div data-testid="erp-detail-flash" title={flash} style={{ marginTop: 4, fontSize: 12.5, color: 'var(--status-red-text)', overflowWrap: 'anywhere' }}>
              {flash}
            </div>
          )}
        </div>
        <span data-testid="erp-detail-status" style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '4px 10px' }}>
          <span style={{ width: 5, height: 5, borderRadius: '50%', background: tone.text }} />
          <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: tone.text, letterSpacing: '0.04em' }}>{pill.text}</span>
        </span>
        {admin && conn.status === 'reconsent' && (
          <button
            type="button"
            data-testid="erp-detail-reconnect"
            disabled={busy}
            onClick={() => void reconnect()}
            className="pf-btn"
            style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 'var(--radius-btn)', border: '1px solid var(--action)', background: 'var(--action)', color: 'var(--primary-foreground)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500, ...(busy ? DISABLED : null) }}
          >
            {RECONNECT}
          </button>
        )}
      </div>

      <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: 16, marginBottom: 16 }}>
        <div data-testid="erp-detail-mapping" style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 14, fontWeight: 600 }}>{MAPPING_HEAD}</div>
              <div className="mono" style={{ ...SUB, marginTop: 3 }}>{MAPPING_SUB}</div>
            </div>
            {canEdit && (
              <button type="button" data-testid="erp-detail-mapping-edit" onClick={() => setEditing(true)} className="v2-btn v2-btn-ghost pf-btn" style={GHOST}>
                {MAPPING_EDIT}
              </button>
            )}
          </div>
          <div style={{ padding: '4px 20px 14px' }}>
            {!admin && rosterLanded && (
              <div data-testid="erp-detail-mapping-admin-only" style={{ ...NOTE, padding: '8px 0' }}>
                {MAPPING_ADMIN_ONLY}
              </div>
            )}
            {mappingBody}
          </div>
        </div>

        <div data-testid="erp-detail-activity" style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>{ACTIVITY_HEAD}</span>
            <span className="mono" style={SUB}>{ACTIVITY_SUB}</span>
          </div>
          <div style={{ padding: '4px 20px 12px' }}>
            <div style={{ ...NOTE, padding: '10px 0' }}>{activitySummary(counts)}</div>
            {documents.length === 0 && <div style={NOTE}>{ACTIVITY_EMPTY}</div>}
            {documents.map((d) => (
              <div key={d.id} data-testid="erp-activity-row" style={ROW}>
                <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>{fmtDateTime(d.updated_at)}</span>
                <span style={{ flex: 'none', width: 5, height: 5, borderRadius: '50%', background: DOT[d.state] ?? 'var(--fg-4)' }} />
                <code className="mono" style={{ fontSize: 11.5, fontWeight: 600, overflowWrap: 'anywhere' }}>{d.source_doc_id}</code>
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, letterSpacing: '0.04em', color: 'var(--fg-2)' }}>{documentOutcome(d)}</span>
                {d.invoice_number && <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)' }}>{d.invoice_number}</span>}
              </div>
            ))}
          </div>
        </div>
      </div>

      <div data-testid="erp-detail-writebacks" style={{ ...CARD, overflow: 'hidden', marginBottom: 16 }}>
        <div style={CARD_HEAD}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>{WRITEBACK_HEAD}</span>
        </div>
        <div style={{ padding: '4px 20px 12px' }}>
          {shown.writebacks.length === 0 && <div style={{ ...NOTE, padding: '10px 0' }}>{WRITEBACK_EMPTY}</div>}
          {shown.writebacks.map((w) => (
            <div key={w.id} data-testid="erp-writeback-row" style={ROW}>
              <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>{fmtDateTime(w.created_at)}</span>
              <code className="mono" style={{ fontSize: 11.5, fontWeight: 600 }}>{w.invoice_number ?? w.invoice_id}</code>
              <span style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)' }}>{`${writebackResultText(w.outcome)} · ${writebackText(w)}`}</span>
              <button type="button" onClick={() => ctx.openImportedInvoice(w.invoice_id)} className="v2-btn v2-btn-ghost pf-btn" style={{ ...GHOST, height: 28 }}>
                {OPEN_INVOICE}
              </button>
            </div>
          ))}
        </div>
      </div>

      <div data-testid="erp-detail-held" style={{ ...CARD, overflow: 'hidden' }}>
        <div style={CARD_HEAD}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
            <span style={{ flex: 'none', width: 32, height: 32, borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', display: 'grid', placeItems: 'center' }}>{warnTriGlyph}</span>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 14, fontWeight: 600 }}>{HELD_HEAD}</div>
              <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 2 }}>{HELD_SUB}</div>
            </div>
          </div>
        </div>
        <div style={{ padding: '4px 20px 12px' }}>
          {shown.held.length === 0 && <div style={{ ...NOTE, padding: '10px 0' }}>{HELD_EMPTY}</div>}
          {shown.held.map((h) => (
            <div key={h.id} data-testid="erp-held-row" data-source-doc-id={h.source_doc_id} style={ROW}>
              <code className="mono" style={{ fontSize: 12.5, fontWeight: 600, overflowWrap: 'anywhere' }}>{h.source_doc_id}</code>
              {h.invoice_number && <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)' }}>{h.invoice_number}</span>}
              <span style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)' }}>
                {h.reasons.map((r) => heldReasonText(r, h.failure)).join(' · ')}
              </span>
              {h.invoice_id && (
                <button type="button" data-testid="erp-held-open" onClick={() => ctx.openImportedInvoice(h.invoice_id!)} className="v2-btn v2-btn-ghost pf-btn" style={{ ...GHOST, height: 28 }}>
                  {OPEN_INVOICE}
                </button>
              )}
            </div>
          ))}
        </div>
      </div>

      {editing && mapping != null && system !== null && (
        <FieldMappingModal
          ctx={ctx}
          entityId={entityId}
          system={system}
          systemName={label.name}
          externalKey={externalKey}
          mapping={mapping}
          lookups={lookups ?? null}
          onSaved={(next) => {
            if (next.mapping) setSavedMap({ key: mapKey, view: next.mapping })
            if (next.lookups) setSavedLook({ key: lookKey, view: next.lookups })
          }}
          onClose={() => setEditing(false)}
        />
      )}
    </>
  )
}
