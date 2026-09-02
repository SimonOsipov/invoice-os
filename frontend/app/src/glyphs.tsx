// Shared icon glyphs, pre-built as <Icon> nodes (mirrors landing/src/data.tsx). Ported
// 1:1 from the prototype's `this.ic(paths, size, sw)` calls in `renderVals()`
// (Platform.dc.html ~L1269-1304, 1288-1289, 1511).

import type { CSSProperties, ReactNode } from 'react'
import { Icon } from './icons'

export const chevDownGlyph = <Icon paths={['m6 9 6 6 6-6']} size={16} />
export const tickGlyph11 = <Icon paths={['M20 6 9 17l-5-5']} size={11} strokeWidth={3} />
export const tickGlyph13 = <Icon paths={['M20 6 9 17l-5-5']} size={13} strokeWidth={3} />
export const crossGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={11} strokeWidth={3} />
export const gripGlyph = <Icon paths={['M9 5h.01', 'M9 12h.01', 'M9 19h.01', 'M15 5h.01', 'M15 12h.01', 'M15 19h.01']} size={13} strokeWidth={2.4} />
export const xSmallGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={11} strokeWidth={2.4} />
export const plusGlyph = <Icon paths={['M12 5v14M5 12h14']} size={15} strokeWidth={2} />
export const searchGlyph = <Icon paths={['M21 21l-4.3-4.3', 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z']} size={15} />
export const shieldGlyph = <Icon paths={['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']} size={16} />
// Env-banner pair, both at 15 so the two states swap without shifting the row
// (mirrors ops-console TopBar.tsx's SHIELD_ICON / SANDBOX_ICON).
export const shieldGlyph15 = <Icon paths={['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']} size={15} />
export const flaskGlyph = <Icon paths={['M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3', 'M7.5 14h9']} size={15} />
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
export const backGlyph = <Icon paths={['M19 12H5', 'm12 19-7-7 7-7']} size={14} />
export const refreshGlyph = <Icon paths={['M21 4v6h-6', 'M3 20v-6h6', 'M3.5 9a9 9 0 0 1 14.9-3.4L21 8', 'M20.5 15a9 9 0 0 1-14.9 3.4L3 16']} size={14} />
export const warnTriGlyph = <Icon paths={['m21.7 18-8-14a2 2 0 0 0-3.5 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.7-3Z', 'M12 9v4', 'M12 17h.01']} size={16} />
export const infoGlyph = <Icon paths={['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'M12 11v5', 'M12 8h.01']} size={15} />
// Recognition Review's point button. A crosshair, NOT crossGlyph: that one is an X and would
// read as "close" on a control that opens a gesture.
export const crosshairGlyph = (
  <Icon paths={['M12 2v4', 'M12 18v4', 'M2 12h4', 'M18 12h4', 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z']} size={16} />
)

// Rules screen: the lock replaces the toggle on every inherited row, and the star
// heads the "Suggested for you" card (same glyph the Support Console's learned-rules
// inbox uses, so the two surfaces read as the same idea).
export const lockGlyph = <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={11} />
export const sparkGlyph = <Icon paths={['M12 3 14.09 8.26 20 9.27l-4 3.64L17.18 19 12 16.1 6.82 19 8 12.91l-4-3.64 5.91-1.01z']} size={15} />

// Settings › Members: the per-row `⋯` overflow-menu trigger, and the only new icon the
// members story adds. There was no row menu anywhere in the app before it, so nothing
// existing could be reused. Hand-authored like everything else here — the gripGlyph
// idiom exactly (same 13 / 2.4 pair), one column of dots instead of two.
export const moreGlyph = <Icon paths={['M12 5h.01', 'M12 12h.01', 'M12 19h.01']} size={13} strokeWidth={2.4} />

export type NavDef = { id: 'dashboard' | 'invoices' | 'workflows' | 'rules' | 'clients' | 'approvals' | 'customers' | 'reports' | 'audit' | 'settings'; label: string; glyph: ReactNode }

// Every NAV_* glyph renders at this size; the icon column is sized to fit it with room to
// spare so label x-offset never depends on which glyph is in play (Sidebar.tsx:234).
export const NAV_ICON_SIZE = 17
export const NAV_ICON_COL = 18
export const navIconColStyle: CSSProperties = {
  flex: 'none',
  display: 'inline-flex',
  justifyContent: 'center',
  alignItems: 'center',
  width: NAV_ICON_COL,
  height: NAV_ICON_COL,
}

export const NAV_DASHBOARD: NavDef = { id: 'dashboard', label: 'Overview', glyph: <Icon paths={['M3 13h8V3H3zM13 21h8V11h-8zM13 3v6h8V3zM3 21h8v-6H3z']} size={17} /> }
export const NAV_INVOICES: NavDef = {
  id: 'invoices',
  label: 'Invoices',
  glyph: <Icon paths={['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'M16 13H8M16 17H8']} size={17} />,
}
// Former firmModeIcon glyph, inlined at NAV_ICON_SIZE (was its sole consumer, at 14px).
export const NAV_CLIENTS: NavDef = {
  id: 'clients',
  label: 'Clients',
  glyph: <Icon paths={['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M22 21v-2a4 4 0 0 0-3-3.87']} size={NAV_ICON_SIZE} />,
}
// git-branch. The approval-policy builder — the prototype's own Workflows glyph,
// carried over unchanged so the two surfaces read as the same screen.
export const NAV_WORKFLOWS: NavDef = {
  id: 'workflows',
  label: 'Workflows',
  glyph: <Icon paths={['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M15 6a9 9 0 0 0-9 9']} size={17} />,
}
// list-checks. Rules sits directly after Workflows, which is where the brief places it.
export const NAV_RULES: NavDef = {
  id: 'rules',
  label: 'Rules',
  glyph: <Icon paths={['m3 17 2 2 4-4', 'm3 7 2 2 4-4', 'M13 6h8', 'M13 12h8', 'M13 18h8']} size={17} />,
}
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
// Audit is FIRM-WIDE, not client-scoped: the log spans the whole workspace, so in firm mode
// it belongs beside Clients and Settings, never in the group that follows the switcher.
// Scroll-with-a-seal glyph — the append-only record, not a generic list.
export const NAV_AUDIT: NavDef = {
  id: 'audit',
  label: 'Audit',
  glyph: (
    <Icon
      paths={['M8 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-3', 'M8 3v4h4', 'M17 4a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z', 'm17 10 2 4h-4z']}
      size={17}
    />
  ),
}
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
