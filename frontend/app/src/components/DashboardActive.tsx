// Dashboard — firm-wide compliance overview. Reads the M4-07 rollup
// (GET /api/dashboard/v1/rollup) via lib/dashboard.ts and renders honest
// loading / error / empty / ready states — no mock generator behind it.
// Structurally mirrors ClientsView.tsx (typed API module + useAsync +
// no-gateway short-circuit). Ported donut + failures markup from the old
// mock dashboard (Platform.dc.html ~L147-310); the readiness ring, KPI
// sparklines, 12-week trend, VAT KPI, and activity feed had no live source
// and were removed (M4-10 AC-5 / [hide-sourceless]).

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { crossGlyph, tickGlyph13 } from '../glyphs'
import {
  dashboardViewState,
  donutSegments,
  getRollup,
  isEmptyRollup,
  resolveCtaLabel,
  topFailures,
  type Rollup,
} from '../lib/dashboard'
import type { PlatformCtx } from '../types'

export function DashboardActive({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  // base ? … : … narrowing (not a base! assertion) keeps the producer well-typed
  // without trusting a non-null base; immediate: base != null keeps the no-gateway
  // build at zero network. Mirrors ClientsView.tsx:38-41.
  const roll = useAsync<Rollup>(
    () => (base ? getRollup(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null, isEmpty: isEmptyRollup },
  )
  const state = dashboardViewState(base, roll)

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      {/* Firm-wide header — rebound to tenant context ([header-chrome-firmwide]);
          the mock taxpayer pill, "SYNCED …", and "Period to date" chrome are gone. */}
      <div style={{ marginBottom: 26 }}>
        <div className="label" style={{ marginBottom: 10 }}>
          / COMPLIANCE OVERVIEW
        </div>
        <h1 style={{ fontSize: 28, fontWeight: 600, letterSpacing: '-0.03em', margin: '0 0 5px' }}>
          {ctx.user.tenantName ?? 'Your firm'}
        </h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Firm-wide invoice compliance</p>
      </div>

      {state === 'loading' && <Loading label="Loading dashboard…" />}

      {state === 'error' && roll.error && <ErrorState error={roll.error} onRetry={roll.run} />}

      {(state === 'idle' || state === 'empty') && (
        <EmptyState title="No invoice activity yet" message="Counts appear once invoices are created." />
      )}

      {state === 'ready' && roll.data && <DashboardTiles data={roll.data} ctx={ctx} />}
    </div>
  )
}

function DashboardTiles({ data, ctx }: { data: Rollup; ctx: PlatformCtx }) {
  const segments = donutSegments(data.totals.counts)
  const total = Object.values(data.totals.counts).reduce((a, b) => a + b, 0)
  const needsAttention = data.totals.needs_attention
  const failures = topFailures(data.top_violations)

  return (
    <>
      {/* Row 1: exceptions-first needs-attention KPI + invoice-status donut */}
      <div
        className="pf-dash-row-b"
        style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 360px', gap: 18, marginBottom: 18 }}
      >
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', padding: 26, display: 'flex', flexDirection: 'column' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
            <span className="label">Needs attention</span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
              EXCEPTIONS FIRST
            </span>
          </div>
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center' }}>
            <span
              className="money"
              style={{ fontSize: 56, fontWeight: 700, lineHeight: 1, color: needsAttention > 0 ? 'var(--status-red-text)' : 'var(--status-green-text)' }}
            >
              {needsAttention}
            </span>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: '14px 0 0' }}>
              Invoices rejected, failed, or blocked by an error-severity validation issue.
            </p>
          </div>
          <button
            onClick={() => ctx.nav('invoices')}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ height: 38, fontSize: 13, marginTop: 22, justifyContent: 'center' }}
          >
            {resolveCtaLabel(needsAttention)}
          </button>
        </div>

        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Invoice status</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {total} TOTAL
            </span>
          </div>
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
            <div style={{ width: '100%', marginTop: 22, display: 'flex', flexDirection: 'column', gap: 11 }}>
              {segments.map((d) => (
                <div key={d.label} style={{ display: 'grid', gridTemplateColumns: '12px 1fr auto 40px', alignItems: 'center', gap: 10 }}>
                  <span style={{ width: 10, height: 10, borderRadius: 'var(--radius-xs)', background: d.color }} />
                  <span style={{ fontSize: 13, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{d.label}</span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', textAlign: 'right' }}>
                    {d.pct}
                  </span>
                  <span className="money" style={{ fontSize: 13, fontWeight: 600, textAlign: 'right' }}>
                    {d.count}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>

      {/* Row 2: top validation failures (firm-wide, de-slugged rule keys) */}
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '15px 20px', borderBottom: '1px solid var(--line-1)' }}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Top validation failures</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            FIRM-WIDE
          </span>
        </div>
        {failures.length > 0 ? (
          <div>
            {failures.map((f) => (
              <div key={f.ruleKey} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
                <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-lg)', background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'grid', placeItems: 'center' }}>{crossGlyph}</span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 6 }}>{f.label}</div>
                  <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden', maxWidth: 240 }}>
                    <div style={{ width: f.bar, height: '100%', background: 'var(--status-red-text)', opacity: 0.55, borderRadius: 'var(--radius-sm)' }} />
                  </div>
                </div>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', flex: 'none', width: 96 }}>
                  {f.ruleKey}
                </span>
                <div style={{ textAlign: 'right', flex: 'none', width: 54 }}>
                  <span className="money" style={{ fontSize: 16, fontWeight: 700, color: 'var(--status-red-text)' }}>
                    {f.count}
                  </span>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div style={{ padding: '40px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
            <span style={{ width: 40, height: 40, borderRadius: 99, background: 'var(--status-green-bg)', color: 'var(--status-green-text)', display: 'grid', placeItems: 'center', marginBottom: 12 }}>{tickGlyph13}</span>
            <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 3 }}>No open failures</div>
            <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>Every invoice passed validation.</div>
          </div>
        )}
      </div>
    </>
  )
}
