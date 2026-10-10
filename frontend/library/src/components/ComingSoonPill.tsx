import type { CSSProperties } from 'react'

const TONES: Record<'light' | 'dark', CSSProperties> = {
  light: {
    background: 'var(--status-progress-bg)',
    color: 'var(--status-progress-fg)',
    border: '1px solid var(--status-progress-border)',
  },
  dark: {
    background: 'var(--on-dark-10)',
    color: 'var(--surface-foreground)',
    border: '1px solid var(--on-dark-20)',
  },
}

// DS Badge shape. Callers add placement (flex: none, align-self) through `style`.
export function ComingSoonPill({ tone, style }: { tone: 'light' | 'dark'; style?: CSSProperties }) {
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        height: 26,
        padding: '0 10px',
        borderRadius: 'var(--radius-sm)',
        fontFamily: 'var(--font-sans)',
        fontSize: 'var(--fs-xs)',
        fontWeight: 'var(--fw-semibold)',
        whiteSpace: 'nowrap',
        ...TONES[tone],
        ...style,
      }}
    >
      Coming soon
    </span>
  )
}
