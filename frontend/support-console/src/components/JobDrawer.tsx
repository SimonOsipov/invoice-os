import { REDRIVE_ICON } from '../data'
import { requestJSON, responseJSON, sentenceCase, statusStyle } from '../helpers'
import { Drawer, MetaGrid } from './Drawer'
import { StateBadge } from './StatusBadge'
import type { Env, Job } from '../types'

type Props = {
  job: Job
  env: Env
  reqOpen: boolean
  resOpen: boolean
  onToggleReq: () => void
  onToggleRes: () => void
  onClose: () => void
  onReDrive: () => void
  onRePoll: () => void
  onCancel: () => void
}

// proto:1133. A timeline step's colour depends on whether it is the CURRENT state
// (active) or an already-passed one. Every step the console shows is passed or current —
// there are no future steps, because the pipeline's next hop isn't knowable from a row.
function step(label: string, active: boolean, ts: string, detail: string) {
  return {
    label,
    ts,
    detail,
    color: active ? 'var(--fg-1)' : 'var(--fg-2)',
    dot: 'var(--action)',
    line: 'var(--action)',
  }
}

export function JobDrawer({ job, env, reqOpen, resOpen, onToggleReq, onToggleRes, onClose, onReDrive, onRePoll, onCancel }: Props) {
  const st = statusStyle(job.state)
  // proto:1140. A terminal failure stamps the last step at the moment the attempt died and
  // shows the error; a live/accepted job stamps it at clearance time.
  const failed = job.state === 'dead-letter' || job.state === 'failed' || job.state === 'rejected'
  const timeline = [
    step('Ingested', false, '08:01:55', 'Validated against rule-set v8'),
    step('Queued', false, '08:01:58', 'idempotency key assigned'),
    step('Submitting', false, '08:02:01', `POST → ${job.app}`),
    failed
      ? step(sentenceCase(st.label), true, '08:02:10', job.lastError)
      : step(sentenceCase(st.label), true, '09:14:22', job.state === 'accepted' ? 'IRN cleared' : 'awaiting APP clearance'),
  ]

  // proto:1153. One row per attempt made, capped at the three the ladder describes.
  const retries = [
    { at: 'attempt 1 · 08:02:01', backoff: '+0s' },
    { at: 'attempt 2 · 08:02:11', backoff: '+10s' },
    { at: 'attempt 3 · 08:02:41', backoff: '+30s' },
  ].slice(0, Math.max(1, Math.min(job.attempts, 3)))

  const polls = [
    { at: '08:05:00', result: '202 pending', color: 'var(--status-amber-text)' },
    { at: '08:20:00', result: '202 pending', color: 'var(--status-amber-text)' },
    {
      at: '09:14:22',
      result: job.state === 'accepted' ? '200 accepted' : '503 timeout',
      color: job.state === 'accepted' ? 'var(--status-green-text)' : 'var(--status-red-text)',
    },
  ]

  const toggleBtn = (label: string, onClick: () => void) => (
    <button
      type="button"
      onClick={onClick}
      className="ops-btn"
      style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 600, color: 'var(--action)' }}
    >
      {label}
    </button>
  )

  return (
    <Drawer
      onClose={onClose}
      header={
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 5 }}>
            <span className="mono" style={{ fontSize: 15, fontWeight: 700 }}>
              {job.id}
            </span>
            <StateBadge state={job.state} />
          </div>
          <div style={{ fontSize: 13, color: 'var(--fg-2)' }}>
            {job.tenant} · <span className="mono">{job.invoice}</span>
          </div>
        </>
      }
      footer={
        <>
          <button type="button" onClick={onReDrive} className="ops-btn v2-btn v2-btn-primary" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {REDRIVE_ICON} Re-drive
          </button>
          <button type="button" onClick={onRePoll} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            Re-poll
          </button>
          <button
            type="button"
            onClick={onCancel}
            className="ops-btn"
            style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 40, padding: '0 16px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 500, color: 'var(--status-red-text)' }}
          >
            Cancel
          </button>
        </>
      }
    >
      <MetaGrid
        items={[
          { label: 'Idempotency key', value: `${job.id.replace('job_', 'idem_')}c3` },
          { label: 'APP target', value: job.app },
          { label: 'Attempts', value: `${job.attempts} / 5` },
          { label: 'Age', value: job.age },
        ]}
      />

      <div className="label" style={{ marginBottom: 12 }}>
        State timeline
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', marginBottom: 24 }}>
        {timeline.map((t, i) => (
          <div key={t.label} style={{ display: 'grid', gridTemplateColumns: '18px 1fr', gap: 12 }}>
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
              <span style={{ width: 11, height: 11, borderRadius: 99, background: t.dot, border: `2px solid ${t.dot}` }} />
              {/* No connector below the last step — the prototype drew one into empty space. */}
              {i < timeline.length - 1 && <span style={{ flex: 1, width: 2, background: t.line }} />}
            </div>
            <div style={{ paddingBottom: 16 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
                <span style={{ fontSize: 13, fontWeight: 600, color: t.color }}>{t.label}</span>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                  {t.ts}
                </span>
              </div>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                {t.detail}
              </div>
            </div>
          </div>
        ))}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 24 }}>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '13px 14px' }}>
          <div className="label" style={{ marginBottom: 9 }}>
            Retry / backoff
          </div>
          {retries.map((x) => (
            <div key={x.at} style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', fontSize: 11.5 }}>
              <span className="mono" style={{ color: 'var(--fg-3)' }}>
                {x.at}
              </span>
              <span className="mono" style={{ color: 'var(--fg-2)', fontWeight: 600 }}>
                {x.backoff}
              </span>
            </div>
          ))}
        </div>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '13px 14px' }}>
          <div className="label" style={{ marginBottom: 9 }}>
            Poll history
          </div>
          {polls.map((x) => (
            <div key={x.at} style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', fontSize: 11.5 }}>
              <span className="mono" style={{ color: 'var(--fg-3)' }}>
                {x.at}
              </span>
              <span className="mono" style={{ color: x.color, fontWeight: 600 }}>
                {x.result}
              </span>
            </div>
          ))}
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
        <span className="label">APP request</span>
        {toggleBtn(reqOpen ? 'COLLAPSE' : 'EXPAND', onToggleReq)}
      </div>
      {reqOpen && (
        <pre className="ops-json" style={{ marginBottom: 18 }}>
          {requestJSON(job, env)}
        </pre>
      )}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
        <span className="label">APP response</span>
        {toggleBtn(resOpen ? 'COLLAPSE' : 'EXPAND', onToggleRes)}
      </div>
      {resOpen && <pre className="ops-json">{responseJSON(job)}</pre>}
    </Drawer>
  )
}
