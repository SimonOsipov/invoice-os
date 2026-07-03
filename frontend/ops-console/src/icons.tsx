// Icon primitives. The prototype built its SVGs imperatively via a `g(paths,
// size)` helper (React.createElement); here they are a single declarative
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

// Four-square brand mark. currentColor drives the three lighter squares so it
// adapts to its context; the anchor square stays teal-600.
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true">
      <rect x="0" y="0" width="9" height="9" rx="1.5" fill="#26735A" />
      <rect x="11" y="0" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
      <rect x="0" y="11" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
      <rect x="11" y="11" width="9" height="9" rx="1.5" fill="currentColor" opacity="0.82" />
    </svg>
  )
}
