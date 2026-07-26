// Icon primitives. The prototype built its SVGs imperatively via
// React.createElement (icMulti/ic helpers); here they are a single declarative
// component. All icons are stroke-based, 24x24 viewBox, currentColor — the
// parent sets color/size to match the design.

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

// Lucide `circle-check` — the design system's fixed semantic for "capability
// we provide". It carries confirmation, so it is always amber, never the plain
// `check` glyph and never teal. 2px stroke per the iconography rules.
export function CircleCheck({ size = 16 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="10" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  )
}

// Four-square brand mark. Placeholder: the design system's mark is a RASTER
// (assets/logo-mark.png) and must never be redrawn — this SVG is a stand-in until
// that file lands in the repo. currentColor throughout so it adapts to context;
// the anchor square previously carried #26735A, an emerald from the retired Base
// system that no token can reach inside an SVG presentation attribute.
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true">
      <rect x="0" y="0" width="9" height="9" rx="1.5" fill="currentColor" />
      <rect x="11" y="0" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
      <rect x="0" y="11" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
      <rect x="11" y="11" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
    </svg>
  )
}
