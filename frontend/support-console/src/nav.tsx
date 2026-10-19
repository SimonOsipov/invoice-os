import { Icon } from './icons'
import { GLYPHS } from './glyphs'
import type { NavItem, Screen } from './types'

// Each live screen adds its entry.
export const NAV_ITEMS: NavItem[] = [
  {
    key: 'submissions',
    label: 'Submissions',
    glyph: <Icon paths={['M3 12h4l2 5 4-12 2 7h6']} size={17} />,
  },
  {
    key: 'rules',
    label: 'Rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={17} />,
  },
  {
    key: 'audit',
    label: 'Audit',
    glyph: <Icon paths={['M21 8v13H3V8', 'M1 3h22v5H1z', 'M10 12h4']} size={17} />,
  },
  {
    key: 'tenants',
    label: 'Tenants',
    glyph: <Icon paths={['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']} size={17} />,
  },
  {
    key: 'health',
    label: 'System health',
    glyph: <Icon paths={['M22 12h-4l-3 9L9 3l-3 9H2']} size={17} />,
  },
  {
    key: 'ai',
    label: 'AI usage',
    glyph: <Icon paths={['M7 5h10v14H7z', 'M10 9h4v6h-4z', 'M9 2v3', 'M15 2v3', 'M9 19v3', 'M15 19v3', 'M2 9h5', 'M2 15h5', 'M17 9h5', 'M17 15h5']} size={17} />,
  },
  {
    key: 'blog',
    label: 'Blog',
    glyph: <Icon paths={GLYPHS['file-text']} size={17} />,
  },
]

export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  submissions: 'Submissions ops',
  rules: 'Rules admin',
  audit: 'Audit & evidence',
  tenants: 'Tenants',
  health: 'System health',
  ai: 'AI usage',
  blog: 'Blog',
}
