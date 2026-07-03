// Shared icon glyphs, pre-built as <Icon> nodes (mirrors landing/src/data.tsx). Ported
// 1:1 from the prototype's `this.ic(paths, size, sw)` calls in `renderVals()`
// (Platform.dc.html ~L1269-1304, 1288-1289, 1511).

import type { ReactNode } from 'react'
import { Icon } from './icons'

export const chevDownGlyph = <Icon paths={['m6 9 6 6 6-6']} size={16} />
export const tickGlyph11 = <Icon paths={['M20 6 9 17l-5-5']} size={11} strokeWidth={3} />
export const tickGlyph13 = <Icon paths={['M20 6 9 17l-5-5']} size={13} strokeWidth={3} />
export const crossGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={11} strokeWidth={3} />
export const warnGlyph = <Icon paths={['M12 9v4M12 17h.01']} size={12} strokeWidth={3} />
export const plusGlyph = <Icon paths={['M12 5v14M5 12h14']} size={15} strokeWidth={2} />
export const searchGlyph = <Icon paths={['M21 21l-4.3-4.3', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={15} />
export const gearGlyph = (
  <Icon
    paths={[
      'M12.2 2h-.4a2 2 0 0 0-2 2v.2a2 2 0 0 1-1 1.7l-.4.3a2 2 0 0 1-2 0l-.2-.1a2 2 0 0 0-2.7.7l-.2.4a2 2 0 0 0 .7 2.7l.2.1a2 2 0 0 1 1 1.7v.5a2 2 0 0 1-1 1.7l-.2.1a2 2 0 0 0-.7 2.7l.2.4a2 2 0 0 0 2.7.7l.2-.1a2 2 0 0 1 2 0l.4.3a2 2 0 0 1 1 1.7v.2a2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2v-.2a2 2 0 0 1 1-1.7l.4-.3a2 2 0 0 1 2 0l.2.1a2 2 0 0 0 2.7-.7l.2-.4a2 2 0 0 0-.7-2.7l-.2-.1a2 2 0 0 1-1-1.7v-.5a2 2 0 0 1 1-1.7l.2-.1a2 2 0 0 0 .7-2.7l-.2-.4a2 2 0 0 0-2.7-.7l-.2.1a2 2 0 0 1-2 0l-.4-.3a2 2 0 0 1-1-1.7V4a2 2 0 0 0-2-2Z',
      'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z',
    ]}
    size={16}
  />
)
export const shieldGlyph = <Icon paths={['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']} size={16} />
export const importGlyph = <Icon paths={['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']} size={14} />
export const downloadGlyph = <Icon paths={['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3']} size={15} />
export const sendGlyph = <Icon paths={['M22 2 11 13', 'M22 2l-7 20-4-9-9-4 20-7Z']} size={15} />
export const docGlyph2 = <Icon paths={['m18 16 4-4-4-4', 'm6 8-4 4 4 4', 'm14.5 4-5 16']} size={15} />
export const copyGlyph = (
  <Icon
    paths={[
      'M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2',
      'M9 2h6a1 1 0 0 1 1 1v2a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1Z',
    ]}
    size={13}
  />
)
export const docGlyph = <Icon paths={['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6']} size={20} />
export const rocketGlyph = (
  <Icon
    paths={[
      'M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z',
      'M12 15l-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z',
      'M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0',
      'M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5',
    ]}
    size={22}
  />
)
export const closeGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={16} strokeWidth={2} />
export const firmModeIcon = <Icon paths={['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']} size={14} />
export const inhouseModeIcon = (
  <Icon paths={['M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z', 'M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2', 'M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2', 'M10 6h4', 'M10 10h4', 'M10 14h4']} size={14} />
)

export type NavDef = { id: 'dashboard' | 'invoices' | 'clients' | 'approvals' | 'customers' | 'reports' | 'settings'; label: string; glyph: ReactNode }

export const NAV_DASHBOARD: NavDef = { id: 'dashboard', label: 'Overview', glyph: <Icon paths={['M3 13h8V3H3zM13 21h8V11h-8zM13 3v6h8V3zM3 21h8v-6H3z']} size={17} /> }
export const NAV_INVOICES: NavDef = {
  id: 'invoices',
  label: 'Invoices',
  glyph: <Icon paths={['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'M16 13H8M16 17H8']} size={17} />,
}
export const NAV_CLIENTS: NavDef = { id: 'clients', label: 'Clients', glyph: firmModeIcon }
export const NAV_APPROVALS: NavDef = {
  id: 'approvals',
  label: 'Approvals',
  glyph: <Icon paths={['M9 5H7a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-2', 'M9 5a2 2 0 0 1 2-2h2a2 2 0 0 1 2 2', 'm9 14 2 2 4-4']} size={17} />,
}
export const NAV_CUSTOMERS: NavDef = {
  id: 'customers',
  label: 'Customers',
  glyph: <Icon paths={['M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2', 'M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z']} size={17} />,
}
export const NAV_REPORTS: NavDef = { id: 'reports', label: 'Reports', glyph: <Icon paths={['M3 3v18h18', 'm19 9-5 5-4-4-3 3']} size={17} /> }
export const NAV_SETTINGS: NavDef = {
  id: 'settings',
  label: 'Settings',
  glyph: (
    <Icon
      paths={[
        'M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2Z',
        'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z',
      ]}
      size={17}
    />
  ),
}
