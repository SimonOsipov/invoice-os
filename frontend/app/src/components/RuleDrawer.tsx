// Rule detail drawer. One component covers both halves of the table: the presence of
// `onRemove` is what makes a rule custom, so a golden rule cannot be rendered with a
// destructive action by accident — there is no prop to pass one through.

import { GOLDEN_SET, GOLDEN_SOURCE_REF, ruleJSON, tenantSlug, type CustomRule, type Rule } from '../lib/rules'
import { closeGlyph } from '../glyphs'
import { SeverityPill, TypePill } from './RulePills'

type Props = {
  rule: Rule | CustomRule
  /** Client (firm mode) or org (in-house) — the tenant this custom rule belongs to. */
  scope: string
  onClose: () => void
  /** Custom rules only. Its absence is what marks the rule inherited and read-only. */
  onRemove?: () => void
}

export function RuleDrawer({ rule, scope, onClose, onRemove }: Props) {
  const isCustom = onRemove != null
  const enabled = isCustom ? (rule as CustomRule).enabled : true
  const sourceRef = isCustom ? `tenant:${tenantSlug(scope)}` : GOLDEN_SOURCE_REF

  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.32)', animation: 'pfFade 160ms ease-out' }} />
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
          boxShadow: '-24px 0 48px -24px oklch(20% .02 210 / 0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4, flexWrap: 'wrap' }}>
              <span className="mono" style={{ fontSize: 14, fontWeight: 700, wordBreak: 'break-all' }}>
                {rule.key}
              </span>
              <TypePill type={rule.type} />
              <SeverityPill severity={rule.severity} />
            </div>
            <div className="mono" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{rule.field}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 'var(--radius-input)', color: 'var(--fg-2)', display: 'grid', placeItems: 'center', flex: 'none' }}
          >
            {closeGlyph}
          </button>
        </div>

        {/* Provenance banner. Muted for something the tenant merely receives; accent
            for something the tenant owns — the same colour split the table's Source
            column uses, so the two surfaces agree at a glance. */}
        <div
          style={{
            flex: 'none',
            padding: '11px 22px',
            borderBottom: '1px solid var(--line-1)',
            background: isCustom ? 'var(--action-tint)' : 'var(--bg-3)',
            fontSize: 12.5,
            lineHeight: 1.5,
            color: isCustom ? 'var(--action)' : 'var(--fg-3)',
          }}
        >
          {isCustom
            ? `Custom rule · ${scope} · editable, runs after the golden ruleset`
            : `Managed by ASComply · inherited from golden ruleset ${GOLDEN_SET.id} ${GOLDEN_SET.version} · read-only`}
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <div className="label" style={{ marginBottom: 12 }}>
            Parameters
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 24 }}>
            {rule.params.map((p) => (
              <div key={p.label}>
                <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
                  {p.label}
                </div>
                <div className="pf-input" style={{ display: 'flex', alignItems: 'center', height: 36 }}>
                  <span className="mono" style={{ fontSize: 12.5, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {p.value}
                  </span>
                </div>
              </div>
            ))}
            <div>
              <div className="label" style={{ marginBottom: 5, textTransform: 'none', letterSpacing: 0 }}>
                Failure message
              </div>
              <div className="pf-input" style={{ display: 'flex', alignItems: 'center', height: 36, fontSize: 12.5, color: 'var(--fg-1)' }}>
                {rule.message}
              </div>
            </div>
          </div>

          <div className="label" style={{ marginBottom: 10 }}>
            Underlying rule JSON
          </div>
          <pre className="pf-json">{ruleJSON(rule, sourceRef, enabled)}</pre>
        </div>

        <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Live status</span>
            {/* An inherited rule has no off state to report, so it says ALWAYS ON
                rather than borrowing the custom rule's LIVE/OFF vocabulary. */}
            <span
              className="mono"
              style={{ fontSize: 10.5, fontWeight: 700, color: !isCustom || enabled ? 'var(--status-green-text)' : 'var(--fg-3)' }}
            >
              {!isCustom ? 'ALWAYS ON' : enabled ? 'LIVE' : 'OFF'}
            </span>
          </div>
          {onRemove && (
            <button
              type="button"
              onClick={onRemove}
              className="pf-btn"
              style={{ border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', cursor: 'pointer', height: 36, padding: '0 14px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)' }}
            >
              Remove rule
            </button>
          )}
        </div>
      </div>
    </>
  )
}
