import markUrl from '@invoice-os/design-tokens/v2/assets/mark.png'

// Dark lockup of the DS Logo. The wordmark is live text, so the mark is decorative.
export function Logo({ size = 28 }: { size?: number }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 10 }}>
      <img
        src={markUrl}
        alt=""
        aria-hidden="true"
        width={size}
        height={size}
        style={{ display: 'block', borderRadius: 'var(--radius-md)' }}
      />
      <span style={{ display: 'inline-flex', flexDirection: 'column', gap: 3 }}>
        <span
          style={{
            fontFamily: 'var(--font-display)',
            fontSize: Math.round(size * 0.58),
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
            fontSize: size >= 32 ? 9 : 8,
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
