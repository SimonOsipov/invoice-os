const circlePath = (cx: number, cy: number, r: number) =>
  `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`

export type GlyphName = 'chevron-right' | 'layout-dashboard' | 'mail' | 'search'

// lucide-static@1.8.0 path data.
export const GLYPHS: Record<GlyphName, readonly string[]> = {
  'chevron-right': ['m9 18 6-6-6-6'],
  'layout-dashboard': [
    'M4 3h5a1 1 0 0 1 1 1v7a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-7a1 1 0 0 1 1 -1Z',
    'M15 3h5a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z',
    'M15 12h5a1 1 0 0 1 1 1v7a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-7a1 1 0 0 1 1 -1Z',
    'M4 16h5a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z',
  ],
  'mail': ['M4 4h16a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2Z', 'm22 7-8.991 5.727a2 2 0 0 1-2.009 0L2 7'],
  'search': [circlePath(11, 11, 8), 'm21 21-4.34-4.34'],
}

type IconProps = { name: GlyphName; size?: number }

export function Icon({ name, size = 16 }: IconProps) {
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
      {GLYPHS[name].map((d, i) => (
        <path key={i} d={d} />
      ))}
    </svg>
  )
}
