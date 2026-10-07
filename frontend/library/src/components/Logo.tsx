import markUrl from '@invoice-os/design-tokens/v2/assets/mark.png'

// Dark lockup of the DS Logo. The wordmark is live text, so the mark is decorative.
export function Logo() {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 10 }}>
      <img
        src={markUrl}
        alt=""
        aria-hidden="true"
        width={28}
        height={28}
        style={{ display: 'block', borderRadius: 'var(--radius-md)' }}
      />
      <span style={{ display: 'inline-flex', flexDirection: 'column', gap: 3 }}>
        <span
          style={{
            fontFamily: 'var(--font-display)',
            fontSize: 16,
            fontWeight: 'var(--fw-bold)',
            lineHeight: 1,
            letterSpacing: '-0.02em',
            color: 'var(--surface-foreground)',
          }}
        >
          ASComply
        </span>
        <span
          style={{
            fontFamily: 'var(--font-sans)',
            fontSize: 8,
            fontWeight: 'var(--fw-bold)',
            lineHeight: 1,
            letterSpacing: 'var(--tracking-brand)',
            textTransform: 'uppercase',
            color: 'var(--eyebrow-on-dark)',
          }}
        >
          AFRICA
        </span>
      </span>
    </span>
  )
}
