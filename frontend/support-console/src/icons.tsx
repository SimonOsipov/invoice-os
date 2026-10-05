// Icon primitives. The prototype built its SVGs imperatively via a `g(paths, size)`
// helper; here they are a single declarative component.
// All icons are stroke-based, 24x24 viewBox, currentColor — the parent sets color/size
// to match the design.

import markUrl from '@invoice-os/design-tokens/v2/assets/mark.png'

type IconProps = {
  paths: string[]
  size?: number
  strokeWidth?: number
}

export function Icon({ paths, size = 16, strokeWidth = 1.6 }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths.map((d, i) => (
        <path key={i} d={d} />
      ))}
    </svg>
  )
}

// ASComply brand mark. RASTER ONLY — the design system says so in bold. Decorative; the
// "ASComply" wordmark beside every call site is live text.
export function BrandMark({ size = 26 }: { size?: number }) {
  return <img src={markUrl} alt="" aria-hidden="true" width={size} height={size} style={{ display: 'block', borderRadius: 'var(--radius-md)' }} />
}
