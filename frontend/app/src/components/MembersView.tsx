// Settings › Members — the first and default Settings tab.
//
// A TAB, not a screen: no `View` member, no `NavId`, no crumb. SettingsView already
// renders the page eyebrow / heading / subtitle above the tab strip, so this panel opens
// with an intro paragraph and its top bar, exactly as the ERP connectors tab does.
//
// Everything transient lives here rather than on ctx — the search box, the role filter,
// and (later) which drawer or menu is open — following the rationale SettingsView states
// for its own view state at SettingsView.tsx:6-9. The roster itself is on ctx, because the
// Workflows builder resolves a step's role against the same list.
//
// The consequence, accepted rather than worked around: switching to another Settings tab
// unmounts this one, so the search text and role filter reset.

import { useCallback, useEffect, useState } from 'react'

import { EmptyState } from '@invoice-os/api-client'
import { plusGlyph } from '../glyphs'
import { ACCESS_ROLES, filterMembers, isFiltering, type AccessRole } from '../lib/members'
import { unassignedNotice, unassignedRoles } from '../lib/roles'
import { InviteMembersModal } from './InviteMembersModal'
import { MemberDrawer } from './MemberDrawer'
import { AmberNote } from './MemberParts'
import { ClientUsersCard, MemberRoleMatrix } from './MemberRoleMatrix'
import { MembersTable } from './MembersTable'
import { WfSelect, type WfOption } from './WorkflowParts'
import type { PlatformCtx } from '../types'

// A constant, not a derivation — the ROLE_OPTIONS idiom at WorkflowParts.tsx:123.
// 'all' is just another option; WfSelect's `value`/`onChange` are plain strings and the
// caller narrows on the way back, the WorkflowInspector.tsx:78 idiom.
const ROLE_FILTER_OPTIONS: WfOption[] = [
  { value: 'all', label: 'All roles' },
  ...ACCESS_ROLES.map((r) => ({ value: r.id, label: r.label })),
]

// Copy forks on mode, structure never does — the Rules/Workflows screens' rule. The two
// halves name each mode's own extra column: client scoping in firm mode, the approval
// chain in-house.
const INTRO: Record<PlatformCtx['mode'], string> = {
  firm: 'Who can sign in to this workspace, what each person is allowed to do, and which client companies they can see.',
  inhouse: 'Who can sign in to this workspace, what each person is allowed to do, and where they sit in the approval chain.',
}

// §10.7. The message is the load-bearing half and is the same in both modes: this product
// is priced by compliance need, so there is no seat counter anywhere on this tab.
const EMPTY_TITLE: Record<PlatformCtx['mode'], string> = {
  firm: 'Just you at the firm',
  inhouse: 'Just you on the team',
}

const EMPTY_MESSAGE = "Invite as many people as you need — you're priced by compliance need, not per seat."

export function MembersView({ ctx }: { ctx: PlatformCtx }) {
  const { members, mode } = ctx
  const [query, setQuery] = useState('')
  const [roleFilter, setRoleFilter] = useState<AccessRole | 'all'>('all')
  const [inviteOpen, setInviteOpen] = useState(false)
  // The member whose drawer is open — the ID, never the row. A captured row goes stale the
  // instant `saveMember` replaces it, and resolving by id also auto-closes the drawer when
  // `dropMember` takes the row away, which is exactly what a confirmed Remove wants.
  const [drawerId, setDrawerId] = useState<string | null>(null)
  // `useCallback`, not an inline arrow: both overlays feed these straight to `useDismiss`, where
  // they are effect dependencies (useDismiss.ts:36-37), and a fresh closure every render would
  // tear down and re-register the Escape listener on every keystroke in the chip input.
  const closeInvite = useCallback(() => setInviteOpen(false), [])
  const closeDrawer = useCallback(() => setDrawerId(null), [])
  // The invite actions' inline confirmation — the WorkflowBuilder saved-flash idiom
  // (WorkflowBuilder.tsx:72-78): one transient string plus one effect-owned timer, so a
  // second action restarts the flash instead of stacking a timer that would clear the
  // newer message early.
  const [flash, setFlash] = useState<string | null>(null)
  useEffect(() => {
    if (!flash) return
    const t = window.setTimeout(() => setFlash(null), 2600)
    return () => window.clearTimeout(t)
  }, [flash])
  const shown = filterMembers(members, query, roleFilter)
  // `[two-banners]` / `[members-banner-loses-mode-gate]`: the mode gate is GONE. It existed
  // because firm rows carried no approval position, so every role read as unstaffed there;
  // both modes now seed real holders, and this renders the same `unassignedNotice` the Roles
  // tab does so the two cannot drift.
  const unassigned = unassignedRoles(ctx.roles, members)
  // Two different empty surfaces, and whether a filter is running decides which. "It's
  // just you" is a statement about the ROSTER, so it must never appear over a live
  // search — that reads as the search having deleted everyone. Filtered-to-zero always
  // gets the muted row instead: a dashed card says "nothing here yet", a muted line
  // inside the table says "your filter excluded everyone". The rule itself is NOT restated
  // here — `isFiltering` is the same one `filterMembers` short-circuits on, so `shown` and
  // this flag always answer to one definition of an empty query.
  const filtering = isFiltering(query, roleFilter)
  const justYou = members.length === 1 && !filtering

  return (
    <>
      <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '0 0 16px', maxWidth: 560, lineHeight: 1.55 }}>{INTRO[mode]}</p>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
        <input
          type="text"
          className="pf-input"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search name or email"
          aria-label="Search members"
          // .pf-input is width: 100%, so a box in a toolbar row needs its own width.
          style={{ flex: 'none', width: 260 }}
        />
        <WfSelect
          label="Access role"
          hideLabel
          value={roleFilter}
          options={ROLE_FILTER_OPTIONS}
          onChange={(v) => setRoleFilter(v as AccessRole | 'all')}
          width={180}
        />
        <button
          type="button"
          onClick={() => setInviteOpen(true)}
          data-testid="members-invite"
          className="v2-btn pf-btn"
          style={{ marginLeft: 'auto', flex: 'none', height: 38, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)', gap: 7 }}
        >
          <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Invite people
        </button>
      </div>

      {flash && (
        <div
          data-testid="members-flash"
          style={{ marginBottom: 16, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', fontSize: 12.5, color: 'var(--status-green-text)' }}
        >
          {flash}
        </div>
      )}

      {/* Above the table, and above the two empty surfaces too: it is a statement about the
          workspace's approval coverage, which a search box cannot change. */}
      {unassigned.length > 0 && (
        <AmberNote testId="members-unassigned" style={{ marginBottom: 16 }}>
          {unassignedNotice(unassigned.length)}{' '}
          <span style={{ fontWeight: 600 }}>{unassigned.map((r) => r.title).join(' · ')}</span>
        </AmberNote>
      )}

      {justYou ? (
        <EmptyState title={EMPTY_TITLE[mode]} message={EMPTY_MESSAGE} />
      ) : shown.length === 0 ? (
        // Inside the table's chrome, not a card — RulesView's empty-row-slot idiom
        // (RulesView.tsx:167-170 / :240-243). No borderBottom: this is the container's
        // last child, which already draws that edge.
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
          <div data-testid="members-no-match" style={{ padding: '20px 16px', fontSize: 13, lineHeight: 1.6, color: 'var(--fg-3)' }}>
            No members match this search.
          </div>
        </div>
      ) : (
        <MembersTable ctx={ctx} rows={shown} policies={ctx.policies} roles={ctx.roles} onOpen={setDrawerId} onFlash={setFlash} />
      )}

      {/* AFTER the ternary, not inside its last arm — so both render over the
          roster-of-one empty state and over the filtered-to-zero row as well as under the
          table. Same reasoning as the unassigned notice above: what the three roles can do
          is a statement about ROLES, which no search string and no roster size can change,
          and hiding it because a filter matched nothing would say the search had deleted
          it. The `Client users` placeholder follows for §6's own reason — it exists to
          keep an open question visible, and a search box must not close a question. */}
      <MemberRoleMatrix />
      {/* Firm only, and gated here beside this tab's other two mode forks: in-house
          renders no node at all, not a hidden one. */}
      {mode === 'firm' && <ClientUsersCard />}

      {/* Rendered conditionally rather than mounted-and-hidden, the ClientsView/EntityFormModal
          form — which is also what lets the modal call `useDismiss(true, …)` and register no
          listener at all while closed. Its success confirmation goes through THIS tab's existing
          flash, not a second mechanism: every other action here flashes, and the one that
          actually changes the roster must not be the silent one. */}
      {inviteOpen && <InviteMembersModal ctx={ctx} onClose={closeInvite} onFlash={setFlash} />}

      {/* Same conditional-mount form, for the same two reasons. The drawer raises no flash of
          its own: §8's rule is that every change persists IMMEDIATELY and is visible in the
          table behind it, so the table is the confirmation and a green banner over it would be
          a second, slower one.

          `key={drawerId}` is one mechanism doing two jobs: it re-seeds `ClientAccessPicker`,
          whose ticked set is own state seeded once from `value`, and it clears the drawer's
          remove-confirm — both of which would otherwise carry over from the previous subject.
          Opening another member without closing first is unreachable behind the scrim today,
          which is exactly why a key is the right guard rather than two effects. */}
      {drawerId != null && <MemberDrawer key={drawerId} ctx={ctx} memberId={drawerId} onClose={closeDrawer} />}
    </>
  )
}
