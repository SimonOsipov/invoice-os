const TONES = {
  red: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' },
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
} as const

// The boxed fault line of the test and publish modals.
export function Fault({ text, tone = 'red' }: { text: string; tone?: keyof typeof TONES }) {
  const t = TONES[tone]
  return (
    <div role={tone === 'red' ? 'alert' : undefined} style={{ margin: '8px 0', padding: '9px 12px', borderRadius: 'var(--radius-md)', background: t.bg, border: `1px solid ${t.border}`, color: t.text, fontSize: 12.5, lineHeight: 1.5, overflowWrap: 'anywhere' }}>
      {text}
    </div>
  )
}
