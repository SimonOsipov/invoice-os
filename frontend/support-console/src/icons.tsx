// Icon primitives. The prototype built its SVGs imperatively via a `g(paths, size)`
// helper (Support Console.dc.html:730); here they are a single declarative component,
// identical to frontend/ops-console/src/icons.tsx. All icons are stroke-based, 24x24
// viewBox, currentColor — the parent sets color/size to match the design.

import markUrl from '@invoice-os/design-tokens/assets/logo-mark.png'

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
export function BrandMark({ size = 22 }: { size?: number }) {
  return <img src={markUrl} alt="" aria-hidden="true" width={size} height={size} style={{ display: 'block' }} />
}
