// @vitest-environment jsdom
// Explicit props, no ctx -- plain render(), no vi.stubEnv/resetModules/ctx cast.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'
import { accessRoleLabel, type Member } from '../lib/members'
import { SEAT_LABEL, SUSPENDED_REASON } from './copy'
import { PersonaPopover } from './PersonaPopover'

afterEach(cleanup)

function member(over: Partial<Member> = {}): Member {
  return {
    id: 'm-chinedu',
    name: 'Chinedu Okafor',
    initials: 'CO',
    email: 'chinedu@example.ng',
    role: 'admin',
    status: 'active',
    isYou: true,
    ...over,
  }
}

// The seeded Okafor & Partners firm cast (db/seed.dev.sql:41-46): 5 active + 1 suspended.
const SEAT = member()
const FOLAKE = member({ id: 'm-folake', name: 'Folake Adesina', initials: 'FA', role: 'preparer', isYou: false })
const MUSA = member({ id: 'm-musa', name: 'Musa Danjuma', initials: 'MD', role: 'reviewer', isYou: false })
const CHIAMAKA = member({ id: 'm-chiamaka', name: 'Chiamaka Nwosu', initials: 'CN', role: 'reviewer', isYou: false })
const OLUWASEYIFUNMI = member({ id: 'm-oluwaseyifunmi', name: 'Oluwaseyifunmi Adebanjo-Ogunleye', initials: 'OA', role: 'preparer', isYou: false })
const HALIMA = member({ id: 'm-halima', name: 'Halima Yusuf', initials: 'HY', role: 'reviewer', status: 'suspended', isYou: false })

const FIRM_ROSTER: Member[] = [SEAT, FOLAKE, MUSA, CHIAMAKA, OLUWASEYIFUNMI, HALIMA]

type PopoverProps = {
  members: Member[]
  membersState: AsyncStatus
  membersError: ApiError | null
  seatSubject: string | undefined
  standingIn: boolean
  onSelect: (member: Member) => void
  onReturn: () => void
}

function renderPopover(over: Partial<PopoverProps> = {}) {
  const props: PopoverProps = {
    members: FIRM_ROSTER,
    membersState: 'ready',
    membersError: null,
    seatSubject: SEAT.id,
    standingIn: false,
    onSelect: vi.fn(),
    onReturn: vi.fn(),
    ...over,
  }
  return render(<PersonaPopover {...props} />)
}

describe('PersonaPopover', () => {
  // Row 1 (AC-3). Red today: no component.
  it('renders exactly ctx.members, in order', () => {
    renderPopover({ members: FIRM_ROSTER })
    const rows = screen.getAllByTestId('persona-row')
    expect(rows.length).toBe(FIRM_ROSTER.length)
    const names = rows.map((r) => within(r).getByTestId('persona-row-name').textContent)
    expect(names).toEqual(FIRM_ROSTER.map((m) => m.name))
  })

  // Row 3 (AC-4). textContent ignores CSS truncation -- both the full string and the
  // truncation styles are asserted, since jsdom cannot measure whether it actually fits.
  it('renders the 32-character name in full, with truncation CSS in place', () => {
    renderPopover({ members: FIRM_ROSTER })
    const rows = screen.getAllByTestId('persona-row')
    const target = rows.find((r) => within(r).getByTestId('persona-row-name').textContent === OLUWASEYIFUNMI.name)
    expect(target).not.toBeUndefined()
    const name = within(target!).getByTestId('persona-row-name')
    expect(name.textContent).toBe(OLUWASEYIFUNMI.name)
    expect(name.style.whiteSpace).toBe('nowrap')
    expect(name.style.textOverflow).toBe('ellipsis')
  })

  // Row 4 (AC-4). accessRoleLabel is sentence case (lib/members.ts:76) -- the caps come
  // from CSS on the meta node, not from the label itself.
  it("states each row's access role in sentence case, uppercased by CSS", () => {
    renderPopover({ members: FIRM_ROSTER })
    const rows = screen.getAllByTestId('persona-row')
    FIRM_ROSTER.forEach((m, i) => {
      const meta = within(rows[i]).getByTestId('persona-row-meta')
      expect(meta.textContent?.startsWith(accessRoleLabel(m.role))).toBe(true)
      expect(meta.style.textTransform).toBe('uppercase')
    })
  })

  // Row 5 (AC-5). "carries no onClick" is unobservable in jsdom -- structural oracle instead.
  it('shows the suspended member, unclickable, stating why', () => {
    renderPopover({ members: FIRM_ROSTER })
    const rows = screen.getAllByTestId('persona-row')
    const row = rows[FIRM_ROSTER.findIndex((m) => m.id === HALIMA.id)]
    expect(row.tagName).toBe('DIV')
    expect(row.hasAttribute('disabled')).toBe(false)
    expect(row.style.cursor).toBe('not-allowed')
    expect(row.textContent).toContain(SUSPENDED_REASON)
  })

  // Row 6 (AC-5). Spies onSelect -- this subtask's own prop, not ctx.becomePersona (which
  // nothing calls until DEMO-06-05). The active-row click is the non-vacuity control for
  // the blocked row's zero-call assertion, in the same render.
  it('a blocked row cannot select; an active row can, in the same render', () => {
    const onSelect = vi.fn()
    renderPopover({ members: [FOLAKE, HALIMA], onSelect })
    const rows = screen.getAllByTestId('persona-row')
    const blockedRow = rows.find((r) => r.tagName === 'DIV')
    const activeRow = rows.find((r) => r.tagName === 'BUTTON')
    expect(blockedRow).not.toBeUndefined()
    expect(activeRow).not.toBeUndefined()

    fireEvent.click(blockedRow!)
    expect(onSelect).toHaveBeenCalledTimes(0)

    fireEvent.click(activeRow!)
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(FOLAKE)
  })

  // Row 9 (AC-9). The design's --accent resolves to teal under the prototype's own sheet
  // (app-layer.css:10) -- var(--action) is the faithful reproduction, not a substitution.
  it("the current row's tick uses var(--action)", () => {
    renderPopover({ members: FIRM_ROSTER, seatSubject: SEAT.id })
    const rows = screen.getAllByTestId('persona-row')
    const tick = within(rows[FIRM_ROSTER.findIndex((m) => m.isYou)]).getByTestId('persona-row-tick')
    expect(tick.style.color).toBe('var(--action)')
  })

  // Row 10 (AC-3). Red today: no component.
  it('a not-yet-loaded roster renders the loading surface, not an empty list', () => {
    renderPopover({ membersState: 'loading', members: [] })
    expect(screen.getByTestId('persona-surface-loading')).not.toBeNull()
    expect(screen.queryAllByTestId('persona-row').length).toBe(0)
  })

  // Row 11 (AC-3). The error->empty fold is precisely what membersSurface prevents --
  // asserting only the message would not catch a caller that re-derives 'empty' instead.
  it("a failed fetch renders the server's own message, never an empty roster", () => {
    const error = new ApiError('http', 'gateway is down for maintenance', 503)
    renderPopover({ membersState: 'error', membersError: error, members: [] })
    const surface = screen.getByTestId('persona-surface-error')
    expect(surface.textContent).toContain(error.message)
    expect(screen.queryByTestId('persona-surface-empty')).toBeNull()
    expect(screen.queryAllByTestId('persona-row').length).toBe(0)
  })

  // Row 12 (AC-3).
  it('an empty roster renders the empty surface', () => {
    renderPopover({ membersState: 'empty', members: [] })
    expect(screen.getByTestId('persona-surface-empty')).not.toBeNull()
    expect(screen.queryAllByTestId('persona-row').length).toBe(0)
  })

  // Row 16 (AC-4). Both halves load-bearing: a later edit that re-uppercases SEAT_LABEL
  // and drops the CSS would stay green if only the rendered text were checked -- that is
  // exactly what took CI red on this branch before (envCopy.test.ts's bare-SIGNED guard).
  it('the seat row renders SEAT_LABEL uppercased by CSS, from a sentence-case source', () => {
    renderPopover({ members: FIRM_ROSTER, seatSubject: SEAT.id })
    const rows = screen.getAllByTestId('persona-row')
    const meta = within(rows[FIRM_ROSTER.findIndex((m) => m.id === SEAT.id)]).getByTestId('persona-row-meta')
    expect(meta.textContent?.endsWith(SEAT_LABEL)).toBe(true)
    expect(meta.style.textTransform).toBe('uppercase')

    expect(SEAT_LABEL).toMatch(/^[A-Z][a-z]/)
    expect(SEAT_LABEL).not.toBe(SEAT_LABEL.toUpperCase())
  })

  // Row 17 (AC-4). isYou (who you currently ARE) and isSeat (the seat) agree in every
  // other fixture in this file -- only a roster that puts them on different members can
  // catch an implementation that collapses the two predicates into one.
  it('the seat label marks the seat; the tick marks who you are, on different rows', () => {
    const seatMember = { ...SEAT, isYou: false }
    const standInMember = { ...MUSA, isYou: true }
    const roster = [seatMember, standInMember, CHIAMAKA]
    renderPopover({ members: roster, seatSubject: seatMember.id })
    const rows = screen.getAllByTestId('persona-row')
    const seatRow = rows[roster.findIndex((m) => m.id === seatMember.id)]
    const standInRow = rows[roster.findIndex((m) => m.id === standInMember.id)]

    expect(within(seatRow).getByTestId('persona-row-meta').textContent).toContain(SEAT_LABEL)
    expect(within(seatRow).queryByTestId('persona-row-tick')).toBeNull()

    expect(within(standInRow).queryByTestId('persona-row-tick')).not.toBeNull()
    expect(within(standInRow).getByTestId('persona-row-meta').textContent).not.toContain(SEAT_LABEL)

    expect(screen.getAllByTestId('persona-row-tick').length).toBe(1)
    const seatLabelRows = rows.filter((r) => within(r).getByTestId('persona-row-meta').textContent?.includes(SEAT_LABEL))
    expect(seatLabelRows.length).toBe(1)
  })

  // Row 18 (AC-5). Red-first half of "reported, not invented": no invited branch exists
  // to hardcode, so this must pass through the generic status !== 'active' path.
  it('an invited member renders through the generic blocked path', () => {
    const invited = member({ id: 'm-invited', name: 'Newly Invited Person', initials: 'NI', role: 'preparer', status: 'invited', isYou: false })
    renderPopover({ members: [invited] })
    const row = screen.getByTestId('persona-row')
    expect(row.tagName).toBe('DIV')
    expect(within(row).getByTestId('persona-row-lock')).not.toBeNull()
    expect(within(row).getByTestId('persona-row-meta').textContent?.endsWith('invited')).toBe(true)
    expect(within(row).queryByTestId('persona-row-reason')).toBeNull()
  })
})
