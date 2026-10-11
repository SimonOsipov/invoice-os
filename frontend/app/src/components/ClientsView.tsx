// Clients / partner portal — live entity list (M3-08-04). Renders the signed-in
// tenant's real business entities with active/archived status pills, replacing the mock
// `buildClients()` feed for this surface only (Obsidian M3-08 §1/§3/§4/§5). Ported shell
// from Platform.dc.html ~L695-732; the KPI grid and the Readiness/VAT/Failing columns
// have no live source and are removed ([A-d]). Rows are display-only in this subtask —
// the add/edit modal + its open-state land in M3-08-05 ([A-l]).
//
// M4-10-03: a second, independent rollup fetch drives a per-row needs-attention health
// pill, joined to the entity by id — restoring a client-health column now that a live
// source (the 06 rollup) exists. The pill is computed ONLY when the rollup fetch is
// 'ready'; loading/error/idle renders a neutral cell, never a false "NO INVOICES YET".
//
// [entity-picker]: ctx.entities/refetchEntities remain the ONE fetch shared with the
// workspace switcher (Sidebar) and CreateUpload's entity picker (lifted to App.tsx). The
// status filter below adds a SECOND, independent fetch for this view's own rows —
// ctx.entities itself stays unfiltered ([clients-fetches-its-own-filtered-list]).

import { useState } from 'react'

import { ApiError, EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { plusGlyph } from '../glyphs'
import {
  archiveActionFor,
  entityListIsEmpty,
  entityStatusParam,
  entityStatusStyle,
  listEntities,
  offboardEntity,
  onboardEntity,
  portfolioCountLabel,
  type ArchiveAction,
  type Entity,
  type EntityFilterPos,
  type EntityListResponse,
} from '../lib/portfolio'
import { companySetupAccess } from '../lib/members'
import { entityHealth, getRollup, type EntityHealth, type Rollup } from '../lib/dashboard'
import { NO_COMPANY_COPY } from './AddCompanyTask'
import { EntityFormModal } from './EntityFormModal'
import type { PlatformCtx } from '../types'

const FILTER_POSITIONS: Array<{ pos: EntityFilterPos; label: string }> = [
  { pos: 'all', label: 'All' },
  { pos: 'active', label: 'Active' },
  { pos: 'archived', label: 'Archived' },
]

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

// Maps rollup-derived entity health to the shared status-pill vocabulary ({bg,border,
// text,label}, same shape as portfolio.ts entityStatusStyle), reusing the existing
// --status-* token families so the health pill sits beside the lifecycle pill without
// forking the palette. Only called on a non-null (ready) health value.
export function healthPillStyle(h: EntityHealth): { bg: string; border: string; text: string; label: string } {
  switch (h.kind) {
    case 'no-invoices':
      return { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'NO INVOICES YET' }
    case 'needs-attention':
      return {
        bg: 'var(--status-red-bg)',
        border: 'var(--status-red-border)',
        text: 'var(--status-red-text)',
        label: h.count === 1 ? '1 NEEDS ATTENTION' : `${h.count} NEED ATTENTION`,
      }
    case 'clear':
      return { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'ALL CLEAR' }
  }
}

type StatusChipStyle = { bg: string; border: string; text: string; label: string }

// Mono chip in a plain wrapper span, so the inline-flex chip does not stretch to its grid track.
function StatusChip({ s }: { s: StatusChipStyle }) {
  return (
    <span>
      <span
        className="mono"
        style={{ display: 'inline-flex', fontSize: 10, fontWeight: 600, letterSpacing: '0.04em', color: s.text, background: s.bg, border: `1px solid ${s.border}`, borderRadius: 'var(--radius-sm)', padding: '3px 9px' }}
      >
        {s.label}
      </span>
    </span>
  )
}

// One portfolio-row health cell. `health === null` is the not-ready window (rollup still
// loading/error/idle) → a neutral em-dash, NOT "NO INVOICES YET" (QA finding #1). Renders
// a single element either way, so the row grid still sees exactly one Health cell.
function HealthCell({ health }: { health: EntityHealth | null }) {
  if (health === null) {
    return <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>—</span>
  }
  return <StatusChip s={healthPillStyle(health)} />
}

export function ClientsView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const { refetchEntities } = ctx

  const [pos, setPos] = useState<EntityFilterPos>('all')

  // Own filtered fetch, not a client-side filter over ctx.entities -- the switcher's
  // roster (ctx.entities) must stay unfiltered ([clients-fetches-its-own-filtered-list]).
  const filtered = useAsync<EntityListResponse>(
    () =>
      base
        ? listEntities(ctx.authedFetch, base, { status: entityStatusParam(pos) })
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: entityListIsEmpty, immediate: base != null, deps: [pos] },
  )
  const filteredState = base == null ? 'idle' : filtered.status
  const access = companySetupAccess(ctx.membersState, ctx.members)
  const noClients = access === 'wait' ? NO_COMPANY_COPY : { title: 'No entities yet', message: 'Add your first business entity to get started.' }
  const rows = filtered.data?.entities ?? []
  const shown = rows.length
  const total = filtered.data?.pagination.total ?? 0

  // Second, independent rollup fetch (Decision [fetch-per-surface]) driving the per-row
  // health pill — separate from the shared entity list, which alone gates row
  // visibility. A slow/failed rollup must NOT block the table; it just leaves the
  // neutral health cell.
  const rollup = useAsync<Rollup>(
    () => (base ? getRollup(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  // QA finding #1: read clients ONLY when the rollup fetch is 'ready'. On every other
  // status asyncReducer clears data to null (async-state.ts:51), so `rollupData` stays
  // null and each row falls back to the neutral cell — NOT a false "NO INVOICES YET"
  // (which an unconditional `rollup.data?.clients ?? []` would produce during the fetch).
  const rollupData = rollup.status === 'ready' ? rollup.data : null

  const orgSegment = ctx.user.tenantName ? `${ctx.user.tenantName} · ` : ''

  // Add/edit form's open/mode/edit-target state ([A-l]) — local, not PlatformCtx: it
  // derives from this view's own live list + refetch handle, neither of which live on
  // Workspace ctx. EntityFormModal receives it as props.
  const [modal, setModal] = useState<{ mode: 'create' | 'edit'; entity?: Entity } | null>(null)

  // Archive/restore (BUG-01-12): armedId is the one row currently primed to fire on its
  // next click ([archive-arms-then-confirms]); archivingId is a JS-level in-flight guard
  // (archiveActionFor's `armed` is only two-state, so it cannot itself stop a fast
  // double-click on confirm from posting twice — EntityFormModal's submitting guard is
  // the same idiom, one row scoped).
  const [armedId, setArmedId] = useState<string | null>(null)
  const [archivingId, setArchivingId] = useState<string | null>(null)
  const [archiveError, setArchiveError] = useState<{ id: string; message: string } | null>(null)
  const activeEntityId = ctx.activeEntity?.id ?? null

  async function handleArchiveClick(entity: Entity, action: ArchiveAction) {
    if (base == null || archivingId === entity.id) return
    if (!action.confirming) {
      setArmedId(entity.id)
      setArchiveError(null)
      return
    }
    setArchivingId(entity.id)
    try {
      if (action.kind === 'offboard') await offboardEntity(ctx.authedFetch, base, entity.id)
      else await onboardEntity(ctx.authedFetch, base, entity.id)
      setArmedId(null)
      setArchiveError(null)
      refetchEntities()
      filtered.run()
    } catch (err) {
      setArmedId(null)
      setArchiveError({ id: entity.id, message: err instanceof ApiError ? err.message : 'Something went wrong. Please try again.' })
    } finally {
      setArchivingId(null)
    }
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            FIRM PORTFOLIO
          </div>
          <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Client portfolio</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
            {orgSegment}
            {portfolioCountLabel(shown, total)} · partner program
          </p>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {FILTER_POSITIONS.map(({ pos: p, label }) => (
              <button
                key={p}
                onClick={() => setPos(p)}
                className="pf-chip"
                style={{
                  height: 30,
                  padding: '0 12px',
                  fontFamily: 'var(--font-sans)',
                  fontSize: 12.5,
                  fontWeight: 500,
                  border: `1px solid ${pos === p ? 'var(--action)' : 'var(--line-2)'}`,
                  background: pos === p ? 'var(--action)' : 'var(--bg-2)',
                  color: pos === p ? 'var(--primary-foreground)' : 'var(--fg-2)',
                }}
              >
                {label}
              </button>
            ))}
          {access === 'add' && (
            <button
              onClick={() => setModal({ mode: 'create' })}
              disabled={base == null}
              className="v2-btn v2-btn-primary pf-btn"
            >
              <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> Add client
            </button>
          )}
        </div>
      </div>

      {filteredState === 'loading' && <Loading label="Loading entities…" />}

      {filteredState === 'error' && filtered.error && <ErrorState error={filtered.error} onRetry={filtered.run} />}

      {filteredState === 'idle' && <EmptyState title={noClients.title} message={noClients.message} />}

      {filteredState === 'empty' &&
        (pos === 'all' ? (
          <EmptyState title={noClients.title} message={noClients.message} />
        ) : (
          <EmptyState title="No clients match this filter" message="Try a different status filter." />
        ))}

      {filteredState === 'ready' && (
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
          <div
            className="pf-list-head"
            style={{ display: 'grid', gridTemplateColumns: 'minmax(160px, 1fr) 160px 130px 150px 160px', gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}
          >
            <span className="label">Company</span>
            <span className="label">Sector</span>
            <span className="label">Status</span>
            <span className="label">Health</span>
            <span className="label">Action</span>
          </div>
          {rows.map((e) => {
            const st = entityStatusStyle(e.status)
            // Join by id (Entity.id === RollupClient.entity_id). null while the rollup is
            // not 'ready' → HealthCell renders a neutral cell (QA finding #1).
            const health = rollupData ? entityHealth(rollupData.clients, e.id) : null
            const action = archiveActionFor(e, activeEntityId, armedId === e.id)
            return (
              <div
                key={e.id}
                onClick={() => setModal({ mode: 'edit', entity: e })}
                className="pf-row pf-list-row"
                style={{ display: 'grid', gridTemplateColumns: 'minmax(160px, 1fr) 160px 130px 150px 160px', gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
              >
                <span style={{ display: 'flex', alignItems: 'center', gap: 12, minWidth: 0 }}>
                  <span style={{ flex: 'none', width: 32, height: 32, borderRadius: '50%', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 12, fontWeight: 700 }}>
                    {initials(e.name)}
                  </span>
                  <span style={{ minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{e.name}</span>
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>TIN {e.tin ?? '—'}</span>
                  </span>
                </span>
                <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>{e.sector ?? '—'}</span>
                <StatusChip s={st} />
                <HealthCell health={health} />
                <span style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4, minWidth: 0 }}>
                  <button
                    type="button"
                    onClick={(ev) => {
                      ev.stopPropagation() // the row's own onClick opens the edit modal (AC #6)
                      void handleArchiveClick(e, action)
                    }}
                    disabled={archivingId === e.id}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={{ height: 30, padding: '0 12px', fontSize: 12.5, color: action.confirming ? 'var(--status-red-text)' : 'var(--fg-1)' }}
                  >
                    {action.label}
                  </button>
                  {action.notice && <span style={{ fontSize: 11.5, lineHeight: 1.4, color: 'var(--fg-3)' }}>{action.notice}</span>}
                  {archiveError?.id === e.id && <span style={{ fontSize: 11.5, lineHeight: 1.4, color: 'var(--status-red-text)' }}>{archiveError.message}</span>}
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
            refetchEntities()
            filtered.run()
            setModal(null)
          }}
        />
      )}
    </div>
  )
}
