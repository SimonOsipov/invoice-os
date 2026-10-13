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

// Shared glyphs.
export const SEARCH_ICON = <Icon paths={['M21 21l-4.35-4.35', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={16} />
export const CLOSE_ICON = <Icon paths={['M18 6 6 18M6 6l12 12']} size={15} />
export const CHECK_ICON = <Icon paths={['M20 6 9 17l-5-5']} size={16} />
export const ALERT_ICON = (
  <Icon paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']} size={18} />
)
export const GLOBE_ICON = (
  <Icon
    paths={['M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z', 'M3.6 9h16.8M3.6 15h16.8', 'M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18Z']}
    size={16}
  />
)
export const PUBLISH_ICON = <Icon paths={['M12 19V5', 'm5 12 7-7 7 7']} size={15} />
export const KILL_ICON = <Icon paths={['M18.36 6.64a9 9 0 1 1-12.73 0', 'M12 2v10']} size={15} />
export const SANDBOX_ICON = (
  <Icon paths={['M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3', 'M7.5 14h9']} size={15} />
)
export const SIGN_OUT_ICON = <Icon paths={['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9']} size={16} />
