import { healthCards } from '../data'
import { SPARK_HEIGHT, SPARK_WIDTH, healthTone, sparkline } from '../helpers'

type Props = {
  deadLetterCount: number
}

export function Health({ deadLetterCount }: Props) {
  const cards = healthCards(deadLetterCount)

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 16, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            INFRASTRUCTURE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 500, letterSpacing: '-0.03em', margin: 0 }}>System health</h1>
        </div>
        <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
          LIVE · REFRESHED 8s AGO
        </span>
      </div>

      <div className="ops-health-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16 }}>
        {cards.map((c) => {
          const tone = healthTone(c.tone)
          const { line, area } = sparkline(c.points)
          return (
            <div key={c.label} style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: 20 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14, gap: 10 }}>
                <span className="label">{c.label}</span>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                  <span style={{ width: 7, height: 7, borderRadius: 99, background: tone.dot }} />
                  <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: tone.dot, letterSpacing: '0.04em' }}>
                    {c.status}
                  </span>
                </span>
              </div>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 12 }}>
                <span className="mono" style={{ fontSize: 30, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)' }}>
                  {c.value}
                </span>
                <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
                  {c.unit}
                </span>
              </div>
              <svg
                viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`}
                width="100%"
                height={SPARK_HEIGHT}
                preserveAspectRatio="none"
                style={{ display: 'block', overflow: 'visible' }}
                aria-hidden="true"
              >
                <path d={area} fill={tone.fill} />
                <path d={line} fill="none" stroke={tone.stroke} strokeWidth={1.6} vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </div>
          )
        })}
      </div>
    </div>
  )
}
