// Settings › Members — the first and default Settings tab.
//
// A TAB, not a screen: no `View` member, no nav item, no crumb. SettingsView already
// renders the page eyebrow / heading / subtitle above the tab strip, so this panel opens
// with an intro paragraph and its top bar, exactly as the ERP connectors tab does.
//
// Everything transient lives here rather than on ctx — the search box, the role filter,
// which drawer is open, and a failed status write's reason — following the rationale
// SettingsView states for its own view state at SettingsView.tsx:6-9. The roster itself is
// on ctx, because the Workflows builder resolves a step's role against the same list.
//
// The consequence, accepted rather than worked around: switching to another Settings tab
// unmounts this one, so the search text and role filter reset.

import { useCallback, useId, useState } from 'react'

import { EmptyState, ErrorState, Loading, toApiError } from '@invoice-os/api-client'
import { plusGlyph, searchGlyph } from '../glyphs'
import {
  ACCESS_ROLES,
  filterMembers,
  isFiltering,
  MEMBER_UNBACKED,
  type AccessRole,
  type MemberStatus,
} from '../lib/members'
import { rolesSurface, unassignedNotice, unassignedRoles } from '../lib/roles'
import { MemberDrawer } from './MemberDrawer'
import { AmberNote } from './MemberParts'
import { ClientUsersCard, MemberRoleMatrix } from './MemberRoleMatrix'
import { MembersTable } from './MembersTable'
import { WfSelect, type WfOption } from './WorkflowParts'
import type { PlatformCtx } from '../types'

// A constant, not a derivation — the SLA_OPTIONS idiom in WorkflowParts.tsx.
// 'all' is just another option; WfSelect's `value`/`onChange` are plain strings and the
// caller narrows on the way back, the WorkflowInspector.tsx:120 idiom.
const ROLE_FILTER_OPTIONS: WfOption[] = [
  { value: 'all', label: 'All roles' },
  ...ACCESS_ROLES.map((r) => ({ value: r.id, label: r.label })),
]

// Copy forks on mode, structure never does — the Rules/Workflows screens' rule. Firm's
// third clause named the client-scoping column, which no membership row backs; it is
// deleted rather than reworded.
const INTRO: Record<PlatformCtx['mode'], string> = {
  firm: 'Who can sign in to this workspace, what each person is allowed to do.',
  inhouse: 'Who can sign in to this workspace, what each person is allowed to do, and where they sit in the approval chain.',
}

// §10.7. The message is the load-bearing half and is the same in both modes: this product
// is priced by compliance need, so there is no seat counter anywhere on this tab.
const EMPTY_TITLE: Record<PlatformCtx['mode'], string> = {
  firm: 'Just you at the firm',
  inhouse: 'Just you on the team',
}

// The invite clause is DELETED, not reworded: invite is disabled-with-a-reason on this
// tab, and an empty state that promises it is the defect this story exists to fix.
const EMPTY_MESSAGE = "You're priced by compliance need, not per seat."

export function MembersView({ ctx }: { ctx: PlatformCtx }) {
  const { members, mode, setMemberStatus } = ctx
  const [query, setQuery] = useState('')
  const [roleFilter, setRoleFilter] = useState<AccessRole | 'all'>('all')
  // The member whose drawer is open — the ID, never the row. A captured row goes stale the
  // instant a status write replaces it.
  const [drawerId, setDrawerId] = useState<string | null>(null)
  // `useCallback`, not an inline arrow: the drawer feeds this straight to `useDismiss`, where
  // it is an effect dependency (useDismiss.ts:36-37), and a fresh closure every render would
  // tear down and re-register the Escape listener on every keystroke in the search box.
  const closeDrawer = useCallback(() => setDrawerId(null), [])
  const inviteNoteId = useId()
  // The failed status write's SERVER reason, scoped to the row it happened on. Transient view
  // state, so it lives here rather than on ctx — and here rather than in either child, because
  // the table's `⋯` menu closes on select and the drawer can be shut before the promise
  // settles, so neither control is guaranteed to exist when the answer arrives.
  const [statusError, setStatusError] = useState<{ id: string; message: string } | null>(null)
  const changeStatus = useCallback(
    (id: string, status: Exclude<MemberStatus, 'invited'>) => {
      setStatusError(null)
      // `toApiError(err).message` IS the gateway's own sentence (api-client's client.ts parses
      // the `{"error": …}` envelope into it). Rendered verbatim — a generic substitution here
      // would tell the user the write failed but never why.
      return setMemberStatus(id, status).catch((err: unknown) => setStatusError({ id, message: toApiError(err).message }))
    },
    [setMemberStatus],
  )
  // rolesSurface's 'empty' branch is RolesView's own "no roles yet" card — wrong here, since
  // this roster doesn't care how many roles exist, only whether the fetch landed.
  const rolesStatusForRoster = ctx.rolesState === 'empty' ? 'ready' : ctx.rolesState
  const surface = rolesSurface(rolesStatusForRoster, ctx.membersState)
  // RolesView.tsx:68-71's own shape: only the fetch(es) that actually failed get retried.
  const retryRoster = useCallback(() => {
    if (ctx.rolesState === 'error') ctx.refetchRoles()
    if (ctx.membersState === 'error') ctx.refetchMembers()
  }, [ctx.rolesState, ctx.membersState, ctx.refetchRoles, ctx.refetchMembers])
  const shown = filterMembers(members, query, roleFilter)
  // No mode gate, and the same `unassignedNotice` the Roles tab renders, so the two cannot
  // drift. Both resolve against the live roster.
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
      <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '-4px 0 16px', maxWidth: 680, lineHeight: 1.6 }}>{INTRO[mode]}</p>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: 300, height: 36, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)' }}>
          <span aria-hidden="true" style={{ flex: 'none', display: 'inline-flex', color: 'var(--fg-3)' }}>
            {searchGlyph}
          </span>
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search name or email"
            aria-label="Search members"
            style={{ flex: 1, minWidth: 0, border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)' }}
          />
        </div>
        <WfSelect
          label="Access role"
          hideLabel
          value={roleFilter}
          options={ROLE_FILTER_OPTIONS}
          onChange={(v) => setRoleFilter(v as AccessRole | 'all')}
          height={36}
          background="var(--bg-2)"
        />
        {/* The real `disabled` attribute plus the inline recipe: `filter: 'none'` outranks
            `.v2-btn:hover`'s brightness lift, which nothing in design-tokens guards with `:disabled`. */}
        <button
          type="button"
          disabled
          title={MEMBER_UNBACKED.invite}
          aria-describedby={inviteNoteId}
          data-testid="members-invite"
          className="v2-btn v2-btn-primary pf-btn"
          style={{ marginLeft: 'auto', flex: 'none', height: 36, gap: 7, opacity: 0.45, cursor: 'not-allowed', filter: 'none' }}
        >
          <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Invite people
        </button>
      </div>

      <div id={inviteNoteId} data-testid="members-invite-reason" style={{ margin: '0 0 14px', fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'right' }}>
        {MEMBER_UNBACKED.invite}
      </div>

      {surface === 'loading' && <Loading label="Loading members…" />}

      {surface === 'error' && (ctx.rolesError ?? ctx.membersError) && (
        <ErrorState error={(ctx.rolesError ?? ctx.membersError)!} onRetry={retryRoster} />
      )}

      {surface === 'empty' && <EmptyState title={EMPTY_TITLE[mode]} message={EMPTY_MESSAGE} />}

      {surface === 'roster' && (
        <>
          {/* Above the table, and above the two empty surfaces too: it is a statement about
              the workspace's approval coverage, which a search box cannot change. Inside
              this arm, though — over an errored roster every role resolves to zero holders
              and this would assert a coverage failure that is really a fetch failure. */}
          {unassigned.length > 0 && (
            <AmberNote testId="members-unassigned" style={{ marginBottom: 14 }}>
              {unassignedNotice(unassigned.length)}{' '}
              <span style={{ fontWeight: 700 }}>{unassigned.map((r) => r.title).join(' · ')}</span>
            </AmberNote>
          )}

          {justYou ? (
            <EmptyState title={EMPTY_TITLE[mode]} message={EMPTY_MESSAGE} />
          ) : (
            <MembersTable
              ctx={ctx}
              rows={shown}
              policies={ctx.policies}
              roles={ctx.roles}
              onOpen={setDrawerId}
              onStatus={changeStatus}
              statusError={statusError}
            />
          )}
        </>
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
          form — which is also what lets the drawer call `useDismiss(true, …)` and register no
          listener at all while closed. It raises no confirmation of its own: a status write is
          visible in the table behind it the moment the server's row lands, so the table is the
          confirmation and a banner over it would be a second, slower one.

          `key={drawerId}` re-seeds `ClientAccessPicker`, whose ticked set is own state seeded
          once from `value`. Opening another member without closing first is unreachable behind
          the scrim today, which is exactly why a key is the right guard rather than an effect. */}
      {drawerId != null && (
        <MemberDrawer key={drawerId} ctx={ctx} memberId={drawerId} onClose={closeDrawer} onStatus={changeStatus} statusError={statusError} />
      )}
    </>
  )
}
