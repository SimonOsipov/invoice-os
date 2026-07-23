// API status — the client-facing status page (prototype lines 527–583). It replaces
// the operator-only Health screen, and deliberately surfaces none of that screen's
// internal-plumbing metrics: the prototype range carries only component health,
// latency, uptime and incident copy, so porting it faithfully satisfies that
// constraint by construction. (The metric names themselves are kept out of this file
// so the audit grep that proves the constraint stays free of comment false-positives —
// the same trap the SVG-attribute grep hit earlier in this story.)
//
// EVERY figure on this screen is a literal ported from the prototype seed block
// (proto:1044–1058), never a derivation. Two of them are near-misses for a
// computation and must stay literals:
//   • the banner's `99.98%` (proto:545) is one rounding step away from the mean of
//     the six component uptimes (599.85 / 6 = 99.975), and
//   • `5 of 6 components operational` (proto:543) is one filter away from
//     STATUS_COMPONENTS.
// Computing either would let the copy drift from the design the moment a seed changes.
// Same rule, same reason as the rate-limit `68%` in data.tsx.
//
// The 90-cell uptime strips come from `upStrip` (charts.ts, unit-tested, seeded and so
// deterministic). Their colours land as an inline `background` on a plain <div>, where a
// CSS custom property resolves normally — this screen has no SVG, and so is not
// exposed to the presentation-attribute hazard that a custom property in an SVG paint
// attribute would create.

import { ALERT_ICON, INCIDENTS, STATUS_COMPONENTS, STATUS_TONE, UPDATED_AGO } from '../data'

export function Status() {
  return (
    <div className="ops-screen-pad">
      {/* header (proto:530-536) */}
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 06 — API STATUS
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>API status</h1>
        </div>
        <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
          LIVE · REFRESHED {UPDATED_AGO}
        </span>
      </div>

      {/* overall banner (proto:539-546). Both figures below are literals — see the file header. */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 14,
          background: 'var(--status-amber-bg)',
          border: '1px solid var(--status-amber-border)',
          borderRadius: 'var(--radius-xl)',
          padding: '16px 20px',
          marginBottom: 16,
        }}
      >
        <span
          style={{
            flex: 'none',
            width: 40,
            height: 40,
            borderRadius: 'var(--radius-xl)',
            background: 'var(--status-amber-text)',
            color: '#fff',
            display: 'grid',
            placeItems: 'center',
          }}
        >
          {ALERT_ICON}
        </span>
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--status-amber-text)' }}>
            Partial degradation — tax-authority latency elevated
          </div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--status-amber-text)', opacity: 0.85, marginTop: 2 }}>
            5 of 6 components operational · clearance times above target
          </div>
        </div>
        <div style={{ textAlign: 'right' }}>
          <div className="mono" style={{ fontSize: 22, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--status-amber-text)' }}>
            99.98%
          </div>
          <div className="label" style={{ marginTop: 2, color: 'var(--status-amber-text)' }}>
            90-DAY UPTIME
          </div>
        </div>
      </div>

      {/* components (proto:549-563) */}
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', background: 'var(--bg-2)', overflow: 'hidden', marginBottom: 24 }}>
        {STATUS_COMPONENTS.map((c) => {
          const tone = STATUS_TONE[c.tone]
          return (
            // Keyed on `name`: the six are pairwise distinct, and unlike an index the key
            // survives a reorder of the seed list.
            <div key={c.name} style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
                <span style={{ flex: 1, fontSize: 13.5, fontWeight: 600 }}>{c.name}</span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {c.latency}
                </span>
                <span
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    background: tone.bg,
                    border: '1px solid ' + tone.border,
                    borderRadius: 999,
                    padding: '3px 10px',
                  }}
                >
                  <span style={{ width: 7, height: 7, borderRadius: 99, background: tone.text }} />
                  <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
                    {c.status}
                  </span>
                </span>
              </div>
              {/* 90-day uptime strip (proto:557-559). A flex row of 90 flex:1 cells, NOT a
                  grid and NOT a table: the cells are empty, so their min-content width is 0
                  and the row shrinks with the viewport instead of overflowing. It therefore
                  takes no minWidth — adding one is what would introduce a horizontal
                  scrollbar at the narrow breakpoints. */}
              <div style={{ display: 'flex', gap: 1.5, height: 26, alignItems: 'stretch' }}>
                {c.strip.map((d, i) => (
                  // Index key. A cell carries only `fill`, drawn from three values across 90
                  // cells, so there is no natural key to use. Index keys are unsafe only when
                  // a list reorders, filters or splices; this one is a fixed 90-element array
                  // built once at seed time and never mutated. Precedent: JobDrawer.tsx.
                  <div key={i} style={{ flex: 1, background: d.fill, borderRadius: 1 }} />
                ))}
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}>
                <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-4)' }}>
                  90 days ago
                </span>
                <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-4)' }}>
                  {c.uptime} uptime
                </span>
              </div>
            </div>
          )
        })}
      </div>

      {/* incident history (proto:566-577) */}
      <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 12 }}>Incident history</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
        {INCIDENTS.map((inc) => {
          const tone = STATUS_TONE[inc.tone]
          return (
            // `inc`, not the prototype's `i` (proto:568) — `i` is the strip's index
            // variable above and reads as one everywhere else in this package.
            // `.ops-incident-row` carries the 70px/1fr track pair; the 1fr column holds
            // wrapping prose, so it needs no minWidth and no breakpoint reflow.
            <div key={inc.date} className="ops-incident-row" style={{ padding: '16px 0', borderBottom: '1px solid var(--line-1)' }}>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', paddingTop: 2 }}>
                {inc.date}
              </span>
              <div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 9, marginBottom: 4 }}>
                  <span style={{ fontSize: 13.5, fontWeight: 600 }}>{inc.title}</span>
                  <span
                    className="mono"
                    style={{
                      fontSize: 9,
                      fontWeight: 700,
                      color: tone.text,
                      background: tone.bg,
                      border: '1px solid ' + tone.border,
                      borderRadius: 'var(--radius-md)',
                      padding: '2px 7px',
                    }}
                  >
                    {inc.status}
                  </span>
                </div>
                <div style={{ fontSize: 12.5, color: 'var(--fg-2)', lineHeight: 1.5 }}>{inc.detail}</div>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
