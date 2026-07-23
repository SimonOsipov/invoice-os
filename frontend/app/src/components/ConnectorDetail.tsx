// Connector detail — the per-connector integration surface behind the connector list's
// Manage button (SettingsView). Everything numeric on this screen comes from
// lib/connectors.ts `connectorDetail()`, which derives internally-consistent figures from
// the connector id, so each connector reads distinct but never re-rolls between visits.
//
// The env pill (LIVE/SANDBOX) is a per-connector view toggle owned by SettingsView, since
// it is scoped to this tab; saved field mappings live in the workspace (ctx) so they
// survive navigating away from Settings entirely.

import { useMemo } from 'react'

import { CONNECTOR_TAX_CODES, type ConnectorDef } from '../data'
import { connectorDetail, mappingFor, type SyncEventKind } from '../lib/connectors'
import { fmtPlain } from '../lib/format'
import { backGlyph, refreshGlyph, warnTriGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

const CARD: React.CSSProperties = { background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)' }
const CARD_HEAD: React.CSSProperties = { padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }

// Faint = the connector doing its own housekeeping (a scheduled pull, a poll); the
// document outcomes are the ones that get colour.
const DOT_COLOR: Record<SyncEventKind, string> = {
  transmitted: 'var(--status-green-text)',
  validated: 'var(--status-green-text)',
  held: 'var(--status-red-text)',
  scheduled: 'var(--line-3)',
  pull: 'var(--line-3)',
}

function Stat({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div>
      <div className="label" style={{ marginBottom: 6 }}>
        {label}
      </div>
      <div className="mono" style={{ fontSize: 15, fontWeight: 600, color: color ?? 'var(--fg-1)' }}>
        {value}
      </div>
    </div>
  )
}

function FunnelStep({ n, label, sub }: { n: number; label: string; sub: string }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div className="mono" style={{ fontSize: 28, fontWeight: 600, letterSpacing: '-0.02em', color: 'var(--fg-1)' }}>
        {fmtPlain(n)}
      </div>
      <div className="label" style={{ marginTop: 6 }}>
        {label}
      </div>
      <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 4, lineHeight: 1.45 }}>{sub}</div>
    </div>
  )
}

function Arrow() {
  return (
    <span className="mono" style={{ fontSize: 15, color: 'var(--line-3)', alignSelf: 'start', paddingTop: 8 }} aria-hidden="true">
      →
    </span>
  )
}

export function ConnectorDetail({
  ctx,
  def,
  env,
  onToggleEnv,
  onBack,
  onEditMapping,
}: {
  ctx: PlatformCtx
  def: ConnectorDef
  env: 'LIVE' | 'SANDBOX'
  onToggleEnv: () => void
  onBack: () => void
  onEditMapping: () => void
}) {
  const d = useMemo(() => connectorDetail(def), [def])
  const mapping = mappingFor(def, ctx.connectorMappings)
  const live = env === 'LIVE'
  const volMax = Math.max(...d.volume)
  const driftClean = d.funnel.drift === 0

  return (
    <>
      <button
        onClick={onBack}
        className="pf-btn"
        style={{ display: 'inline-flex', alignItems: 'center', gap: 7, border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, padding: 0, marginBottom: 14 }}
      >
        {backGlyph} All connectors
      </button>

      {/* Header */}
      <div style={{ ...CARD, padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 15, marginBottom: 16 }}>
        <span style={{ flex: 'none', width: 42, height: 42, borderRadius: 'var(--radius-xl)', background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, letterSpacing: '0.02em' }}>{def.mono}</span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ fontSize: 16, fontWeight: 600 }}>{def.name}</span>
            <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 5px', letterSpacing: '0.06em' }}>{def.cat}</span>
          </div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 4, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {def.host} · {def.module}
          </div>
        </div>
        <button
          onClick={onToggleEnv}
          className="pf-btn"
          aria-label={`Environment: ${env} — switch to ${live ? 'sandbox' : 'live'}`}
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer', background: live ? 'var(--status-green-bg)' : 'var(--status-amber-bg)', border: `1px solid ${live ? 'var(--status-green-border)' : 'var(--status-amber-border)'}`, borderRadius: 999, padding: '5px 11px' }}
        >
          <span style={{ width: 5, height: 5, borderRadius: 99, background: live ? 'var(--status-green-text)' : 'var(--status-amber-text)' }} />
          <span className="mono" style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', color: live ? 'var(--status-green-text)' : 'var(--status-amber-text)' }}>{env}</span>
        </button>
        <button className="v2-btn v2-btn-primary pf-btn" style={{ flex: 'none', height: 34, fontSize: 13, padding: '0 14px' }}>
          <span style={{ display: 'inline-flex' }}>{refreshGlyph}</span> Sync now
        </button>
      </div>

      {/* Health strip */}
      <div className="pf-health" style={{ ...CARD, display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', marginBottom: 16, overflow: 'hidden' }}>
        {[
          { label: 'Last sync', value: d.lastSync },
          { label: 'Frequency', value: d.frequency },
          { label: 'Queue depth', value: String(d.queueDepth) },
          { label: 'Token expires', value: d.tokenExpires },
          { label: 'Error rate · 24h', value: d.errorRate },
        ].map((t, i) => (
          <div key={t.label} style={{ padding: '15px 18px', borderLeft: i === 0 ? 0 : '1px solid var(--line-1)' }}>
            <Stat label={t.label} value={t.value} />
          </div>
        ))}
      </div>

      {/* Reconciliation funnel */}
      <div style={{ ...CARD, marginBottom: 16 }}>
        <div style={CARD_HEAD}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Reconciliation · ERP ↔ FIRS</span>
          <span
            className="mono"
            style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', borderRadius: 999, padding: '4px 9px', background: driftClean ? 'var(--status-green-bg)' : 'var(--status-amber-bg)', border: `1px solid ${driftClean ? 'var(--status-green-border)' : 'var(--status-amber-border)'}`, color: driftClean ? 'var(--status-green-text)' : 'var(--status-amber-text)' }}
          >
            DRIFT {d.funnel.drift}
          </span>
        </div>
        <div style={{ padding: '20px 20px 18px' }}>
          <div className="pf-funnel" style={{ display: 'grid', gridTemplateColumns: '1fr auto 1fr auto 1fr auto 1fr', gap: 14, alignItems: 'start' }}>
            <FunnelStep n={d.funnel.inErp} label="In ERP" sub="Pulled · 30 days" />
            <Arrow />
            <FunnelStep n={d.funnel.validated} label="Validated" sub="Passed rule pack" />
            <Arrow />
            <FunnelStep n={d.funnel.transmitted} label="Transmitted" sub="Sent to FIRS" />
            <Arrow />
            <FunnelStep n={d.funnel.accepted} label="FIRS-accepted" sub="IRN + CSID returned" />
          </div>
          <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 18, paddingTop: 14, borderTop: '1px solid var(--line-1)' }}>
            {driftClean ? 'Every transmitted document has been acknowledged by FIRS.' : `${d.funnel.drift} document${d.funnel.drift === 1 ? '' : 's'} not yet acknowledged by FIRS.`}
          </div>
        </div>
      </div>

      {/* Volume */}
      <div style={{ ...CARD, marginBottom: 16 }}>
        <div style={CARD_HEAD}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Documents pulled</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            {fmtPlain(d.volumeTotal)} PULLED · 30 DAYS
          </span>
        </div>
        <div style={{ padding: '20px 20px 18px' }}>
          <div style={{ display: 'flex', alignItems: 'flex-end', gap: 4, height: 76 }}>
            {d.volume.map((v, i) => (
              <div key={i} title={`${v} documents`} style={{ flex: 1, height: `${Math.max(4, (v / volMax) * 100)}%`, background: 'var(--accent)', opacity: 0.55, borderRadius: 'var(--radius-xs)' }} />
            ))}
          </div>
        </div>
      </div>

      {/* Field mapping + sync activity */}
      <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: 16, marginBottom: 16 }}>
        <div style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 14, fontWeight: 600 }}>Field mapping</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 3 }}>
                ERP → UBL
              </div>
            </div>
            <button onClick={onEditMapping} className="v2-btn v2-btn-ghost pf-btn" style={{ flex: 'none', height: 30, fontSize: 12.5, padding: '0 12px' }}>
              Edit
            </button>
          </div>
          <div style={{ padding: '4px 20px 12px' }}>
            {/* Index keys: a user can edit two rows onto the same UBL path in the modal,
                so neither column is a stable identity. The list is static per render. */}
            {mapping.map((m, i) => (
              <div key={i} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 0.85fr) 12px minmax(0, 1.15fr)', gap: 8, alignItems: 'baseline', padding: '10px 0', borderBottom: '1px solid var(--line-1)' }}>
                <code className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', overflowWrap: 'anywhere' }}>{m.erp}</code>
                <span className="mono" style={{ fontSize: 11, color: 'var(--line-3)' }} aria-hidden="true">
                  →
                </span>
                <code className="mono" style={{ fontSize: 11.5, color: 'var(--accent)', overflowWrap: 'anywhere' }}>{m.ubl}</code>
              </div>
            ))}
          </div>
        </div>

        <div style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Sync activity</span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
              TODAY
            </span>
          </div>
          <div style={{ padding: '4px 20px 12px' }}>
            {d.activity.map((a, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '10px 0', borderBottom: '1px solid var(--line-1)' }}>
                <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)', width: 36 }}>{a.time}</span>
                <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: DOT_COLOR[a.kind], transform: 'translateY(-2px)' }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <code className="mono" style={{ fontSize: 11.5, fontWeight: 600, color: a.doc === '—' ? 'var(--fg-3)' : 'var(--fg-1)' }}>{a.doc}</code>
                  <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 2, lineHeight: 1.45 }}>{a.desc}</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Master data + write-back */}
      <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.4fr) minmax(0, 1fr)', gap: 16, marginBottom: 16 }}>
        <div style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Master data mirror</span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
              REFRESHED 2 MIN AGO
            </span>
          </div>
          <div style={{ padding: '18px 20px' }}>
            <div className="pf-grid-4" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 10, marginBottom: 18 }}>
              {[
                { label: 'Customers', value: fmtPlain(d.master.customers) },
                { label: 'Tax codes', value: String(d.master.taxCodes) },
                { label: 'Items / SKUs', value: fmtPlain(d.master.items) },
                { label: 'Units of measure', value: String(d.master.uoms) },
              ].map((s) => (
                <div key={s.label} style={{ background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-lg)', padding: '12px 13px' }}>
                  <div className="mono" style={{ fontSize: 19, fontWeight: 600, letterSpacing: '-0.02em' }}>{s.value}</div>
                  <div className="label" style={{ marginTop: 5 }}>
                    {s.label}
                  </div>
                </div>
              ))}
            </div>
            <div className="label" style={{ marginBottom: 4 }}>
              Tax codes
            </div>
            {CONNECTOR_TAX_CODES.map((t) => (
              <div key={t.code} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '9px 0', borderBottom: '1px solid var(--line-1)' }}>
                <code className="mono" style={{ flex: 'none', width: 46, fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)' }}>{t.code}</code>
                <span style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)' }}>{t.desc}</span>
                <span className="mono" style={{ flex: 'none', fontSize: 11.5, color: 'var(--fg-3)' }}>{t.rate}</span>
              </div>
            ))}
          </div>
        </div>

        <div style={{ ...CARD, overflow: 'hidden' }}>
          <div style={CARD_HEAD}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Write-back to ERP</span>
          </div>
          <div style={{ padding: '18px 20px' }}>
            <div style={{ display: 'flex', gap: 10, marginBottom: 18 }}>
              {[
                { label: 'Stamped back', value: fmtPlain(d.writeBack.stamped), color: 'var(--status-green-text)' },
                { label: 'Pending', value: String(d.writeBack.pending), color: 'var(--status-amber-text)' },
                { label: 'Failed', value: String(d.writeBack.failed), color: 'var(--status-red-text)' },
              ].map((s) => (
                <div key={s.label} style={{ flex: 1, minWidth: 0 }}>
                  <div className="mono" style={{ fontSize: 19, fontWeight: 600, letterSpacing: '-0.02em', color: s.color }}>{s.value}</div>
                  <div className="label" style={{ marginTop: 5 }}>
                    {s.label}
                  </div>
                </div>
              ))}
            </div>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
              <span style={{ fontSize: 12, color: 'var(--fg-2)' }}>IRN + CSID stamped</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{d.writeBack.pct}%</span>
            </div>
            <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden', marginBottom: 14 }}>
              <div style={{ width: `${d.writeBack.pct}%`, height: '100%', background: 'var(--accent)', borderRadius: 'var(--radius-sm)' }} />
            </div>
            <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: 0, lineHeight: 1.5 }}>IRN + CSID synced back into {def.name} invoice records.</p>
          </div>
        </div>
      </div>

      {/* Held documents */}
      <div style={{ ...CARD, overflow: 'hidden' }}>
        <div style={CARD_HEAD}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 11, minWidth: 0 }}>
            <span style={{ flex: 'none', width: 32, height: 32, borderRadius: 'var(--radius-lg)', background: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', display: 'grid', placeItems: 'center' }}>{warnTriGlyph}</span>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 14, fontWeight: 600 }}>Held documents</div>
              <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 2 }}>{fmtPlain(d.heldTotal)} pulled but not transmitted</div>
            </div>
          </div>
          <button className="v2-btn v2-btn-ghost pf-btn" style={{ flex: 'none', height: 30, fontSize: 12.5, padding: '0 12px' }}>
            Backfill history
          </button>
        </div>
        {d.held.map((h, i) => (
          <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '13px 20px', borderBottom: '1px solid var(--line-1)' }}>
            <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: 'var(--status-red-text)' }} />
            <code className="mono" style={{ flex: 'none', fontSize: 12.5, fontWeight: 600, color: 'var(--fg-1)' }}>{h.doc}</code>
            <span style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{h.reason}</span>
            <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>{h.age}</span>
            <button className="v2-btn v2-btn-ghost pf-btn" style={{ flex: 'none', height: 28, fontSize: 12, padding: '0 11px' }}>
              Review
            </button>
          </div>
        ))}
        {d.heldTotal > d.held.length && (
          <div className="mono" style={{ padding: '11px 20px', fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
            SHOWING {d.held.length} OF {fmtPlain(d.heldTotal)} — OLDEST FIRST
          </div>
        )}
      </div>
    </>
  )
}
