import { CHEVRON_RIGHT_ICON, FILTER_ICON, JOB_FILTER_KEYS, REDRIVE_ICON } from '../data'
import { Icon } from '../icons'
import { jobStateStyle } from '../helpers'
import type { Job, JobFilter } from '../types'

const alertGlyph = <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />

type Props = {
  jobs: Job[]
  filter: JobFilter
  onFilterChange: (f: JobFilter) => void
  onOpenJob: (id: string) => void
  onReDriveAll: () => void
}

const tableGridCols = '150px minmax(220px,1.3fr) 130px 116px 56px minmax(200px,1.2fr) 64px 96px 22px'

export function Submissions({ jobs, filter, onFilterChange, onOpenJob, onReDriveAll }: Props) {
  const dlCount = jobs.filter((j) => j.state === 'dead-letter').length
  const dlAges = jobs.filter((j) => j.state === 'dead-letter').map((j) => j.age)
  const filtered = filter === 'all' ? jobs : jobs.filter((j) => j.state === filter)

  const subStats = [
    { label: 'In flight', value: String(jobs.filter((j) => ['queued', 'submitting', 'pending'].includes(j.state)).length), color: 'var(--fg-1)' },
    { label: 'Accepted 24h', value: '1,204', color: 'var(--status-green-text)' },
    { label: 'Rejected', value: String(jobs.filter((j) => ['rejected', 'failed'].includes(j.state)).length), color: 'var(--status-red-text)' },
    { label: 'Dead-letter', value: String(dlCount), color: '#8A1F18' },
  ]

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 02 — SUBMISSION JOBS
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Submissions</h1>
        </div>
        <div className="ops-sub-stats" style={{ display: 'flex', gap: 10 }}>
          {subStats.map((s) => (
            <div key={s.label} style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 8, padding: '10px 16px', minWidth: 96 }}>
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

      {dlCount > 0 && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 14,
            background: '#FBE3DF',
            border: '1px solid #E59A8F',
            borderLeft: '3px solid #A12822',
            borderRadius: 8,
            padding: '12px 16px',
            marginBottom: 16,
          }}
        >
          <span style={{ flex: 'none', color: '#8A1F18', display: 'inline-flex' }}>{alertGlyph}</span>
          <div style={{ flex: 1 }}>
            <div style={{ fontSize: 13.5, fontWeight: 600, color: '#8A1F18' }}>{dlCount} jobs in the dead-letter queue</div>
            <div className="mono" style={{ fontSize: 11, color: '#A12822', marginTop: 1 }}>
              Max retries exhausted · oldest {dlAges.length ? dlAges[dlAges.length - 1] : '—'} · review before re-driving
            </div>
          </div>
          <button
            type="button"
            onClick={onReDriveAll}
            className="ops-btn"
            style={{
              border: 0,
              cursor: 'pointer',
              height: 34,
              padding: '0 14px',
              borderRadius: 6,
              background: '#A12822',
              color: '#fff',
              fontFamily: 'var(--font-sans)',
              fontSize: 13,
              fontWeight: 600,
              display: 'inline-flex',
              alignItems: 'center',
              gap: 7,
            }}
          >
            {REDRIVE_ICON} Re-drive all
          </button>
        </div>
      )}

      {/* filters */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        {(['all', ...JOB_FILTER_KEYS] as JobFilter[]).map((k) => {
          const active = filter === k
          const count = k === 'all' ? jobs.length : jobs.filter((j) => j.state === k).length
          const b = k === 'all' ? { text: 'var(--fg-1)', border: 'var(--line-2)', bg: 'var(--bg-3)' } : jobStateStyle(k)
          return (
            <button
              key={k}
              type="button"
              onClick={() => onFilterChange(k)}
              className="ops-chip"
              style={{
                border: `1px solid ${active ? (k === 'all' ? 'var(--line-3)' : b.border) : 'var(--line-1)'}`,
                background: active ? (k === 'all' ? 'var(--bg-3)' : b.bg) : 'var(--bg-2)',
                color: active ? (k === 'all' ? 'var(--fg-1)' : b.text) : 'var(--fg-3)',
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
              {k === 'all' ? 'ALL' : jobStateStyle(k).label}
              <span style={{ fontSize: 10, opacity: 0.7 }}>{count}</span>
            </button>
          )
        })}
        <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
          <div className="ops-input ops-hide-narrow" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, color: 'var(--fg-2)', cursor: 'pointer' }}>
            {FILTER_ICON} All tenants <span style={{ color: 'var(--fg-4)' }}>▾</span>
          </div>
          <div className="ops-input ops-hide-narrow" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, color: 'var(--fg-2)', cursor: 'pointer' }}>
            Last 24h <span style={{ color: 'var(--fg-4)' }}>▾</span>
          </div>
        </div>
      </div>

      {/* jobs table */}
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflowX: 'auto', background: 'var(--bg-2)' }}>
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: tableGridCols,
            gap: 0,
            padding: '10px 16px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            minWidth: 1040,
          }}
        >
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
        {filtered.map((j) => {
          const b = jobStateStyle(j.state)
          const errColor = j.lastError === '—' ? 'var(--fg-4)' : 'var(--status-red-text)'
          const attemptColor = j.attempts >= 4 ? 'var(--status-red-text)' : 'var(--fg-2)'
          return (
            <div
              key={j.id}
              onClick={() => onOpenJob(j.id)}
              className="ops-row"
              style={{ display: 'grid', gridTemplateColumns: tableGridCols, gap: 0, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 1040 }}
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
                <span
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: b.bg, border: `1px solid ${b.border}`, borderRadius: 999, padding: '2px 8px' }}
                >
                  <span style={{ width: 6, height: 6, borderRadius: 99, background: b.dot }} />
                  <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: b.text, letterSpacing: '0.03em' }}>
                    {b.label}
                  </span>
                </span>
              </span>
              <span className="mono" style={{ fontSize: 12, color: attemptColor, textAlign: 'center', fontWeight: 600 }}>
                {j.attempts}
              </span>
              <span className="mono" style={{ fontSize: 11, color: errColor, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 12 }}>
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
          )
        })}
      </div>
    </div>
  )
}
