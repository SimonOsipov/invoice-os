import { ALERT_ICON, CHEVRON_RIGHT_ICON, FILTER_ICON, JOB_FILTERS, RECON_ROWS, REDRIVE_ICON } from '../data'
import { statusStyle } from '../helpers'
import { StateBadge } from './StatusBadge'
import type { Job, JobFilter, SubTab } from '../types'

type Props = {
  jobs: Job[]
  filter: JobFilter
  subTab: SubTab
  onFilterChange: (f: JobFilter) => void
  onSubTabChange: (t: SubTab) => void
  onOpenJob: (id: string) => void
  onReDriveAll: () => void
  onReconcile: (id: string, appLabel: string) => void
  onRunSweep: () => void
}

// The jobs table and the reconciliation table share this column template, one each.
const JOB_COLS = '150px minmax(220px,1.3fr) 130px 116px 56px minmax(200px,1.2fr) 64px 96px 22px'
const RECON_COLS = '140px minmax(120px,1fr) 1fr 1fr minmax(160px,1.4fr) 120px'

export function Submissions({ jobs, filter, subTab, onFilterChange, onSubTabChange, onOpenJob, onReDriveAll, onReconcile, onRunSweep }: Props) {
  const deadLetter = jobs.filter((j) => j.state === 'dead-letter')
  const dlCount = deadLetter.length
  const rows = filter === 'all' ? jobs : jobs.filter((j) => j.state === filter)

  // proto:879. "Accepted 24h" is a fixed figure — it counts a window wider than the ten
  // seeded jobs, so deriving it from `jobs` would make it fall to 3 and read as a bug.
  const stats = [
    { label: 'In flight', value: String(jobs.filter((j) => ['queued', 'submitting', 'pending'].includes(j.state)).length), color: 'var(--fg-1)' },
    { label: 'Accepted 24h', value: '1,204', color: 'var(--status-green-text)' },
    { label: 'Rejected', value: String(jobs.filter((j) => ['rejected', 'failed'].includes(j.state)).length), color: 'var(--status-red-text)' },
    { label: 'Dead-letter', value: String(dlCount), color: 'var(--status-red-text)' },
  ]

  const tabs: { key: SubTab; label: string }[] = [
    { key: 'jobs', label: 'Jobs' },
    { key: 'recon', label: 'Reconciliation' },
  ]

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            SUBMISSION PIPELINE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 500, letterSpacing: '-0.03em', margin: 0 }}>Submissions ops</h1>
        </div>
        <div className="ops-sub-stats" style={{ display: 'flex', gap: 10 }}>
          {stats.map((s) => (
            <div key={s.label} style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 'var(--radius-md)', padding: '10px 16px', minWidth: 96 }}>
              <div className="mono" style={{ fontSize: 20, fontWeight: 600, letterSpacing: '-0.02em', color: s.color }}>
                {s.value}
              </div>
              <div className="label" style={{ marginTop: 3 }}>
                {s.label}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* sub-tabs */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 4, borderBottom: '1px solid var(--line-1)', marginBottom: 18 }}>
        {tabs.map((t) => {
          const active = subTab === t.key
          return (
            <button
              key={t.key}
              type="button"
              onClick={() => onSubTabChange(t.key)}
              style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: active ? 600 : 500, color: active ? 'var(--fg-1)' : 'var(--fg-3)', padding: '10px 4px', marginRight: 18, borderBottom: `2px solid ${active ? 'var(--action)' : 'transparent'}` }}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      {subTab === 'jobs' && (
        <>
          {/* dead-letter callout (proto:149) — the reason this screen exists */}
          {dlCount > 0 && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', borderLeft: '3px solid var(--status-red-text)', borderRadius: 'var(--radius-md)', padding: '12px 16px', marginBottom: 16, flexWrap: 'wrap' }}>
              <span style={{ flex: 'none', color: 'var(--status-red-text)', display: 'inline-flex' }}>{ALERT_ICON}</span>
              <div style={{ flex: 1, minWidth: 200 }}>
                <div style={{ fontSize: 13.5, fontWeight: 600, color: 'var(--status-red-text)' }}>
                  {dlCount} {dlCount === 1 ? 'job' : 'jobs'} in the dead-letter queue
                </div>
                <div className="mono" style={{ fontSize: 11, color: 'var(--status-red-text)', marginTop: 1 }}>
                  Max retries exhausted · oldest {deadLetter[deadLetter.length - 1].age} · review before re-driving
                </div>
              </div>
              <button
                type="button"
                onClick={onReDriveAll}
                className="ops-btn"
                style={{ border: 0, cursor: 'pointer', height: 34, padding: '0 14px', borderRadius: 'var(--radius-input)', background: 'var(--status-red-text)', color: 'var(--text-on-dark)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 7 }}
              >
                {REDRIVE_ICON} Re-drive all
              </button>
            </div>
          )}

          {/* filters */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
            {JOB_FILTERS.map((k) => {
              const active = filter === k
              const count = k === 'all' ? jobs.length : jobs.filter((j) => j.state === k).length
              const s = k === 'all' ? null : statusStyle(k)
              return (
                <button
                  key={k}
                  type="button"
                  onClick={() => onFilterChange(k)}
                  className="ops-chip"
                  aria-pressed={active}
                  style={{
                    border: `1px solid ${active ? (s ? s.border : 'var(--line-3)') : 'var(--line-1)'}`,
                    background: active ? (s ? s.bg : 'var(--bg-3)') : 'var(--bg-2)',
                    color: active ? (s ? s.text : 'var(--fg-1)') : 'var(--fg-3)',
                    borderRadius: 99,
                    height: 30,
                    padding: '0 12px',
                    fontFamily: 'var(--font-mono)',
                    fontSize: 10.5,
                    fontWeight: 600,
                    letterSpacing: '0.04em',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 7,
                  }}
                >
                  {s ? s.label : 'ALL'}
                  <span style={{ fontSize: 10, opacity: 0.7 }}>{count}</span>
                </button>
              )
            })}
            {/* Decorative scope pickers (proto:166) — inert in the prototype too. They
                state the default scope rather than offering a working control. */}
            <div className="ops-hide-narrow" style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
              <div className="ops-input" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, color: 'var(--fg-2)' }}>
                {FILTER_ICON} All tenants <span style={{ color: 'var(--fg-4)' }}>▾</span>
              </div>
              <div className="ops-input" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, color: 'var(--fg-2)' }}>
                Last 24h <span style={{ color: 'var(--fg-4)' }}>▾</span>
              </div>
            </div>
          </div>

          {/* jobs table */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
            <div style={{ display: 'grid', gridTemplateColumns: JOB_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: 1040 }}>
              <span className="label">Job ID</span>
              <span className="label">Tenant / entity</span>
              <span className="label">Invoice #</span>
              <span className="label">State</span>
              <span className="label" style={{ textAlign: 'center' }}>
                Try
              </span>
              <span className="label">Last error</span>
              <span className="label">Age</span>
              <span className="label">APP</span>
              <span />
            </div>
            {rows.map((j) => (
              <div
                key={j.id}
                onClick={() => onOpenJob(j.id)}
                className="ops-row"
                style={{ display: 'grid', gridTemplateColumns: JOB_COLS, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 1040 }}
              >
                <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)' }}>
                  {j.id}
                </span>
                <span style={{ minWidth: 0, paddingRight: 12 }}>
                  <span style={{ display: 'block', fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{j.tenant}</span>
                  <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                    {j.tin}
                  </span>
                </span>
                <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)' }}>
                  {j.invoice}
                </span>
                <span>
                  <StateBadge state={j.state} />
                </span>
                <span className="mono" style={{ fontSize: 12, color: j.attempts >= 4 ? 'var(--status-red-text)' : 'var(--fg-2)', textAlign: 'center', fontWeight: 600 }}>
                  {j.attempts}
                </span>
                <span className="mono" style={{ fontSize: 11, color: j.lastError === '—' ? 'var(--fg-4)' : 'var(--status-red-text)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 12 }}>
                  {j.lastError}
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {j.age}
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {j.app}
                </span>
                <span style={{ color: 'var(--fg-4)' }}>{CHEVRON_RIGHT_ICON}</span>
              </div>
            ))}
            {rows.length === 0 && (
              <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-4)' }}>
                No jobs in this state.
              </div>
            )}
          </div>
        </>
      )}

      {subTab === 'recon' && (
        <div className="ops-recon-grid" style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 300px', gap: 18 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
            <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 900 }}>
              <span style={{ fontSize: 14, fontWeight: 500, fontFamily: 'var(--font-display)' }}>State mismatches · internal vs APP</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--status-red-text)', fontWeight: 600 }}>
                {RECON_ROWS.length} OPEN
              </span>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: RECON_COLS, padding: '9px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: 900 }}>
              <span className="label">Job ID</span>
              <span className="label">Tenant</span>
              <span className="label">Internal</span>
              <span className="label">APP says</span>
              <span className="label">Detail</span>
              <span className="label" />
            </div>
            {RECON_ROWS.map((r) => (
              <div key={r.id} style={{ display: 'grid', gridTemplateColumns: RECON_COLS, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 900 }}>
                <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>
                  {r.id}
                </span>
                <span style={{ fontSize: 12, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>{r.tenant}</span>
                <span>
                  <StateBadge state={r.internal} dot={false} />
                </span>
                <span>
                  <StateBadge state={r.app} dot={false} />
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                  {r.detail}
                </span>
                <span style={{ display: 'flex', gap: 6 }}>
                  <button
                    type="button"
                    onClick={() => onReconcile(r.id, statusStyle(r.app).label.toLowerCase())}
                    className="ops-btn"
                    style={{ border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 28, padding: '0 10px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)' }}
                  >
                    Reconcile
                  </button>
                </span>
              </div>
            ))}
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: 18 }}>
              <div className="label" style={{ marginBottom: 14 }}>
                APP rate limit
              </div>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 4 }}>
                <span className="mono" style={{ fontSize: 30, fontWeight: 700, letterSpacing: '-0.02em' }}>
                  82
                </span>
                <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
                  / 100 req·s
                </span>
              </div>
              <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden', margin: '12px 0 8px' }}>
                <div style={{ width: '82%', height: '100%', background: 'var(--status-amber-text)', borderRadius: 'var(--radius-sm)' }} />
              </div>
              <div className="mono" style={{ fontSize: 10.5, color: 'var(--status-amber-text)', fontWeight: 600 }}>
                APPROACHING LIMIT · BACKOFF ACTIVE
              </div>
            </div>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: 18 }}>
              <div className="label" style={{ marginBottom: 12 }}>
                Reconciliation sweep
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
                  <span style={{ color: 'var(--fg-2)' }}>Last full sweep</span>
                  <span className="mono" style={{ fontWeight: 600 }}>
                    14 min ago
                  </span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
                  <span style={{ color: 'var(--fg-2)' }}>Jobs compared</span>
                  <span className="mono" style={{ fontWeight: 600 }}>
                    12,840
                  </span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12.5 }}>
                  <span style={{ color: 'var(--fg-2)' }}>Mismatches</span>
                  <span className="mono" style={{ fontWeight: 600, color: 'var(--status-red-text)' }}>
                    {RECON_ROWS.length}
                  </span>
                </div>
              </div>
              <button
                type="button"
                onClick={onRunSweep}
                className="ops-btn"
                style={{ marginTop: 16, width: '100%', border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 34, borderRadius: 'var(--radius-input)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}
              >
                Run sweep now
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
