// Pure series helpers for the Overview sparklines and Readiness trend (OVER-04).
import { sparkPath, trendPath } from './chartPath'
import { metricCount, metricRatio, type HistoryRow, type RollupBucket } from './dashboard'

export type DayTotals = Omit<HistoryRow, 'entity_id'>

export interface TrendView {
  line: string
  area: string
  grid: string[]
  months: { label: string; at: number }[]
  now: number
  deltaLabel: string
  meta: string
}

const SPARK_DAYS = 30
const TREND_DAYS = 84
const MONTHS = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC']
const FIELDS = ['total', 'vat_kobo', 'needs_attention', 'not_submitted'] as const

// The server picks a row's day the same way (WAT = UTC+1).
// ceiling: client clock trusted for the WAT date, read the server day if skew shows up
export function watToday(now: Date): string {
  return new Date(now.getTime() + 3600_000).toISOString().slice(0, 10)
}

// Firm: the active client's rows. In-house: raw fields summed per day (divide after, never before).
export function scopedDays(isInhouse: boolean, entityId: string | null, rows: unknown): DayTotals[] {
  if (!Array.isArray(rows)) return []
  const byDay = new Map<string, DayTotals>()
  for (const r of rows as HistoryRow[]) {
    if (!isInhouse && r.entity_id !== entityId) continue
    const d = byDay.get(r.day)
    if (d) {
      d.total += r.total
      d.readiness_num += r.readiness_num
      d.needs_attention += r.needs_attention
      d.not_submitted += r.not_submitted
      d.vat_kobo += r.vat_kobo
    } else {
      byDay.set(r.day, {
        day: r.day,
        total: r.total,
        readiness_num: r.readiness_num,
        needs_attention: r.needs_attention,
        not_submitted: r.not_submitted,
        vat_kobo: r.vat_kobo,
      })
    }
  }
  return [...byDay.values()].sort((a, b) => (a.day < b.day ? -1 : a.day > b.day ? 1 : 0))
}

// The curve ends on the live value: a row dated today may be an hour stale.
export function drawnDays(recorded: DayTotals[], live: DayTotals): DayTotals[] {
  return [...recorded.filter((d) => d.day !== live.day), live].slice(-TREND_DAYS)
}

function dayMs(day: string): number {
  return Date.parse(day + 'T00:00:00Z')
}

function dayLabel(day: string): string {
  const m = MONTHS[Number(day.slice(5, 7)) - 1]
  return `${Number(day.slice(8, 10))} ${m.charAt(0) + m.slice(1).toLowerCase()}`
}

export function historyView(
  rows: unknown,
  isInhouse: boolean,
  entityId: string | null,
  bucket: RollupBucket,
  today: string,
): { sparks: string[] | null; trend: TrendView | null } {
  const recorded = scopedDays(isInhouse, entityId, rows)
  if (recorded.length < 2) return { sparks: null, trend: null }

  // metrics may be {} (EMPTY_BUCKET): read absent metrics as 0.
  const live: DayTotals = {
    day: today,
    total: Object.values(bucket.counts).reduce((a, b) => a + b, 0),
    readiness_num: metricCount(bucket.metrics, 'readiness') ?? 0,
    needs_attention: bucket.needs_attention,
    not_submitted: bucket.counts.draft + bucket.counts.validated,
    vat_kobo: metricCount(bucket.metrics, 'vat_tracked') ?? 0,
  }
  const drawn = drawnDays(recorded, live)

  const sparkDays = drawn.slice(-SPARK_DAYS)
  const sparks = FIELDS.map((f) => sparkPath(sparkDays.map((d) => d[f])))

  const pts = drawn
    .map((d) => ({ day: d.day, v: metricRatio({ readiness: { num: d.readiness_num, den: d.total } }, 'readiness') }))
    .filter((p): p is { day: string; v: number } => p.v !== null)
  if (pts.length < 2) return { sparks, trend: null }

  const first = pts[0]
  const last = pts[pts.length - 1]
  const delta = last.v - first.v
  const since = `since ${dayLabel(first.day)}`
  const span = (dayMs(last.day) - dayMs(first.day)) / 86400000 + 1

  const months: TrendView['months'] = []
  let prevMonth = ''
  pts.forEach((p, i) => {
    const ym = p.day.slice(0, 7)
    if (ym === prevMonth) return
    prevMonth = ym
    const at = i / (pts.length - 1)
    if (months.length && at - months[months.length - 1].at < 0.1) return
    months.push({ label: MONTHS[Number(ym.slice(5, 7)) - 1], at })
  })

  return {
    sparks,
    trend: {
      ...trendPath(pts.map((p) => p.v)),
      grid: [0.25, 0.5, 0.75].map((f) => (176 * f).toFixed(1)),
      months,
      now: last.v,
      deltaLabel:
        delta > 0 ? `▲ +${delta} pts ${since}` : delta < 0 ? `▼ -${-delta} pts ${since}` : `no change ${since}`,
      meta: span >= TREND_DAYS ? '12 WEEKS' : `SINCE ${dayLabel(first.day).toUpperCase()} · ${span} DAYS`,
    },
  }
}
