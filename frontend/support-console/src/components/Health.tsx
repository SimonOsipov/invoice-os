import type { CSSProperties } from 'react'
import { CHART_H, SPARK_HEIGHT, SPARK_WIDTH, healthTone, sentryServiceUrl, sparkline } from '../helpers'
import { fleetSummary, jobSeries, jobsOf, series, shortBuild, type StaffHealth } from '../healthApi'
import { DASHBOARDS, METRIC_SERVICES, NO_SENTRY, RANGES, SERIES, THRESHOLDS, colourMap, fleetSeries, formatValue, hasData, judge, latestOf, panelOf, type PanelKey, type Point, type RangeKey, type StaffMetrics, type Stat, type Verdict } from '../metricsApi'
import { syncStamp } from '../rulesApi'
import { G } from './BlogList'
import { Badge } from './StatusBadge'
import { chipStyle } from './Submissions'
import { TimeChart } from './TimeChart'

export type HealthStatus = 'loading' | 'ready' | 'forbidden' | 'error'

type Props = {
  status: HealthStatus
  errorText?: string
  data: StaffHealth | null
  healthStale: boolean
  metricsStale: boolean
  onRetry: () => void
  metricsStatus: HealthStatus
  metricsErrorText?: string
  metrics: StaffMetrics | null
  onRetryMetrics: () => void
  range: RangeKey
  onRange: (r: RangeKey) => void
  service: string
  onService: (s: string) => void
}

type Tone = 'green' | 'amber' | 'red' | 'muted'
type Tile = { key: string; label: string; value: string; unit: string; word?: string; scope?: string; tone: Tone; down?: string[]; points: number[] }

const LEGEND_TEXT: CSSProperties = { fontSize: 11, color: 'var(--fg-3)' }
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

const ALL_SERVICES_TAG = 'All services'

// Fleet tile, then the jobs tiles for the pick; null jobs when the service owns no job kind.
function tiles(d: StaffHealth, service: string): { fleet: Tile; jobs: Tile[] | null } {
  const { up, total, down } = fleetSummary(d.fleet)
  const fleet = FLEET_WORD[d.fleet.status]
  const fleetTile: Tile = { key: 'fleet', label: 'Fleet', value: d.fleet.status === 'unknown' ? '—' : `${up}/${total}`, unit: 'services up', word: fleet.word, scope: ALL_SERVICES_TAG, tone: fleet.tone, down, points: series(d.trend, 'services_down') }
  if (!d.river_jobs) {
    const gap: Omit<Tile, 'key' | 'label'> = { value: '—', unit: 'jobs', word: 'UNAVAILABLE', tone: 'muted', points: [] }
    return { fleet: fleetTile, jobs: [{ key: 'queue', label: 'Queue depth', ...gap }, { key: 'dead-lettered', label: 'Dead-lettered', ...gap }] }
  }
  const j = jobsOf(d.river_jobs, service)
  if (!j) return { fleet: fleetTile, jobs: null }
  return {
    fleet: fleetTile,
    jobs: [
      { key: 'queue', label: 'Queue depth', value: String(j.queue_depth), unit: 'jobs', tone: 'green', points: jobSeries(d.trend, service, 'queue_depth') },
      { key: 'dead-lettered', label: 'Dead-lettered', value: String(j.dead_lettered), unit: 'jobs', word: j.dead_lettered > 0 ? 'ATTENTION' : 'CLEAR', tone: j.dead_lettered > 0 ? 'red' : 'green', points: jobSeries(d.trend, service, 'dead_lettered') },
    ],
  }
}

const VERDICT: Record<Verdict, { word: string; tone: Tone }> = {
  ok: { word: 'OK', tone: 'green' },
  peaked: { word: 'PEAKED', tone: 'amber' },
  high: { word: 'HIGH', tone: 'red' },
}

type MetricStat = { key: string; label: string; unit: string; stat?: Stat; read: (f: ReturnType<typeof fleetSeries>) => [number, number][] }
const METRIC_STATS: MetricStat[] = [
  { key: 'req', label: 'Requests', unit: 'req/min', read: (f) => f.requests },
  { key: 'errors', label: '5xx', unit: '%', stat: 'error', read: (f) => f.error },
  { key: 'p95', label: 'p95 latency', unit: 'ms', stat: 'p95', read: (f) => f.p95 },
  { key: 'connections', label: 'DB connections', unit: 'count', stat: 'connections', read: (f) => f.connections },
]

// Without metrics the tile is blank while loading, UNAVAILABLE after a failed read and NO DATA once an answer has no value.
function metricTiles(m: StaffMetrics | null, status: HealthStatus): Tile[] {
  const f = m ? fleetSeries(m) : null
  return METRIC_STATS.map((s) => {
    const points = f ? s.read(f) : []
    const latest = latestOf(points)
    const tail = s.stat === 'connections' ? (m ? `of ${m.connection_budget}` : '') : s.unit
    if (latest === null) return { key: s.key, label: s.label, scope: s.stat === 'connections' ? ALL_SERVICES_TAG : undefined, value: '—', unit: tail, word: !m && status === 'loading' ? undefined : !m && status !== 'ready' ? 'UNAVAILABLE' : 'NO DATA', tone: 'muted', points: [] }
    const shown = formatValue(s.unit, latest)
    const base = { key: s.key, label: s.label, scope: s.stat === 'connections' ? ALL_SERVICES_TAG : undefined, value: shown.value, unit: s.stat === 'connections' ? tail : shown.unit, points: points.map((p) => p[1]) }
    if (!s.stat) return { ...base, tone: 'green' }
    const peak = Math.max(...points.map((p) => formatValue(s.unit, p[1]).n))
    return { ...base, ...VERDICT[judge(s.stat, shown.n, peak, m?.connection_budget ?? 0)] }
  })
}

const OTHER_LINE = 'var(--fg-4)'
const RANK: Record<Verdict, number> = { ok: 0, peaked: 1, high: 2 }
const WORD_TONE: Record<Exclude<Verdict, 'ok'>, Tone> = { high: 'red', peaked: 'amber' }

type Entry = { name: string; color: string; unit: string; points: Point[]; extra?: string }
type PanelSpec = { key: string; title: string; stat?: Stat; guide?: { value: number; label: string } }

const PANELS: PanelSpec[] = [
  { key: 'req_per_min', title: 'Requests / min' },
  { key: 'error_pct', title: '5xx %', stat: 'error', guide: { value: THRESHOLDS.error, label: '1 %' } },
  { key: 'latency_p50_ms', title: 'Latency p50 (ms)' },
  { key: 'latency_p95_ms', title: 'Latency p95 (ms)', stat: 'p95', guide: { value: THRESHOLDS.p95, label: '1 s' } },
  { key: 'cpu_pct', title: 'CPU % of limit', stat: 'cpu', guide: { value: THRESHOLDS.cpu, label: '80 %' } },
  { key: 'ram_pct', title: 'RAM % of limit', stat: 'ram', guide: { value: THRESHOLDS.ram, label: '80 %' } },
  { key: 'instances', title: 'Instances' },
  { key: 'cold_starts_per_min', title: 'Cold starts / min' },
  { key: 'sql_util', title: 'Cloud SQL CPU, RAM, disk (%)' },
  { key: 'sql_connections', title: 'Cloud SQL connections', stat: 'connections' },
]
const SQL_UTIL: [string, PanelKey][] = [['CPU', 'sql_cpu_pct'], ['RAM', 'sql_ram_pct'], ['Disk', 'sql_disk_pct']]
const ALL_SERVICES = new Set(['sql_util', 'sql_connections'])

function sqlPoints(m: StaffMetrics, key: PanelKey): Point[] {
  const p = panelOf(m, key)
  return hasData(p) ? (p.series.find((s) => s.name === 'database') ?? p.series[0]).points : []
}

function entriesOf(spec: PanelSpec, m: StaffMetrics, colours: Record<string, string>): Entry[] {
  if (spec.key === 'sql_util') return SQL_UTIL.map(([name, key], i) => ({ name, color: SERIES[i], unit: '%', points: sqlPoints(m, key) }))
  if (spec.key === 'sql_connections') return [{ name: 'connections', color: SERIES[0], unit: 'count', points: sqlPoints(m, 'sql_connections') }]
  const p = panelOf(m, spec.key as PanelKey)
  if (!hasData(p)) return []
  return p.series
    .filter((s) => s.points.length > 0)
    .map((s) => {
      const max = spec.key === 'instances' && m.limits.available ? m.limits.services.find((l) => l.name === s.name)?.max_instances : undefined
      return { name: s.name, color: colours[s.name] ?? OTHER_LINE, unit: p.unit, points: s.points, extra: max === undefined ? undefined : `· max ${max}` }
    })
}

// The worst verdict over the series, each judged on displayed values.
function panelVerdict(stat: Stat, entries: Entry[], budget: number): Verdict {
  let worst: Verdict = 'ok'
  for (const e of entries) {
    const latest = latestOf(e.points)
    if (latest === null) continue
    const v = judge(stat, formatValue(e.unit, latest).n, Math.max(...e.points.map(([, y]) => formatValue(e.unit, y).n)), budget)
    if (RANK[v] > RANK[worst]) worst = v
  }
  return worst
}

const shown = (e: Entry): string => {
  const latest = latestOf(e.points)
  if (latest === null) return 'No data'
  const f = formatValue(e.unit, latest)
  const unit = f.unit === '% of limit' ? '%' : f.unit
  return unit ? `${f.value} ${unit}` : f.value
}

function Panel({ spec, m, colours, service, markers }: { spec: PanelSpec; m: StaffMetrics; colours: Record<string, string>; service: string; markers: { t: number; title: string }[] }) {
  const entries = entriesOf(spec, m, colours)
  const drawn = entries.filter((e) => e.points.length > 0)
  const verdict = spec.stat && drawn.length > 0 ? panelVerdict(spec.stat, drawn, m.connection_budget) : 'ok'
  const tone = verdict === 'ok' ? null : healthTone(WORD_TONE[verdict])
  const guides = []
  if (spec.guide) guides.push({ ...spec.guide, dashed: true })
  if (spec.key === 'sql_connections') guides.push({ value: m.connection_budget, label: `budget ${m.connection_budget}`, dashed: false })
  const cap = spec.key === 'instances' && service && m.limits.available ? m.limits.services.find((l) => l.name === service) : undefined
  if (cap) guides.push({ value: cap.max_instances, label: `max ${cap.max_instances}`, dashed: true })
  const coloured = entries.filter((e) => e.color !== OTHER_LINE)
  const grey = drawn.filter((e) => e.color === OTHER_LINE).length
  return (
    <div data-panel={spec.key} style={{ ...CARD, minWidth: 0, padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'space-between', gap: 10, rowGap: 4 }}>
        <span className="label" style={{ minWidth: 0 }}>
          {spec.title}
        </span>
        {(ALL_SERVICES.has(spec.key) || tone) && (
          <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 10 }}>
            {ALL_SERVICES.has(spec.key) && (
              <span className="mono" style={LEGEND_TEXT}>
                {ALL_SERVICES_TAG}
              </span>
            )}
            {tone && (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                <span style={{ width: 7, height: 7, borderRadius: '50%', background: tone.dot }} />
                <span data-word="" className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: tone.dot, letterSpacing: '0.04em' }}>
                  {VERDICT[verdict].word}
                </span>
              </span>
            )}
          </span>
        )}
      </div>
      {drawn.length > 0 ? (
        <TimeChart lines={drawn} start={Date.parse(m.start) / 1000} end={Date.parse(m.end) / 1000} step={m.step_seconds} guides={guides} markers={markers} />
      ) : (
        <div data-no-data="" style={{ ...NOTE, padding: 0, minHeight: CHART_H, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          No data
        </div>
      )}
      <div style={{ display: 'flex', flexWrap: 'wrap', columnGap: 14, rowGap: 4, minHeight: 16 }}>
        {coloured.map((e) => (
          <span key={e.name} data-series={e.name} style={{ display: 'inline-flex', alignItems: 'center', gap: 6, ...LEGEND_TEXT }}>
            <span style={{ width: 8, height: 8, borderRadius: 2, background: e.color }} />
            <span>{e.name}</span>
            <span className="mono">{shown(e)}</span>
            {e.extra && <span className="mono">{e.extra}</span>}
          </span>
        ))}
        {grey > 0 && <span data-series-other="" style={LEGEND_TEXT}>{`+${grey} other ${grey === 1 ? 'service' : 'services'}`}</span>}
      </div>
    </div>
  )
}

function Panels({ m, service }: { m: StaffMetrics; service: string }) {
  const colours = colourMap(m)
  const markers = m.deploys.available ? m.deploys.revisions.map((r) => ({ t: Date.parse(r.created_at) / 1000, title: `${r.revision} · ${new Date(r.created_at).toISOString().slice(11, 16)} UTC` })) : []
  return (
    <>
      {!m.deploys.available && <div style={{ ...LEGEND_TEXT, marginBottom: 8 }}>Deploy markers unavailable</div>}
      <div className="ops-health-panels" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 16 }}>
        {PANELS.map((spec) => (
          <Panel key={spec.key} spec={spec} m={m} colours={colours} service={service} markers={markers} />
        ))}
      </div>
    </>
  )
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
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14, gap: 10, rowGap: 4 }}>
        <span className="label" style={{ minWidth: 0 }}>
          {t.label}
        </span>
        {(t.word || t.scope) && (
          <span style={{ display: 'inline-flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'flex-end', gap: 10, rowGap: 4, minWidth: 0 }}>
            {t.scope && (
              <span data-scope="" className="mono" style={LEGEND_TEXT}>
                {t.scope}
              </span>
            )}
            {t.word && (
              <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                <span style={{ width: 7, height: 7, borderRadius: '50%', background: tone.dot }} />
                <span data-word="" className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: tone.dot, letterSpacing: '0.04em' }}>
                  {t.word}
                </span>
              </span>
            )}
          </span>
        )}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: 6, rowGap: 4, marginBottom: 12 }}>
        <span className="money" style={{ flex: 'none', fontSize: 30, fontWeight: 700, color: 'var(--fg-1)' }}>
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

function Services({ fleet, service }: { fleet: StaffHealth['fleet']; service: string }) {
  const rows = fleet.status === 'unknown' ? [] : service === '' ? fleet.services : fleet.services.filter((s) => s.name === service)
  return (
    <div style={{ ...CARD, marginTop: 16 }}>
      <div className="label" style={{ padding: '14px 16px' }}>
        Services
      </div>
      {fleet.status === 'unknown' && <div style={NOTE}>The gateway roll-up is unavailable.</div>}
      {fleet.status !== 'unknown' && rows.length === 0 && service !== '' && (
        <div data-ping-note="" style={NOTE}>{`${service} has no live ping. The live ping covers the backend services.`}</div>
      )}
      {fleet.status !== 'unknown' &&
        rows.map((s) => (
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
            {!NO_SENTRY.includes(s.name) && (
              <a
                href={sentryServiceUrl(s.name)}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={`Open ${s.name} in Sentry`}
                className="ops-btn v2-btn v2-btn-ghost"
                style={{ flex: 'none', height: 28, padding: '0 10px', fontSize: 12, textDecoration: 'none' }}
              >
                <G name="external-link" size={15} />
                Sentry
              </a>
            )}
          </div>
        ))}
    </div>
  )
}

const DASHBOARD_LINKS = [
  { label: 'Services dashboard', href: DASHBOARDS.services },
  { label: 'Database and edge dashboard', href: DASHBOARDS.database_edge },
]

export function Health({ status, errorText, data, healthStale, metricsStale, onRetry, metricsStatus, metricsErrorText, metrics, onRetryMetrics, range, onRange, service, onService }: Props) {
  const panelsNote = () => {
    if (metricsStatus === 'forbidden') return <Note text={STAFF_ONLY} />
    if (metricsStatus === 'error') return <Note text={metricsErrorText ?? 'Unavailable'} onRetry={onRetryMetrics} />
    if (metricsStatus === 'loading' && !metrics) return <Note text="Loading metrics…" />
    return null
  }
  const body = () => {
    if (status === 'loading' || (status === 'ready' && !data)) return <Note text="Loading system health…" />
    if (status === 'forbidden') return <Note text={STAFF_ONLY} />
    if (status === 'error' || !data) return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
    const { fleet, jobs } = tiles(data, service)
    const note = panelsNote()
    return (
      <>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
          {RANGES.map((r) => (
            <button key={r} type="button" className="ops-chip" aria-pressed={range === r} onClick={() => onRange(r)} style={chipStyle(range === r, null)}>
              {r}
            </button>
          ))}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginLeft: 'auto' }}>
            <select className="ops-input" aria-label="Service" value={service} onChange={(e) => onService(e.target.value)} style={{ maxWidth: 260 }}>
              <option value="">All services</option>
              {METRIC_SERVICES.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </div>
        </div>
        <div className="ops-health-stats" style={{ display: 'grid', gridTemplateColumns: 'repeat(5, minmax(0, 1fr))', gap: 16 }}>
          {[fleet, ...metricTiles(metrics, metricsStatus)].map((t) => (
            <TileCard key={t.key} t={t} />
          ))}
        </div>
        {jobs ? (
          <div className="ops-health-jobs" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 16, marginTop: 16 }}>
            {jobs.map((t) => (
              <TileCard key={t.key} t={t} />
            ))}
          </div>
        ) : (
          <div data-jobs-note="" style={{ marginTop: 16 }}>
            <Note text={`${service} owns no background jobs. Pick submission, notifications, worker or All services to see them.`} />
          </div>
        )}
        {(note || metrics) && (
          <div data-panels style={{ marginTop: 16 }}>
            {note ?? (metrics && <Panels m={metrics} service={service} />)}
          </div>
        )}
        <Services fleet={data.fleet} service={service} />
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
              {`CHECKED ${syncStamp(data.checked_at)}`}
            </span>
            {(healthStale || metricsStale) && (
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--status-amber-text)', letterSpacing: '0.05em' }}>
                REFRESH FAILED
              </span>
            )}
            {DASHBOARD_LINKS.map((l) => (
              <a key={l.label} href={l.href} target="_blank" rel="noopener noreferrer" className="ops-btn v2-btn v2-btn-ghost" style={{ height: 34, padding: '0 12px', fontSize: 12, textDecoration: 'none' }}>
                <G name="external-link" size={15} />
                {l.label}
              </a>
            ))}
          </div>
        )}
      </div>
      {body()}
    </div>
  )
}
