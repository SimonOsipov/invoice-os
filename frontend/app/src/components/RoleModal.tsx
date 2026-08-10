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

import { useState } from 'react'

import { closeGlyph } from '../glyphs'
import { accessRoleLabel, emailLabel } from '../lib/members'
import {
  canSaveRole,
  deletedNotice,
  deleteRoleConfirm,
  EDIT_ROLE_SUBTITLE,
  filterPickerMembers,
  hiddenInvitedFootnote,
  hiddenSelectionNote,
  NEW_ROLE_SUBTITLE,
  newRoleKey,
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
const LIST_MAX_HEIGHT = 232

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

  // No `outsideRef` — the scrim's own onClick is the outside click, the call shape
  // useDismiss.ts:20-21 pre-authorises for a modal.
  useDismiss(true, onClose)

  const selectable = pickerMembers(ctx.members)
  const shown = filterPickerMembers(selectable, query)
  // Off the two lib derivations rather than re-testing `status === 'invited'` here.
  const hidden = ctx.members.length - selectable.length
  // `[invite-writes-both-stores]`: a role's `members` can hold a still-invited id the picker
  // has no row for, which `selected.length` above would otherwise count with no way to untick.
  const hiddenSelected = pickerHiddenAmongSelected(selected, ctx.members)
  const canSave = canSaveRole(name)

  function toggle(id: string) {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]))
  }

  function save() {
    // The gate judges the trimmed name, so the trimmed name is what is stored.
    const title = name.trim()
    // `newRoleKey`'s removal (the key is minted server-side now) is a later, separate pass.
    const fields = { key: newRoleKey(ctx.roles, title), title, desc: desc.trim(), members: selected.slice() }
    // Split onto the new verbs. A members edit made here does not reach the server yet —
    // that wiring lands separately.
    if (role) ctx.renameRole(role.key, fields.title, fields.desc)
    else ctx.createRole(fields.title, fields.desc, fields.members)
    onFlash(savedNotice(title))
    onClose()
  }

  function remove() {
    if (!role) return
    // `[delete-does-not-demote]`: the role goes and NO policy is written. A published policy
    // whose step named it stays published, and that step blocks.
    ctx.deleteRole(role.key)
    // The STORED title, not the field: an unsaved rename is not what is being deleted.
    onFlash(deletedNotice(role.title))
    onClose()
  }

  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={role ? 'Edit role' : 'New role'}
        data-testid="role-modal"
        style={{ width: 560, maxWidth: '100%', maxHeight: '86vh', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div className="card-title">{role ? 'Edit role' : 'New role'}</div>
            {/* A sentence, so NOT the `.mono` eyebrow a modal's second line usually takes. */}
            <div style={{ marginTop: 3, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
              {role ? EDIT_ROLE_SUBTITLE : NEW_ROLE_SUBTITLE}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="role-modal-close"
            // No inline `borderRadius` — `.pf-btn` is `border-radius: var(--radius-pill)
            // !important` (app-layer.css:194-197) and the pill is wanted on a 34px icon button.
            style={{ flex: 'none', width: 34, height: 34, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px 18px' }}>
          {/* `.label` is `text-transform: uppercase` (app-layer.css:155-162), so these render
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
          />

          <div className="label" style={{ margin: '16px 0 6px' }}>
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
          />

          <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, margin: '16px 0 6px' }}>
            <div className="label" style={{ flex: 1, minWidth: 0 }}>
              Who holds this role
            </div>
            {/* The denominator is the SELECTABLE count, so it agrees with the rows below. */}
            <span className="mono" data-testid="role-modal-count" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
              {pickerSelectionCount(selected.length, ctx.members)}
              {hiddenSelected > 0 && <span data-testid="role-modal-count-hidden"> ({hiddenSelectionNote(hiddenSelected)})</span>}
            </span>
          </div>

          {/* `ClientAccessPicker`'s panel (MemberParts.tsx:355) — same ground, same radius. */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)', padding: 10 }}>
            <input
              type="text"
              className="pf-input"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search people"
              aria-label="Search people"
              data-testid="role-modal-search"
              style={{ height: 34, fontSize: 13, marginBottom: 8 }}
            />
            <div style={{ maxHeight: LIST_MAX_HEIGHT, overflowY: 'auto' }}>
              {shown.length === 0 ? (
                <div data-testid="role-modal-empty" style={{ padding: '8px 10px', fontSize: 12.5, color: 'var(--fg-3)' }}>
                  {NO_PERSON_MATCH}
                </div>
              ) : (
                shown.map((m) => {
                  const sel = selected.includes(m.id)
                  return (
                    // A `<label>`, so the whole row is the toggle without a second handler.
                    // The inline tint outranks `.pf-row:hover` (app-layer.css:270, no
                    // `!important`), so a selected row stays tinted under the pointer.
                    <label
                      key={m.id}
                      className="pf-row"
                      data-testid="role-modal-member"
                      style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '6px 8px', borderRadius: 'var(--radius-md)', background: sel ? 'var(--action-tint)' : 'transparent' }}
                    >
                      <input type="checkbox" checked={sel} onChange={() => toggle(m.id)} style={{ flex: 'none' }} />
                      <InitialsChip initials={m.initials} status={m.status} size={26} />
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span style={{ display: 'block', fontSize: 13, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {m.name}
                        </span>
                        <span className="mono" style={{ display: 'block', fontSize: 11, color: 'var(--fg-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
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
                style={{ marginTop: 8, paddingTop: 8, borderTop: '1px solid var(--line-1)', fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}
              >
                {hiddenInvitedFootnote(ctx.members)}
              </div>
            )}
          </div>
        </div>

        <div style={{ flex: 'none', padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          {confirming && role ? (
            // `[delete-confirms-inline]`, on MemberDrawer's danger-zone shape. It replaces the
            // whole button row rather than only its left slot: at 560px a three-line red block
            // beside Cancel/Save reads as two competing questions.
            <div
              data-testid="role-delete-confirm"
              style={{ padding: '11px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)' }}
            >
              <div style={{ fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>
                {deleteRoleConfirm(role.title, steps(ctx.policies, role.key))}
              </div>
              <div style={{ display: 'flex', gap: 9, marginTop: 10 }}>
                <button type="button" onClick={() => setConfirming(false)} className="v2-btn v2-btn-ghost pf-btn" data-testid="role-delete-cancel" style={{ height: 32, fontSize: 12.5 }}>
                  Keep role
                </button>
                <button
                  type="button"
                  onClick={remove}
                  className="pf-btn"
                  data-testid="role-delete-confirmed"
                  style={{ border: '1px solid var(--status-red-border)', background: 'var(--bg-2)', cursor: 'pointer', height: 32, padding: '0 14px', fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 600, color: 'var(--status-red-text)' }}
                >
                  Delete role
                </button>
              </div>
            </div>
          ) : (
            <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
              {role && (
                <button
                  type="button"
                  onClick={() => setConfirming(true)}
                  className="pf-btn"
                  data-testid="role-delete"
                  // MemberDrawer's Remove treatment (MemberDrawer.tsx:341-354), same tokens.
                  style={{ flex: 'none', height: 36, padding: '0 14px', border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', color: 'var(--status-red-text)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600 }}
                >
                  Delete role
                </button>
              )}
              <div style={{ flex: 1 }} />
              <button type="button" onClick={onClose} className="v2-btn v2-btn-ghost pf-btn" data-testid="role-modal-cancel" style={{ height: 36, fontSize: 13 }}>
                Cancel
              </button>
              <button
                type="button"
                onClick={save}
                disabled={!canSave}
                className="v2-btn v2-btn-primary pf-btn"
                data-testid="role-modal-save"
                // The app's disabled-primary treatment: the real attribute, plus an inline swap
                // so a dead button is not still painted as the action.
                style={{ height: 36, fontSize: 13, background: canSave ? 'var(--action)' : 'var(--bg-3)', color: canSave ? 'var(--text-on-dark)' : 'var(--fg-4)', cursor: canSave ? 'pointer' : 'not-allowed' }}
              >
                {role ? 'Save role' : 'Create role'}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
