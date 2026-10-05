// @vitest-environment jsdom
//
// QA (Stage 4) coverage for APPR-15-06 -- no component test existed for this tab before this
// commit. RolesView shares `membersSurface` with MembersView (MembersView.tsx / RolesView.tsx
// docblocks: "two rosters of one tenant on one screen must not disagree"), so the ladder is
// re-verified here rather than assumed from the sibling tab's coverage.
//
// Extended for the rolesSurface repoint (AC-1 through AC-4): the ladder must branch on BOTH
// the roles fetch and the members fetch, not membersState alone.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { RolesView } from './RolesView'

function member(over: Partial<Member> = {}): Member {
  return {
    id: 'u1',
    name: 'Ada Person',
    initials: 'AP',
    email: 'ada@x.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
    ...over,
  }
}

function role(over: Partial<Role> = {}): Role {
  return { key: 'finance-approver', title: 'Finance Approver', desc: 'Approves finance invoices', members: [], ...over }
}

/** One approval step naming `role()`'s default key — the landed answer the gate must let through. */
function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'finance-approver', sla: '24', delegate: false }],
    ...over,
  }
}

function Harness({
  members = [],
  roles = [],
  policies = [],
  policiesState = 'ready',
  rolesState = 'ready',
  rolesError = null,
  membersState = 'ready',
  membersError = null,
  refetchRoles,
  refetchMembers,
}: {
  members?: Member[]
  roles?: Role[]
  policies?: Policy[]
  policiesState?: AsyncStatus
  rolesState?: AsyncStatus
  rolesError?: ApiError | null
  membersState?: AsyncStatus
  membersError?: ApiError | null
  refetchRoles?: () => void
  refetchMembers?: () => void
}) {
  const ctx = {
    members,
    roles,
    policies,
    policiesState,
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    rolesState,
    rolesError,
    refetchRoles: refetchRoles ?? vi.fn(),
    membersState,
    membersError,
    refetchMembers: refetchMembers ?? vi.fn(),
    createRole: vi.fn(),
    renameRole: vi.fn(),
    staffRole: vi.fn(),
    deleteRole: vi.fn(),
  }
  return <RolesView ctx={ctx as unknown as PlatformCtx} />
}

afterEach(cleanup)

describe('AC-1: RolesView branches on rolesSurface(rolesState, membersState), never on roles.length/members.length', () => {
  it('an errored roles fetch renders the error surface, not an empty grid', () => {
    render(<Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" />)

    expect(screen.getByText('gateway is down')).toBeTruthy()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
    expect(screen.queryByTestId('roles-empty')).toBeNull()
  })

  it('an errored members fetch under a landed roles fetch still blocks the grid', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.queryByTestId('roles-grid')).toBeNull()
    expect(screen.getByText('gateway is down')).toBeTruthy()
  })

  it('a loading roles fetch renders no card', () => {
    render(<Harness roles={[role()]} rolesState="loading" membersState="ready" />)

    expect(screen.queryByTestId('role-card')).toBeNull()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
  })
})

describe('AC-2: ErrorState renders rolesError ?? membersError; retry calls the matching refetch', () => {
  it('retry calls refetchRoles when the roles fetch is the one that failed', () => {
    const refetchRoles = vi.fn()
    render(<Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" refetchRoles={refetchRoles} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
  })

  it('retry calls refetchMembers when the members fetch is the one that failed', () => {
    const refetchMembers = vi.fn()
    render(<Harness rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchMembers={refetchMembers} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchMembers).toHaveBeenCalledOnce()
  })

  // QA: mutation-tested gap. `retry` firing refetchMembers unconditionally stayed green
  // under the existing two tests, since neither asserted the OTHER fetch's refetch was NOT
  // called. The docblock's own claim ("a roles-only failure must not re-kick members",
  // RolesView.tsx:67) had nothing pinning it.
  it('retry does not call refetchMembers when only the roles fetch errored', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" refetchRoles={refetchRoles} refetchMembers={refetchMembers} />,
    )

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
    expect(refetchMembers).not.toHaveBeenCalled()
  })

  it('retry does not call refetchRoles when only the members fetch errored', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchRoles={refetchRoles} refetchMembers={refetchMembers} />,
    )

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchMembers).toHaveBeenCalledOnce()
    expect(refetchRoles).not.toHaveBeenCalled()
  })

  it('retry calls both refetches when both fetches errored, and the roles error message wins', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness
        rolesState="error"
        rolesError={new ApiError('http', 'roles down', 503)}
        membersState="error"
        membersError={new ApiError('http', 'members down', 503)}
        refetchRoles={refetchRoles}
        refetchMembers={refetchMembers}
      />,
    )

    expect(screen.getByText('roles down')).toBeTruthy()
    expect(screen.queryByText('members down')).toBeNull()

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
    expect(refetchMembers).toHaveBeenCalledOnce()
  })
})

describe('AC-3: roles-empty renders only on a landed, genuinely empty roles list', () => {
  it('the empty state renders on a landed, empty roles list', () => {
    render(<Harness roles={[]} rolesState="ready" membersState="ready" />)

    expect(screen.getByTestId('roles-empty')).toBeTruthy()
  })

  it('the empty state does not render over an errored roles fetch, even with an empty list', () => {
    render(<Harness roles={[]} rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" />)

    expect(screen.queryByTestId('roles-empty')).toBeNull()
  })
})

describe('AC-4: the roles-unassigned banner stays gated on the full roster surface', () => {
  it('the unassigned banner never fires over an unlanded roles fetch', () => {
    // Zero holders makes the role genuinely unassigned once the roster has landed -- what
    // this isolates is that an UNLANDED roles fetch suppresses the notice regardless.
    render(<Harness roles={[role()]} rolesState="loading" membersState="ready" />)

    expect(screen.queryByTestId('roles-unassigned')).toBeNull()
  })

  it('the same unheld role does raise the notice once the roster has actually landed', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" />)

    expect(screen.getByTestId('roles-unassigned')).toBeTruthy()
  })
})

// ============================================================================
// APPR-09-06 (task-510) — the fifth `ctx.policies` reader
// ============================================================================
// The card footer renders on EVERY card and was gated only by `rolesSurface(rolesState,
// membersState)`, which does not carry policies — so an unlanded fetch read 'not used in any
// policy' on every card at once, the same false claim as RoleModal's delete confirm at grid
// volume. The footer now forks on `policiesLanded` (RolesView.tsx:181).

describe('APPR-09-06 AC-1/AC-3: a role card claims policy usage only off a landed policies fetch', () => {
  /** Header / holders / footer. MembersTable.test.tsx:42's `row.children[2]` idiom — the
   *  footer carries no test id, and RolesView.tsx is not edited to give it one. */
  function footerOf(card: HTMLElement): HTMLElement {
    return card.children[2] as HTMLElement
  }

  it('a role card claims no policy usage only once the policies fetch has landed', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="loading" />)

    const footer = footerOf(screen.getByTestId('role-card'))
    // Population floor: the footer's OTHER slot must still render, or both absences below
    // would pass on a card that simply lost its footer.
    expect(footer.textContent, 'the footer did not render, so the absences below are vacuous').toContain('0 people')
    expect(footer.textContent, 'an unlanded policies fetch reads as "this role is used nowhere"').not.toContain('not used in any policy')
    // The unlanded answer is the EMPTY STRING. '—' claims "nothing here" in its own right —
    // MembersTable.tsx:195-197 sets that convention, pinned on the roster side at
    // MembersTable.test.tsx:49.
    expect(footer.textContent, "the unlanded footer fell back to the em dash, which is its own claim").not.toContain('—')
  })

  it('a landed-empty policy list still prints the real zero-usage footer', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="empty" />)

    expect(footerOf(screen.getByTestId('role-card')).textContent, 'the guard swallowed a genuinely landed-empty answer').toContain(
      'not used in any policy',
    )
  })

  // ------------------------------------------------------------------------
  // QA (Stage 4) — adversarial coverage the RED set did not carry
  // ------------------------------------------------------------------------

  it('a landed policy that names the role prints the real count — the footer CAN make a claim', () => {
    // The population floor under BOTH absences above: without this, a gate that blanked the
    // footer in every state would satisfy every assertion in this describe.
    render(
      <Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[policy()]} policiesState="ready" />,
    )

    expect(footerOf(screen.getByTestId('role-card')).textContent).toContain('1 approval step · 1 policy')
  })

  it('an errored policies fetch withholds the claim too, not only a loading one', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="error" />)

    const footer = footerOf(screen.getByTestId('role-card'))
    expect(footer.textContent, 'the footer did not render, so the absences below are vacuous').toContain('0 people')
    expect(footer.textContent).not.toContain('not used in any policy')
    expect(footer.textContent).not.toContain('—')
  })

  it("'idle' — no gateway configured — is the LANDED side, matching the Workflows screen", () => {
    // `membersSurface` folds 'idle' into 'empty' (lib/members.ts:586), which WorkflowsView.tsx:65-68
    // relies on to render its own no-policies-yet card on that build. A gate written as
    // `surface === 'roster'` would disagree with the Workflows screen about the same fetch.
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="idle" />)

    expect(footerOf(screen.getByTestId('role-card')).textContent).toContain('not used in any policy')
  })

  it('a refetch WITHHOLDS the last landed count rather than printing it stale', () => {
    // Reachable: createPolicy/deletePolicy call `policiesAsync.run()` (App.tsx:1037,:1046), which
    // dispatches 'start' → status 'loading' while the mirror keeps the rows it just patched
    // (App.tsx:299-301). So 'loading' WITH rows is a real state, and this pins which way it falls.
    // DELIBERATE DIVERGENCE from MemberDrawer/MembersTable, which keep printing off the same
    // stale rows — their gate is `stepsForMember`'s null-at-zero, which never makes a negative
    // claim, so they have nothing to withhold. Withholding here is one round trip of silence
    // against a sentence that could be wrong; the Workflows list blanks wholesale in the same
    // window (WorkflowsView.tsx:116's `surface === 'loading'` arm).
    render(
      <Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[policy()]} policiesState="loading" />,
    )

    const footer = footerOf(screen.getByTestId('role-card'))
    expect(footer.textContent, 'the footer did not render, so the absence below is vacuous').toContain('0 people')
    expect(footer.textContent, 'the mid-refetch footer printed a count the fetch had not confirmed').not.toContain('approval step')
  })

  it('EVERY card blanks, not merely the first', () => {
    const roles = [role(), role({ key: 'cfo', title: 'CFO' }), role({ key: 'ceo', title: 'CEO' })]
    render(<Harness roles={roles} rolesState="ready" membersState="ready" policies={[]} policiesState="loading" />)

    const cards = screen.getAllByTestId('role-card')
    expect(cards, 'the grid rendered no cards, so the loop below is vacuous').toHaveLength(roles.length)
    for (const card of cards) {
      const footer = footerOf(card)
      expect(footer.textContent, 'a card lost its footer, so its absence below is vacuous').toContain('0 people')
      expect(footer.textContent, 'one card still claims "used nowhere" off an unlanded fetch').not.toContain('not used in any policy')
    }
  })
})

describe('a null-email member in the role modal picker renders the shared em dash, not "null"', () => {
  it('the picker row shows the em dash label', () => {
    render(<Harness members={[member({ email: null })]} roles={[role()]} rolesState="ready" membersState="ready" />)

    fireEvent.click(screen.getByTestId('role-card-edit'))
    const picker = screen.getByTestId('role-modal-member')
    expect(within(picker).getByText('—')).toBeTruthy()
    expect(within(picker).queryByText(/null/i)).toBeNull()
  })
})

// ============================================================================
// v2 paint: the Roles tab against the prototype's resolved values
// ============================================================================

describe('Roles tab v2 paint', () => {
  const crew = [
    member({ id: 'u1', name: 'Ada Person', initials: 'AP', role: 'reviewer' }),
    member({ id: 'u2', name: 'Bo Person', initials: 'BP', role: 'reviewer' }),
    member({ id: 'u3', name: 'Cy Person', initials: 'CP', role: 'reviewer', status: 'invited' }),
    member({ id: 'u4', name: 'Di Person', initials: 'DP', role: 'reviewer', status: 'suspended' }),
    member({ id: 'u5', name: 'Ed Person', initials: 'EP', role: 'reviewer' }),
    member({ id: 'u6', name: 'Fi Person', initials: 'FP', role: 'reviewer' }),
    member({ id: 'u7', name: 'Gi Person', initials: 'GP', role: 'reviewer' }),
  ]
  const crewRoles = [
    role({ key: 'many', title: 'Many', desc: 'Seven hold it', members: crew.map((m) => m.id) }),
    role({ key: 'one', title: 'One', desc: 'Ada holds it', members: ['u1'] }),
    role({ key: 'nobody', title: 'Nobody', desc: '', members: [] }),
    role({ key: 'mixed', title: 'Mixed', desc: 'Invited and suspended', members: ['u3', 'u4'] }),
  ]
  const cardFor = (title: string) =>
    screen.getAllByTestId('role-card').find((c) => c.textContent?.includes(title)) as HTMLElement

  function renderCrew() {
    render(<Harness members={crew} roles={crewRoles} policies={[policy()]} rolesState="ready" membersState="ready" />)
  }

  it('no oklch, no 99/999 radius and no avatar box-shadow in the grid, the no-match card or the empty card', () => {
    const states: [string, () => void][] = [
      ['grid', () => renderCrew()],
      [
        'no match',
        () => {
          renderCrew()
          fireEvent.change(screen.getByLabelText('Search roles'), { target: { value: 'zzzz' } })
        },
      ],
      ['empty', () => render(<Harness roles={[]} rolesState="ready" membersState="ready" />)],
    ]
    for (const [name, mount] of states) {
      cleanup()
      mount()
      const all = Array.from(document.body.querySelectorAll<HTMLElement>('*'))
      expect(all.length, `${name}: nothing rendered`).toBeGreaterThan(10)
      expect(document.body.innerHTML, `${name}: oklch`).not.toContain('oklch')
      for (const el of all) {
        expect(['99px', '999px'], `${name}: radius on <${el.tagName.toLowerCase()}>`).not.toContain(el.style.borderRadius)
      }
    }
    cleanup()
    renderCrew()
    const avatars = within(cardFor('Many')).getAllByText(/^[A-Z]P$/)
    expect(avatars.length, 'the avatar stack rendered').toBe(5)
    for (const a of avatars) {
      expect([a.style.boxShadow, a.parentElement!.style.boxShadow], `avatar ${a.textContent} box-shadow`).toEqual(['', ''])
    }
  })

  it('the card is padded 16/16/14, gapped 12, with a 15/700 title and a 12px lh 1.5 description', () => {
    renderCrew()
    const card = cardFor('One')
    expect(card.style.padding).toBe('16px 16px 14px')
    expect(card.style.gap).toBe('12px')
    expect(card.style.border).toContain('var(--line-1)')
    expect(card.style.borderRadius).toBe('var(--radius-md)')
    const title = screen.getAllByText('One', { exact: true })[0]
    expect([title.style.fontSize, title.style.fontWeight]).toEqual(['15px', '700'])
    const desc = screen.getByText('Ada holds it', { exact: true })
    expect([desc.style.fontSize, desc.style.lineHeight, desc.style.marginTop, desc.style.minHeight]).toEqual(['12px', '1.5', '3px', '36px'])
  })

  it('Edit is the 28px ghost: 12px, padding 0 11px, no inline border or fill', () => {
    renderCrew()
    const edits = screen.getAllByTestId('role-card-edit')
    expect(edits.length).toBe(crewRoles.length)
    for (const e of edits) {
      expect(e.className).toContain('v2-btn-ghost')
      expect([e.style.height, e.style.padding, e.style.fontSize]).toEqual(['28px', '0px 11px', '12px'])
      expect([e.style.border, e.style.background]).toEqual(['', ''])
    }
  })

  it('avatars overlap by -7 with the card-ground ring, and +N shows only past five holders', () => {
    renderCrew()
    const many = within(cardFor('Many'))
    const stack = many.getByText('AP').parentElement!.parentElement as HTMLElement
    expect(stack.style.paddingRight).toBe('7px')
    for (const a of many.getAllByText(/^[A-Z]P$/)) {
      expect(a.style.border, `${a.textContent} ring`).toBe(a.textContent === 'CP' ? '1px dashed var(--line-3)' : '2px solid var(--bg-2)')
      expect(a.parentElement!.style.marginRight).toBe('-7px')
      expect(a.style.borderRadius).toBe('50%')
    }
    const more = many.getByText('+2')
    expect([more.className, more.style.fontSize, more.style.color]).toEqual(['mono', '10px', 'var(--fg-3)'])
    expect(within(cardFor('One')).queryByText(/^\+\d/)).toBeNull()
    expect(many.getByText('AP').parentElement!.getAttribute('title'), 'avatar names itself').toBe('Ada Person')
  })

  it('a role with holders always draws the overflow slot, empty when there is no overflow', () => {
    renderCrew()
    const one = within(cardFor('One')).getByTestId('role-card-overflow')
    expect(one.textContent).toBe('')
    expect(within(cardFor('Many')).getByTestId('role-card-overflow').textContent).toBe('+2')
    expect(within(cardFor('Nobody')).queryByTestId('role-card-overflow')).toBeNull()
  })

  it('the holder line keeps var(--fg-2), and var(--status-red-text) when the seat cannot sign', () => {
    renderCrew()
    expect(screen.getByText('Ada Person', { exact: true }).style.color).toBe('var(--fg-2)')
    const nobody = within(cardFor('Nobody')).getByText('Nobody assigned')
    expect(nobody.style.color).toBe('var(--status-red-text)')
    expect(nobody.style.flex).toContain('1')
  })

  it('the footer is a bordered, space-between row of 9.5px mono labels', () => {
    renderCrew()
    const footer = cardFor('One').children[2] as HTMLElement
    expect([footer.style.borderTop, footer.style.paddingTop, footer.style.justifyContent]).toEqual(['1px solid var(--line-1)', '10px', 'space-between'])
    expect(footer.children.length).toBe(2)
    for (const label of Array.from(footer.children) as HTMLElement[]) {
      expect([label.className, label.style.fontSize, label.style.textTransform, label.style.letterSpacing]).toEqual(['mono', '9.5px', 'uppercase', '0.05em'])
    }
    expect(footer.textContent).toContain('1 person')
  })

  it('New role is the 36px primary pf-btn, in the toolbar and in the empty card', () => {
    renderCrew()
    const toolbarBtn = screen.getByTestId('roles-new') as HTMLButtonElement
    cleanup()
    render(<Harness roles={[]} rolesState="ready" membersState="ready" />)
    for (const b of [toolbarBtn, screen.getByTestId('roles-empty-new') as HTMLButtonElement]) {
      expect(b.className.split(' ').sort()).toEqual(['pf-btn', 'v2-btn', 'v2-btn-primary'])
      expect(b.style.height).toBe('36px')
      expect(b.style.background, 'the primary fill is the class').toBe('')
    }
  })

  it('the empty card holds New role as its last child, under a 460px message', () => {
    render(<Harness roles={[]} rolesState="ready" membersState="ready" />)
    const empty = screen.getByTestId('roles-empty')
    expect(empty.children.length, 'the wrapper is the card alone').toBe(1)
    const card = empty.firstElementChild as HTMLElement
    expect(card.lastElementChild).toBe(screen.getByTestId('roles-empty-new'))
    expect(within(card).getByText(/Create the seats/).style.maxWidth).toBe('460px')
    expect(screen.getAllByRole('button', { name: /New role/ }).length, 'the toolbar button is the only other').toBe(2)
  })

  it('the intro reads 680 wide at lh 1.6 and the toolbar search is the 300x36 bordered box with a glyph', () => {
    renderCrew()
    const intro = screen.getByText(/A role is a named seat/)
    expect([intro.style.maxWidth, intro.style.lineHeight, intro.style.margin]).toEqual(['680px', '1.6', '-4px 0px 16px'])
    const input = screen.getByTestId('roles-search')
    expect(input.className, 'a bare input inside the wrapper').toBe('')
    const box = input.parentElement as HTMLElement
    expect([box.style.width, box.style.height, box.style.padding, box.style.background]).toEqual(['300px', '36px', '0px 12px', 'var(--bg-2)'])
    expect(box.style.border).toContain('var(--line-2)')
    expect(box.querySelector('svg'), 'the search glyph').toBeTruthy()
  })

  it('the unassigned notice carries 14px under it and a bold names line', () => {
    renderCrew()
    const note = screen.getByTestId('roles-unassigned')
    expect(note.style.marginBottom).toBe('14px')
    const names = note.querySelector('div') as HTMLElement
    expect(names.style.fontWeight).toBe('700')
    expect(names.textContent).toBe('Nobody · Mixed')
  })

  it('the no-match card is centred, 12.5px, padded 26/16', () => {
    renderCrew()
    fireEvent.change(screen.getByLabelText('Search roles'), { target: { value: 'zzzz' } })
    const card = screen.getByTestId('roles-no-match')
    expect([card.style.textAlign, card.style.fontSize, card.style.padding]).toEqual(['center', '12.5px', '26px 16px'])
  })

  it('the unassigned notice sits above the toolbar', () => {
    renderCrew()
    const note = screen.getByTestId('roles-unassigned')
    const toolbar = screen.getByTestId('roles-new').parentElement as HTMLElement
    expect(note.compareDocumentPosition(toolbar) & Node.DOCUMENT_POSITION_FOLLOWING, 'notice precedes toolbar').toBeTruthy()
  })
})
