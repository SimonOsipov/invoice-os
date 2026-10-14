import { Icon } from './icons'
import type { NavItem, Screen } from './types'

// Each live screen adds its entry.
export const NAV_ITEMS: NavItem[] = [
  {
    key: 'rules',
    label: 'Rules',
    glyph: <Icon paths={['m9 12 2 2 4-4', 'M12 3a9 9 0 1 0 9 9 9 9 0 0 0-9-9Z']} size={17} />,
  },
  {
    key: 'tenants',
    label: 'Tenants',
    glyph: <Icon paths={['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']} size={17} />,
  },
]

export const CRUMB_BY_SCREEN: Record<Screen, string> = {
  submissions: 'Submissions ops',
  rules: 'Rules admin',
  audit: 'Audit & evidence',
  tenants: 'Tenants',
  health: 'System health',
  ai: 'AI usage',
}
