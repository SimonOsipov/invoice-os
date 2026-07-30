// Settings › Members — the first and default Settings tab.
//
// A TAB, not a screen: no `View` member, no `NavId`, no crumb. SettingsView already
// renders the page eyebrow / heading / subtitle above the tab strip, so this panel opens
// with an intro paragraph and its top bar, exactly as the ERP connectors tab does.
//
// Everything transient lives here rather than on ctx — the search box, the role filter,
// and (later) which drawer or menu is open — following the rationale SettingsView states
// for its own view state at SettingsView.tsx:6-9. The roster itself is on ctx, because
// in-house Workflows resolves approval positions against the same list.
//
// The consequence, accepted rather than worked around: switching to another Settings tab
// unmounts this one, so the search text and role filter reset.

import { useState } from 'react'

import { EmptyState } from '@invoice-os/api-client'
import { plusGlyph } from '../glyphs'
import { ACCESS_ROLES, filterMembers, isFiltering, type AccessRole } from '../lib/members'
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
  const shown = filterMembers(members, query, roleFilter)
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
        {/* Wired to the invite modal in MEMB-01-06. */}
        <button
          type="button"
          className="v2-btn pf-btn"
          style={{ marginLeft: 'auto', flex: 'none', height: 38, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)', gap: 7 }}
        >
          <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Invite people
        </button>
      </div>

      {/* The table itself lands in MEMB-01-04, in the `shown.length > 0` case. */}
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
      ) : null}
    </>
  )
}
