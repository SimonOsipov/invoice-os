// Dashboard — onboarding client (empty state + getting-started checklist).
// Ported from Platform.dc.html ~L312-341.

import { ONBOARD_STEPS } from '../data'
import { importGlyph, plusGlyph, rocketGlyph, tickGlyph11 } from '../glyphs'
import type { PlatformCtx } from '../types'

export function DashboardOnboarding({ ctx }: { ctx: PlatformCtx }) {
  const { active } = ctx

  return (
    <div style={{ padding: '30px 36px 56px', maxWidth: 1280 }}>
      <div style={{ marginBottom: 26 }}>
        <div className="label" style={{ marginBottom: 10 }}>
          / COMPLIANCE OVERVIEW
        </div>
        <h1 style={{ fontSize: 28, fontWeight: 600, letterSpacing: '-0.03em', margin: '0 0 5px' }}>{active.name}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Newly added · onboarding in progress</p>
      </div>
      <div className="pf-onboard-grid" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: 48, display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 48, alignItems: 'center' }}>
        <div>
          <div style={{ width: 48, height: 48, borderRadius: 10, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', marginBottom: 20 }}>{rocketGlyph}</div>
          <h2 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.02em', margin: '0 0 10px' }}>Let's get {active.short} compliance-ready</h2>
          <p style={{ fontSize: 15, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 24px' }}>
            No invoices yet. Connect a system or import a file, and the validation engine will start scoring readiness against the Nigeria MBS rule pack.
          </p>
          <div style={{ display: 'flex', gap: 12 }}>
            <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
              <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Create first invoice
            </button>
            <button className="v2-btn v2-btn-ghost pf-btn">
              <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Import CSV
            </button>
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 1, background: 'var(--line-1)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          {ONBOARD_STEPS.map((s) => (
            <div key={s.n} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '16px 18px', background: 'var(--bg-2)' }}>
              <span
                style={{
                  flex: 'none',
                  width: 26,
                  height: 26,
                  borderRadius: 99,
                  border: `1px solid ${s.done ? 'var(--accent)' : 'var(--line-2)'}`,
                  background: s.done ? 'var(--accent)' : 'var(--bg-2)',
                  color: s.done ? '#fff' : 'var(--fg-3)',
                  display: 'grid',
                  placeItems: 'center',
                  fontFamily: 'var(--font-mono)',
                  fontSize: 11,
                  fontWeight: 600,
                }}
              >
                {s.n}
              </span>
              <div style={{ flex: 1 }}>
                <div style={{ fontSize: 13.5, fontWeight: 500 }}>{s.title}</div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)' }}>{s.body}</div>
              </div>
              <span style={{ color: 'var(--accent)' }}>{s.done ? tickGlyph11 : ''}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
