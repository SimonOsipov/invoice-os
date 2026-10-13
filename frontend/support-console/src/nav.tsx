import { Icon } from './icons'
import type { NavItem, Screen } from './types'

// Each live screen adds its entry.
export const NAV_ITEMS: NavItem[] = [
  {
    key: 'rules',
    label: 'Rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={17} />,
  },
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
