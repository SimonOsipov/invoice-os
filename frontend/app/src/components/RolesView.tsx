// Settings › Roles — the named approval seats a policy's steps point at, and who holds them.
//
// A TAB beside Members, and the same split for the same reason (SettingsView.tsx:6-9): the
// role list is on ctx because the Workflows builder resolves a step against it, while the
// search text, the flash and (next subtask) the open modal are local here.
//
// Almost nothing is DEFINED in this file: vitest is `environment: node`, so a string or a
// derivation authored inside a component is one no spec can reach. Every sentence and every
// count on a card comes from lib/roles.ts, `filterRoles` and `intro` included. The one
// exception is marked below.

import { useCallback, useEffect, useState } from 'react'

import { EmptyState, ErrorState, Loading } from '@invoice-os/api-client'
import { plusGlyph, searchGlyph } from '../glyphs'
import { membersSurface } from '../lib/members'
import {
  filterRoles,
  holderCount,
  holders,
  intro,
  resolve,
  roleUsage,
  rolesSurface,
  steps,
  unassignedNotice,
  unassignedRoles,
  type Role,
} from '../lib/roles'
import { AmberNote, InitialsChip } from './MemberParts'
import { RoleModal, type RoleModalSubject } from './RoleModal'
import type { PlatformCtx } from '../types'

const EMPTY_TITLE = 'No roles yet'
// NOT IN BRIEF: §2.5 asks for "one explanatory line" without supplying its words.
const EMPTY_MESSAGE = 'Create the seats your approval policies point at — a step names a role, and whoever holds it signs.'
const NO_MATCH = 'No role matches that search.'

/** Beyond this the stack overflows to a `+N` chip. */
const AVATAR_MAX = 5

export function RolesView({ ctx }: { ctx: PlatformCtx }) {
  const { roles, members } = ctx
  const [query, setQuery] = useState('')
  // MembersView's flash idiom (MembersView.tsx:72-77) retimed to 3000ms: one transient
  // string plus one effect-owned timer, so a second save restarts the flash rather than
  // stacking a timer that would clear the newer message early. The modal raises it through
  // `onFlash` — `setFlash` is stable, so it is passed straight down.
  const [flash, setFlash] = useState<string | null>(null)
  useEffect(() => {
    if (!flash) return
    const t = window.setTimeout(() => setFlash(null), 3000)
    return () => window.clearTimeout(t)
  }, [flash])

  // Local, not on ctx: nothing outside this tab can open the modal. The union is what the
  // modal reads, so an edit without a subject is unrepresentable.
  const [modal, setModal] = useState<RoleModalSubject | null>(null)
  function openRoleModal(mode: 'create' | 'edit', role?: Role) {
    setModal(mode === 'edit' && role ? { mode: 'edit', role } : { mode: 'create' })
  }
  // STABLE — it is a `useDismiss` dependency inside the modal (useDismiss.ts:36-37).
  const closeModal = useCallback(() => setModal(null), [])

  // Worst-of BOTH fetches: every count and avatar below resolves against `roles` AND
  // `members`, so either one erroring would otherwise paint the grid off half-loaded data.
  const surface = rolesSurface(ctx.rolesState, ctx.membersState)
  // Whichever fetch(es) actually failed — a roles-only failure must not re-kick members.
  const retry = useCallback(() => {
    if (ctx.rolesState === 'error') ctx.refetchRoles()
    if (ctx.membersState === 'error') ctx.refetchMembers()
  }, [ctx.rolesState, ctx.membersState, ctx.refetchRoles, ctx.refetchMembers])
  const unassigned = unassignedRoles(roles, members)
  const shown = filterRoles(roles, query)
  const searching = query.trim() !== ''
  // The Members rule (MembersView.tsx): "nothing here yet" is a statement about the LIST, so
  // it must never appear over a live search — that reads as the search having deleted
  // everything. Filtered-to-zero always gets the muted card instead.
  const noRoles = roles.length === 0 && !searching

  return (
    <>
      <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '-4px 0 16px', maxWidth: 680, lineHeight: 1.6 }}>{intro(roles)}</p>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: 300, height: 36, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)' }}>
          <span aria-hidden="true" style={{ flex: 'none', display: 'inline-flex', color: 'var(--fg-3)' }}>
            {searchGlyph}
          </span>
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search roles"
            aria-label="Search roles"
            data-testid="roles-search"
            style={{ flex: 1, minWidth: 0, border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)' }}
          />
        </div>
        <div style={{ flex: 1 }} />
        {flash && (
          <span data-testid="roles-flash" style={{ flex: 'none', fontSize: 12.5, color: 'var(--status-green-text)' }}>
            {flash}
          </span>
        )}
        <NewRoleButton testId="roles-new" onClick={() => openRoleModal('create')} />
      </div>

      {surface === 'loading' && <Loading label="Loading members…" />}

      {surface === 'error' && (ctx.rolesError ?? ctx.membersError) && (
        <ErrorState error={(ctx.rolesError ?? ctx.membersError)!} onRetry={retry} />
      )}

      {surface !== 'loading' && surface !== 'error' && (
        <>
          {/* Above the grid AND above both empty surfaces: coverage is a statement about the
              workspace, which a search box cannot change. Gated on the roster having landed,
              though — over an errored fetch every role reads unheld. */}
          {surface === 'roster' && unassigned.length > 0 && (
            <AmberNote testId="roles-unassigned" style={{ marginBottom: 14 }}>
              {unassignedNotice(unassigned.length)}
              <div style={{ fontWeight: 700, marginTop: 3 }}>{unassigned.map((r) => r.title).join(' · ')}</div>
            </AmberNote>
          )}

          {noRoles ? (
            <div data-testid="roles-empty">
              <EmptyState
                title={EMPTY_TITLE}
                message={EMPTY_MESSAGE}
                messageMaxWidth={460}
                action={<NewRoleButton testId="roles-empty-new" onClick={() => openRoleModal('create')} />}
              />
            </div>
          ) : shown.length === 0 ? (
            // WorkflowsView's empty-list card (WorkflowsView.tsx:73): a card grid has no table
            // chrome to hang a muted row inside, so the muted row is its own card.
            <div
              data-testid="roles-no-match"
              style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '26px 16px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg-3)' }}
            >
              {NO_MATCH}
            </div>
          ) : (
            <div data-testid="roles-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))', gap: 14 }}>
              {shown.map((r) => (
                <RoleCard key={r.key} ctx={ctx} role={r} onEdit={() => openRoleModal('edit', r)} />
              ))}
            </div>
          )}
        </>
      )}

      {/* Conditional, so every open is a fresh mount and the draft it seeds is the CURRENT role. */}
      {modal && <RoleModal ctx={ctx} subject={modal} onClose={closeModal} onFlash={setFlash} />}
    </>
  )
}

function NewRoleButton({ onClick, testId }: { onClick: () => void; testId: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-testid={testId}
      className="v2-btn v2-btn-primary pf-btn"
      style={{ flex: 'none', height: 36, gap: 7 }}
    >
      <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> New role
    </button>
  )
}

function RoleCard({ ctx, role, onEdit }: { ctx: PlatformCtx; role: Role; onEdit: () => void }) {
  const { roles, members, policies } = ctx
  const held = holders(roles, members, role.key)
  const who = resolve(roles, members, role.key)
  const overflow = held.length - AVATAR_MAX
  // `surface` above covers roles and members; policies are not in that ladder, and `steps` off
  // an unlanded fetch reads as "used nowhere" on every card at once. Unlanded renders the EMPTY
  // STRING — `ABSENT_LABEL`'s em dash claims "nothing here" in its own right (MembersTable.tsx:195).
  const policiesSurface = membersSurface(ctx.policiesState)
  const policiesLanded = policiesSurface !== 'loading' && policiesSurface !== 'error'

  return (
    // Content, not a control: no onClick and no hover treatment anywhere on the box —
    // `Edit` is the only thing here that acts.
    <div
      data-testid="role-card"
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', flexDirection: 'column', background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '16px 16px 14px', gap: 12 }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 15, fontWeight: 700, color: 'var(--fg-1)' }}>{role.title}</div>
          {/* Two lines RESERVED whatever the text says: `desc` is the only variable-height
              thing on a card, so without this the avatar rows across a grid row stagger. */}
          <div
            style={{ marginTop: 3, fontSize: 12, lineHeight: 1.5, minHeight: 36, color: 'var(--fg-3)', display: '-webkit-box', WebkitBoxOrient: 'vertical', WebkitLineClamp: '2', overflow: 'hidden' }}
          >
            {role.desc}
          </div>
        </div>
        <button
          type="button"
          onClick={onEdit}
          data-testid="role-card-edit"
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ flex: 'none', height: 28, padding: '0 11px', fontSize: 12 }}
        >
          Edit
        </button>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minHeight: 26 }}>
        {held.length > 0 && (
          <>
            <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', paddingRight: 7 }}>
              {held.slice(0, AVATAR_MAX).map((m) => (
                <span key={m.id} title={m.name} style={{ display: 'inline-flex', marginRight: -7 }}>
                  <InitialsChip initials={m.initials} status={m.status} size={26} ring />
                </span>
              ))}
            </span>
            {overflow > 0 && (
              <span className="mono" aria-hidden="true" style={{ flex: 'none', fontSize: 10, color: 'var(--fg-3)' }}>
                +{overflow}
              </span>
            )}
          </>
        )}
        {/* Red carries the whole fact: `resolve` deliberately appends no "suspended". */}
        <span
          style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: who.warn ? 'var(--status-red-text)' : 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
        >
          {who.text}
        </span>
      </div>

      {/* Pinned. With `height: 100%` on the card, the tallest card in a grid row sets that
          row's height and every footer in it lands on one line. */}
      <div style={{ marginTop: 'auto', paddingTop: 10, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="mono" style={{ minWidth: 0, fontSize: 9.5, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--fg-3)' }}>
          {policiesLanded ? roleUsage(steps(policies, role.key)) : ''}
        </span>
        <span className="mono" style={{ flex: 'none', fontSize: 9.5, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--fg-3)' }}>
          {holderCount(held.length)}
        </span>
      </div>
    </div>
  )
}
