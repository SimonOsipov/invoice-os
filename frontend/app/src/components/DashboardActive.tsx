// Dashboard — active client (score ring, KPIs w/ sparklines, readiness trend chart,
// status donut, top validation failures, recent activity). Ported from
// Platform.dc.html ~L147-310. Reads the client's precomputed `dash` (see lib/clients.ts).

import { crossGlyph, tickGlyph13 } from '../glyphs'
import type { PlatformCtx } from '../types'

export function DashboardActive({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx
  const dash = active.dash!

  return (
    <div style={{ padding: '30px 36px 56px', maxWidth: 1680, margin: '0 auto' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 26, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 10 }}>
            / COMPLIANCE OVERVIEW
          </div>
          <h1 style={{ fontSize: 28, fontWeight: 600, letterSpacing: '-0.03em', margin: '0 0 5px' }}>{active.name}</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Period to date · June 2026</p>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8 }}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7, background: dash.pill.bg, border: `1px solid ${dash.pill.border}`, borderRadius: 999, padding: '5px 12px' }}>
            <span style={{ width: 7, height: 7, borderRadius: 99, background: dash.pill.text }} />
            <span className="mono" style={{ fontSize: 11, fontWeight: 600, color: dash.pill.text }}>
              {dash.pill.label}
            </span>
          </span>
          <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
            SYNCED 2 MIN AGO · ERP CONNECTED
          </span>
        </div>
      </div>

      {/* Row A: readiness + KPIs */}
      <div className="pf-dash-row-a" style={{ display: 'grid', gridTemplateColumns: 'minmax(320px, 360px) minmax(0, 1fr)', gap: 18, marginBottom: 18 }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: 26, display: 'flex', flexDirection: 'column' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
            <span className="label">Readiness score</span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>
              UPDATED 2 MIN AGO
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 22, marginBottom: 24 }}>
            <div style={{ position: 'relative', width: 116, height: 116, flex: 'none' }}>
              <svg width="116" height="116" viewBox="0 0 116 116" style={{ transform: 'rotate(-90deg)' }}>
                <circle cx="58" cy="58" r="50" fill="none" stroke="var(--bg-3)" strokeWidth="11" />
                <circle cx="58" cy="58" r="50" fill="none" stroke={dash.ring.color} strokeWidth="11" strokeLinecap="round" strokeDasharray={dash.ring.circ} strokeDashoffset={dash.ring.offset} />
              </svg>
              <div style={{ position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
                <span className="money" style={{ fontSize: 32, fontWeight: 700, lineHeight: 1 }}>
                  {dash.score}
                </span>
                <span className="mono" style={{ fontSize: 9, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 2 }}>
                  % READY
                </span>
              </div>
            </div>
            <p style={{ flex: 1, fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>{dash.readinessNote}</p>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 13, paddingTop: 20, borderTop: '1px solid var(--line-1)' }}>
            {dash.readinessMetrics.map((m) => (
              <div key={m.label}>
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 5 }}>
                  <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{m.label}</span>
                  <span className="money mono" style={{ fontSize: 12, fontWeight: 600, color: m.color }}>
                    {m.pct}
                  </span>
                </div>
                <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden' }}>
                  <div style={{ width: m.pct, height: '100%', background: m.color, borderRadius: 3 }} />
                </div>
              </div>
            ))}
          </div>
          <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38, fontSize: 13, marginTop: 22, justifyContent: 'center' }}>
            {dash.resolveLabel}
          </button>
        </div>

        <div className="pf-grid-2" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 18 }}>
          {dash.kpis.map((k) => (
            <div key={k.label} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: 22, display: 'flex', flexDirection: 'column', justifyContent: 'space-between', minHeight: 138, minWidth: 0 }}>
              <span className="label">{k.label}</span>
              <span className="money" style={{ fontSize: 32, fontWeight: 700, margin: '12px 0' }}>
                {k.value}
              </span>
              <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 12 }}>
                <span className="mono" style={{ fontSize: 12, fontWeight: 500, color: k.deltaColor }}>
                  {k.delta}
                </span>
                <svg viewBox="0 0 88 30" width="88" height="30" preserveAspectRatio="none" style={{ overflow: 'visible', flex: 'none' }}>
                  <path d={k.spark} fill="none" stroke={k.stroke} strokeWidth="1.6" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
                </svg>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Row B: throughput chart + donut */}
      <div className="pf-dash-row-b" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 340px', gap: 18, marginBottom: 18 }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 14 }}>
            <div>
              <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 8 }}>Readiness trend</div>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                <span className="money" style={{ fontSize: 26, fontWeight: 700 }}>
                  {dash.chart.now}%
                </span>
                <span className="label">{dash.chart.deltaLabel}</span>
              </div>
            </div>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              12 WEEKS
            </span>
          </div>
          <svg viewBox="0 0 680 176" width="100%" height="176" preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }}>
            {dash.chart.grid.map((g) => (
              <line key={g} x1="0" y1={g} x2="680" y2={g} stroke="var(--line-1)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
            ))}
            <path d={dash.chart.area} fill="var(--accent-tint)" />
            <path d={dash.chart.line} fill="none" stroke="var(--accent)" strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 10 }}>
            {dash.chart.months.map((mo) => (
              <span key={mo} className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
                {mo}
              </span>
            ))}
          </div>
        </div>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Invoice status</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {dash.donutMeta.total} TOTAL
            </span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
            <div style={{ position: 'relative', width: 128, height: 128 }}>
              <svg width="124" height="124" viewBox="0 0 124 124" style={{ transform: 'rotate(-90deg)' }}>
                <circle cx="62" cy="62" r={dash.donutMeta.r} fill="none" stroke="var(--bg-3)" strokeWidth="13" />
                {dash.donut.map((d) => (
                  <circle key={d.label} cx="62" cy="62" r={dash.donutMeta.r} fill="none" stroke={d.color} strokeWidth="13" strokeDasharray={d.dash} strokeDashoffset={d.offset} />
                ))}
              </svg>
              <div style={{ position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
                <span className="money" style={{ fontSize: 22, fontWeight: 700, lineHeight: 1 }}>
                  {dash.donutMeta.total}
                </span>
                <span className="mono" style={{ fontSize: 9, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 2 }}>
                  DOCS
                </span>
              </div>
            </div>
            <div style={{ width: '100%', marginTop: 22, display: 'flex', flexDirection: 'column', gap: 11 }}>
              {dash.donut.map((d) => (
                <div key={d.label} style={{ display: 'grid', gridTemplateColumns: '12px 1fr auto 40px', alignItems: 'center', gap: 10 }}>
                  <span style={{ width: 10, height: 10, borderRadius: 2, background: d.color }} />
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

      {/* Row C: failures + activity */}
      <div className="pf-dash-row-c" style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.5fr) minmax(0, 1fr)', gap: 18 }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '15px 20px', borderBottom: '1px solid var(--line-1)' }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Top validation failures</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              LAST 30 DAYS
            </span>
          </div>
          {dash.hasFailures && (
            <div>
              {dash.failures.map((f) => (
                <div key={f.rule} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 6, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'grid', placeItems: 'center' }}>{crossGlyph}</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 6 }}>{f.label}</div>
                    <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden', maxWidth: 240 }}>
                      <div style={{ width: f.bar, height: '100%', background: 'var(--status-red-text)', opacity: 0.55, borderRadius: 3 }} />
                    </div>
                  </div>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', flex: 'none', width: 96 }}>
                    {f.rule}
                  </span>
                  <div style={{ textAlign: 'right', flex: 'none', width: 54 }}>
                    <span className="money" style={{ fontSize: 16, fontWeight: 700, color: 'var(--status-red-text)' }}>
                      {f.count}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
          {dash.noFailures && (
            <div style={{ padding: '40px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
              <span style={{ width: 40, height: 40, borderRadius: 99, background: 'var(--status-green-bg)', color: 'var(--status-green-text)', display: 'grid', placeItems: 'center', marginBottom: 12 }}>{tickGlyph13}</span>
              <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 3 }}>No open failures</div>
              <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>Every invoice in the period passed validation.</div>
            </div>
          )}
        </div>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
          <div style={{ padding: '15px 20px', borderBottom: '1px solid var(--line-1)' }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Recent activity</span>
          </div>
          <div style={{ padding: '18px 20px 6px' }}>
            {dash.activity.map((a, i) => (
              <div key={i} style={{ display: 'flex', gap: 12 }}>
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
                  <span style={{ width: 8, height: 8, borderRadius: 99, background: a.dot, marginTop: 4 }} />
                  <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: a.line }} />
                </div>
                <div style={{ paddingBottom: 16, flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, lineHeight: 1.4 }}>
                    <span style={{ fontWeight: 600 }}>{a.who}</span> <span style={{ color: 'var(--fg-2)' }}>{a.action}</span>{' '}
                    <span className="mono" style={{ fontSize: 12, color: 'var(--accent)' }}>
                      {a.target}
                    </span>
                  </div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                    {a.time}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
