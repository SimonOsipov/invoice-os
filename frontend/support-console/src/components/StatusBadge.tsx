import { severityStyle, statusStyle } from '../helpers'
import type { JobState, Severity } from '../types'

// The status pill the prototype repeats inline on every table, drawer and list
// (proto:181, 207, 260, 298, 415, 463). One component instead of nine copies of the same
// span-in-a-span — the only variation is whether a leading dot is drawn.

type Props = {
  /** Token triplet + label to render. */
  style: { bg: string; border: string; text: string; label: string }
  dot?: boolean
  fontSize?: number
}

export function Badge({ style, dot = false, fontSize = 9.5 }: Props) {
  return (
    <span
      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: style.bg, border: `1px solid ${style.border}`, borderRadius: 999, padding: '2px 8px' }}
    >
      {dot && <span style={{ width: 6, height: 6, borderRadius: 99, background: style.text }} />}
      <span className="mono" style={{ fontSize, fontWeight: 700, color: style.text, letterSpacing: '0.03em' }}>
        {style.label}
      </span>
    </span>
  )
}

export function StateBadge({ state, dot = true, fontSize }: { state: JobState; dot?: boolean; fontSize?: number }) {
  return <Badge style={statusStyle(state)} dot={dot} fontSize={fontSize} />
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  return <Badge style={severityStyle(severity)} fontSize={9} />
}
