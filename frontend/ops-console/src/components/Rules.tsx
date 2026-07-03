import { LEARNED_ROWS, PUBLISH_ICON, SPARK_ICON, VERSION_ROWS } from '../data'
import { severityStyle } from '../helpers'
import type { Rule } from '../types'

type Props = {
  rules: Rule[]
  onOpenRule: (key: string) => void
  onToggleRule: (key: string) => void
  onOpenPublish: () => void
  onPromoteLearned: (key: string) => void
}

const ruleGridCols = 'minmax(150px,1.1fr) 150px minmax(120px,1fr) 78px 96px minmax(160px,1.3fr) 50px'

export function Rules({ rules, onOpenRule, onToggleRule, onOpenPublish, onPromoteLearned }: Props) {
  return (
    <div className="ops-screen-pad" style={{ padding: '24px 26px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 04 — VALIDATION ENGINE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Rules admin</h1>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <button type="button" onClick={onOpenPublish} className="ops-btn v2-btn v2-btn-primary" style={{ height: 36, padding: '0 14px' }}>
            {PUBLISH_ICON} Publish draft
          </button>
        </div>
      </div>

      <div className="ops-rules-grid" style={{ display: 'grid', gridTemplateColumns: '230px minmax(0,1fr)', gap: 18 }}>
        {/* versions rail */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div className="label" style={{ padding: '12px 14px 8px' }}>
              Rule-set versions · NG-MBS
            </div>
            {VERSION_ROWS.map((v) => (
              <div key={v.version} style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10, background: v.bg }}>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span className="mono" style={{ display: 'block', fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>
                    {v.version}
                  </span>
                  <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', marginTop: 1 }}>
                    {v.meta}
                  </span>
                </span>
                <span
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: v.tagBg, border: `1px solid ${v.tagBorder}`, borderRadius: 999, padding: '2px 8px' }}
                >
                  <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: v.tagText, letterSpacing: '0.04em' }}>
                    {v.tag}
                  </span>
                </span>
              </div>
            ))}
          </div>
          {/* learned rules inbox */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div style={{ padding: '12px 14px', display: 'flex', alignItems: 'center', gap: 8, borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ color: 'var(--accent)' }}>{SPARK_ICON}</span>
              <span style={{ fontSize: 13, fontWeight: 600 }}>Learned rules</span>
              <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, fontWeight: 700, background: 'var(--accent-tint)', color: 'var(--accent)', borderRadius: 99, padding: '1px 7px' }}>
                {LEARNED_ROWS.length}
              </span>
            </div>
            {LEARNED_ROWS.map((l) => (
              <div key={l.key} style={{ padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                <div className="mono" style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)', marginBottom: 2 }}>
                  {l.key}
                </div>
                <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', lineHeight: 1.4, marginBottom: 8 }}>
                  {l.source}
                </div>
                <button
                  type="button"
                  onClick={() => onPromoteLearned(l.key)}
                  className="ops-btn"
                  style={{ width: '100%', border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 28, borderRadius: 5, fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--accent)' }}
                >
                  Promote to draft
                </button>
              </div>
            ))}
          </div>
        </div>

        {/* rule table */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflowX: 'auto', background: 'var(--bg-2)' }}>
          <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 880 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Rules</span>
              <span
                className="mono"
                style={{ fontSize: 10, fontWeight: 700, background: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', border: '1px solid var(--status-amber-border)', borderRadius: 99, padding: '1px 8px' }}
              >
                EDITING DRAFT v9
              </span>
            </div>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {rules.length} RULES
            </span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: ruleGridCols, padding: '9px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: 880 }}>
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
          {rules.map((r) => {
            const sv = severityStyle(r.severity)
            const scopeColor = r.scope === 'global' ? 'var(--fg-3)' : 'var(--accent)'
            const scopeLabel = r.scope === 'global' ? 'GLOBAL' : 'TENANT'
            return (
              <div
                key={r.key}
                className="ops-row"
                onClick={() => onOpenRule(r.key)}
                style={{ display: 'grid', gridTemplateColumns: ruleGridCols, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 880 }}
              >
                <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                  {r.key}
                </span>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 4, padding: '2px 6px', justifySelf: 'start' }}>
                  {r.type}
                </span>
                <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
                  {r.field}
                </span>
                <span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: sv.bg, border: `1px solid ${sv.border}`, borderRadius: 999, padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: sv.text, letterSpacing: '0.03em' }}>
                      {sv.label}
                    </span>
                  </span>
                </span>
                <span className="mono" style={{ fontSize: 10.5, color: scopeColor, fontWeight: 600 }}>
                  {scopeLabel}
                </span>
                <span style={{ fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 12 }}>{r.message}</span>
                <span style={{ justifySelf: 'end' }} onClick={(e) => e.stopPropagation()}>
                  <span
                    onClick={() => onToggleRule(r.key)}
                    className="ops-toggle"
                    style={{ display: 'inline-flex', width: 34, height: 20, borderRadius: 99, background: r.enabled ? 'var(--accent)' : 'var(--line-3)', padding: 2, cursor: 'pointer' }}
                  >
                    <span
                      className="ops-knob"
                      style={{ width: 16, height: 16, borderRadius: 99, background: '#fff', transform: r.enabled ? 'translateX(14px)' : 'translateX(0)', boxShadow: '0 1px 2px rgba(0,0,0,0.2)' }}
                    />
                  </span>
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
