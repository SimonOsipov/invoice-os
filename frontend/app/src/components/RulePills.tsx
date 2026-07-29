// The two pills the Rules table and its drawer both draw. Their own module so the
// drawer can use them without importing the view that renders the drawer — the same
// role support-console/src/components/StatusBadge.tsx plays over there.

import type { RuleSeverity } from '../lib/rules'

const SEVERITY_TONE: Record<RuleSeverity, { bg: string; border: string; text: string; label: string }> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'ERROR' },
  warn: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'WARN' },
  info: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'INFO' },
}

export function SeverityPill({ severity }: { severity: RuleSeverity }) {
  const tone = SEVERITY_TONE[severity]
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding: '2px 8px' }}>
      <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
        {tone.label}
      </span>
    </span>
  )
}

export function TypePill({ type }: { type: string }) {
  return (
    <span
      className="mono"
      style={{ fontSize: 10.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-sm)', padding: '2px 6px', justifySelf: 'start' }}
    >
      {type}
    </span>
  )
}
