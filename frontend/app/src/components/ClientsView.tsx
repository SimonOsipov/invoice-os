// Clients / partner portal — live entity list (M3-08-04). Fetches the signed-in
// tenant's real business entities from the portfolio service and renders them with
// active/archived status pills, replacing the mock `buildClients()` feed for this
// surface only (Obsidian M3-08 §1/§3/§4/§5). Ported shell from Platform.dc.html
// ~L695-732; the KPI grid and the Readiness/VAT/Failing columns have no live source
// and are removed ([A-d]). Rows are display-only in this subtask — the add/edit
// modal + its open-state land in M3-08-05 ([A-l]).

import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { plusGlyph } from '../glyphs'
import { clientsViewState, entityStatusStyle, listEntities, shouldFetchEntities, type Entity } from '../lib/portfolio'
import { EntityFormModal } from './EntityFormModal'
import type { PlatformCtx } from '../types'

// Local avatar-bubble helper — deliberately NOT reused from lib/customers.ts (that
// module is the customer/buyer domain; this surface is the portfolio-entity domain,
// and the two are unrelated aside from both wanting initials from a name).
function initials(name: string): string {
  return name
    .replace(/[^A-Za-z ]/g, '')
    .split(' ')
    .filter(Boolean)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase()
}

export function ClientsView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  // The `base ? … : …` narrowing (rather than a `base!` assertion) means the producer
  // is well-typed without ever trusting a non-null base at the call site; in practice
  // it never runs when base is null anyway — `immediate: shouldFetchEntities(base)`
  // (= base != null) keeps the no-gateway build at zero network ([A-e]/[A-m]).
  const list = useAsync<Entity[]>(
    () => (base ? listEntities(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchEntities(base) },
  )
  const state = clientsViewState(base, list)

  const count = list.data?.length ?? 0
  const orgSegment = ctx.user.tenantName ? `${ctx.user.tenantName} · ` : ''

  // Add/edit form's open/mode/edit-target state ([A-l]) — local, not PlatformCtx: it
  // derives from this view's own live list + refetch handle, neither of which live on
  // Workspace ctx. EntityFormModal receives it as props.
  const [modal, setModal] = useState<{ mode: 'create' | 'edit'; entity?: Entity } | null>(null)

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Client portfolio</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
            {orgSegment}
            {count} companies · partner program
          </p>
        </div>
        <button
          onClick={() => setModal({ mode: 'create' })}
          disabled={base == null}
          className="v2-btn v2-btn-primary pf-btn"
        >
          <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Add client
        </button>
      </div>

      {state === 'loading' && <Loading label="Loading entities…" />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {(state === 'idle' || state === 'empty') && (
        <EmptyState title="No entities yet" message="Add your first business entity to get started." />
      )}

      {state === 'ready' && (
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          <div
            className="pf-list-head"
            style={{ display: 'grid', gridTemplateColumns: 'minmax(160px, 1fr) 160px 130px', gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}
          >
            <span className="label">Company</span>
            <span className="label">Sector</span>
            <span className="label">Status</span>
          </div>
          {(list.data ?? []).map((e) => {
            const st = entityStatusStyle(e.status)
            return (
              <div
                key={e.id}
                onClick={() => setModal({ mode: 'edit', entity: e })}
                className="pf-row pf-list-row"
                style={{ display: 'grid', gridTemplateColumns: 'minmax(160px, 1fr) 160px 130px', gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
              >
                <span style={{ display: 'flex', alignItems: 'center', gap: 12, minWidth: 0 }}>
                  <span style={{ flex: 'none', width: 32, height: 32, borderRadius: 6, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 12, fontWeight: 700 }}>
                    {initials(e.name)}
                  </span>
                  <span style={{ minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{e.name}</span>
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>TIN {e.tin ?? '—'}</span>
                  </span>
                </span>
                <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{e.sector ?? '—'}</span>
                <span>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}>
                    <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
                  </span>
                </span>
              </div>
            )
          })}
        </div>
      )}

      {modal && base != null && (
        <EntityFormModal
          mode={modal.mode}
          entity={modal.entity}
          ctx={ctx}
          base={base}
          onClose={() => setModal(null)}
          onSuccess={() => {
            list.run()
            setModal(null)
          }}
        />
      )}
    </div>
  )
}
