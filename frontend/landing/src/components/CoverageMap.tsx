import { AFRICA_MAP } from '../africaMap'
import { COUNTRY_ORDER, COVERAGE, type CountryId } from '../countries'

const FILL: Record<CountryId, string> = { NG: 'var(--accent)', KE: 'var(--sage)', ZA: 'var(--sage)' }
// Label offsets from the marker, tag text and anchor per country.
const LABEL = {
  NG: { dx: -34, dy: 64, tag: 'FIRST LAUNCH', anchor: 'end' },
  KE: { dx: 40, dy: 10, tag: 'PLANNED', anchor: 'start' },
  ZA: { dx: 78, dy: -4, tag: 'PLANNED', anchor: 'start' },
} as const
const VIEW_BOX = `0 0 ${AFRICA_MAP.w} ${AFRICA_MAP.h}`
const FILL_BOX = { position: 'absolute', inset: 0, width: '100%', height: '100%', display: 'block' } as const

// Two stacked SVGs: role="img" makes its children presentational, so the controls sit in a sibling layer.
export function CoverageMap({ selected, onSelect }: { selected: CountryId; onSelect: (id: CountryId) => void }) {
  return (
    <div data-cov-map style={{ position: 'relative', width: '100%', maxWidth: 'calc(600px * 600 / 673)', aspectRatio: '600 / 673' }}>
      <svg role="img" aria-label="Map of Africa showing Nigeria, Kenya and South Africa" viewBox={VIEW_BOX} style={FILL_BOX}>
        {AFRICA_MAP.countries.map((c) => (
          <path
            key={c.id + c.name}
            d={c.d}
            style={{ fill: c.id in FILL ? FILL[c.id as CountryId] : 'var(--on-dark-10)', stroke: 'var(--surface-panel)', strokeWidth: 1, strokeLinejoin: 'round' }}
          />
        ))}
        {COUNTRY_ORDER.map((id) => {
          const { dx, dy, tag, anchor } = LABEL[id]
          const [x, y] = AFRICA_MAP.markers[id]
          return (
            <g key={id} style={{ fontFamily: 'var(--font-sans)', pointerEvents: 'none' }}>
              <text x={x + dx} y={y + dy} textAnchor={anchor} style={{ fontSize: 18, fontWeight: 800, fill: FILL[id], letterSpacing: '-0.02em' }}>
                {COVERAGE[id].name}
              </text>
              <text x={x + dx} y={y + dy + 15} textAnchor={anchor} style={{ fontSize: 11, fontWeight: 700, fill: 'var(--eyebrow-on-dark)', letterSpacing: '0.1em' }}>
                {tag}
              </text>
            </g>
          )
        })}
      </svg>
      <svg viewBox={VIEW_BOX} data-cov-markers style={FILL_BOX}>
        {COUNTRY_ORDER.map((id) => {
          const [x, y] = AFRICA_MAP.markers[id]
          return (
            <g
              key={id}
              role="button"
              tabIndex={0}
              aria-label={COVERAGE[id].name}
              aria-pressed={selected === id}
              className="cov-marker"
              style={{ cursor: 'pointer' }}
              onClick={() => onSelect(id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onSelect(id)
                }
              }}
            >
              <circle cx={x} cy={y} r={20} style={{ fill: 'transparent' }} />
              {selected === id && <circle cx={x} cy={y} r={15} style={{ fill: 'none', stroke: 'var(--surface-foreground)', strokeWidth: 1.5 }} />}
              <circle cx={x} cy={y} r={9} style={{ fill: FILL[id], opacity: 0.35 }} />
              <circle cx={x} cy={y} r={5} style={{ fill: 'var(--surface)' }} />
              <circle className="cov-marker-focus" cx={x} cy={y} r={19} fill="none" stroke="var(--ring)" strokeWidth={2} />
            </g>
          )
        })}
      </svg>
    </div>
  )
}
