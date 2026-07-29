// Sidebar — brand, company switcher (firm) / single-company card (in-house), nav list
// with badges, user footer. The workspace type is fixed by the signed-in persona (no
// firm/in-house toggle — see App.tsx), so this renders one workspace, not a switch.
// Ported from Platform.dc.html ~L40-117 (markup) + slices of `renderVals()` (~L1284-1310).
//
// [dashboard-scope-per-client] (persona-handoff-fix step 2): the Invoices/Approvals nav
// badges used to read active.failing/active.pending — a SAMPLE overlay (lib/clients.ts)
// with no "SAMPLE" marker a badge could ever carry, so a fabricated count there read as
// real. Both now come off this component's OWN rollup fetch (Decision [fetch-per-surface],
// same posture as ClientsView.tsx/DashboardActive.tsx — each live surface fetches
// independently rather than sharing one ctx-level rollup).

import { Fragment } from 'react'
import { gatewayBase, useAsync } from '@invoice-os/api-client'
import { BrandMark, Icon } from '../icons'
import { entityHealth, getRollup, scopedBucket, type Rollup } from '../lib/dashboard'
import { visibleEntityIds } from '../lib/portfolio'
import {
  chevDownGlyph,
  NAV_APPROVALS,
  NAV_CLIENTS,
  NAV_CUSTOMERS,
  NAV_DASHBOARD,
  NAV_INVOICES,
  NAV_REPORTS,
  NAV_RULES,
  NAV_SETTINGS,
  NAV_VALIDATION,
  tickGlyph11,
  type NavDef,
} from '../glyphs'
import type { PlatformCtx } from '../types'

type SidebarNavItem = NavDef & { badge?: string | null }

export function Sidebar({ ctx }: { ctx: PlatformCtx }) {
  const { user, mode, active, clients, switcherOpen, view, filter } = ctx
  const isFirm = mode === 'firm'
  const isInhouse = !isFirm
  const orgLabel = isFirm ? 'OKAFOR & PARTNERS' : active.short.toUpperCase() + ' · FINANCE'

  // Same `base ? … : …` narrowing + `immediate: base != null` idiom as ClientsView.tsx/
  // DashboardActive.tsx (no-gateway build stays at zero network). A slow/failed rollup
  // must not block the sidebar chrome — `bucket` below just stays null (both badges off)
  // until 'ready', the same neutral posture as ClientsView's HealthCell.
  const base = gatewayBase()
  const rollup = useAsync<Rollup>(
    () => (base ? getRollup(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const bucket = rollup.status === 'ready' && rollup.data ? scopedBucket(isInhouse, active.entityId, rollup.data) : null

  // persona-handoff-fix Task A: the switcher lists ACTIVE companies only — an archived
  // one is retired, and offering to switch INTO one reads as "still open for business".
  // `clients` itself stays the FULL (unfiltered) roster — App.tsx's `active` resolution
  // and switchClient() both key off it — only this dropdown's rendered options are
  // narrowed. `active.entityId` is always included even if archived (visibleEntityIds'
  // own comment): the workspace currently open must not vanish from its own switcher.
  const switcherIds = visibleEntityIds(ctx.entities, active.entityId)
  const switcherClients = clients.filter((c) => c.entityId != null && switcherIds.has(c.entityId))

  // "Switch company" dropdown row descriptor. Used to read c.score/c.failing — the SAME
  // fabricated overlay (lib/clients.ts) the two nav badges above just moved off, so a
  // company's own dropdown row still showing a made-up number one line away from that
  // same company's now-REAL nav badge (once selected) would be a same-screen
  // contradiction this fix would otherwise leave standing. score has no live source at
  // all (business_entities carries no readiness concept, db/seed.dev.sql) — rather than
  // pair one real and one fake figure with nothing to mark which is which, this drops
  // the score and reuses entityHealth (lib/dashboard.ts), the exact per-entity join
  // ClientsView.tsx's own health pill already uses, off this component's own rollup
  // fetch above. Not-yet-ready renders a neutral em dash, never the old numbers.
  function rowHealthLabel(entityId: string | null): string {
    if (rollup.status !== 'ready' || !rollup.data || entityId == null) return '—'
    const health = entityHealth(rollup.data.clients, entityId)
    if (health.kind === 'no-invoices') return 'no invoices yet'
    if (health.kind === 'clear') return 'all clear'
    return `${health.count} needing attention`
  }

  const invoicesItem: SidebarNavItem = {
    ...NAV_INVOICES,
    badge: active.onboarding || bucket == null ? null : bucket.needs_attention > 0 ? String(bucket.needs_attention) : null,
  }
  const approvalsItem: SidebarNavItem = {
    ...NAV_APPROVALS,
    // The real 7-state lifecycle has no "awaiting approval" status. `validated` (passed
    // the gate, not yet batch-submitted) is the closest live equivalent to the old mock's
    // Pending count — batch-submitting a selection IS the approval action in this
    // workflow (InvoicesList.tsx), so a validated invoice is exactly one still awaiting it.
    badge: active.onboarding || bucket == null ? null : bucket.counts.validated > 0 ? String(bucket.counts.validated) : null,
  }

  // The nav is GROUPED BY SCOPE, because a flat list gave no signal about which
  // destinations follow the client picked in the switcher above and which are the whole
  // firm's. In firm mode that is two groups split by a hairline; in-house has no client
  // scope to contrast with, so it stays one group and draws no divider.
  //
  // firmName uses the same source as the user-card footer below: the live tenant name only
  // once /v1/me has PROVEN it, otherwise the mode-derived label. Reusing `orgLabel` keeps
  // one answer to "what is this firm called" rather than two that can disagree.
  const firmName = user.verified && user.tenantName ? user.tenantName : orgLabel
  //
  // NAV_RULES is CLIENT-SCOPED in both modes: custom rules are stored per client
  // (lib/rules.ts), so in firm mode it belongs in the group that follows the
  // switcher, never the firm-wide one. It sits directly after Validation because it
  // configures the validation engine — see NAV_RULES in glyphs.tsx for why that is
  // the position the brief's "directly after Workflows" resolves to here.
  const navGroups: { key: string; label: string; scope: string; items: SidebarNavItem[] }[] = isFirm
    ? [
        { key: 'client', label: active.short, scope: 'CLIENT', items: [NAV_DASHBOARD, invoicesItem, NAV_VALIDATION, NAV_RULES, NAV_CUSTOMERS, NAV_REPORTS] },
        { key: 'firm', label: firmName, scope: 'FIRM-WIDE', items: [NAV_CLIENTS, NAV_SETTINGS] },
      ]
    : [
        {
          key: 'workspace',
          label: 'Workspace',
          scope: active.short,
          items: [NAV_DASHBOARD, invoicesItem, NAV_VALIDATION, NAV_RULES, approvalsItem, NAV_REPORTS, NAV_SETTINGS],
        },
      ]
  let activeNav: string = view === 'create' || view === 'detail' ? 'invoices' : view
  if (isInhouse && view === 'invoices' && filter === 'Pending') activeNav = 'approvals'

  return (
    <aside className="pf-sidebar" style={{ width: 252, flex: 'none', background: 'var(--bg-2)', borderRight: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column' }}>
      <div style={{ padding: '16px 16px 14px', borderBottom: '1px solid var(--line-1)' }}>
        <a href="#" style={{ display: 'flex', alignItems: 'center', gap: 9, color: 'var(--fg-1)', marginBottom: 14 }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 15, letterSpacing: '-0.02em' }}>ASComply</span>
          <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
            AFRICA
          </span>
        </a>

        {/* company switcher (firm mode) */}
        {isFirm && (
          <div style={{ position: 'relative' }}>
            <button
              onClick={ctx.toggleSwitcher}
              data-testid="company-switcher"
              className="pf-btn"
              style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: `1px solid ${switcherOpen ? 'var(--action)' : 'var(--line-2)'}`, borderRadius: 'var(--radius-input)', padding: '8px 10px', cursor: 'pointer', textAlign: 'left' }}
            >
              <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{active.initials}</span>
              <span style={{ flex: 1, minWidth: 0 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{active.short}</span>
                <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>TIN {active.tin}</span>
              </span>
              <span style={{ flex: 'none', color: 'var(--fg-3)', transform: switcherOpen ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}>{chevDownGlyph}</span>
            </button>
            {switcherOpen && (
              <div style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0, right: 0, zIndex: 60, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 16px 40px -16px oklch(20% .02 210 / 0.28)', overflow: 'hidden', animation: 'popIn 140ms ease-out' }}>
                <div className="label" style={{ padding: '10px 12px 6px' }}>
                  Switch company
                </div>
                {switcherClients.map((c) => {
                  const isActive = c.entityId === active.entityId
                  return (
                    <button
                      key={c.entityId ?? c.name}
                      // c.entityId is always a real portfolio entity id here — `clients`
                      // only ever holds buildClients() output, never the in-house/empty
                      // fallback ([entity-picker] keystone) — the guard is belt-and-
                      // suspenders against the type's `string | null`, not a real case.
                      onClick={() => c.entityId && ctx.switchClient(c.entityId)}
                      data-testid="company-switcher-option"
                      className="pf-menu-item"
                      style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 10, border: 0, background: isActive ? 'var(--bg-3)' : 'transparent', cursor: 'pointer', textAlign: 'left', padding: '9px 12px' }}
                    >
                      <span style={{ flex: 'none', width: 26, height: 26, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 10, fontWeight: 700 }}>{c.initials}</span>
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span style={{ display: 'block', fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.short}</span>
                        <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                          {rowHealthLabel(c.entityId)}
                        </span>
                      </span>
                      <span style={{ flex: 'none', color: 'var(--action)' }}>{isActive ? tickGlyph11 : ''}</span>
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        )}

        {/* in-house mode: single company, no switching */}
        {isInhouse && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '8px 10px' }}>
            <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>{active.initials}</span>
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

      {/* `flex: 1 1 0` + `min-height: 0` (not the bare `flex: 1` this had) is what lets the
          nav SHRINK below its content height. Without it the extra group headers grew the
          aside past its own height and pushed the user card below the fold. */}
      <nav className="pf-nav-list" style={{ flex: '1 1 0', minHeight: 0, overflowY: 'auto', padding: '12px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        {navGroups.map((g, gi) => (
          <Fragment key={g.key}>
            <div
              className="label"
              style={{ display: 'flex', alignItems: 'baseline', gap: 6, padding: '6px 8px 8px', marginTop: gi > 0 ? 10 : 0, borderTop: gi > 0 ? '1px solid var(--line-1)' : undefined, paddingTop: gi > 0 ? 14 : 6, minWidth: 0 }}
            >
              <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{g.label}</span>
              <span className="mono" style={{ flex: 'none', fontSize: 8.5, color: 'var(--fg-4)', letterSpacing: '0.08em', textTransform: 'uppercase' }}>
                · {g.scope}
              </span>
            </div>
            {g.items.map((n) => {
              const a = n.id === activeNav
              return (
                <button
                  key={n.id}
                  onClick={() => ctx.nav(n.id)}
                  className="pf-nav"
                  style={{ display: 'flex', alignItems: 'center', gap: 11, width: '100%', border: 0, cursor: 'pointer', borderRadius: 'var(--radius-sm)', padding: '9px 10px', textAlign: 'left', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: a ? 600 : 500, background: a ? 'var(--bg-3)' : 'transparent', color: a ? 'var(--fg-1)' : 'var(--fg-2)', position: 'relative' }}
                >
                  <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 'var(--radius-xs)', background: a ? 'var(--action)' : 'transparent' }} />
                  <span style={{ color: a ? 'var(--action)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
                  <span style={{ flex: 1 }}>{n.label}</span>
                  {n.badge && (
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', borderRadius: 99, padding: '1px 6px' }}>
                      {n.badge}
                    </span>
                  )}
                </button>
              )
            })}
          </Fragment>
        ))}
      </nav>

      {/* `flex: 0 0 auto` — the user card is the one thing that must stay pinned when the
          nav above it scrolls. */}
      <div style={{ flex: '0 0 auto', padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 99, background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 600 }}>{user.initials}</span>
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
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 28, height: 28, padding: 0, border: 0, borderRadius: 'var(--radius-sm)', background: 'transparent', cursor: 'pointer' }}
        >
          <Icon paths={['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9']} size={16} />
        </button>
      </div>
    </aside>
  )
}
