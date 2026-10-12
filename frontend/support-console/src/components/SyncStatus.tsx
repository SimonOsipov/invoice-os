// A sync's status is an open string from the sync log; only `held` changes wording.
const TONE: Record<string, { bg: string; border: string; text: string }> = {
  applied: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  held: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
}
const OTHER = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' }

export const isHeld = (status: string): boolean => status === 'held'

export function SyncChip({ status }: { status: string }) {
  const tone = Object.hasOwn(TONE, status) ? TONE[status] : OTHER
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px', flex: 'none' }}>
      <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
        {status.toUpperCase()}
      </span>
    </span>
  )
}
