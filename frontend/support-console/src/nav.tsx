import { Icon } from './icons'
import type { NavItem, Screen } from './types'

// proto:820. Five operations screens — this console has no "Overview": Submissions ops
// IS the landing screen, because a support engineer arrives here to work a queue.
export const NAV_ITEMS: NavItem[] = [
  { key: 'submissions', label: 'Submissions', glyph: <Icon paths={['M3 12h4l2 5 4-12 2 7h6']} size={17} /> },
  {
    key: 'rules',
    label: 'Rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={17} />,
  },
  { key: 'audit', label: 'Audit', glyph: <Icon paths={['M21 8v13H3V8', 'M1 3h22v5H1z', 'M10 12h4']} size={17} /> },
  {
    key: 'tenants',
    label: 'Tenants',
    glyph: (
      <Icon
        paths={[
          'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2',
          'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z',
          'M22 21v-2a4 4 0 0 0-3-3.87',
        ]}
        size={17}
      />
    ),
  },
  { key: 'health', label: 'System health', glyph: <Icon paths={['M22 12h-4l-3 9L9 3l-3 9H2']} size={17} /> },
]

// proto:832. The crumb differs from the nav label on three of the five screens.
export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  submissions: 'Submissions ops',
  rules: 'Rules admin',
  audit: 'Audit & evidence',
  tenants: 'Tenants',
  health: 'System health',
  ai: 'AI usage',
}
