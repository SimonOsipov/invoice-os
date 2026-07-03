import { CHEVRON_RIGHT_ICON, FILTER_ICON, JOB_FILTER_KEYS, RECON_BASE, REDRIVE_ICON } from '../data'
import { Icon } from '../icons'
import { jobStateStyle } from '../helpers'
import type { Job, JobFilter, SubTab } from '../types'

const alertGlyph = <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />

type Props = {
  jobs: Job[]
  filter: JobFilter
  onFilterChange: (f: JobFilter) => void
  subTab: SubTab
  onSubTabChange: (t: SubTab) => void
  onOpenJob: (id: string) => void
  onReDriveAll: () => void
  onReconcileFix: (id: string, appLabel: string) => void
}

const tableGridCols = '150px minmax(220px,1.3fr) 130px 116px 56px minmax(200px,1.2fr) 64px 96px 22px'
const reconGridCols = '140px minmax(120px,1fr) 1fr 1fr minmax(160px,1.4fr) 120px'

export function Submissions({ jobs, filter, onFilterChange, subTab, onSubTabChange, onOpenJob, onReDriveAll, onReconcileFix }: Props) {
  const dlCount = jobs.filter((j) => j.state === 'dead-letter').length
  const dlAges = jobs.filter((j) => j.state === 'dead-letter').map((j) => j.age)
  const filtered = filter === 'all' ? jobs : jobs.filter((j) => j.state === filter)

  const subStats = [
    { label: 'In flight', value: String(jobs.filter((j) => ['queued', 'submitting', 'pending'].includes(j.state)).length), color: 'var(--fg-1)' },
    { label: 'Accepted 24h', value: '1,204', color: 'var(--status-green-text)' },
    { label: 'Rejected', value: String(jobs.filter((j) => ['rejected', 'failed'].includes(j.state)).length), color: 'var(--status-red-text)' },
    { label: 'Dead-letter', value: String(dlCount), color: '#8A1F18' },
  ]

  const reconRows = RECON_BASE.map((r) => {
    const a = jobStateStyle(r.int)
    const b = jobStateStyle(r.app)
    return { ...r, intStyle: a, appStyle: b }
  })

  return (
    <div className="ops-screen-pad" style={{ padding: '24px 26px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 05 — SUBMISSION PIPELINE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Submissions ops</h1>
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

      {/* sub-tabs */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 4, borderBottom: '1px solid var(--line-1)', marginBottom: 18 }}>
        {(
          [
            { key: 'jobs', label: 'Jobs' },
            { key: 'recon', label: 'Reconciliation' },
          ] as const
        ).map((t) => {
          const active = subTab === t.key
          return (
            <button
              key={t.key}
              type="button"
              onClick={() => onSubTabChange(t.key)}
              className="ops-btn"
              style={{
                border: 0,
                background: 'transparent',
                cursor: 'pointer',
                fontFamily: 'var(--font-sans)',
                fontSize: 13,
                fontWeight: active ? 600 : 500,
                color: active ? 'var(--fg-1)' : 'var(--fg-3)',
                padding: '10px 4px',
                marginRight: 18,
                borderBottom: `2px solid ${active ? 'var(--accent)' : 'transparent'}`,
              }}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      {subTab === 'jobs' && (
        <>
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
        </>
      )}

      {subTab === 'recon' && (
        <div className="ops-recon-grid" style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 300px', gap: 18 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflowX: 'auto', background: 'var(--bg-2)' }}>
            <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 900 }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>State mismatches · internal vs APP</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--status-red-text)', fontWeight: 600 }}>
                {reconRows.length} OPEN
              </span>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: reconGridCols, padding: '9px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: 900 }}>
              <span className="label">Job ID</span>
              <span className="label">Tenant</span>
              <span className="label">Internal</span>
              <span className="label">APP says</span>
              <span className="label">Detail</span>
              <span className="label" />
            </div>
            {reconRows.map((r) => (
              <div
                key={r.id}
                style={{ display: 'grid', gridTemplateColumns: reconGridCols, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 900 }}
              >
                <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>
                  {r.id}
                </span>
                <span style={{ fontSize: 12, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>{r.tenant}</span>
                <span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: r.intStyle.bg, border: `1px solid ${r.intStyle.border}`, borderRadius: 999, padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: r.intStyle.text }}>
                      {r.intStyle.label}
                    </span>
                  </span>
                </span>
                <span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: r.appStyle.bg, border: `1px solid ${r.appStyle.border}`, borderRadius: 999, padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: r.appStyle.text }}>
                      {r.appStyle.label}
                    </span>
                  </span>
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                  {r.detail}
                </span>
                <span style={{ display: 'flex', gap: 6 }}>
                  <button
                    type="button"
                    onClick={() => onReconcileFix(r.id, r.appStyle.label)}
                    className="ops-btn"
                    style={{ border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 28, padding: '0 10px', borderRadius: 5, fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)' }}
                  >
                    Reconcile
                  </button>
                </span>
              </div>
            ))}
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', padding: 18 }}>
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
              <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden', margin: '12px 0 8px' }}>
                <div style={{ width: '82%', height: '100%', background: 'var(--status-amber-text)', borderRadius: 3 }} />
              </div>
              <div className="mono" style={{ fontSize: 10.5, color: 'var(--status-amber-text)', fontWeight: 600 }}>
                APPROACHING LIMIT · BACKOFF ACTIVE
              </div>
            </div>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', padding: 18 }}>
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
                    {reconRows.length}
                  </span>
                </div>
              </div>
              <button
                type="button"
                className="ops-btn"
                style={{ marginTop: 16, width: '100%', border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 34, borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}
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
