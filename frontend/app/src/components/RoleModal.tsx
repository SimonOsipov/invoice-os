// §2's create/edit role modal — a 560px centred modal, the app's sixth overlay.
//
// The app's scrim + panel + `useDismiss` overlay shape, with `maxHeight: 86vh` and a
// scrolling body: the picker list makes this the first modal whose height is driven by
// workspace data. The draft is local and lifted to the workspace only on Save, so Cancel,
// Escape and the backdrop all discard cleanly.
//
// Every sentence it renders that §2 does not supply comes from lib/roles.ts — vitest is
// `environment: node`, so a string authored here is a string no spec can hold. The one
// exception is marked below, matching `RolesView`'s own `NO_MATCH`.

import { useCallback, useState } from 'react'

import { toApiError } from '@invoice-os/api-client'
import { closeGlyph, tickGlyph11 } from '../glyphs'
import { accessRoleLabel, emailLabel, membersSurface } from '../lib/members'
import {
  canSaveRole,
  deletedNotice,
  deleteRoleConfirm,
  deleteRoleConfirmUnknownUsage,
  EDIT_ROLE_SUBTITLE,
  filterPickerMembers,
  hiddenInvitedFootnote,
  hiddenSelectionNote,
  NEW_ROLE_SUBTITLE,
  pickerHiddenAmongSelected,
  pickerMembers,
  pickerSelectionCount,
  savedNotice,
  steps,
  type Role,
} from '../lib/roles'
import { useDismiss } from '../lib/useDismiss'
import { InitialsChip } from './MemberParts'
import type { PlatformCtx } from '../types'

/** Edit without a subject is unrepresentable, so no call site needs a non-null assertion. */
export type RoleModalSubject = { mode: 'create' } | { mode: 'edit'; role: Role }

// NOT IN BRIEF: §2 excludes invited people from the picker but writes no zero-hit line.
const NO_PERSON_MATCH = 'No one matches that search.'

/** Beyond this the list scrolls — both seeds are taller than it. */
const LIST_MAX_HEIGHT = 268

const DISABLED = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } as const
const DISABLED_GHOST = { background: 'transparent', ...DISABLED } as const

/** Set comparison, not array equality — a re-tick in a different order is not a change. */
function membersChanged(selected: readonly string[], original: readonly string[]): boolean {
  if (selected.length !== original.length) return true
  const originalSet = new Set(original)
  return selected.some((id) => !originalSet.has(id))
}

export function RoleModal({ ctx, subject, onClose, onFlash }: {
  ctx: PlatformCtx
  subject: RoleModalSubject
  /** Must be STABLE — it is a `useDismiss` dependency (useDismiss.ts:36-37). */
  onClose: () => void
  /** RolesView's existing toolbar flash setter, not a second mechanism (RolesView.tsx:46-51). */
  onFlash: (message: string) => void
}) {
  const role = subject.mode === 'edit' ? subject.role : null
  // Seeded once. This modal is rendered conditionally, so every open is a fresh mount.
  const [name, setName] = useState(role?.title ?? '')
  const [desc, setDesc] = useState(role?.desc ?? '')
  const [selected, setSelected] = useState<string[]>(() => role?.members.slice() ?? [])
  const [query, setQuery] = useState('')
  // MODAL-LOCAL, the MemberDrawer posture: it dies with the modal rather than needing to be
  // cleared on close.
  const [confirming, setConfirming] = useState(false)
  // EntityFormModal's idiom (EntityFormModal.tsx:64,94): a write in flight disables the
  // form and blocks a second submit; a rejected one renders the gateway's own sentence here
  // instead of closing on it.
  const [submitting, setSubmitting] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)

  // AC-7: a write in flight must not be closed out from under by any route, X or Escape included.
  const closeIfIdle = useCallback(() => {
    if (!submitting) onClose()
  }, [submitting, onClose])

  // No `outsideRef` — the scrim's own onClick is the outside click, the call shape
  // useDismiss.ts:20-21 pre-authorises for a modal.
  useDismiss(true, closeIfIdle)

  const selectable = pickerMembers(ctx.members)
  const shown = filterPickerMembers(selectable, query)
  // Off the two lib derivations rather than re-testing `status === 'invited'` here.
  const hidden = ctx.members.length - selectable.length
  // `[invite-writes-both-stores]`: a role's `members` can hold a still-invited id the picker
  // has no row for, which `selected.length` above would otherwise count with no way to untick.
  const hiddenSelected = pickerHiddenAmongSelected(selected, ctx.members)
  const canSave = canSaveRole(name)
  // `steps` off an unlanded policies fetch reads as "used nowhere" — the [D-BUILDER-GUARD]
  // class of bug. `membersSurface`, not `rolesSurface`: that one takes two statuses and is
  // shared by two views, so folding policies in would be a new predicate.
  const policiesSurface = membersSurface(ctx.policiesState)
  const policiesLanded = policiesSurface !== 'loading' && policiesSurface !== 'error'

  function toggle(id: string) {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]))
  }

  async function save() {
    if (submitting) return // double-submit guard
    // The gate judges the trimmed name, so the trimmed name is what is stored.
    const title = name.trim()
    const trimmedDesc = desc.trim()
    setSubmitting(true)
    setWriteError(null)
    try {
      if (role) {
        // Two independent verbs, fired only for what actually changed — a rename-only edit
        // must not restaff, and a restaff-only edit must not rename.
        if (title !== role.title || trimmedDesc !== role.desc) await ctx.renameRole(role.key, title, trimmedDesc)
        if (membersChanged(selected, role.members)) await ctx.staffRole(role.key, selected.slice())
      } else {
        await ctx.createRole(title, trimmedDesc, selected.slice())
      }
      onFlash(savedNotice(title))
      onClose()
    } catch (err) {
      setWriteError(toApiError(err).message)
      // `[D-PARTIAL-CREATE]`: createRole's POST can land even though the PUT staffing step
      // after it fails, and App.tsx's mirror is only patched off the whole call's resolved
      // value — so the created-but-unstaffed card would otherwise stay invisible.
      if (!role) ctx.refetchRoles()
    } finally {
      setSubmitting(false)
    }
  }

  async function remove() {
    if (!role || submitting) return
    setSubmitting(true)
    setWriteError(null)
    try {
      // `[delete-does-not-demote]`: the role goes and NO policy is written. A published
      // policy whose step named it stays published, and that step blocks.
      await ctx.deleteRole(role.key)
      // The STORED title, not the field: an unsaved rename is not what is being deleted.
      onFlash(deletedNotice(role.title))
      onClose()
    } catch (err) {
      setWriteError(toApiError(err).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      onClick={() => {
        if (!submitting) onClose()
      }}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={role ? 'Edit role' : 'New role'}
        data-testid="role-modal"
        style={{ width: 560, maxWidth: '100%', maxHeight: '86vh', background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: 'var(--shadow-card)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '18px 20px 14px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 16, fontWeight: 700, color: 'var(--fg-1)' }}>{role ? 'Edit role' : 'New role'}</div>
            {/* A sentence, so NOT the `.mono` eyebrow a modal's second line usually takes. */}
            <div style={{ marginTop: 3, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
              {role ? EDIT_ROLE_SUBTITLE : NEW_ROLE_SUBTITLE}
            </div>
          </div>
          <button
            type="button"
            onClick={closeIfIdle}
            className="pf-btn"
            aria-label="Close"
            data-testid="role-modal-close"
            // No inline `borderRadius` — `.pf-btn` forces `border-radius` with `!important`.
            style={{ flex: 'none', width: 30, height: 30, border: 0, background: 'var(--bg-3)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '16px 20px 20px' }}>
          {/* `.label` is `text-transform: uppercase` (app-layer.css), so these render
              ROLE NAME / WHAT THIS ROLE SIGNS OFF / WHO HOLDS THIS ROLE — the invite modal's
              and the drawer's own field labels. */}
          <div className="label" style={{ marginBottom: 6 }}>
            Role name
          </div>
          <input
            type="text"
            className="pf-input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Finance Director"
            aria-label="Role name"
            data-testid="role-modal-name"
            disabled={submitting}
            style={{ height: 38, marginBottom: 14 }}
          />

          <div className="label" style={{ marginBottom: 6 }}>
            What this role signs off
          </div>
          <input
            type="text"
            className="pf-input"
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            placeholder="e.g. Second sign-off above ₦500m"
            aria-label="What this role signs off"
            data-testid="role-modal-desc"
            disabled={submitting}
            style={{ height: 38, marginBottom: 18 }}
          />

          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, marginBottom: 8 }}>
            <div className="label" style={{ minWidth: 0 }}>
              Who holds this role
            </div>
            {/* The denominator is the SELECTABLE count, so it agrees with the rows below. */}
            <span className="mono" data-testid="role-modal-count" style={{ flex: 'none', fontSize: 10, letterSpacing: '0.04em', color: 'var(--fg-3)' }}>
              {pickerSelectionCount(selected.length, ctx.members)}
              {hiddenSelected > 0 && <span data-testid="role-modal-count-hidden"> ({hiddenSelectionNote(hiddenSelected)})</span>}
            </span>
          </div>

          <div style={{ border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '10px 10px 6px' }}>
            <input
              type="text"
              className="pf-input"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search people"
              aria-label="Search people"
              data-testid="role-modal-search"
              style={{ height: 32, fontSize: 12.5, marginBottom: 6 }}
              disabled={submitting}
            />
            <div style={{ maxHeight: LIST_MAX_HEIGHT, overflowY: 'auto' }}>
              {shown.length === 0 ? (
                <div data-testid="role-modal-empty" style={{ padding: '16px 8px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg-3)' }}>
                  {NO_PERSON_MATCH}
                </div>
              ) : (
                shown.map((m) => {
                  const sel = selected.includes(m.id)
                  return (
                    // A `<label>`, so the whole row is the toggle without a second handler.
                    // The inline tint outranks `.pf-row:hover` (app-layer.css, no
                    // `!important`), so a selected row stays tinted under the pointer.
                    <label
                      key={m.id}
                      className="pf-row"
                      data-testid="role-modal-member"
                      style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '7px 8px', borderRadius: 'var(--radius-md)', background: sel ? 'var(--action-tint)' : 'transparent', cursor: submitting ? 'not-allowed' : 'pointer' }}
                    >
                      {/* A native input under a painted box: `.asc-app input` forces a 7px radius. */}
                      <span style={{ position: 'relative', flex: 'none', width: 16, height: 16 }}>
                        <span
                          aria-hidden="true"
                          style={{ position: 'absolute', inset: 0, boxSizing: 'border-box', borderRadius: 4, border: `1px solid ${sel ? 'var(--action)' : 'var(--line-2)'}`, background: sel ? 'var(--action)' : 'var(--bg-2)', color: 'var(--primary-foreground)', display: 'grid', placeItems: 'center', ...(submitting ? DISABLED : null) }}
                        >
                          {sel ? tickGlyph11 : null}
                        </span>
                        <input
                          type="checkbox"
                          checked={sel}
                          onChange={() => toggle(m.id)}
                          disabled={submitting}
                          style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', margin: 0, opacity: 0, cursor: 'inherit' }}
                        />
                      </span>
                      <InitialsChip initials={m.initials} status="active" size={26} fontSize={9.5} />
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span style={{ display: 'block', fontSize: 12.5, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {m.name}
                        </span>
                        <span className="mono" style={{ display: 'block', marginTop: 1, fontSize: 10, color: 'var(--fg-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {emailLabel(m)}
                        </span>
                      </span>
                      {/* Both modes. The in-house fork read a department, which no membership
                          row carries. */}
                      <span style={{ flex: 'none', fontSize: 11.5, color: 'var(--fg-3)' }}>
                        {accessRoleLabel(m.role)}
                        {/* Red carries the fact, as it does on the role card. */}
                        {m.status === 'suspended' && <span style={{ color: 'var(--status-red-text)' }}> · suspended</span>}
                      </span>
                    </label>
                  )
                })
              )}
            </div>
            {hidden > 0 && (
              <div
                data-testid="role-modal-hidden"
                style={{ marginTop: 4, padding: '8px 4px 6px', borderTop: '1px solid var(--line-1)', fontSize: 11.5, color: 'var(--fg-3)' }}
              >
                {hiddenInvitedFootnote(ctx.members)}
              </div>
            )}
          </div>

          {confirming && role && (
            // Inline in the body, so Cancel and Save stay in the footer beside it.
            <div
              data-testid="role-delete-confirm"
              style={{ marginTop: 14, padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)' }}
            >
              <p style={{ margin: '0 0 10px', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>
                {policiesLanded ? deleteRoleConfirm(role.title, steps(ctx.policies, role.key)) : deleteRoleConfirmUnknownUsage(role.title)}
              </p>
              <div style={{ display: 'flex', gap: 8 }}>
                <button
                  type="button"
                  onClick={() => setConfirming(false)}
                  disabled={submitting}
                  className="v2-btn v2-btn-ghost pf-btn"
                  data-testid="role-delete-cancel"
                  style={{ height: 32, fontSize: 12.5, ...(submitting ? DISABLED_GHOST : null) }}
                >
                  Keep role
                </button>
                <button
                  type="button"
                  onClick={() => void remove()}
                  disabled={submitting}
                  className="v2-btn pf-btn"
                  data-testid="role-delete-confirmed"
                  style={{ height: 32, fontSize: 12.5, background: 'var(--status-red-text)', color: 'var(--primary-foreground)', ...(submitting ? DISABLED : null) }}
                >
                  Delete role
                </button>
              </div>
            </div>
          )}
        </div>

        <div style={{ flex: 'none', padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          {/* The SERVER's own reason for the write it just refused, verbatim — no prefix,
              no substitute. MemberDrawer's `statusError` precedent. */}
          {writeError && (
            <div
              data-testid="role-modal-error"
              style={{ marginBottom: 10, padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 13, lineHeight: 1.5, color: 'var(--status-red-text)' }}
            >
              {writeError}
            </div>
          )}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {role && !confirming && (
              <button
                type="button"
                onClick={() => setConfirming(true)}
                disabled={submitting}
                className="v2-btn v2-btn-ghost pf-btn"
                data-testid="role-delete"
                style={{ flex: 'none', height: 36, color: 'var(--status-red-text)', borderColor: 'var(--status-red-border)', ...(submitting ? DISABLED_GHOST : null) }}
              >
                Delete role
              </button>
            )}
            <div style={{ flex: 1 }} />
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="v2-btn v2-btn-ghost pf-btn"
              data-testid="role-modal-cancel"
              style={{ height: 36, ...(submitting ? DISABLED_GHOST : null) }}
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void save()}
              disabled={!canSave || submitting}
              className="v2-btn v2-btn-primary pf-btn"
              data-testid="role-modal-save"
              // Inline: the repo has no `:disabled` rule, and `filter: none` outranks the hover lift.
              style={{ height: 36, ...(!canSave || submitting ? DISABLED : null) }}
            >
              {submitting ? 'Saving…' : role ? 'Save role' : 'Create role'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
