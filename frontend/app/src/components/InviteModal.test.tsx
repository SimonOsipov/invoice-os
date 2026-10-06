// @vitest-environment jsdom
//
// The invite modal's props-only contract. An address chip reads as its remove button's
// aria-label; a red chip carries `invite-chip-error` inside the `invite-chip`.
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { ACCESS_ROLES, type Member } from '../lib/members'
import { InviteModal, type InviteModalProps } from './InviteModal'

afterEach(cleanup)

// invitations_handler.go: `invalid email address: ` + each Go %q of the raw input.
const refusedMessage = (raw: string) => `invalid email address: ${JSON.stringify(raw)}`
// tenancy.go errorStatus: ErrDailyInviteLimit, maxInviteMailsPerDay = 20.
const DAILY_LIMIT = 'daily invite limit reached: 20 invite mails per workspace per 24 hours'
// invitations_handler.go: `emails must hold 1 to %d addresses`, maxInviteEmails = 20.
const TOO_MANY = 'emails must hold 1 to 20 addresses'
// members.ts INVITE_ERROR, as the member drawer's spec pins them.
const NOT_VALID = 'Not a valid email'

function member(over: Partial<Member> = {}): Member {
  return { id: 'u1', name: 'Ada Person', initials: 'AP', email: 'ada@x.ng', role: 'admin', status: 'active', isYou: false, ...over }
}

function deferred() {
  let resolve!: () => void
  let reject!: (e: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

type SendMock = Mock<InviteModalProps['onSend']>

function sendMock(): SendMock {
  return vi.fn<InviteModalProps['onSend']>().mockResolvedValue(undefined)
}

function renderModal(over: { existing?: Member[]; onSend?: SendMock; onClose?: Mock<() => void> } = {}) {
  const onSend = over.onSend ?? sendMock()
  const onClose = over.onClose ?? vi.fn<() => void>()
  render(<InviteModal existing={over.existing ?? []} onSend={onSend} onClose={onClose} />)
  return { onSend, onClose, user: userEvent.setup({ delay: null }) }
}

const input = () => screen.getByTestId('invite-modal-input') as HTMLInputElement
const send = () => screen.getByTestId('invite-modal-send') as HTMLButtonElement
const chips = () => screen.queryAllByTestId('invite-chip')
const addressOf = (chip: HTMLElement) =>
  within(chip).getByRole('button', { name: /^Remove / }).getAttribute('aria-label')!.slice('Remove '.length)
const reasonOf = (chip: HTMLElement) => within(chip).queryByTestId('invite-chip-error')?.textContent ?? null
const addresses = () => chips().map(addressOf)
const reasons = () => chips().map(reasonOf)

type User = ReturnType<typeof userEvent.setup>
async function addChips(user: User, ...list: string[]) {
  for (const a of list) await user.type(input(), `${a}{Enter}`)
}

/** Lets a rejected send's catch and the re-send settle before a "no further call" assertion. */
const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })

describe('InviteModal', () => {
  it('InviteModal: preparer is preselected among exactly three role cards', () => {
    renderModal()
    const radios = screen.getAllByRole('radio')
    expect(radios).toHaveLength(3)
    expect(screen.getAllByTestId(/^invite-role-/)).toHaveLength(3)
    expect(ACCESS_ROLES).toHaveLength(3)
    for (const r of ACCESS_ROLES) {
      const card = screen.getByTestId(`invite-role-${r.id}`)
      expect((within(card).getByRole('radio') as HTMLInputElement).checked).toBe(r.id === 'preparer')
      expect(within(card).getByText(r.label)).toBeTruthy()
      expect(within(card).getByText(r.description)).toBeTruthy()
    }
  })

  it('InviteModal: Enter, comma and semicolon commit chips', async () => {
    const { user } = renderModal()
    await user.type(input(), 'a@x.ng{Enter}')
    await user.type(input(), 'b@x.ng,')
    await user.type(input(), 'c@x.ng;')
    expect(chips()).toHaveLength(3)
    expect(addresses()).toEqual(['a@x.ng', 'b@x.ng', 'c@x.ng'])
    expect(input().value).toBe('')
  })

  it('InviteModal: a pasted list becomes one chip per address', async () => {
    const { user } = renderModal()
    await user.click(input())
    await user.paste('a@x.ng b@x.ng\nc@x.ng; a@x.ng')
    expect(chips()).toHaveLength(3)
    expect(addresses()).toEqual(['a@x.ng', 'b@x.ng', 'c@x.ng'])
    expect(input().value).toBe('')
  })

  it('InviteModal: a case variant does not chip twice', async () => {
    const { user } = renderModal()
    await addChips(user, 'a@x.ng')
    expect(chips()).toHaveLength(1)
    await addChips(user, 'A@X.ng')
    expect(addresses()).toEqual(['a@x.ng'])
  })

  it('InviteModal: Backspace on an empty input removes the last chip', async () => {
    const { user } = renderModal()
    await addChips(user, 'a@x.ng', 'b@x.ng')
    expect(chips()).toHaveLength(2)
    expect(input().value).toBe('')
    await user.type(input(), '{Backspace}')
    expect(addresses()).toEqual(['a@x.ng'])
    await user.type(input(), 'c')
    expect(input().value).toBe('c')
    await user.type(input(), '{Backspace}')
    expect(input().value).toBe('')
    expect(addresses()).toEqual(['a@x.ng'])
  })

  it("InviteModal: a chip's remove button removes that chip", async () => {
    const { user } = renderModal()
    await addChips(user, 'a@x.ng', 'b@x.ng')
    expect(chips()).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Remove a@x.ng' }))
    expect(addresses()).toEqual(['b@x.ng'])
  })

  it('InviteModal: each bad chip carries its own reason', async () => {
    const { user } = renderModal({
      existing: [member({ id: 'm', email: 'm@x.ng' }), member({ id: 'p', email: 'p@x.ng', status: 'invited' })],
    })
    // 255 UTF-8 bytes; the server's maxEmailBytes is 254 (invitations_handler.go).
    const oversize = `${'a'.repeat(250)}@x.ng`
    expect(new TextEncoder().encode(oversize).length).toBe(255)
    await addChips(user, 'nope', 'm@x.ng', 'p@x.ng', '-@x.ng', oversize)
    expect(chips()).toHaveLength(5)
    expect(reasons()).toEqual([NOT_VALID, 'Already a member', 'Already invited', NOT_VALID, NOT_VALID])
    expect(chips().map((c) => c.getAttribute('data-verdict'))).toEqual(['malformed', 'member', 'invited', 'malformed', 'malformed'])
  })

  it('InviteModal: Send passes only the ok chips and the chosen role', async () => {
    const { user, onSend } = renderModal()
    await addChips(user, 'ok1@x.ng', 'nope')
    expect(chips()).toHaveLength(2)
    await user.click(within(screen.getByTestId('invite-role-reviewer')).getByRole('radio'))
    await user.click(send())
    await settle()
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(onSend).toHaveBeenCalledWith(['ok1@x.ng'], 'reviewer')
  })

  it('InviteModal: Send commits the uncommitted draft first', async () => {
    const { user, onSend } = renderModal()
    await addChips(user, 'a@x.ng')
    await user.type(input(), 'b@x.ng')
    expect(chips()).toHaveLength(1)
    await user.click(send())
    await settle()
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(onSend).toHaveBeenCalledWith(['a@x.ng', 'b@x.ng'], 'preparer')
  })

  it('InviteModal: Send is disabled with no ok chip', async () => {
    const { user } = renderModal()
    expect(chips()).toHaveLength(0)
    expect(send().disabled).toBe(true)
    await addChips(user, 'nope')
    expect(chips()).toHaveLength(1)
    expect(send().disabled).toBe(true)
    await addChips(user, 'a@x.ng')
    expect(chips()).toHaveLength(2)
    expect(send().disabled).toBe(false)
  })

  it('InviteModal: a double click sends once', async () => {
    const pending = deferred()
    const { user, onSend } = renderModal({ onSend: vi.fn<InviteModalProps['onSend']>().mockReturnValue(pending.promise) })
    await addChips(user, 'a@x.ng')
    await user.dblClick(send())
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(send().disabled).toBe(true)
    pending.resolve()
    await settle()
  })

  describe('server-refused addresses', () => {
    // D5: each form passes the client rule (EMAIL_RE + hasDerivableName) and the server refuses it.
    const REFUSED_FORMS = ['a..b@x.com', '.a@x.com', 'a.@x.com', 'a@x..com', 'a@x.com.', 'a(b)@x.com', 'a"b@x.com', '<a@x.com>']

    it.each(REFUSED_FORMS)('InviteModal: a server-refused address turns red and the rest re-send once (%s)', async (R) => {
      const onSend = sendMock()
        .mockRejectedValueOnce(new ApiError('http', refusedMessage(R), 400))
        .mockResolvedValueOnce(undefined)
      const onClose = vi.fn<() => void>()
      const { user } = renderModal({ onSend, onClose })
      await addChips(user, 'ok@x.com', R)
      expect(chips().map((c) => c.getAttribute('data-verdict'))).toEqual(['ok', 'ok'])
      await user.click(send())
      await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2))
      await settle()
      expect(onSend.mock.calls[0]).toEqual([['ok@x.com', R], 'preparer'])
      expect(onSend.mock.calls[1]).toEqual([['ok@x.com'], 'preparer'])
      expect(addresses()).toEqual([R])
      expect(reasons()).toEqual([NOT_VALID])
      expect(screen.getByTestId('invite-modal')).toBeTruthy()
      expect(onClose).not.toHaveBeenCalled()
      expect(onSend).toHaveBeenCalledTimes(2)
    })

    it('InviteModal: a server 400 refusing every chip sends nothing more', async () => {
      const onSend = sendMock().mockRejectedValue(new ApiError('http', refusedMessage('a..b@x.com'), 400))
      const { user } = renderModal({ onSend })
      await addChips(user, 'a..b@x.com')
      await user.click(send())
      await waitFor(() => expect(reasons()).toEqual([NOT_VALID]))
      await settle()
      expect(onSend).toHaveBeenCalledTimes(1)
      expect(screen.queryByTestId('invite-modal-error')).toBeNull()
    })

    it('InviteModal: a rejected re-send shows its message and keeps the chips', async () => {
      const onSend = sendMock()
        .mockRejectedValueOnce(new ApiError('http', refusedMessage('a..b@x.com'), 400))
        .mockRejectedValueOnce(new ApiError('http', DAILY_LIMIT, 429))
      const onClose = vi.fn()
      const { user } = renderModal({ onSend, onClose })
      await addChips(user, 'ok@x.com', 'a..b@x.com')
      await user.click(send())
      await waitFor(() => expect(screen.getByTestId('invite-modal-error').textContent).toBe(DAILY_LIMIT))
      await settle()
      expect(addresses()).toEqual(['ok@x.com', 'a..b@x.com'])
      expect(chips().map((c) => c.getAttribute('data-verdict'))).toEqual(['ok', 'malformed'])
      expect(reasons()).toEqual([null, NOT_VALID])
      expect(onSend).toHaveBeenCalledTimes(2)
      expect(onClose).not.toHaveBeenCalled()
    })

    it('InviteModal: a 400 naming no chip re-sends nothing', async () => {
      const message = refusedMessage('other@x.com')
      const onSend = sendMock().mockRejectedValue(new ApiError('http', message, 400))
      const { user } = renderModal({ onSend })
      await addChips(user, 'ok@x.com')
      await user.click(send())
      await waitFor(() => expect(screen.getByTestId('invite-modal-error').textContent).toBe(message))
      await settle()
      expect(onSend).toHaveBeenCalledTimes(1)
      expect(chips()).toHaveLength(1)
      expect(chips().map((c) => c.getAttribute('data-verdict'))).toEqual(['ok'])
    })
  })

  it('InviteModal: closes when every chip sent', async () => {
    const { user, onSend, onClose } = renderModal()
    await addChips(user, 'a@x.ng', 'b@x.ng')
    expect(chips()).toHaveLength(2)
    await user.click(send())
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it('InviteModal: stays open with only the red chips', async () => {
    const { user, onSend, onClose } = renderModal()
    await addChips(user, 'ok@x.ng', 'nope')
    expect(chips()).toHaveLength(2)
    await user.click(send())
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(chips()).toHaveLength(1))
    await settle()
    expect(addresses()).toEqual(['nope'])
    expect(reasons()).toEqual([NOT_VALID])
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByTestId('invite-modal')).toBeTruthy()
  })

  it("InviteModal: the server's refusal renders verbatim and keeps the chips", async () => {
    const onSend = sendMock().mockRejectedValue(new ApiError('http', DAILY_LIMIT, 429))
    const { user, onClose } = renderModal({ onSend })
    await addChips(user, 'a@x.ng', 'b@x.ng')
    await user.click(send())
    await waitFor(() => expect(screen.getByTestId('invite-modal-error').textContent).toBe(DAILY_LIMIT))
    await settle()
    expect(addresses()).toEqual(['a@x.ng', 'b@x.ng'])
    expect(reasons()).toEqual([null, null])
    expect(onSend).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('InviteModal: a 400 for too many addresses renders verbatim', async () => {
    const onSend = sendMock().mockRejectedValue(new ApiError('http', TOO_MANY, 400))
    const { user } = renderModal({ onSend })
    await addChips(user, 'a@x.ng', 'b@x.ng')
    await user.click(send())
    await waitFor(() => expect(screen.getByTestId('invite-modal-error').textContent).toBe(TOO_MANY))
    await settle()
    expect(chips()).toHaveLength(2)
    expect(reasons()).toEqual([null, null])
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it('InviteModal: Escape and Cancel close when idle', async () => {
    const { user, onClose } = renderModal()
    expect(onClose).not.toHaveBeenCalled()
    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
    await user.click(screen.getByTestId('invite-modal-cancel'))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('InviteModal: Escape does not close while sending', async () => {
    const pending = deferred()
    const { user, onSend, onClose } = renderModal({ onSend: vi.fn<InviteModalProps['onSend']>().mockReturnValue(pending.promise) })
    await addChips(user, 'a@x.ng')
    await user.click(send())
    expect(onSend).toHaveBeenCalledTimes(1)
    await user.keyboard('{Escape}')
    await user.click(screen.getByTestId('invite-modal-cancel'))
    expect(onClose).not.toHaveBeenCalled()
    pending.resolve()
    await settle()
  })

  it('InviteModal: the chip box is the pf-chipbox field', () => {
    renderModal()
    const box = screen.getByTestId('invite-chipbox')
    expect(box.classList.contains('pf-chipbox')).toBe(true)
    expect(within(box).getByTestId('invite-modal-input')).toBe(input())
  })
})
