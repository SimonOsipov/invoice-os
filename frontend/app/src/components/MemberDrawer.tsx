// §8's member drawer — a 560px right drawer, the app's fifth overlay.
//
// Structurally `RuleDrawer` (RuleDrawer.tsx:23-46): scrim, `role="dialog"`, `aria-modal`,
// the `pfDrawer` animation and the `pf-drawer` class. That class is taken for a concrete
// reason and not as decoration — its ONLY rule is `width: 100vw !important` under a
// max-width media query (platform.css:259-261), so omitting it silently deletes the mobile
// collapse.
//
// TWO deliberate departures from that shell.
//
// (1) THE PANEL IS `--bg-2`, WITH `--bg-1` BANDS — RuleDrawer has it the other way round.
// Every control this drawer reuses was drawn for a `--bg-2` ground: `RoleCards` paints its
// unselected cards `--bg-1` (MemberParts.tsx, and its docblock says so explicitly),
// `WfSelect` paints its box `--bg-1` (WorkflowParts.tsx:217) and `ClientAccessPicker`'s
// panel is `--bg-1` too. On RuleDrawer's `--bg-1` body all three would be ground-on-ground.
// Fixed at the CALLER, per the architect: `RoleCards` is not edited, and the same
// `--bg-2` card with a `--bg-1` band relationship already ships eight inches away on this
// very tab (MembersTable.tsx's card and its head row).
//
// (2) IT CLOSES ON ESCAPE. `RuleDrawer` registers no keydown listener at all — it predates
// `useDismiss` — and AC#1 requires one. `useDismiss(true, onClose)` with no `outsideRef`:
// the scrim's own `onClick` is the outside click, which is the call shape useDismiss.ts:20-21
// pre-authorises for this drawer by name.
//
// No derivation and no copy is authored here. §8/§9's sentences and the three facts behind
// them live in lib/members.ts with specs, because vitest is `environment: node` and this
// component's oracle is a screenshot — the one gate a fluent paraphrase walks through
// (§15.8).

import { useId, useState } from 'react'

import { closeGlyph } from '../glyphs'
import {
  ACCESS_ROLES,
  DEPARTMENTS,
  isProtectedAdmin,
  joinedLabel,
  lastActiveLabel,
  PROTECTED_ADMIN_NOTE,
  REMOVE_EXPLANATION,
  removeConfirmQuestion,
  stepsFor,
  stepsNamedLine,
  SUSPEND_EXPLANATION,
  SUSPENDED_STEPS_NOTE,
  type AccessRole,
} from '../lib/members'
import { useDismiss } from '../lib/useDismiss'
import { AmberNote, ClientAccessPicker, InitialsChip, MemberStatusPill, PositionFields, RoleCards } from './MemberParts'
import type { PlatformCtx } from '../types'

export function MemberDrawer({ ctx, memberId, onClose }: {
  ctx: PlatformCtx
  /** The ID, never the row — see MembersView.tsx and the `row` lookup below. */
  memberId: string
  /** Must be STABLE — it is a `useDismiss` dependency (useDismiss.ts:36-37). */
  onClose: () => void
}) {
  // DRAWER-LOCAL, a narrow and deliberate deviation from §15.4's list, which enumerates "the
  // remove-confirm state" under MembersView. That enumeration's point is that it is not on
  // `ctx`, which this satisfies — and holding it here means it dies with the drawer for free,
  // rather than needing to be cleared on close and on subject change.
  const [confirming, setConfirming] = useState(false)
  const noteId = useId()
  useDismiss(true, onClose)

  // Resolved from the CURRENT list on every render, never captured. Three things ride on it:
  // `isProtectedAdmin` does no identity lookup and answers `true` for a detached row
  // (members.test.ts:1421), so a stale row gives a wrong lock; every control writes through
  // `ctx.saveMember` immediately, so the row this drawer opened with is obsolete after the
  // first click; and when `dropMember` takes the row away this evaluates to `null` and the
  // drawer closes itself, which is what a confirmed Remove wants.
  const row = ctx.members.find((m) => m.id === memberId) ?? null
  if (!row) return null

  const protectedAdmin = isProtectedAdmin(ctx.members, row)
  // §9's lock, applied to whoever the sole active admin is rather than to `isYou`. §9 phrases
  // it for "your own drawer", but the condition it describes is about the LAST ACTIVE ADMIN —
  // so a firm admin looking at another sole admin hits the same lock. Decided, not inherited.
  const lockedRoles: readonly AccessRole[] | undefined = protectedAdmin
    ? ACCESS_ROLES.filter((r) => r.id !== row.role).map((r) => r.id)
    : undefined

  // Gated on the member HOLDING a position, not on mode — firm rows carry no position at all
  // (members.test.ts:222), so this is already mode-proof. `ctx.policies` is the CURRENT
  // workspace's set (App.tsx); `seedPolicies()` would never reflect a Workflows edit.
  const steps = row.position != null ? stepsFor(ctx.policies, row.position) : null
  // §8 and AC#5 phrase the trigger as "when the member holds a position", but the count is
  // what the section renders, and three shipped in-house rows hold a position no policy names
  // (T7.6) — Tunde Adeyemi, Ibrahim Bello and Zainab Lawal. Taken literally, their drawers
  // would read "Named in 0 approval steps" over an empty policy list. Gate on the count.
  const involved = steps != null && steps.total > 0

  // §6's rule, and a SEPARATE one from the last-admin lock: your own row has no Remove.
  // Load-bearing rather than cosmetic — `dropMember` is an unguarded pass-through by design
  // (task-296 AC#4) and MembersView's empty-surface chain has no `members.length === 0`
  // branch, so an empty roster would fall through to "No members match this search." with no
  // search running. The `⋯` menu holds the same line (MembersTable.tsx); this is the second
  // removal path and must not be the one that opens the hole. OMITTED, not disabled.
  const canRemove = !row.isYou
  // An invited person has no sign-in to block, and `suspended` has no path back to `invited`
  // — so offering Suspend here would ship a one-way trap on a state the table's own `⋯` menu
  // already forks away from (Resend / Copy link / Revoke invite, no Suspend). Mirrors it.
  const canSuspend = row.status !== 'invited'
  const suspending = row.status !== 'suspended'

  const disabledGhost = { background: 'transparent', borderColor: 'var(--line-1)', color: 'var(--fg-4)', cursor: 'not-allowed' } as const

  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.32)', animation: 'pfFade 160ms ease-out' }} />
      <div
        className="pf-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={`Member ${row.name}`}
        data-testid="member-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 560,
          maxWidth: '94vw',
          background: 'var(--bg-2)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px oklch(20% .02 210 / 0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        {/* Header — AC#2. NOT an <h1>: SettingsView.tsx:49-53 owns the page heading and a
            second one inside a tab would give the page two. */}
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <InitialsChip initials={row.initials} status={row.status} size={40} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 9, flexWrap: 'wrap', marginBottom: 3 }}>
              <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-word' }}>{row.name}</span>
              <MemberStatusPill status={row.status} />
            </div>
            <div className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)', wordBreak: 'break-all' }}>
              {row.email}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="member-drawer-close"
            // No inline `borderRadius`, for the reason InviteMembersModal.tsx already
            // recorded: `.pf-btn` is `border-radius: var(--radius-pill) !important`
            // (app-layer.css:194-197), so RuleDrawer.tsx:63's `--radius-input` here is a
            // declaration that has never applied. The pill is wanted on a 30px icon button.
            style={{ flex: 'none', width: 30, height: 30, border: 0, background: 'var(--bg-3)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <div className="label" style={{ marginBottom: 6 }}>
            Access role
          </div>
          {/* `idPrefix="drawer"`, never 'invite': the two surfaces would otherwise name the
              same radio group and one could steal the other's selection. Writes straight
              through — §8 says every change persists immediately and there is no Save button.
              A bare spread is safe here for the same reason MembersTable's is: `replaceMember`
              discards the old object, so nothing is left aliasing its `clientAccess`. */}
          <RoleCards
            idPrefix="drawer"
            value={row.role}
            onChange={(role) => ctx.saveMember({ ...row, role })}
            disabledIds={lockedRoles}
            note={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
          />

          {ctx.mode === 'firm' ? (
            <>
              <div className="label" style={{ margin: '20px 0 6px' }}>
                Client access
              </div>
              {/* The one write on this surface that a spread does NOT cover: `{...row}` copies
                  the `clientAccess` REFERENCE, and `replaceMember` does not copy it either
                  (members.ts:861-863 — its row is caller-built, so the caller owns that copy).
                  `ClientAccessPicker` emits a fresh array every time, so the row it hands back
                  never shares state with the row it replaced. */}
              <ClientAccessPicker
                idPrefix="drawer"
                value={row.clientAccess ?? 'all'}
                onChange={(clientAccess) => ctx.saveMember({ ...row, clientAccess })}
              />
            </>
          ) : (
            <div style={{ marginTop: 20 }}>
              <PositionFields
                idPrefix="drawer"
                // `department` is optional on `Member` because a FIRM row carries none; every
                // in-house row has one, by seed and by `memberFromInvite` alike. The fallback
                // is the type's, not a state this branch can reach.
                department={row.department ?? DEPARTMENTS[0]}
                position={row.position ?? null}
                onDepartment={(department) => ctx.saveMember({ ...row, department })}
                onPosition={(position) => ctx.saveMember({ ...row, position })}
              />
            </div>
          )}

          {/* Activity — AC#3. Label-left rows (the ConnectorDetail.tsx:200 form) rather than
              SettingsView's 3-up grid: at 560px minus 44px of padding each of three columns is
              ~172px, and 'Expires in 6 days' beside a full name crowds at that width. */}
          <div className="label" style={{ margin: '20px 0 6px' }}>
            Activity
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', columnGap: 16, rowGap: 7, alignItems: 'baseline' }}>
            <span className="label">Last active</span>
            <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)' }}>
              {lastActiveLabel(row)}
            </span>
            <span className="label">Joined</span>
            {/* `joined` is null on every invited row and a bare `{row.joined}` renders nothing
                at all, which reads as a layout bug rather than as "not yet" (T7.7). */}
            <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)' }}>
              {joinedLabel(row)}
            </span>
            <span className="label">Invited by</span>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{row.invitedBy}</span>
          </div>

          {involved && (
            <>
              <div className="label" style={{ margin: '20px 0 6px' }}>
                Approval involvement
              </div>
              <div data-testid="member-steps-named" style={{ fontSize: 13, color: 'var(--fg-1)' }}>
                {stepsNamedLine(steps.total)}
              </div>
              {/* The per-policy breakdown `stepsFor` has always returned and nothing rendered
                  until now. Joined on ' · ', the same way MembersView.tsx joins the unassigned
                  position titles — one separator for lists of names on this tab. */}
              <div style={{ marginTop: 3, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {steps.policies.map((p) => p.name).join(' · ')}
              </div>
              {row.status === 'suspended' && (
                <AmberNote testId="member-drawer-steps-warning" style={{ marginTop: 10 }}>
                  {SUSPENDED_STEPS_NOTE}
                </AmberNote>
              )}
            </>
          )}
        </div>

        {/* §8: "Footer is the danger zone." Its copy is the most important text in the story,
            so both explanations are rendered as visible prose beside their buttons rather than
            hidden behind a hover. */}
        <div
          data-testid="member-danger-zone"
          style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', flexDirection: 'column', gap: 12 }}
        >
          {canSuspend && (
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
              <button
                type="button"
                onClick={() => ctx.saveMember({ ...row, status: suspending ? 'suspended' : 'active' })}
                disabled={protectedAdmin}
                title={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
                aria-describedby={protectedAdmin ? noteId : undefined}
                className="v2-btn v2-btn-ghost pf-btn"
                data-testid="member-suspend"
                // Layers (1) and (2) of MoreMenu's four-layer disabled treatment: the real
                // attribute, plus an inline swap that outranks `.v2-btn-ghost:hover`'s
                // unguarded `background: var(--muted)` (app-layer.css) so a dead button stops
                // reacting to the pointer. (3) is the visible note below, (4) the title and
                // aria-describedby above — additions to it, never its replacement.
                style={{ flex: 'none', minWidth: 118, justifyContent: 'center', height: 36, fontSize: 13, ...(protectedAdmin ? disabledGhost : null) }}
              >
                {suspending ? 'Suspend' : 'Reactivate'}
              </button>
              {/* Rendered in BOTH states. §8 gives one sentence here and it describes what
                  suspension IS, which is exactly what `Reactivate` undoes; AC#6 is a verbatim
                  gate, so inventing a second sentence would add unspec'd copy to satisfy a
                  requirement nothing states. */}
              <span style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>{SUSPEND_EXPLANATION}</span>
            </div>
          )}

          {canRemove &&
            (confirming ? (
              // INLINE two-state, not a stacked overlay. `useDismiss` has no topmost-surface
              // concept (InviteMembersModal.tsx documents the limitation), so a second overlay
              // would register a second window keydown listener and one Escape would fire
              // both — dismissing the confirm AND the drawer. The app's only two-step confirms
              // live in other apps and neither transfers: ops-console's RotateConfirm records
              // a DELIBERATE no-Escape decision, which is the opposite of AC#1.
              <div
                data-testid="member-remove-confirm"
                style={{ padding: '11px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)' }}
              >
                <div style={{ fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}>{removeConfirmQuestion(row.name)}</div>
                <div style={{ display: 'flex', gap: 9, marginTop: 10 }}>
                  <button type="button" onClick={() => setConfirming(false)} className="v2-btn v2-btn-ghost pf-btn" data-testid="member-remove-cancel" style={{ height: 32, fontSize: 12.5 }}>
                    Cancel
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      ctx.dropMember(row.id)
                      // Belt-and-braces rather than load-bearing: the `row` lookup above
                      // already evaluates to null on the next render and this drawer returns
                      // nothing. It also clears MembersView's now-dangling open id.
                      onClose()
                    }}
                    className="pf-btn"
                    data-testid="member-remove-confirmed"
                    style={{ border: '1px solid var(--status-red-border)', background: 'var(--bg-2)', cursor: 'pointer', height: 32, padding: '0 14px', fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 600, color: 'var(--status-red-text)' }}
                  >
                    Remove
                  </button>
                </div>
              </div>
            ) : (
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
                <button
                  type="button"
                  onClick={() => setConfirming(true)}
                  disabled={protectedAdmin}
                  title={protectedAdmin ? PROTECTED_ADMIN_NOTE : undefined}
                  aria-describedby={protectedAdmin ? noteId : undefined}
                  className="pf-btn"
                  data-testid="member-remove"
                  // RuleDrawer's own footer treatment (RuleDrawer.tsx:133-142) — same surface
                  // type, same band, same tokens. Its inline `borderRadius: var(--radius-sm)`
                  // is NOT copied: `.pf-btn` forces the pill with `!important`, so that
                  // declaration has never applied and carrying it forward would propagate a
                  // false claim.
                  style={{
                    flex: 'none',
                    minWidth: 118,
                    height: 36,
                    padding: '0 14px',
                    border: '1px solid var(--status-red-border)',
                    background: 'var(--status-red-bg)',
                    color: 'var(--status-red-text)',
                    cursor: 'pointer',
                    fontFamily: 'var(--font-sans)',
                    fontSize: 13,
                    fontWeight: 600,
                    ...(protectedAdmin ? disabledGhost : null),
                  }}
                >
                  Remove
                </button>
                <span style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>{REMOVE_EXPLANATION}</span>
              </div>
            ))}

          {/* Layer (3) — the only layer a screenshot, a keyboard user and a text assertion can
              all reach, since a disabled control is out of the tab order and `title` never
              fires on one in Chromium. §9 gives the same sentence to the cards above and to
              these buttons, so it appears twice in a locked drawer by design. */}
          {protectedAdmin && (
            <div id={noteId} data-testid="member-danger-note" style={{ fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
              {PROTECTED_ADMIN_NOTE}
            </div>
          )}
        </div>
      </div>
    </>
  )
}
