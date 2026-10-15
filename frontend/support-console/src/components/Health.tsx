import type { CSSProperties } from 'react'
import { SPARK_HEIGHT, SPARK_WIDTH, healthTone, sparkline } from '../helpers'
import { fleetSummary, series, shortBuild, type StaffHealth, type TrendPoint } from '../healthApi'
import { syncStamp } from '../rulesApi'
import { Badge } from './StatusBadge'

export type HealthStatus = 'loading' | 'ready' | 'forbidden' | 'error'

type Props = {
  status: HealthStatus
  errorText?: string
  data: StaffHealth | null
  onRetry: () => void
}

type Tone = 'green' | 'amber' | 'red'
type Tile = { key: string; label: string; value: string; unit: string; word?: string; tone: Tone; down?: string[]; points: number[] }

const ONE_LINE: CSSProperties = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }
const CARD: CSSProperties = { border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', background: 'var(--bg-2)' }
const NOTE: CSSProperties = { padding: '14px 16px', fontSize: 13, color: 'var(--fg-3)' }
const STAFF_ONLY = 'Your account is not ASComply staff.'
const UP = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'UP' }
const DOWN = { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'DOWN' }

const FLEET_WORD: Record<StaffHealth['fleet']['status'], { word: string; tone: Tone }> = {
  ok: { word: 'HEALTHY', tone: 'green' },
  degraded: { word: 'DEGRADED', tone: 'red' },
  unknown: { word: 'UNKNOWN', tone: 'amber' },
}

function tiles(d: StaffHealth): Tile[] {
  const { up, total, down } = fleetSummary(d.fleet)
  const fleet = FLEET_WORD[d.fleet.status]
  const dead = d.jobs.dead_lettered ?? 0
  const of = (key: Exclude<keyof TrendPoint, 'taken_at'>) => series(d.trend, key)
  return [
    { key: 'fleet', label: 'Fleet', value: d.fleet.status === 'unknown' ? '—' : `${up}/${total}`, unit: 'services up', word: fleet.word, tone: fleet.tone, down, points: of('services_down') },
    { key: 'queue', label: 'Queue depth', value: String(d.queue_depth), unit: 'jobs', tone: 'green', points: of('queue_depth') },
    { key: 'dead-lettered', label: 'Dead-lettered', value: String(dead), unit: 'jobs', word: dead > 0 ? 'ATTENTION' : 'CLEAR', tone: dead > 0 ? 'red' : 'green', points: of('dead_lettered') },
  ]
}

const Retry = ({ onClick }: { onClick: () => void }) => (
  <button type="button" onClick={onClick} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30 }}>
    Retry
  </button>
)

const Note = ({ text, onRetry }: { text: string; onRetry?: () => void }) => (
  <div style={{ ...CARD, ...NOTE, display: 'flex', alignItems: 'center', gap: 12 }}>
    <span>{text}</span>
    {onRetry && <Retry onClick={onRetry} />}
  </div>
)

function TileCard({ t }: { t: Tile }) {
  const tone = healthTone(t.tone)
  const { line, area } = sparkline(t.points)
  return (
    <div data-tile={t.key} style={{ ...CARD, minWidth: 0, padding: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14, gap: 10 }}>
        <span className="label">{t.label}</span>
        {t.word && (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
            <span style={{ width: 7, height: 7, borderRadius: '50%', background: tone.dot }} />
            <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: tone.dot, letterSpacing: '0.04em' }}>
              {t.word}
            </span>
          </span>
        )}
      </div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 12 }}>
        <span className="money" style={{ fontSize: 30, fontWeight: 700, color: 'var(--fg-1)' }}>
          {t.value}
        </span>
        <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
          {t.unit}
        </span>
      </div>
      {t.down && t.down.length > 0 && (
        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginBottom: 12, ...ONE_LINE }}>
          {`Down: ${t.down.join(', ')}`}
        </div>
      )}
      {line && (
        <svg viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`} width="100%" height={SPARK_HEIGHT} preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }} aria-hidden="true">
          <path d={area} fill={tone.fill} />
          <path d={line} fill="none" stroke={tone.stroke} strokeWidth={1.6} vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )}
    </div>
  )
}

function Services({ fleet }: { fleet: StaffHealth['fleet'] }) {
  return (
    <div style={{ ...CARD, marginTop: 16 }}>
      <div className="label" style={{ padding: '14px 16px' }}>
        Services
      </div>
      {fleet.status === 'unknown' && <div style={NOTE}>The gateway roll-up is unavailable.</div>}
      {fleet.status !== 'unknown' &&
        fleet.services.map((s) => (
          <div key={s.name} data-service={s.name} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 16px', borderTop: '1px solid var(--line-1)' }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 13, fontWeight: 600, ...ONE_LINE }}>{s.name}</div>
            </div>
            <span style={{ flex: 'none', display: 'inline-flex' }}>
              <Badge style={s.status === 'up' ? UP : DOWN} fontSize={9} />
            </span>
            <span className="mono" title={s.build || undefined} style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
              {shortBuild(s.build)}
            </span>
          </div>
        ))}
    </div>
  )
}

export function Health({ status, errorText, data, onRetry }: Props) {
  const body = () => {
    if (status === 'loading' || (status === 'ready' && !data)) return <Note text="Loading system health…" />
    if (status === 'forbidden') return <Note text={STAFF_ONLY} />
    if (status === 'error' || !data) return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
    return (
      <>
        <div className="ops-health-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 16 }}>
          {tiles(data).map((t) => (
            <TileCard key={t.key} t={t} />
          ))}
        </div>
        <Services fleet={data.fleet} />
      </>
    )
  }
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 16, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            INFRASTRUCTURE
          </div>
          <h1 style={{ fontSize: 24, margin: 0 }}>System health</h1>
        </div>
        {status === 'ready' && data && (
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
            {`CHECKED ${syncStamp(data.checked_at)}`}
          </span>
        )}
      </div>
      {body()}
    </div>
  )
}
