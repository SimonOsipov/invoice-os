// Dashboard — compliance overview. Reads the M4-07 rollup
// (GET /api/dashboard/v1/rollup) via lib/dashboard.ts and renders honest
// loading / error / empty / ready states.
// Structurally mirrors ClientsView.tsx (typed API module + useAsync +
// no-gateway short-circuit). Markup ported from Platform.dc.html ~L147-310.
//
// Which panel is which:
//
//   LIVE (rollup)   needs-attention KPI · invoice-status donut · readiness ring + bars ·
//                   top validation failures · all four KPI tile VALUES
//   LIVE (audit)    recent activity (RecentActivityBody)
//   LIVE (history)  KPI sparklines + Readiness trend; empty state under two recorded days.
//
// [dashboard-scope-per-client] (persona-handoff-fix step 2): this page is a CLIENT-scoped
// surface (Sidebar.tsx's CLIENT nav group), so every LIVE panel above scopes to the
// SELECTED client's own bucket, not the whole tenant — scopedBucket (lib/dashboard.ts)
// resolves in-house to the tenant totals (its one "client" IS the tenant) and firm mode
// to the active entity's own row, or an honest zero bucket when none resolves yet / the
// entity has never had an invoice.

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { crossGlyph, tickGlyph13 } from '../glyphs'
import {
  dashboardViewState,
  donutSegments,
  formatMetric,
  getRollup,
  getRollupHistory,
  readinessBars,
  readinessNote,
  readinessRing,
  resolveCtaLabel,
  scopedBucket,
  topFailures,
  type Counts,
  type HistoryRow,
  type Rollup,
  type RollupHistory,
} from '../lib/dashboard'
import { historyView, watToday } from '../lib/dashboardHistory'
import { ruleKeyLabel } from '../lib/rulesApi'
import type { PlatformCtx } from '../types'
import { RecentActivityBody } from './RecentActivityBody'

export function DashboardActive({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  // base ? … : … narrowing (not a base! assertion) keeps the producer well-typed
  // without trusting a non-null base; immediate: base != null keeps the no-gateway
  // build at zero network. Mirrors ClientsView.tsx:38-41.
  const roll = useAsync<Rollup>(
    () => (base ? getRollup(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    // No isEmpty predicate on purpose. isEmptyRollup (all seven counts zero) used to
    // classify a fresh workspace as 'empty', and useAsync NULLS data in that state
    // (async-state.ts:51) — so the tiles could never mount. Six of the nine panels now
    // carry content regardless of invoice volume, and each live panel has its own zero
    // treatment, so a zero-count rollup is 'ready', not empty. isEmptyRollup stays
    // exported and tested for other callers.
    { immediate: base != null },
  )
  // A failed or pending history read draws nothing and shows no error.
  const hist = useAsync<RollupHistory>(
    () => (base ? getRollupHistory(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const state = dashboardViewState(base, roll)

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      {/* Header identity follows the SAME scope as the tiles below it
          ([dashboard-scope-per-client]): in-house has one workspace = the tenant, so it
          keeps the tenant name + "Firm-wide" copy ([header-chrome-firmwide]); firm mode
          now names the SELECTED CLIENT instead of the firm itself, so this heading can
          never again disagree with the switcher above it (the original bug this story
          fixes). The mock taxpayer pill, "SYNCED …", and "Period to date" chrome are
          still gone, unrelated to this change. */}
      <div style={{ marginBottom: 26 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          COMPLIANCE OVERVIEW
        </div>
        <h1 style={{ fontSize: 28, letterSpacing: '-0.03em', margin: '0 0 5px', overflowWrap: 'anywhere' }}>
          {ctx.mode === 'inhouse' ? ctx.user.tenantName ?? 'Your firm' : ctx.active.name}
        </h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
          {ctx.mode === 'inhouse' ? 'Firm-wide invoice compliance' : 'Invoice compliance for this client'}
        </p>
      </div>

      {state === 'loading' && <Loading label="Loading dashboard…" />}

      {state === 'error' && roll.error && <ErrorState error={roll.error} onRetry={() => { void roll.run(); void hist.run() }} />}

      {/* 'idle' is the no-gateway build: nothing live to draw, so keep the zero-state. */}
      {state === 'idle' && (
        <EmptyState dense title="No invoice activity yet" message="Counts appear once invoices are created." />
      )}

      {state === 'ready' && roll.data && (
        <DashboardTiles data={roll.data} ctx={ctx} history={hist.data?.history ?? null} />
      )}
    </div>
  )
}

// KPI tile values come off the live counts, so a tile never contradicts the donut beside it.
function kpiValues(counts: Counts, needsAttention: number, vatLabel: string, awaitingApproval: number) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0)
  const transmitted = counts.submitted + counts.accepted
  const awaiting = counts.draft + counts.validated
  return [
    { label: 'Invoices', value: String(total), delta: `${transmitted} transmitted`, deltaColor: 'var(--fg-3)', stroke: 'var(--action)' },
    { label: 'VAT tracked', value: vatLabel, delta: 'output VAT', deltaColor: 'var(--fg-3)', stroke: 'var(--action)' },
    { label: 'Exceptions', value: String(needsAttention), delta: needsAttention ? 'to resolve' : 'all clear', deltaColor: needsAttention ? 'var(--status-red-text)' : 'var(--status-green-text)', stroke: 'var(--status-red-text)' },
    { label: 'Not yet submitted', value: String(awaiting), delta: awaiting ? `${awaitingApproval} awaiting approval` : 'none waiting', deltaColor: awaiting ? 'var(--status-amber-text)' : 'var(--fg-3)', stroke: 'var(--status-amber-text)' },
  ]
}

// Every tile on this page — the four KPI tiles included — wears the same head: a
// .card-title on the left, optional mono meta on the right, cut off from
// the body by a full-bleed hairline. The hairline only reaches both edges if the
// CARD carries no padding — the head strip and the body each own theirs — hence
// padding:0 + overflow:hidden here and an explicit padded body inside every tile.
const TILE_CARD = {
  background: 'var(--bg-2)',
  border: '1px solid var(--line-1)',
  borderRadius: 'var(--radius-md)',
  overflow: 'hidden',
} as const

// Reserved sparkline row height: the tile does not grow when the history arrives.
const SPARK_H = 30

// Reserved trend body height (padding 42 + head + 14 + chart + 10 + months 14): the rows below do not move.
const TREND_CHART_H = 176
const TREND_HEAD_H = 32
const TREND_BODY_H = 42 + TREND_HEAD_H + 14 + TREND_CHART_H + 10 + 14

const TILE_BODY = { padding: '22px 20px 24px' } as const

// meta is optional: the four KPI tiles' head is the title alone. The strip keeps its height either way.
function TileHead({ title, meta }: { title: string; meta?: string }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 10,
        padding: '14px 20px',
        borderBottom: '1px solid var(--line-1)',
      }}
    >
      <span className="card-title">{title}</span>
      {meta && (
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
          {meta}
        </span>
      )}
    </div>
  )
}

function DashboardTiles({ data, ctx, history }: { data: Rollup; ctx: PlatformCtx; history: HistoryRow[] | null }) {
  // [dashboard-scope-per-client]: every LIVE number below is the SELECTED client's own
  // bucket (see the file-header comment) — never data.totals directly in firm mode.
  const bucket = scopedBucket(ctx.mode === 'inhouse', ctx.active.entityId, data)
  const segments = donutSegments(bucket.counts)
  const total = Object.values(bucket.counts).reduce((a, b) => a + b, 0)
  const anyValidated = total - bucket.counts.draft > 0 // drafts are the only pre-validation status
  const needsAttention = bucket.needs_attention
  const failures = topFailures(bucket.top_violations)
  const ring = readinessRing(bucket.metrics)
  const note = readinessNote(bucket.metrics)
  const bars = readinessBars(bucket.metrics)
  const kpis = kpiValues(bucket.counts, needsAttention, formatMetric(bucket.metrics, 'vat_tracked'), bucket.awaiting_approval)
  const { sparks, trend } = historyView(history, ctx.mode === 'inhouse', ctx.active.entityId, bucket, watToday(new Date()))

  return (
    <>
      {/* Row A: readiness ring + bars (live) | four KPI tiles (live values) */}
      <div
        className="pf-dash-row-a"
        style={{ display: 'grid', gridTemplateColumns: 'minmax(260px, 360px) minmax(0, 1fr)', gap: 18, marginBottom: 18 }}
      >
        <div style={{ ...TILE_CARD, display: 'flex', flexDirection: 'column' }}>
          <TileHead title="Readiness score" />
          <div style={{ ...TILE_BODY, flex: 1 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 22, marginBottom: 24 }}>
              <div style={{ position: 'relative', width: 116, height: 116, flex: 'none' }}>
                <svg width="116" height="116" viewBox="0 0 116 116" style={{ transform: 'rotate(-90deg)' }}>
                  <circle cx="58" cy="58" r="50" fill="none" stroke="var(--bg-3)" strokeWidth="11" />
                  <circle cx="58" cy="58" r="50" fill="none" stroke={ring.color} strokeWidth="11" strokeLinecap="round" strokeDasharray={ring.circ} strokeDashoffset={ring.offset} />
                </svg>
                <div style={{ position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
                  <span className="money" style={{ fontSize: 32, fontWeight: 700, lineHeight: 1 }}>
                    {ring.score ?? '—'}
                  </span>
                  <span className="mono" style={{ fontSize: 9, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 2 }}>
                    % READY
                  </span>
                </div>
              </div>
              <p style={{ flex: 1, fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>{note}</p>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 13, paddingTop: 20, borderTop: '1px solid var(--line-1)' }}>
              {bars.map((m) => (
                <div key={m.label}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 5 }}>
                    <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{m.label}</span>
                    <span className="money mono" style={{ fontSize: 12, fontWeight: 600, color: m.color }}>
                      {m.pctLabel}
                    </span>
                  </div>
                  <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden' }}>
                    <div style={{ width: m.pct === null ? '0%' : m.pctLabel, height: '100%', background: m.color, borderRadius: 'var(--radius-sm)' }} />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>

        <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(140px, 1fr))', gap: 18 }}>
          {kpis.map((k, i) => (
            <div key={k.label} style={{ ...TILE_CARD, display: 'flex', flexDirection: 'column', minHeight: 138, minWidth: 0 }}>
              <TileHead title={k.label} />
              <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'space-between', padding: '18px 20px 20px' }}>
                <span className="money" style={{ fontSize: 'clamp(22px, 3.2vw, 32px)', fontWeight: 700, margin: '0 0 12px', whiteSpace: 'nowrap' }}>
                  {k.value}
                </span>
                <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 12, minHeight: SPARK_H }}>
                  <span className="mono" style={{ fontSize: 12, fontWeight: 500, color: k.deltaColor }}>
                    {k.delta}
                  </span>
                  {sparks && (
                    <svg viewBox="0 0 88 30" height={SPARK_H} preserveAspectRatio="none" style={{ overflow: 'visible', flex: 1, width: '100%', minWidth: 0 }}>
                      <path d={sparks[i]} fill="none" stroke={k.stroke} strokeWidth="1.6" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
                    </svg>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Row B: exceptions-first needs-attention KPI + invoice-status donut.
          The narrow column is `calc((100% - 396px) / 2)`, not a fixed px: 396 = row A's
          readiness column (360) + its two 18px gaps, so this resolves to exactly one KPI
          tile and the gutter below lands under the gutter between the two KPI tiles. */}
      <div
        className="pf-dash-row-b"
        style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) calc((100% - 396px) / 2)', gap: 18, marginBottom: 18 }}
      >
        <div style={{ ...TILE_CARD, display: 'flex', flexDirection: 'column' }}>
          <TileHead title="Needs attention" meta="EXCEPTIONS FIRST" />
          <div style={{ ...TILE_BODY, flex: 1 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 16, marginBottom: 14, flexWrap: 'wrap' }}>
              <span className="money" style={{ fontSize: 56, fontWeight: 700, lineHeight: 1, color: 'var(--ink)' }}>
                {needsAttention}
              </span>
              <span
                className="mono"
                style={{
                  fontSize: 10,
                  fontWeight: 600,
                  letterSpacing: '0.04em',
                  padding: '3px 9px',
                  borderRadius: 'var(--radius-sm)',
                  background: needsAttention > 0 ? 'var(--status-red-bg)' : 'var(--status-green-bg)',
                  border: `1px solid ${needsAttention > 0 ? 'var(--status-red-border)' : 'var(--status-green-border)'}`,
                  color: needsAttention > 0 ? 'var(--status-red-text)' : 'var(--status-green-text)',
                }}
              >
                {needsAttention > 0 ? 'REJECTED / FAILED / BLOCKED / SENT BACK' : 'ALL CLEAR'}
              </span>
            </div>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: '0 0 20px', maxWidth: 520 }}>
              Invoices rejected, failed, blocked by an error-severity validation issue, or sent back by an approver.
            </p>
            <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38, fontSize: 13 }}>
              {resolveCtaLabel(needsAttention)}
            </button>
          </div>
        </div>

        <div style={TILE_CARD}>
          <TileHead title="Invoice status" meta={`${total} TOTAL`} />
          <div style={TILE_BODY}>
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
              <div style={{ position: 'relative', width: 128, height: 128 }}>
                {/* Arc dash/offset in donutSegments are computed for R=49 — the circle
                    r is hardcoded to 49 to match (donutMeta is gone). */}
                <svg width="124" height="124" viewBox="0 0 124 124" style={{ transform: 'rotate(-90deg)' }}>
                  <circle cx="62" cy="62" r="49" fill="none" stroke="var(--bg-3)" strokeWidth="13" />
                  {segments.map((d) => (
                    <circle key={d.label} cx="62" cy="62" r="49" fill="none" stroke={d.color} strokeWidth="13" strokeDasharray={d.dash} strokeDashoffset={d.offset} />
                  ))}
                </svg>
                <div style={{ position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
                  <span className="money" style={{ fontSize: 22, fontWeight: 700, lineHeight: 1 }}>
                    {total}
                  </span>
                  <span className="mono" style={{ fontSize: 9, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 2 }}>
                    DOCS
                  </span>
                </div>
              </div>
              <div style={{ width: '100%', marginTop: 22, display: 'flex', flexDirection: 'column', gap: 9 }}>
                {segments.map((d) => (
                  <div key={d.label} style={{ display: 'grid', gridTemplateColumns: '10px minmax(0, 1fr) auto auto', alignItems: 'center', gap: 8 }}>
                    <span style={{ width: 10, height: 10, borderRadius: 2, background: d.color }} />
                    <span style={{ fontSize: 13, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{d.label}</span>
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', textAlign: 'right' }}>
                      {d.pct}
                    </span>
                    <span className="money" style={{ fontSize: 13, fontWeight: 600, textAlign: 'right', minWidth: 18 }}>
                      {d.count}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* Row C: readiness trend from recorded days; under two of them (or no live score) the empty state. */}
      <div style={{ ...TILE_CARD, marginBottom: 18 }}>
        <TileHead title="Readiness trend" meta={ring.score !== null && trend ? trend.meta : '12 WEEKS'} />
        {ring.score === null ? (
          <div style={{ padding: '20px 20px 22px' }}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10 }}>
              <span className="money" style={{ fontSize: 26, fontWeight: 700 }}>
                —
              </span>
              <span style={{ fontSize: 13, color: 'var(--fg-3)' }}>No invoices yet</span>
            </div>
          </div>
        ) : trend ? (
          <div style={{ padding: '20px 20px 22px' }}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 14, height: TREND_HEAD_H }}>
              <span className="money" style={{ fontSize: 26, fontWeight: 700 }}>
                {trend.now}%
              </span>
              <span className="label">{trend.deltaLabel}</span>
            </div>
            <svg viewBox="0 0 680 176" width="100%" height={TREND_CHART_H} preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }}>
              {trend.grid.map((g) => (
                <line key={g} x1="0" y1={g} x2="680" y2={g} stroke="var(--line-1)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
              ))}
              <path d={trend.area} fill="var(--action-tint)" />
              <path d={trend.line} fill="none" stroke="var(--action)" strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
            <div style={{ position: 'relative', height: 14, marginTop: 10 }}>
              {trend.months.map((mo) => (
                <span
                  key={mo.label + mo.at}
                  className="mono"
                  style={{ position: 'absolute', ...(mo.at > 0.9 ? { right: 0 } : { left: `${mo.at * 100}%` }), fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}
                >
                  {mo.label}
                </span>
              ))}
            </div>
          </div>
        ) : (
          <div style={{ padding: '40px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', textAlign: 'center', minHeight: TREND_BODY_H }}>
            <div className="card-title" style={{ marginBottom: 3 }}>No history yet</div>
            <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>The trend starts on the first day this workspace records.</div>
          </div>
        )}
      </div>

      {/* Row D: top validation failures (live) | recent activity. Same
          `calc((100% - 396px) / 2)` narrow column as row B — see there for the 396. */}
      <div className="pf-dash-row-c" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) calc((100% - 396px) / 2)', gap: 18 }}>
      <div style={TILE_CARD}>
        <TileHead title="Top validation failures" />
        {failures.length > 0 ? (
          <div>
            {failures.map((f) => (
              <div key={f.ruleKey} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
                <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'grid', placeItems: 'center' }}>{crossGlyph}</span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 6 }}>{f.label}</div>
                  <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden', maxWidth: 240 }}>
                    <div style={{ width: f.bar, height: '100%', background: 'var(--action)', borderRadius: 'var(--radius-sm)' }} />
                  </div>
                </div>
                <span className="mono" title={f.ruleKey} style={{ fontSize: 11, color: 'var(--fg-3)', flex: 'none', width: 150, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {ruleKeyLabel(f.ruleKey)}
                </span>
                <div style={{ textAlign: 'right', flex: 'none', minWidth: 40, whiteSpace: 'nowrap' }}>
                  <span className="money" style={{ fontSize: 16, fontWeight: 700, color: 'var(--status-red-text)' }}>
                    {f.count}
                  </span>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div style={{ padding: '40px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
            {anyValidated && (
              <span style={{ width: 40, height: 40, borderRadius: '50%', background: 'var(--status-green-bg)', color: 'var(--status-green-text)', display: 'grid', placeItems: 'center', marginBottom: 12 }}>{tickGlyph13}</span>
            )}
            <div className="card-title" style={{ marginBottom: 3 }}>{anyValidated ? 'No open failures' : 'No invoices validated yet'}</div>
            <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>{anyValidated ? 'Every invoice passed validation.' : 'Failures appear here once invoices are validated.'}</div>
          </div>
        )}
      </div>

        <div style={TILE_CARD}>
          <TileHead title="Recent activity" />
          <RecentActivityBody key={ctx.mode === 'inhouse' ? 'inhouse' : `firm:${ctx.active.entityId ?? ''}`} ctx={ctx} />
        </div>
      </div>
    </>
  )
}
