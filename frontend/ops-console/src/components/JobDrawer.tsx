import { CLOSE_ICON, REDRIVE_ICON } from '../data'
import { buildJobDrawer } from '../helpers'
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

export function JobDrawer({ job, env, reqOpen, resOpen, onToggleReq, onToggleRes, onClose, onReDrive, onRePoll, onCancel }: Props) {
  const d = buildJobDrawer(job, env)
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.32)', animation: 'opsFade 160ms ease-out' }} />
      <div
        className="ops-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 560,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px rgba(20,23,26,0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'opsDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 5 }}>
              <span className="mono" style={{ fontSize: 15, fontWeight: 700 }}>
                {d.id}
              </span>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: d.stBg, border: `1px solid ${d.stBorder}`, borderRadius: 999, padding: '2px 9px' }}>
                <span style={{ width: 6, height: 6, borderRadius: 99, background: d.stDot }} />
                <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: d.stText }}>
                  {d.stLabel}
                </span>
              </span>
            </div>
            <div style={{ fontSize: 13, color: 'var(--fg-2)' }}>
              {d.tenant} · <span className="mono">{d.invoice}</span>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="ops-btn"
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 6, color: 'var(--fg-2)', display: 'grid', placeItems: 'center' }}
          >
            {CLOSE_ICON}
          </button>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          {/* meta grid */}
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, background: 'var(--line-1)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden', marginBottom: 22 }}>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Idempotency key</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {d.idem}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">APP target</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {d.app}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Attempts</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {d.attempts} / 5
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Age</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {d.age}
              </div>
            </div>
          </div>

          {/* state timeline */}
          <div className="label" style={{ marginBottom: 12 }}>
            State timeline
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0, marginBottom: 24 }}>
            {d.timeline.map((t, i) => (
              <div key={i} style={{ display: 'grid', gridTemplateColumns: '18px 1fr', gap: 12 }}>
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
                  <span style={{ width: 11, height: 11, borderRadius: 99, background: t.dotBg, border: `2px solid ${t.dotBorder}` }} />
                  <span style={{ flex: 1, width: 2, background: t.line }} />
                </div>
                <div style={{ paddingBottom: 16 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
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

          {/* retry / poll history */}
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 24 }}>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-2)', padding: '13px 14px' }}>
              <div className="label" style={{ marginBottom: 9 }}>
                Retry / backoff
              </div>
              {d.retries.map((x, i) => (
                <div key={i} style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', fontSize: 11.5 }}>
                  <span className="mono" style={{ color: 'var(--fg-3)' }}>
                    {x.at}
                  </span>
                  <span className="mono" style={{ color: 'var(--fg-2)', fontWeight: 600 }}>
                    {x.backoff}
                  </span>
                </div>
              ))}
            </div>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-2)', padding: '13px 14px' }}>
              <div className="label" style={{ marginBottom: 9 }}>
                Poll history
              </div>
              {d.polls.map((x, i) => (
                <div key={i} style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', fontSize: 11.5 }}>
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

          {/* payloads */}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
            <span className="label">APP request</span>
            <button type="button" onClick={onToggleReq} className="ops-btn" style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 600, color: 'var(--accent)' }}>
              {reqOpen ? 'COLLAPSE' : 'EXPAND'}
            </button>
          </div>
          {reqOpen && (
            <pre className="ops-json" style={{ marginBottom: 18 }}>
              {d.request}
            </pre>
          )}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
            <span className="label">APP response</span>
            <button type="button" onClick={onToggleRes} className="ops-btn" style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 600, color: 'var(--accent)' }}>
              {resOpen ? 'COLLAPSE' : 'EXPAND'}
            </button>
          </div>
          {resOpen && <pre className="ops-json">{d.response}</pre>}
        </div>

        {/* drawer actions */}
        <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', gap: 10 }}>
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
            style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 40, padding: '0 16px', borderRadius: 4, fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 500, color: 'var(--status-red-text)' }}
          >
            Cancel
          </button>
        </div>
      </div>
    </>
  )
}
