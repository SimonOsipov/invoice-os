import { useState } from 'react'
import type { CSSProperties } from 'react'
import { APPROVALS, CLIENTS, FIRM, INHOUSE, PIPELINE, type Audience as AudienceData } from '../data'

type AudienceKey = 'firm' | 'inhouse'

const seg = (active: boolean) => ({
  bg: active ? 'var(--bg-2)' : 'transparent',
  fg: active ? 'var(--fg-1)' : 'var(--fg-3)',
})

const tabStyle = (active: boolean): CSSProperties => ({
  border: 0,
  cursor: 'pointer',
  height: 38,
  padding: '0 18px',
  borderRadius: 999,
  fontSize: 13,
  fontWeight: 500,
  fontFamily: 'var(--font-sans)',
  background: seg(active).bg,
  color: seg(active).fg,
  display: 'inline-flex',
  alignItems: 'center',
  gap: 8,
  transition: 'background 160ms ease-out, color 160ms ease-out',
})

// Both audience variants are rendered stacked in the same grid cell (all layers at
// row 1 / col 1); only the active one is visible. `visibility: hidden` keeps the
// inactive layer laid out, so every cell permanently reserves the height of its taller
// variant — toggling swaps content without reflowing the page (no layout shift / jump).
const stackCell: CSSProperties = { display: 'grid' }
const layer = (visible: boolean): CSSProperties => ({
  gridArea: '1 / 1',
  visibility: visible ? 'visible' : 'hidden',
})

function FirmMock() {
  return (
    <div style={{ background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, overflow: 'hidden' }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '14px 18px',
          borderBottom: '1px solid var(--line-1)',
        }}
      >
        <span className="label">Client portfolio · 6 companies</span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--accent)', fontWeight: 600 }}>
          + ADD CLIENT
        </span>
      </div>
      {CLIENTS.map((c) => (
        <div
          key={c.name}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 14,
            padding: '13px 18px',
            borderBottom: '1px solid var(--line-1)',
            background: 'var(--bg-2)',
          }}
        >
          <span
            style={{
              flex: 'none',
              width: 30,
              height: 30,
              borderRadius: 5,
              background: 'var(--accent-tint)',
              color: 'var(--accent)',
              display: 'grid',
              placeItems: 'center',
              fontSize: 12,
              fontWeight: 700,
            }}
          >
            {c.initials}
          </span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 14, fontWeight: 500 }}>{c.name}</div>
            <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {c.tin}
            </div>
          </div>
          <div style={{ textAlign: 'right' }}>
            <div className="mono" style={{ fontSize: 13, fontWeight: 600 }}>
              {c.score}
            </div>
            <div className="label" style={{ marginTop: 1 }}>
              ready
            </div>
          </div>
          <span
            style={{
              flex: 'none',
              display: 'inline-flex',
              alignItems: 'center',
              gap: 5,
              background: c.statusBg,
              border: `1px solid ${c.statusBorder}`,
              borderRadius: 999,
              padding: '3px 9px',
            }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 99, background: c.statusText }} />
            <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: c.statusText, letterSpacing: '0.04em' }}>
              {c.status}
            </span>
          </span>
        </div>
      ))}
    </div>
  )
}

function InhouseMock() {
  return (
    <div style={{ background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, overflow: 'hidden' }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '14px 18px',
          borderBottom: '1px solid var(--line-1)',
        }}
      >
        <span className="label">Acme Manufacturing Plc · finance workspace</span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
          JUN 2026
        </span>
      </div>
      {/* approval pipeline strip */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', borderBottom: '1px solid var(--line-1)' }}>
        {PIPELINE.map((p) => (
          <div key={p.stage} style={{ padding: '14px 16px', borderRight: '1px solid var(--line-1)' }}>
            <div className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.02em', color: p.color }}>
              {p.count}
            </div>
            <div className="label" style={{ marginTop: 3 }}>
              {p.stage}
            </div>
          </div>
        ))}
      </div>
      {/* approval queue */}
      {APPROVALS.map((a) => (
        <div
          key={a.id}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 14,
            padding: '13px 18px',
            borderBottom: '1px solid var(--line-1)',
            background: 'var(--bg-2)',
          }}
        >
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="mono" style={{ fontSize: 13, fontWeight: 600 }}>
              {a.id}
            </div>
            <div style={{ fontSize: 12, color: 'var(--fg-3)' }}>{a.party}</div>
          </div>
          <div className="mono" style={{ fontSize: 13, fontWeight: 600 }}>
            {a.amount}
          </div>
          <span
            style={{
              flex: 'none',
              width: 26,
              height: 26,
              borderRadius: 99,
              background: 'var(--accent-tint)',
              color: 'var(--accent)',
              display: 'grid',
              placeItems: 'center',
              fontSize: 10,
              fontWeight: 700,
            }}
            title={a.assignee}
          >
            {a.who}
          </span>
          <span
            style={{
              flex: 'none',
              display: 'inline-flex',
              alignItems: 'center',
              gap: 5,
              background: a.statusBg,
              border: `1px solid ${a.statusBorder}`,
              borderRadius: 999,
              padding: '3px 9px',
            }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 99, background: a.statusText }} />
            <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: a.statusText, letterSpacing: '0.04em' }}>
              {a.stage}
            </span>
          </span>
        </div>
      ))}
    </div>
  )
}

// Right-hand copy column for one audience — extracted so both variants can be rendered
// stacked (see `stackCell`/`layer`) and the cell reserves the taller one's height.
function AudienceCopy({ data, onBookDemo }: { data: AudienceData; onBookDemo: () => void }) {
  return (
    <div>
      <h3 style={{ fontSize: 32, lineHeight: 1.12, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 16px' }}>
        {data.headline}
      </h3>
      <p style={{ fontSize: 16, lineHeight: 1.65, color: 'var(--fg-2)', margin: '0 0 26px' }}>{data.body}</p>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14, marginBottom: 28 }}>
        {data.features.map((f) => (
          <div key={f.title} style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
            <span
              style={{
                flex: 'none',
                width: 24,
                height: 24,
                borderRadius: 4,
                background: 'var(--accent-tint)',
                color: 'var(--accent)',
                display: 'grid',
                placeItems: 'center',
                marginTop: 1,
              }}
            >
              {f.glyph}
            </span>
            <div>
              <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 1 }}>{f.title}</div>
              <div style={{ fontSize: 13, color: 'var(--fg-3)', lineHeight: 1.5 }}>{f.body}</div>
            </div>
          </div>
        ))}
      </div>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 30 }}>
        {data.stats.map((st) => (
          <div key={st.label} style={{ flex: 1, minWidth: 150, border: '1px solid var(--line-1)', borderRadius: 6, padding: '16px 18px' }}>
            <div className="mono" style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.02em', color: st.color }}>
              {st.value}
            </div>
            <div style={{ fontSize: 13, color: 'var(--fg-3)', marginTop: 4 }}>{st.label}</div>
          </div>
        ))}
      </div>
      <button onClick={onBookDemo} className="v2-btn v2-btn-primary" style={{ cursor: 'pointer' }}>
        {data.cta}
      </button>
    </div>
  )
}

export function Audience({ onBookDemo }: { onBookDemo: () => void }) {
  const [audience, setAudience] = useState<AudienceKey>('firm')
  const isFirm = audience === 'firm'

  return (
    <section id="accountants" style={{ borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div style={{ textAlign: 'center', marginBottom: 8 }}>
          <div className="label" style={{ marginBottom: 14 }}>
            / 04 — WHO IT'S FOR
          </div>
          <h2 style={{ fontSize: 40, lineHeight: 1.08, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 14px' }}>
            One platform. Two ways to run compliance.
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--fg-2)', maxWidth: 580, margin: '0 auto 26px' }}>
            Whether you manage compliance for a roster of clients or own it inside a single finance department, FiscalBridge
            reshapes the workspace — and the feature set — to fit how you work.
          </p>
          {/* audience toggle */}
          <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4, background: 'var(--bg-3)', borderRadius: 999, padding: 4 }}>
            <button type="button" onClick={() => setAudience('firm')} style={tabStyle(isFirm)}>
              {FIRM.tabIcon} Accounting &amp; tax firms
            </button>
            <button type="button" onClick={() => setAudience('inhouse')} style={tabStyle(!isFirm)}>
              {INHOUSE.tabIcon} In-house finance teams
            </button>
          </div>
        </div>

        <div className="ios-grid ios-2" style={{ display: 'grid', gridTemplateColumns: '1.08fr 0.92fr', gap: 64, alignItems: 'center', marginTop: 48 }}>
          {/* LEFT: both product mocks stacked; the cell holds the taller one's height */}
          <div style={stackCell}>
            <div style={layer(isFirm)} aria-hidden={!isFirm}>
              <FirmMock />
            </div>
            <div style={layer(!isFirm)} aria-hidden={isFirm}>
              <InhouseMock />
            </div>
          </div>

          {/* RIGHT: both copy columns stacked; toggling never reflows the page below */}
          <div style={stackCell}>
            <div style={layer(isFirm)} aria-hidden={!isFirm}>
              <AudienceCopy data={FIRM} onBookDemo={onBookDemo} />
            </div>
            <div style={layer(!isFirm)} aria-hidden={isFirm}>
              <AudienceCopy data={INHOUSE} onBookDemo={onBookDemo} />
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
