// Sidebar — brand, company switcher (firm) / single-company card (in-house), nav list
// with badges, user footer. The workspace type is fixed by the signed-in persona (no
// firm/in-house toggle — see App.tsx), so this renders one workspace, not a switch.
// Ported from Platform.dc.html ~L40-117 (markup) + slices of `renderVals()` (~L1284-1310).

import { BrandMark, Icon } from '../icons'
import {
  chevDownGlyph,
  NAV_APPROVALS,
  NAV_CLIENTS,
  NAV_CUSTOMERS,
  NAV_DASHBOARD,
  NAV_INVOICES,
  NAV_REPORTS,
  NAV_SETTINGS,
  NAV_VALIDATION,
  tickGlyph11,
  type NavDef,
} from '../glyphs'
import type { PlatformCtx } from '../types'

type SidebarNavItem = NavDef & { badge?: string | null }

export function Sidebar({ ctx }: { ctx: PlatformCtx }) {
  const { user, mode, active, clients, activeIdx, switcherOpen, view, filter } = ctx
  const isFirm = mode === 'firm'
  const isInhouse = !isFirm
  const orgLabel = isFirm ? 'OKAFOR & PARTNERS' : active.short.toUpperCase() + ' · FINANCE'

  const navDef: SidebarNavItem[] = [
    NAV_DASHBOARD,
    { ...NAV_INVOICES, badge: active.onboarding ? null : typeof active.failing === 'number' && active.failing > 0 ? String(active.failing) : null },
    NAV_VALIDATION,
    isFirm ? NAV_CLIENTS : { ...NAV_APPROVALS, badge: active.onboarding ? null : active.pending ? String(active.pending) : null },
    ...(isFirm ? [NAV_CUSTOMERS] : []),
    NAV_REPORTS,
    NAV_SETTINGS,
  ]
  let activeNav: string = view === 'create' || view === 'detail' ? 'invoices' : view
  if (isInhouse && view === 'invoices' && filter === 'Pending') activeNav = 'approvals'

  return (
    <aside className="pf-sidebar" style={{ width: 252, flex: 'none', background: 'var(--bg-2)', borderRight: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column' }}>
      <div style={{ padding: '16px 16px 14px', borderBottom: '1px solid var(--line-1)' }}>
        <a href="#" style={{ display: 'flex', alignItems: 'center', gap: 9, color: 'var(--fg-1)', marginBottom: 14 }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 15, letterSpacing: '-0.02em' }}>FiscalBridge</span>
          <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
            AFRICA
          </span>
        </a>

        {/* company switcher (firm mode) */}
        {isFirm && (
          <div style={{ position: 'relative' }}>
            <button
              onClick={ctx.toggleSwitcher}
              className="pf-btn"
              style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: `1px solid ${switcherOpen ? 'var(--accent)' : 'var(--line-2)'}`, borderRadius: 'var(--radius-lg)', padding: '8px 10px', cursor: 'pointer', textAlign: 'left' }}
            >
              <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-md)', background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{active.initials}</span>
              <span style={{ flex: 1, minWidth: 0 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{active.short}</span>
                <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>TIN {active.tin}</span>
              </span>
              <span style={{ flex: 'none', color: 'var(--fg-3)', transform: switcherOpen ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}>{chevDownGlyph}</span>
            </button>
            {switcherOpen && (
              <div style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0, right: 0, zIndex: 60, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', boxShadow: '0 16px 40px -16px rgba(20,23,26,0.28)', overflow: 'hidden', animation: 'popIn 140ms ease-out' }}>
                <div className="label" style={{ padding: '10px 12px 6px' }}>
                  Switch company
                </div>
                {clients.map((c, i) => (
                  <button
                    key={c.name}
                    onClick={() => ctx.switchClient(i)}
                    className="pf-menu-item"
                    style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 10, border: 0, background: i === activeIdx ? 'var(--bg-3)' : 'transparent', cursor: 'pointer', textAlign: 'left', padding: '9px 12px' }}
                  >
                    <span style={{ flex: 'none', width: 26, height: 26, borderRadius: 'var(--radius-md)', background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 10, fontWeight: 700 }}>{c.initials}</span>
                    <span style={{ flex: 1, minWidth: 0 }}>
                      <span style={{ display: 'block', fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.short}</span>
                      <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                        {c.score == null ? '—%' : c.score + '%'} ready · {c.onboarding ? '—' : c.failing} failing
                      </span>
                    </span>
                    <span style={{ flex: 'none', color: 'var(--accent)' }}>{i === activeIdx ? tickGlyph11 : ''}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
        )}

        {/* in-house mode: single company, no switching */}
        {isInhouse && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', padding: '8px 10px' }}>
            <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-md)', background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{active.initials}</span>
            <span style={{ flex: 1, minWidth: 0 }}>
              <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{active.short}</span>
              <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                WORKSPACE
              </span>
            </span>
            <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 5, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 999, padding: '2px 7px' }}>
              <span style={{ width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} />
              <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--status-green-text)', letterSpacing: '0.04em' }}>
                ERP
              </span>
            </span>
          </div>
        )}
      </div>

      <nav className="pf-nav-list" style={{ flex: 1, padding: '12px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        <div className="label" style={{ padding: '6px 8px 8px' }}>
          Workspace
        </div>
        {navDef.map((n) => {
          const a = n.id === activeNav
          return (
            <button
              key={n.id}
              onClick={() => ctx.nav(n.id)}
              className="pf-nav"
              style={{ display: 'flex', alignItems: 'center', gap: 11, width: '100%', border: 0, cursor: 'pointer', borderRadius: 'var(--radius-md)', padding: '9px 10px', textAlign: 'left', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: a ? 600 : 500, background: a ? 'var(--bg-3)' : 'transparent', color: a ? 'var(--fg-1)' : 'var(--fg-2)', position: 'relative' }}
            >
              <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 'var(--radius-xs)', background: a ? 'var(--accent)' : 'transparent' }} />
              <span style={{ color: a ? 'var(--accent)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
              <span style={{ flex: 1 }}>{n.label}</span>
              {n.badge && (
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', borderRadius: 99, padding: '1px 6px' }}>
                  {n.badge}
                </span>
              )}
            </button>
          )
        })}
      </nav>

      <div style={{ padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 99, background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 600 }}>{user.initials}</span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{user.name}</div>
          <div className="mono" style={{ display: 'flex', alignItems: 'center', gap: 5, fontSize: 10, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden' }}>
            {/* When /me verified, show the tenant name resolved from the live backend with a
                green dot; otherwise fall back to the mode-derived workspace label. */}
            {user.verified && user.tenantName ? (
              <>
                <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} title="Tenant verified via /v1/me" />
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{user.tenantName.toUpperCase()}</span>
              </>
            ) : (
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{orgLabel}</span>
            )}
          </div>
        </div>
        {/* Sign out (M3-07-03). Replaces the old decorative gear: the gear read as
            "settings" (already a nav item) and had no handler — this footer slot now holds
            one real action. Default/hover color live in `.pf-signout` (platform.css) so the
            :hover token can win (an inline color would beat the hover rule). */}
        <button
          onClick={ctx.signOut}
          className="pf-btn pf-signout"
          aria-label="Sign out"
          title="Sign out"
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 28, height: 28, padding: 0, border: 0, borderRadius: 'var(--radius-md)', background: 'transparent', cursor: 'pointer' }}
        >
          <Icon paths={['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9']} size={16} />
        </button>
      </div>
    </aside>
  )
}
