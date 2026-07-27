import { LEARNED_RULES, PUBLISH_ICON, RULE_SET_VERSIONS, SPARK_ICON } from '../data'
import { SeverityBadge } from './StatusBadge'
import type { Rule } from '../types'

type Props = {
  rules: Rule[]
  onOpenRule: (key: string) => void
  onToggleRule: (key: string) => void
  onPublish: () => void
  onPromote: (key: string) => void
}

const RULE_COLS = 'minmax(150px,1.1fr) 150px minmax(120px,1fr) 78px 96px minmax(160px,1.3fr) 50px'

// proto:922. Version chips: the draft is amber, the one in force is green, the rest muted.
const VERSION_TONE = {
  draft: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  active: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  arch: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
} as const

export function Rules({ rules, onOpenRule, onToggleRule, onPublish, onPromote }: Props) {
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            VALIDATION ENGINE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 500, letterSpacing: '-0.03em', margin: 0 }}>Rules admin</h1>
        </div>
        <button type="button" onClick={onPublish} className="ops-btn v2-btn v2-btn-primary" style={{ height: 36, padding: '0 14px' }}>
          {PUBLISH_ICON} Publish draft
        </button>
      </div>

      <div className="ops-rules-grid" style={{ display: 'grid', gridTemplateColumns: '230px minmax(0,1fr)', gap: 18 }}>
        {/* versions rail + learned-rules inbox */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div className="label" style={{ padding: '12px 14px 8px' }}>
              Rule-set versions · NG-MBS
            </div>
            {RULE_SET_VERSIONS.map((v) => {
              const tone = VERSION_TONE[v.kind]
              return (
                <div key={v.version} style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10, background: v.kind === 'draft' ? 'var(--action-tint)' : 'var(--bg-2)' }}>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span className="mono" style={{ display: 'block', fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>
                      {v.version}
                    </span>
                    <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', marginTop: 1 }}>
                      {v.meta}
                    </span>
                  </span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
                      {v.tag}
                    </span>
                  </span>
                </div>
              )
            })}
          </div>

          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div style={{ padding: '12px 14px', display: 'flex', alignItems: 'center', gap: 8, borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ color: 'var(--action)' }}>{SPARK_ICON}</span>
              <span style={{ fontSize: 13, fontWeight: 600 }}>Learned rules</span>
              <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, fontWeight: 700, background: 'var(--action-tint)', color: 'var(--action)', borderRadius: 99, padding: '1px 7px' }}>
                {LEARNED_RULES.length}
              </span>
            </div>
            {LEARNED_RULES.map((l) => (
              <div key={l.key} style={{ padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                <div className="mono" style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)', marginBottom: 2 }}>
                  {l.key}
                </div>
                <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', lineHeight: 1.4, marginBottom: 8 }}>
                  {l.source}
                </div>
                <button
                  type="button"
                  onClick={() => onPromote(l.key)}
                  className="ops-btn"
                  style={{ width: '100%', border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 28, borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--action)' }}
                >
                  Promote to draft
                </button>
              </div>
            ))}
          </div>
        </div>

        {/* rule table */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
          <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 880 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ fontSize: 14, fontWeight: 500, fontFamily: 'var(--font-display)' }}>Rules</span>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, background: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', border: '1px solid var(--status-amber-border)', borderRadius: 99, padding: '1px 8px' }}>
                EDITING DRAFT v9
              </span>
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
                  style={{ display: 'inline-flex', width: 34, height: 20, borderRadius: 99, background: r.enabled ? 'var(--action)' : 'var(--line-3)', padding: 2, border: 0, cursor: 'pointer' }}
                >
                  <span className="ops-knob" style={{ width: 16, height: 16, borderRadius: 99, background: 'var(--bg-2)', transform: r.enabled ? 'translateX(14px)' : 'translateX(0)', boxShadow: 'var(--shadow-soft)' }} />
                </button>
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
