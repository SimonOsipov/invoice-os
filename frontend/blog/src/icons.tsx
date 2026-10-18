const circlePath = (cx: number, cy: number, r: number) =>
  `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`

export type GlyphName =
  | 'chevron-right'
  | 'layout-dashboard'
  | 'mail'
  | 'search'
  | 'package'
  | 'landmark'
  | 'building-2'
  | 'megaphone'
  | 'handshake'
  | 'code-xml'
  | 'globe'
  | 'sparkles'
  | 'shield-check'
  | 'file-check'
  | 'chart-column'
  | 'workflow'
  | 'plug'
  | 'archive'
  | 'key-round'
  | 'clock'
  | 'file-text'
  | 'arrow-right'
  | 'chevron-left'
  | 'circle-check'
  | 'calendar-clock'
  | 'play'
  | 'rotate-cw'
  | 'x'

// lucide-static@1.8.0 path data for the shell glyphs; the category and article glyphs are
// lucide-static@0.428.0 (frontend/ops-console/src/glyphs.ts; rotate-cw, x from frontend/library/src/icons.tsx).
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
  'package': [
    'm7.5 4.27 9 5.15',
    'M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z',
    'm3.3 7 8.7 5 8.7-5',
    'M12 22V12',
  ],
  'landmark': [
    'M3 22L21 22',
    'M6 18L6 11',
    'M10 18L10 11',
    'M14 18L14 11',
    'M18 18L18 11',
    'M12 2L20 7L4 7Z',
  ],
  'building-2': [
    'M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z',
    'M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2',
    'M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2',
    'M10 6h4',
    'M10 10h4',
    'M10 14h4',
    'M10 18h4',
  ],
  'megaphone': [
    'm3 11 18-5v12L3 14v-3z',
    'M11.6 16.8a3 3 0 1 1-5.8-1.6',
  ],
  'handshake': [
    'm11 17 2 2a1 1 0 1 0 3-3',
    'm14 14 2.5 2.5a1 1 0 1 0 3-3l-3.88-3.88a3 3 0 0 0-4.24 0l-.88.88a1 1 0 1 1-3-3l2.81-2.81a5.79 5.79 0 0 1 7.06-.87l.47.28a2 2 0 0 0 1.42.25L21 4',
    'm21 3 1 11h-2',
    'M3 3 2 14l6.5 6.5a1 1 0 1 0 3-3',
    'M3 4h8',
  ],
  'code-xml': [
    'm18 16 4-4-4-4',
    'm6 8-4 4 4 4',
    'm14.5 4-5 16',
  ],
  'globe': [
    'M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0',
    'M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20',
    'M2 12h20',
  ],
  'sparkles': [
    'M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z',
    'M20 3v4',
    'M22 5h-4',
    'M4 17v2',
    'M5 18H3',
  ],
  'shield-check': [
    'M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z',
    'm9 12 2 2 4-4',
  ],
  'file-check': [
    'M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z',
    'M14 2v4a2 2 0 0 0 2 2h4',
    'm9 15 2 2 4-4',
  ],
  'chart-column': [
    'M3 3v16a2 2 0 0 0 2 2h16',
    'M18 17V9',
    'M13 17V5',
    'M8 17v-3',
  ],
  'workflow': [
    'M5 3h4a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-4a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2Z',
    'M7 11v4a2 2 0 0 0 2 2h4',
    'M15 13h4a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-4a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2Z',
  ],
  'plug': [
    'M12 22v-5',
    'M9 8V2',
    'M15 8V2',
    'M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z',
  ],
  'archive': [
    'M3 3h18a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-18a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z',
    'M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8',
    'M10 12h4',
  ],
  'key-round': [
    'M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z',
    'M16 7.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0',
  ],
  'clock': [
    'M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0',
    'M12 6L12 12L16 14',
  ],
  'file-text': [
    'M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z',
    'M14 2v4a2 2 0 0 0 2 2h4',
    'M10 9H8',
    'M16 13H8',
    'M16 17H8',
  ],
  'arrow-right': [
    'M5 12h14',
    'm12 5 7 7-7 7',
  ],
  'chevron-left': [
    'm15 18-6-6 6-6',
  ],
  'circle-check': [
    'M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0',
    'm9 12 2 2 4-4',
  ],
  'calendar-clock': [
    'M21 7.5V6a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h3.5',
    'M16 2v4',
    'M8 2v4',
    'M3 10h5',
    'M17.5 17.5 16 16.3V14',
    'M10 16a6 6 0 1 0 12 0a6 6 0 1 0 -12 0',
  ],
  'play': [
    'M6 3L20 12L6 21L6 3Z',
  ],
  'rotate-cw': [
    'M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8',
    'M21 3v5h-5',
  ],
  'x': [
    'M18 6 6 18',
    'm6 6 12 12',
  ],
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
