import { severityStyle } from '../helpers'
import type { Severity } from '../types'

// The severity pill the Rules table renders.

type Props = {
  /** Token triplet + label to render. */
  style: { bg: string; border: string; text: string; label: string }
  fontSize: number
}

export function Badge({ style, fontSize }: Props) {
  return (
    <span
      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: style.bg, border: `1px solid ${style.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px' }}
    >
      <span className="mono" style={{ fontSize, fontWeight: 700, color: style.text, letterSpacing: '0.03em' }}>
        {style.label}
      </span>
    </span>
  )
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  return <Badge style={severityStyle(severity)} fontSize={9} />
}
