// Settings — tabbed: Members (the workspace's people — MembersView, MEMB-01), Roles (the
// approval seats those people hold — RolesView), ERP
// connectors (toggle connect/disconnect, plus a per-connector detail view behind Manage),
// API & webhooks (base URL, keys dimmed by sandbox mode, endpoints, webhooks), and
// signing & certificates. The last three are ported from Platform.dc.html ~L847-951 +
// the settings slices of renderVals() (~L1397-1435).
//
// The open connector and the mapping modal are view state scoped to this tab, so they live
// here rather than in ctx (the EntityFormModal/ClientsView precedent).
// Saved mappings are the exception — those are workspace state (ctx.connectorMappings),
// so they outlive navigating away from Settings.
//
// Company tab (task-304, INVCR-01-19, AC-4) — IN-HOUSE ONLY, prepended ahead of the
// shared SETTINGS_TABS list. Placement call: a firm workspace already has a dedicated
// multi-entity portfolio screen for this (ClientsView, reached via the firm-only
// NAV_CLIENTS sidebar item) — building a second one for in-house's single-entity case
// would be the "second entity form" AC-4 explicitly forbids. In-house's own workspace
// has no such screen and Settings is already its one config destination (NAV_SETTINGS is
// in in-house's sidebar group already, unlike NAV_CLIENTS), so a tab here is the natural
// home rather than a new nav item / new view. It reuses the EXISTING EntityFormModal
// verbatim, the same component + create/edit contract ClientsView already drives — this
// file only supplies the "when to open which mode" wiring ClientsView's own local
// `useState<{mode,entity}|null>` precedent already established.

import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading } from '@invoice-os/api-client'

import { API_BASE, API_KEYS, CERTS, CONNECTOR_DEFS, ENDPOINTS, SETTINGS_TABS, WEBHOOKS } from '../data'
import { copyGlyph, plusGlyph, shieldGlyph } from '../glyphs'
import { ConnectorDetail } from './ConnectorDetail'
import { EntityFormModal } from './EntityFormModal'
import { FieldMappingModal } from './FieldMappingModal'
import type { Entity } from '../lib/portfolio'
import { MembersView } from './MembersView'
import { RolesView } from './RolesView'
import type { ConnectorId, PlatformCtx, SettingsTab } from '../types'

function methodColor(m: 'POST' | 'GET'): { bg: string; color: string } {
  if (m === 'POST') return { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' }
  if (m === 'GET') return { bg: 'var(--action-tint)', color: 'var(--action)' }
  return { bg: 'var(--bg-3)', color: 'var(--fg-2)' }
}

export function SettingsView({ ctx }: { ctx: PlatformCtx }) {
  const { settingsTab, sandbox, connectors, mode, activeEntity, entitiesState, refetchEntities } = ctx
  const connCount = CONNECTOR_DEFS.filter((c) => connectors[c.id]).length
  const [openId, setOpenId] = useState<ConnectorId | null>(null)
  const [mappingOpen, setMappingOpen] = useState(false)
  // A connector disconnected from another surface must not leave its detail view mounted.
  const openDef = CONNECTOR_DEFS.find((c) => c.id === openId && connectors[c.id]) ?? null

  // Company tab's add/edit modal state — local, not ctx, same precedent as ClientsView's
  // own `modal` state: it derives from this view's own resolved entity + refetch handle,
  // neither of which live on Workspace ctx as view state.
  const [companyModal, setCompanyModal] = useState<{ mode: 'create' | 'edit'; entity?: Entity } | null>(null)
  const base = gatewayBase()

  const tabs: { id: SettingsTab; label: string }[] = mode === 'inhouse' ? [{ id: 'company', label: 'Company' }, ...SETTINGS_TABS] : SETTINGS_TABS

  function goList() {
    setOpenId(null)
    setMappingOpen(false)
  }

  function selectTab(t: SettingsTab) {
    ctx.setSettingsTab(t)
    goList()
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          WORKSPACE CONFIGURATION
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Settings</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>People, roles, integrations, developer access, and signing certificates.</p>
      </div>
      <div style={{ display: 'flex', gap: 26, borderBottom: '1px solid var(--line-1)', marginBottom: 24 }}>
        {tabs.map((t) => {
          const a = settingsTab === t.id
          return (
            <button
              key={t.id}
              onClick={() => selectTab(t.id)}
              // .pf-tab, not .pf-btn: the button classes are forced to a full
              // pill radius, which bends this 2px underline into an arc.
              className="pf-tab"
              style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: a ? 600 : 500, color: a ? 'var(--fg-1)' : 'var(--fg-3)', padding: '0 0 12px', borderBottom: `2px solid ${a ? 'var(--action)' : 'transparent'}`, marginBottom: -1 }}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      {/* Company (in-house only, task-304 AC-4). Guarded on `mode` too, defensively —
          there is no button that can ever set settingsTab:'company' for a firm workspace
          (the tab itself only exists in `tabs` for in-house), but this keeps the block
          honest on its own rather than trusting the tab strip alone. */}
      {settingsTab === 'company' && mode === 'inhouse' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {entitiesState === 'loading' && <Loading label="Loading your company…" />}
          {entitiesState === 'error' && ctx.entitiesError && <ErrorState error={ctx.entitiesError} onRetry={refetchEntities} />}
          {entitiesState !== 'loading' && entitiesState !== 'error' && (
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <span className="card-title">Your company</span>
                {/* Same button, two labels/modes: Add when there's nothing yet (AC-3's
                    bootstrap window), Edit once the one entity this workspace can ever
                    have (AC-1/AC-2) exists. */}
                <button
                  onClick={() => setCompanyModal(activeEntity ? { mode: 'edit', entity: activeEntity } : { mode: 'create' })}
                  disabled={base == null}
                  className="v2-btn v2-btn-primary pf-btn"
                >
                  {activeEntity ? (
                    'Edit company'
                  ) : (
                    <>
                      <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Add company
                    </>
                  )}
                </button>
              </div>
              <div style={{ padding: 20 }}>
                {activeEntity ? (
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 16 }}>
                    <div style={{ gridColumn: '1 / -1' }}>
                      <div className="label" style={{ marginBottom: 4 }}>Name</div>
                      <div style={{ fontSize: 14, fontWeight: 600 }}>{activeEntity.name}</div>
                    </div>
                    <div>
                      <div className="label" style={{ marginBottom: 4 }}>TIN</div>
                      <div className="mono" style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{activeEntity.tin ?? '—'}</div>
                    </div>
                    <div>
                      <div className="label" style={{ marginBottom: 4 }}>Sector</div>
                      <div style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{activeEntity.sector ?? '—'}</div>
                    </div>
                    <div>
                      <div className="label" style={{ marginBottom: 4 }}>Registration</div>
                      <div style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{activeEntity.registration ?? '—'}</div>
                    </div>
                    <div>
                      <div className="label" style={{ marginBottom: 4 }}>Address</div>
                      <div style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{activeEntity.address ?? '—'}</div>
                    </div>
                  </div>
                ) : (
                  <EmptyState title="No company set up yet" message="Add your business entity so invoices have somewhere real to file against." />
                )}
              </div>
            </div>
          )}
          {companyModal && base != null && (
            <EntityFormModal
              mode={companyModal.mode}
              entity={companyModal.entity}
              ctx={ctx}
              base={base}
              onClose={() => setCompanyModal(null)}
              onSuccess={() => {
                refetchEntities()
                setCompanyModal(null)
              }}
            />
          )}
        </div>
      )}

      {/* Members */}
      {settingsTab === 'members' && <MembersView ctx={ctx} />}

      {/* Roles */}
      {settingsTab === 'roles' && <RolesView ctx={ctx} />}

      {/* Connector detail — replaces the list while a connector is open */}
      {settingsTab === 'connectors' && openDef && (
        <>
          <ConnectorDetail
            ctx={ctx}
            def={openDef}
            env="SANDBOX"
            onBack={goList}
            onEditMapping={() => setMappingOpen(true)}
          />
          {mappingOpen && <FieldMappingModal ctx={ctx} def={openDef} onClose={() => setMappingOpen(false)} />}
        </>
      )}

      {/* Connectors */}
      {settingsTab === 'connectors' && !openDef && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: 0, maxWidth: 560, lineHeight: 1.55 }}>
              Sync invoices automatically from your ERP or accounting system. Documents are pulled, validated, and queued for transmission.
            </p>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', flex: 'none' }}>
              {connCount} / 6 CONNECTED
            </span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {CONNECTOR_DEFS.map((c) => {
              const on = !!connectors[c.id]
              return (
                <div key={c.id} style={{ display: 'flex', alignItems: 'center', gap: 15, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '16px 18px' }}>
                  <span style={{ flex: 'none', width: 42, height: 42, borderRadius: 'var(--radius-md)', background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, letterSpacing: '0.02em' }}>{c.mono}</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
                      <span style={{ fontSize: 14.5, fontWeight: 600 }}>{c.name}</span>
                      <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 5px', letterSpacing: '0.06em' }}>{c.cat}</span>
                    </div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                      {on ? 'Synced 2 min ago' : 'No sync yet'}
                    </div>
                  </div>
                  <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: on ? 'var(--status-green-bg)' : 'var(--bg-3)', border: `1px solid ${on ? 'var(--status-green-border)' : 'var(--line-2)'}`, borderRadius: 999, padding: '4px 10px' }}>
                    <span style={{ width: 5, height: 5, borderRadius: 99, background: on ? 'var(--status-green-text)' : 'var(--fg-3)' }} />
                    <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: on ? 'var(--status-green-text)' : 'var(--fg-3)', letterSpacing: '0.04em' }}>{on ? 'CONNECTED' : 'NOT CONNECTED'}</span>
                  </span>
                  {on && (
                    <button
                      onClick={() => setOpenId(c.id)}
                      className="pf-btn"
                      style={{ flex: 'none', height: 34, padding: '0 14px', borderRadius: 'var(--radius-md)', border: 0, background: 'transparent', color: 'var(--fg-2)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500 }}
                    >
                      Manage
                    </button>
                  )}
                  <button
                    onClick={() => ctx.toggleConnector(c.id)}
                    className="pf-btn"
                    style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 'var(--radius-md)', border: `1px solid ${on ? 'var(--line-2)' : 'var(--action)'}`, background: on ? 'transparent' : 'var(--action)', color: on ? 'var(--fg-2)' : 'var(--text-on-dark)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500 }}
                  >
                    {on ? 'Disconnect' : 'Connect'}
                  </button>
                </div>
              )
            })}
          </div>
        </>
      )}

      {/* API & webhooks */}
      {settingsTab === 'api' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '18px 20px' }}>
            <div className="label" style={{ marginBottom: 10 }}>
              Base URL
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <code className="mono" style={{ flex: 1, fontSize: 13, color: 'var(--fg-1)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', padding: '9px 12px' }}>{API_BASE}</code>
              <button className="pf-btn" style={{ flex: 'none', height: 36, padding: '0 12px', borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
                {copyGlyph} Copy
              </button>
            </div>
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
              <span className="card-title">API keys</span>
            </div>
            {API_KEYS.map((k) => {
              const dim = k.env === 'LIVE' ? (sandbox ? 0.45 : 1) : sandbox ? 1 : 0.45
              return (
                <div key={k.env} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '15px 20px', borderBottom: '1px solid var(--line-1)', opacity: dim }}>
                  <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', background: k.envBg, borderRadius: 'var(--radius-sm)', padding: '3px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: k.envColor, letterSpacing: '0.06em' }}>{k.env}</span>
                  </span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <code className="mono" style={{ fontSize: 12.5, color: 'var(--fg-1)', display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{k.key}</code>
                    <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 3 }}>{k.note}</div>
                  </div>
                  <button className="pf-btn" style={{ flex: 'none', height: 32, padding: '0 11px', borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
                    {copyGlyph} Copy
                  </button>
                </div>
              )
            })}
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
              <span className="card-title">Endpoints</span>
            </div>
            {ENDPOINTS.map((e) => {
              const mc = methodColor(e.m)
              return (
                <div key={e.path} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '13px 20px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ flex: 'none', width: 48, textAlign: 'center', background: mc.bg, borderRadius: 'var(--radius-sm)', padding: '3px 0' }}>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: mc.color }}>{e.m}</span>
                  </span>
                  <code className="mono" style={{ flex: 'none', fontSize: 13, color: 'var(--fg-1)', fontWeight: 500 }}>{e.path}</code>
                  <span style={{ flex: 1, fontSize: 12.5, color: 'var(--fg-3)', textAlign: 'right' }}>{e.desc}</span>
                </div>
              )
            })}
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span className="card-title">Webhooks</span>
              <button className="pf-btn" style={{ height: 30, padding: '0 11px', borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12.5 }}>
                {plusGlyph} Add endpoint
              </button>
            </div>
            {WEBHOOKS.map((w) => (
              <div key={w.event} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '13px 20px', borderBottom: '1px solid var(--line-1)' }}>
                <code className="mono" style={{ flex: 'none', fontSize: 12.5, fontWeight: 600, color: 'var(--action)' }}>{w.event}</code>
                <code className="mono" style={{ flex: 1, minWidth: 0, fontSize: 12, color: 'var(--fg-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{w.url}</code>
                <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 999, padding: '3px 9px' }}>
                  <span style={{ width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} />
                  <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--status-green-text)' }}>{w.st}</span>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Signing & certificates */}
      {settingsTab === 'signing' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {CERTS.map((c) => (
            <div key={c.name} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 11 }}>
                <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}>{shieldGlyph}</span>
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 14.5, fontWeight: 600 }}>{c.name}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                    {c.cn}
                  </div>
                </div>
                <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 999, padding: '4px 10px' }}>
                  <span style={{ width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} />
                  <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--status-green-text)' }}>ACTIVE</span>
                </span>
              </div>
              <div style={{ padding: '16px 20px' }}>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, marginBottom: 16 }}>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Issuer
                    </div>
                    <div style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{c.issuer}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Serial
                    </div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--fg-1)' }}>{c.serial}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Issued
                    </div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--fg-1)' }}>{c.issued}</div>
                  </div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                  <span style={{ fontSize: 12, color: 'var(--fg-2)' }}>Expires {c.expires}</span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{c.daysLeft} left</span>
                </div>
                <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden', marginBottom: 16 }}>
                  <div style={{ width: c.pct, height: '100%', background: c.barColor, borderRadius: 'var(--radius-sm)' }} />
                </div>
                <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
                  Renew certificate
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
