import { buildHealthCards } from '../helpers'

type Props = {
  deadLetterCount: number
}

export function Health({ deadLetterCount }: Props) {
  const cards = buildHealthCards(deadLetterCount)
  return (
    <div className="ops-screen-pad" style={{ padding: '24px 26px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / INFRASTRUCTURE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>System health</h1>
        </div>
        <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
          LIVE · REFRESHED 8s AGO
        </span>
      </div>
      <div className="ops-health-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, marginBottom: 16 }}>
        {cards.map((h) => (
          <div key={h.label} style={{ border: '1px solid var(--line-1)', borderRadius: 10, background: 'var(--bg-2)', padding: 20 }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
              <span className="label">{h.label}</span>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                <span style={{ width: 7, height: 7, borderRadius: 99, background: h.dot }} />
                <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: h.dot, letterSpacing: '0.04em' }}>
                  {h.status}
                </span>
              </span>
            </div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 12 }}>
              <span className="mono" style={{ fontSize: 30, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)' }}>
                {h.value}
              </span>
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
                {h.unit}
              </span>
            </div>
            <svg viewBox="0 0 220 44" width="100%" height="44" preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }}>
              <path d={h.area} fill={h.fill} />
              <path d={h.spark} fill="none" stroke={h.stroke} strokeWidth="1.6" vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </div>
        ))}
      </div>
    </div>
  )
}
