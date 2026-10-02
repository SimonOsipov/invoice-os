import markUrl from '@invoice-os/design-tokens/v2/assets/mark.png'

// The accessible name comes from the caller's link; the mark and wordmark are decorative here.
export function Logo({ size = 32 }: { size?: number }) {
  return (
    <span className="ds-logo">
      <img className="ds-logo-mark" src={markUrl} alt="" aria-hidden="true" width={size} height={size} />
      <span className="ds-logo-word">
        <span className="ds-logo-name" style={{ fontSize: Math.round(size * 0.58) }}>ASComply</span>
        <span className="ds-logo-region" style={{ fontSize: size >= 32 ? 9 : 8 }}>AFRICA</span>
      </span>
    </span>
  )
}
