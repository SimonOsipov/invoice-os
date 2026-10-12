// Rule detail drawer. `actions` is what makes a rule custom: a golden rule has no prop
// to carry a destructive action.

import { xSmallGlyph } from '../glyphs'
import { ruleJSON, ruleKeyLabel, type CustomRule, type GoldenRule } from '../lib/rulesApi'
import { SeverityPill, TypePill } from './RulePills'

export type RuleDrawerActions = {
  /** Non-admin or roster not loaded: both buttons are disabled. */
  disabled: boolean
  /** Shown as the buttons' title; null when the viewer is not known to be a non-admin. */
  reason: string | null
  removing: boolean
  /** The server's sentence for a failed remove. */
  error: string | null
  onRemove: () => void
  /** Absent until the editor exists; the button is then not drawn. */
  onEdit?: () => void
}

type Props = {
  rule: GoldenRule | CustomRule
  banner: string
  onClose: () => void
  actions?: RuleDrawerActions
}

const BUTTON_OFF = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } as const

function paramValue(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

export function RuleDrawer({ rule, banner, onClose, actions }: Props) {
  const isCustom = actions != null
  const enabled = 'enabled' in rule ? rule.enabled : true
  const params: [string, string][] = Object.entries(rule.params).map(([k, v]) => [k, paramValue(v)])
  if (rule.when != null) params.push(['Applies when', rule.when])
  const off = actions != null && (actions.disabled || actions.removing)

  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', animation: 'pfFade 160ms ease-out' }} />
      <div
        className="pf-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`Rule ${rule.key}`}
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
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '20px 24px 16px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 14 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 5, flexWrap: 'wrap' }}>
              <span className="mono" title={rule.key} style={{ fontSize: 14, fontWeight: 700, wordBreak: 'break-all' }}>
                {ruleKeyLabel(rule.key)}
              </span>
              <TypePill type={rule.type} />
              <SeverityPill severity={rule.severity} />
            </div>
            <div className="mono" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{rule.target || 'whole invoice'}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ border: 0, background: 'transparent', cursor: 'pointer', width: 30, height: 30, borderRadius: 'var(--radius-btn)', color: 'var(--fg-2)', display: 'grid', placeItems: 'center', flex: 'none' }}
          >
            {xSmallGlyph}
          </button>
        </div>

        {/* Provenance banner. Muted for something the tenant merely receives; accent
            for something the tenant owns — the same colour split the table's Source
            column uses, so the two surfaces agree at a glance. */}
        <div
          style={{
            flex: 'none',
            padding: '10px 24px',
            borderBottom: '1px solid var(--line-1)',
            background: isCustom ? 'var(--action-tint)' : 'var(--bg-3)',
            fontSize: 12,
            lineHeight: 1.45,
            color: isCustom ? 'var(--action)' : 'var(--fg-3)',
          }}
        >
          {banner}
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '18px 24px 28px' }}>
          <div className="label" style={{ marginBottom: 12 }}>
            Parameters
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 24 }}>
            {params.map(([label, value]) => (
              <div key={label}>
                <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
                  {label}
                </div>
                <div className="mono" style={{ display: 'flex', alignItems: 'center', minHeight: 36, padding: '8px 11px', overflowWrap: 'anywhere', fontSize: 12, color: 'var(--fg-1)', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)' }}>
                  {value}
                </div>
              </div>
            ))}
            <div>
              <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
                Failure message
              </div>
              <div style={{ display: 'flex', alignItems: 'center', minHeight: 36, padding: '8px 11px', overflowWrap: 'anywhere', fontSize: 12.5, lineHeight: 1.45, color: 'var(--fg-1)', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)' }}>
                {rule.message}
              </div>
            </div>
          </div>

          <div className="label" style={{ marginBottom: 10 }}>
            Underlying rule JSON
          </div>
          <pre className="pf-json">{ruleJSON(rule)}</pre>
        </div>

        {actions?.error && (
          <div
            data-testid="rule-drawer-error"
            style={{ flex: 'none', padding: '10px 24px', borderTop: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}
          >
            {actions.error}
          </div>
        )}

        <div style={{ flex: 'none', padding: '14px 24px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Live status</span>
            <span
              className="mono"
              style={{ fontSize: 10.5, fontWeight: 700, color: !isCustom || enabled ? 'var(--status-green-text)' : 'var(--fg-3)' }}
            >
              {!isCustom ? 'ALWAYS ON' : enabled ? 'LIVE' : 'OFF'}
            </span>
          </div>
          {actions?.onEdit && (
            <button
              type="button"
              disabled={off}
              onClick={actions.onEdit}
              title={actions.reason ?? undefined}
              data-testid="rule-edit"
              className="v2-btn v2-btn-ghost pf-btn"
              style={{ height: 36, ...(off ? BUTTON_OFF : null) }}
            >
              Edit
            </button>
          )}
          {actions && (
            <button
              type="button"
              disabled={off}
              onClick={actions.onRemove}
              title={actions.reason ?? undefined}
              data-testid="rule-remove"
              className="pf-btn"
              style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 36, padding: '0 14px', borderRadius: 'var(--radius-btn)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', ...(off ? BUTTON_OFF : null) }}
            >
              Remove rule
            </button>
          )}
        </div>
      </div>
    </>
  )
}
