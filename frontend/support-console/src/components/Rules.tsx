import { PUBLISH_ICON } from '../data'
import { stateLabel, versionMeta, type RuleVersion, type VersionState } from '../rulesApi'
import { SeverityBadge } from './StatusBadge'
import type { Rule } from '../types'

export type RulesStatus = 'loading' | 'ready' | 'forbidden' | 'error'

type Props = {
  rules: Rule[]
  status: RulesStatus
  errorText?: string
  version: number | null
  state: VersionState | null
  versions: RuleVersion[]
  versionsNote: string
  selected: number | null
  onSelectVersion: (v: RuleVersion) => void
  busy: boolean
  onRetry: () => void
  onOpenRule: (key: string) => void
  onToggleRule: (key: string) => void
  onPublish: () => void
}

const RULE_COLS = 'minmax(150px,1.1fr) 150px minmax(120px,1fr) 78px 96px minmax(160px,1.3fr) 50px'

// Version chips: draft amber, in force green, scheduled the action tint, superseded and retired muted.
const MUTED = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' }
const VERSION_TONE: Record<VersionState, { bg: string; border: string; text: string }> = {
  draft: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  in_force: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  scheduled: { bg: 'var(--action-tint)', border: 'transparent', text: 'var(--action)' },
  superseded: MUTED,
  retired: MUTED,
}
const SWITCH_LOCKED = 'Only the version in force is switched'

export function Rules({ rules, status, errorText, version, state, versions, versionsNote, selected, onSelectVersion, busy, onRetry, onOpenRule, onToggleRule, onPublish }: Props) {
  const locked = state !== 'in_force'
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            VALIDATION ENGINE
          </div>
          <h1 style={{ fontSize: 24, margin: 0 }}>Rules admin</h1>
        </div>
        <button type="button" onClick={onPublish} className="ops-btn v2-btn v2-btn-primary" style={{ height: 36, padding: '0 14px' }}>
          {PUBLISH_ICON} Publish draft
        </button>
      </div>

      <div className="ops-rules-grid" style={{ display: 'grid', gridTemplateColumns: '230px minmax(0,1fr)', gap: 18 }}>
        {/* versions rail */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div className="label" style={{ padding: '12px 14px 8px' }}>
              Rule-set versions · NG-MBS
            </div>
            {versions.length === 0 && (
              <div style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', fontSize: 12, color: 'var(--fg-3)' }}>{versionsNote}</div>
            )}
            {versions.map((v) => {
              const tone = VERSION_TONE[v.state]
              return (
                <button
                  key={v.version}
                  type="button"
                  onClick={() => onSelectVersion(v)}
                  aria-pressed={v.version === selected}
                  style={{ width: '100%', textAlign: 'left', cursor: 'pointer', fontFamily: 'inherit', borderWidth: '1px 0 0 0', borderStyle: 'solid', borderColor: 'var(--line-1)', padding: '11px 14px', display: 'flex', alignItems: 'center', gap: 10, background: v.version === selected ? 'var(--action-tint)' : 'var(--bg-2)' }}
                >
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span className="mono" style={{ display: 'block', fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>
                      v{v.version}
                    </span>
                    <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', marginTop: 1 }}>
                      {versionMeta(v)}
                    </span>
                  </span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
                      {stateLabel(v.state)}
                    </span>
                  </span>
                </button>
              )
            })}
          </div>
        </div>

        {/* rule table */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
          <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 880 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ fontSize: 14, fontWeight: 700, letterSpacing: 'var(--tracking-card)', fontFamily: 'var(--font-display)' }}>Rules</span>
              {version !== null && state !== null && (
                <span className="mono" style={{ fontSize: 10, fontWeight: 700, background: VERSION_TONE[state].bg, color: VERSION_TONE[state].text, border: `1px solid ${VERSION_TONE[state].border}`, borderRadius: 'var(--radius-sm)', padding: '1px 8px' }}>
                  {`${stateLabel(state)} v${version}`}
                </span>
              )}
            </div>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {rules.length} RULES
            </span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: RULE_COLS, padding: '9px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: 880 }}>
            <span className="label">Key</span>
            <span className="label">Type</span>
            <span className="label">Target field</span>
            <span className="label">Severity</span>
            <span className="label">Scope</span>
            <span className="label">Message</span>
            <span className="label" style={{ textAlign: 'right' }}>
              On
            </span>
          </div>
          {status !== 'ready' && (
            <div style={{ padding: '14px 16px', display: 'flex', alignItems: 'center', gap: 12, fontSize: 13, color: 'var(--fg-3)', minWidth: 880 }}>
              <span>{status === 'loading' ? 'Loading rules…' : status === 'forbidden' ? 'Your account has no rules role.' : errorText}</span>
              {status === 'error' && (
                <button type="button" onClick={onRetry} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30 }}>
                  Retry
                </button>
              )}
            </div>
          )}
          {rules.map((r) => (
            <div
              key={r.key}
              className="ops-row"
              onClick={() => onOpenRule(r.key)}
              style={{ display: 'grid', gridTemplateColumns: RULE_COLS, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 880 }}
            >
              <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                {r.key}
              </span>
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-sm)', padding: '2px 6px', justifySelf: 'start' }}>
                {r.type}
              </span>
              <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                {r.field}
              </span>
              <span>
                <SeverityBadge severity={r.severity} />
              </span>
              <span className="mono" style={{ fontSize: 10.5, color: r.scope === 'global' ? 'var(--fg-3)' : 'var(--action)', fontWeight: 600 }}>
                {r.scope === 'global' ? 'GLOBAL' : 'TENANT'}
              </span>
              <span style={{ fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 12 }}>{r.message}</span>
              {/* The toggle sits inside a clickable row, so its own click must not also
                  open the drawer (proto:301 wraps it in a stopPropagation span). */}
              <span style={{ justifySelf: 'end' }} onClick={(e) => e.stopPropagation()}>
                <button
                  type="button"
                  role="switch"
                  aria-checked={r.enabled}
                  aria-label={`${r.enabled ? 'Disable' : 'Enable'} ${r.key}`}
                  onClick={() => onToggleRule(r.key)}
                  className="ops-toggle"
                  disabled={busy || locked}
                  title={locked ? SWITCH_LOCKED : busy ? 'Switching…' : undefined}
                  style={{ display: 'inline-flex', width: 34, height: 20, borderRadius: 99, background: r.enabled ? 'var(--action)' : 'var(--line-3)', padding: 2, border: 0, ...(busy || locked ? { opacity: 0.45, cursor: 'not-allowed' } : { cursor: 'pointer' }) }}
                >
                  <span className="ops-knob" style={{ width: 16, height: 16, borderRadius: '50%', background: 'var(--bg-2)', transform: r.enabled ? 'translateX(14px)' : 'translateX(0)' }} />
                </button>
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
