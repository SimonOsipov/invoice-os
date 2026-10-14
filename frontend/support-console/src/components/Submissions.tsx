import type { CSSProperties } from 'react'
import { CHEVRON_RIGHT_ICON } from '../icons'
import { JOB_STATES, jobAge, jobStateStyle, type JobState, type StaffJob } from '../jobsApi'
import type { StaffTenant } from '../tenantsApi'
import { Badge } from './StatusBadge'
import { CARD, Note, ONE_LINE, STAFF_ONLY } from './Tenants'

export type SubmissionsStatus = 'loading' | 'ready' | 'forbidden' | 'error'

type Props = {
  status: SubmissionsStatus
  errorText?: string
  jobs: StaffJob[]
  state: JobState | undefined
  onState: (s: JobState | undefined) => void
  tenant: string
  onTenant: (id: string) => void
  tenants: StaffTenant[]
  onRetry: () => void
}

const JOB_COLS = 'minmax(220px,1.3fr) 130px 116px 56px minmax(200px,1.2fr) 64px 22px'
const MIN_WIDTH = 840

const chipStyle = (on: boolean, tone: ReturnType<typeof jobStateStyle> | null): CSSProperties => ({
  border: `1px solid ${on ? (tone ? tone.border : 'var(--line-3)') : 'var(--line-1)'}`,
  background: on ? (tone ? tone.bg : 'var(--bg-3)') : 'var(--bg-2)',
  color: on ? (tone ? tone.text : 'var(--fg-1)') : 'var(--fg-3)',
  borderRadius: 'var(--radius-sm)',
  height: 30,
  padding: '0 12px',
  cursor: 'pointer',
  fontFamily: 'var(--font-mono)',
  fontSize: 10.5,
  fontWeight: 600,
  letterSpacing: '0.04em',
  display: 'inline-flex',
  alignItems: 'center',
})

const ROW: CSSProperties = {
  display: 'grid',
  gridTemplateColumns: JOB_COLS,
  width: '100%',
  minWidth: MIN_WIDTH,
  padding: '12px 16px',
  border: 0,
  borderBottom: '1px solid var(--line-1)',
  background: 'transparent',
  textAlign: 'left',
  color: 'var(--fg-1)',
  fontFamily: 'var(--font-sans)',
  alignItems: 'center',
}

function Rows({ status, errorText, jobs, onRetry }: Pick<Props, 'status' | 'errorText' | 'jobs' | 'onRetry'>) {
  if (status === 'loading') return <Note text="Loading jobs…" />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (jobs.length === 0) {
    return (
      <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-3)' }}>
        No jobs in this state.
      </div>
    )
  }
  const now = Date.now()
  return (
    <>
      {jobs.map((j) => (
        <button key={j.id} type="button" className="ops-row" data-job-id={j.id} style={ROW}>
          <span style={{ fontSize: 13, fontWeight: 500, paddingRight: 12, ...ONE_LINE }}>{j.tenant_name}</span>
          <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', ...ONE_LINE }}>
            {j.invoice_number}
          </span>
          <span>
            <Badge style={jobStateStyle(j.state)} fontSize={9.5} />
          </span>
          <span className="mono" style={{ fontSize: 12, fontWeight: 600, textAlign: 'center', color: j.attempts >= j.max_attempts ? 'var(--status-red-text)' : 'var(--fg-2)' }}>
            {j.attempts}
          </span>
          <span className="mono" style={{ fontSize: 11, paddingRight: 12, color: j.last_error === null ? 'var(--fg-3)' : 'var(--status-red-text)', ...ONE_LINE }}>
            {j.last_error ?? '—'}
          </span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            {jobAge(j.created_at, now)}
          </span>
          <span style={{ display: 'inline-flex', color: 'var(--fg-3)' }}>{CHEVRON_RIGHT_ICON}</span>
        </button>
      ))}
    </>
  )
}

export function Submissions({ status, errorText, jobs, state, onState, tenant, onTenant, tenants, onRetry }: Props) {
  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          SUBMISSION PIPELINE
        </div>
        <h1 style={{ fontSize: 24, margin: 0 }}>Submissions ops</h1>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        <button type="button" className="ops-chip" aria-pressed={state === undefined} onClick={() => onState(undefined)} style={chipStyle(state === undefined, null)}>
          ALL
        </button>
        {JOB_STATES.map((s) => {
          const st = jobStateStyle(s)
          return (
            <button key={s} type="button" className="ops-chip" aria-pressed={state === s} onClick={() => onState(s)} style={chipStyle(state === s, st)}>
              {st.label}
            </button>
          )
        })}
        <select className="ops-input" aria-label="Tenant" value={tenant} onChange={(e) => onTenant(e.target.value)} style={{ marginLeft: 'auto', maxWidth: 260 }}>
          <option value="">All tenants</option>
          {tenants.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
      </div>

      <div style={{ ...CARD, overflowX: 'auto' }}>
        <div style={{ display: 'grid', gridTemplateColumns: JOB_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: MIN_WIDTH }}>
          <span className="label">Tenant</span>
          <span className="label">Invoice #</span>
          <span className="label">State</span>
          <span className="label" style={{ textAlign: 'center' }}>
            Try
          </span>
          <span className="label">Last error</span>
          <span className="label">Age</span>
          <span />
        </div>
        <Rows status={status} errorText={errorText} jobs={jobs} onRetry={onRetry} />
      </div>
    </div>
  )
}
