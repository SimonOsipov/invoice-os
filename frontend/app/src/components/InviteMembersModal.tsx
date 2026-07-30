// §7's invite modal — a 640px centred modal, the app's fourth overlay.
//
// Structurally FieldMappingModal (FieldMappingModal.tsx:1-5), not EntityFormModal: the scrim
// and panel are byte-identical across all three and only the width literal differs, but this
// one wants FieldMappingModal's posture — a local draft that is lifted to the workspace only
// on Save, so Cancel and the backdrop both discard cleanly — and its two-line header, whose
// `.card-title` + 10px `.mono` eyebrow is exactly AC#1's shape. 640 is a fourth width (480 /
// 560 / 620 / 760 already ship); no token governs any of them.
//
// EntityFormModal's `if (!submitting)` scrim guard is deliberately NOT copied. `ctx.inviteMembers`
// is synchronous (App.tsx:676-678) — there is no async submit here, so the guard would ship
// dead state.
//
// This component holds NO rules. Parsing, validation, verdicts, minting and every string it
// renders except its own two titles come from lib/members.ts, where `environment: node` can
// spec them (§15.8).

import { useRef, useState } from 'react'

import { closeGlyph, crossGlyph } from '../glyphs'
import {
  classifyInvites,
  clientSelectionCount,
  DEPARTMENTS,
  filterClientRoster,
  hasDerivableName,
  INVITE_ERROR,
  invitedNotice,
  memberFromInvite,
  parseEmailInput,
  REVIEWER_HINT,
  type AccessRole,
  type Department,
  type InviteOptions,
} from '../lib/members'
import { useDismiss } from '../lib/useDismiss'
import { RoleCards } from './MemberParts'
import { ROLE_OPTIONS, WfSelect, type WfOption } from './WorkflowParts'
import type { RoleKey } from '../lib/workflows'
import type { PlatformCtx } from '../types'

// The surface's own titles. They stay here rather than joining the four strings this subtask
// moved into lib/members.ts, matching MATRIX_HEADING's posture (MemberRoleMatrix.tsx:19-22):
// §7 names them as the modal's chrome, not as its content.
const TITLE = 'Invite people'
const EYEBROW = "THEY'LL RECEIVE AN EMAIL INVITE"

// INVENTED COPY, and the only string this subtask leaves outside a spec — §7 specifies the
// zero-selected state's BEHAVIOUR (nothing to grant, so nothing to send) but supplies no
// sentence for it. Flagged rather than smuggled into lib/members.ts, whose additions this
// subtask's plan enumerates closed.
const NO_CLIENTS_NOTE = 'Pick at least one client, or switch to All clients.'

const NO_MATCH = 'No clients match this search.'

// `position` is `RoleKey | null`, so `None` is a SENTINEL option mapped back to null on the way
// out — the `'all'`-as-just-another-option idiom MembersView.tsx:27-33 already records, where
// the caller narrows on the way back. `department` gets no sentinel: `InviteOptions` declares it
// REQUIRED and non-nullable, so there is no unassigned value for a placeholder to stand for and
// it defaults to a real department instead.
const NO_POSITION = 'none'
const POSITION_OPTIONS: WfOption[] = [{ value: NO_POSITION, label: 'None' }, ...ROLE_OPTIONS]
const DEPARTMENT_OPTIONS: WfOption[] = DEPARTMENTS.map((d) => ({ value: d, label: d }))

/**
 * D3's second duty — de-dupe chips ACROSS successive pastes, which nothing upstream does.
 * `parseEmailInput` de-dupes only WITHIN one paste (T2.5) and `classifyInvites` has no batch
 * memory at all (QA33), so the chip list is the only place that state exists.
 *
 * Keyed on `toLowerCase()`, keeping the first spelling seen — the same key `parseEmailInput`
 * uses and the same case-insensitive comparison `classifyInvites` makes against stored rows. A
 * raw `===` would let `a@x.ng` and `A@x.ng` both chip and both classify `ok`, and the roster
 * would gain the same person twice.
 */
function mergeChips(current: readonly string[], added: readonly string[]): string[] {
  const seen = new Set(current.map((c) => c.toLowerCase()))
  const out = [...current]
  for (const address of added) {
    const key = address.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(address)
  }
  return out
}

export function InviteMembersModal({ ctx, onClose, onFlash }: {
  ctx: PlatformCtx
  /** Must be STABLE — it is a `useDismiss` dependency (useDismiss.ts:36-37). */
  onClose: () => void
  /** MembersView's existing flash setter, not a second mechanism (MembersView.tsx:56-65). */
  onFlash: (message: string) => void
}) {
  const [chips, setChips] = useState<string[]>([])
  const [draft, setDraft] = useState('')
  // Keyed by the LOWER-CASED address, matching the chip list's own de-dupe key, so a chip and
  // its error can never disagree about which address they belong to.
  const [errors, setErrors] = useState<Record<string, string>>({})
  // Least privilege, not ACCESS_ROLES[0]. §7 names no default; `admin` is first in the constant
  // because §3 lists it first, and defaulting an invite to full access — members, settings,
  // connectors, certificates — is the one wrong answer. `preparer` is also the modal role in
  // both seeds.
  const [role, setRole] = useState<AccessRole>('preparer')
  const [scope, setScope] = useState<'all' | 'selected'>('all')
  const [clientIds, setClientIds] = useState<number[]>([])
  const [clientQuery, setClientQuery] = useState('')
  const [department, setDepartment] = useState<Department>(DEPARTMENTS[0])
  const [position, setPosition] = useState<string>(NO_POSITION)

  const inputRef = useRef<HTMLInputElement>(null)

  // Rendered conditionally by MembersView, so `open` is unconditionally true and no `outsideRef`
  // is passed — outside clicks belong to the scrim's own onClick, exactly as EntityFormModal
  // does it. This is the call shape useDismiss.ts:20-21 pre-authorises for this modal by name.
  //
  // The hook's "no topmost-surface concept" limitation is not reachable here: the only opener is
  // the top bar's `Invite people` button, which sits outside every row-menu wrapper, so its
  // mousedown closes any open `⋯` menu before the click that opens this. Once open, the scrim
  // (zIndex 80) covers the menu panel (zIndex 60), so no second Escape listener can be live.
  useDismiss(true, onClose)

  // The uncommitted draft counts. A person who types an address and clicks Send invites without
  // pressing Enter has asked to invite them, and a button that silently did nothing would be a
  // bug rather than a lesson about chip inputs.
  const pending = mergeChips(chips, parseEmailInput(draft))
  const shownClients = filterClientRoster(clientQuery)
  // §7's zero-selected rule: `Selected clients` with nothing ticked is an invite that grants
  // access to nothing, which is not a thing to send. `clientAccessLabel([])` renders 'No clients'
  // rather than 'All clients', so the empty array is representable — it is just not sendable.
  const needsClients = ctx.mode === 'firm' && scope === 'selected' && clientIds.length === 0
  const canSend = pending.length > 0 && !needsClients

  function commit(raw: string) {
    setChips((cur) => mergeChips(cur, parseEmailInput(raw)))
    setDraft('')
  }

  function removeChip(address: string) {
    const key = address.toLowerCase()
    setChips((cur) => cur.filter((c) => c.toLowerCase() !== key))
    // Only this chip's error. Adding or removing a DIFFERENT chip must not clear a red one —
    // the whole point of §7's partial send is that the failures stay put for correction.
    setErrors((cur) => {
      if (!(key in cur)) return cur
      const next = { ...cur }
      delete next[key]
      return next
    })
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
      // Enter would submit a surrounding form and the two separators would otherwise land in
      // the field as text; both commit instead.
      e.preventDefault()
      commit(draft)
      return
    }
    if (e.key === 'Backspace' && draft === '' && chips.length > 0) {
      e.preventDefault()
      removeChip(chips[chips.length - 1])
    }
  }

  function send() {
    const addresses = pending
    // Re-classified HERE, on every send, against the CURRENT roster — D3's third duty. Verdicts
    // are a pure function of (existing, addresses) and go stale the moment a row lands, so a
    // partial send followed by a correction must not reuse the verdicts from the first attempt.
    const verdicts = classifyInvites(ctx.members, addresses)
    const ok: string[] = []
    const failed: string[] = []
    const nextErrors: Record<string, string> = {}

    addresses.forEach((address, i) => {
      // DEFECT D1's gate, applied ON TOP of the verdict and never inside `classifyInvites` —
      // QA35 pins that function returning `ok` for '...@x.ng' and that spec stays literally
      // true. The classifier still says `ok`; this modal declines to mint a nameless row.
      const verdict = verdicts[i] === 'ok' && !hasDerivableName(address) ? 'malformed' : verdicts[i]
      if (verdict === 'ok') {
        ok.push(address)
        return
      }
      // D3's first duty: every non-`ok` chip stays in the modal for correction and never
      // reaches `memberFromInvite`.
      failed.push(address)
      nextErrors[address.toLowerCase()] = INVITE_ERROR[verdict]
    })

    if (ok.length > 0) {
      // ONE `opts` for the whole batch, which is safe on purpose: `memberFromInvite` copies a
      // firm invite's `clientAccess` (QA32), so no two minted rows share an array, and ids come
      // from a module-private counter (T2.22/QA24), so a whole list minted in one tick still
      // gets distinct ids. Neither needs re-implementing here.
      const opts: InviteOptions =
        ctx.mode === 'firm'
          ? { mode: 'firm', role, clientAccess: scope === 'all' ? 'all' : clientIds }
          : { mode: 'inhouse', role, department, position: position === NO_POSITION ? null : (position as RoleKey) }
      ctx.inviteMembers(ok.map((address) => memberFromInvite(address, opts, ctx.user.name)))
      onFlash(invitedNotice(ok.length))
    }

    if (failed.length === 0) {
      onClose()
      return
    }
    // §7: the modal closes only when nothing failed. The chips that did are all that is left.
    setChips(failed)
    setErrors(nextErrors)
    setDraft('')
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
        aria-label={TITLE}
        data-testid="invite-modal"
        // maxWidth matters far more at 640 than at the 480 this shape was lifted from.
        style={{ width: 640, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div className="card-title">{TITLE}</div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 3 }}>
              {EYEBROW}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="invite-close"
            // No inline `borderRadius`. EntityFormModal.tsx:140 and FieldMappingModal.tsx:52 both
            // set --radius-md here and both are silently overridden — `.pf-btn` is
            // `border-radius: var(--radius-pill) !important` (app-layer.css:194-197) and
            // `!important` beats an inline style. The pill is wanted on a 34px icon button
            // (MemberParts.tsx:224-228 takes it deliberately for the same shape), so the
            // declaration is dropped rather than copied forward as a false claim.
            style={{ flex: 'none', width: 34, height: 34, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px 18px' }}>
          {/* `.label` is `text-transform: uppercase` (app-layer.css:155-162), so this and the two
              below render EMAILS / ACCESS ROLE / CLIENT ACCESS. That matches the roster head row
              and WfSelect's own label; not fought with an inline override. */}
          <div className="label" style={{ marginBottom: 6 }}>
            Emails
          </div>
          <div
            onClick={() => inputRef.current?.focus()}
            data-testid="invite-chip-box"
            style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6, minHeight: 38, padding: '6px 8px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', background: 'var(--bg-1)', cursor: 'text' }}
          >
            {chips.map((address) => {
              const error = errors[address.toLowerCase()]
              return (
                <span
                  key={address.toLowerCase()}
                  data-testid="invite-chip"
                  // NO class. `.pf-chip` is `border-radius: var(--radius-pill) !important`
                  // (app-layer.css:275) plus `cursor: pointer` (platform.css:126-129), and all
                  // four of its shipped usages are pressable BUTTONS — the class now means "a
                  // small pointer-cursored button". An address token is the opposite: a value
                  // with an embedded ×, where only the × is clickable. Taking it would render
                  // every address as a stadium and claim the whole chip was pressable.
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    maxWidth: '100%',
                    padding: '3px 4px 3px 9px',
                    borderRadius: 'var(--radius-md)',
                    fontSize: 12.5,
                    background: error ? 'var(--status-red-bg)' : 'var(--bg-2)',
                    border: `1px solid ${error ? 'var(--status-red-border)' : 'var(--line-2)'}`,
                    color: error ? 'var(--status-red-text)' : 'var(--fg-1)',
                  }}
                >
                  <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{address}</span>
                  {error && (
                    // Rendered exactly as `INVITE_ERROR` holds it. Not up-cased through the
                    // `.mono` micro-label idiom the pills and eyebrows use: AC#7 quotes these
                    // three strings, and a reviewer checking for `Already a member` should find
                    // it, not ALREADY A MEMBER.
                    <span data-testid="invite-chip-error" style={{ flex: 'none', fontSize: 11, fontWeight: 500 }}>
                      · {error}
                    </span>
                  )}
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      removeChip(address)
                    }}
                    className="pf-btn"
                    aria-label={`Remove ${address}`}
                    // The pill IS wanted here — a 20px round remove affordance, the same posture
                    // MemberParts.tsx:224-228 takes deliberately for its 28px trigger.
                    style={{ flex: 'none', width: 20, height: 20, padding: 0, border: 0, background: 'transparent', color: 'inherit', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
                  >
                    {crossGlyph}
                  </button>
                </span>
              )
            })}
            <input
              ref={inputRef}
              type="text"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onKeyDown}
              onPaste={(e) => {
                // Routed through `parseEmailInput` rather than left to land as text: it splits on
                // comma / semicolon / whitespace / newline and survives a Windows CRLF paste
                // (QA31), which a hand-rolled `[,;\s]` split does not.
                e.preventDefault()
                commit(e.clipboardData.getData('text'))
              }}
              aria-label="Email addresses"
              data-testid="invite-email-input"
              placeholder={chips.length === 0 ? 'name@company.ng, another@company.ng' : ''}
              // Bare, not `.pf-input`: that class is a fixed 38px full-width box, which is the
              // container here, not the field inside it.
              style={{ flex: 1, minWidth: 150, height: 24, padding: 0, border: 0, background: 'transparent', fontSize: 13, color: 'var(--fg-1)' }}
            />
          </div>
          <div style={{ marginTop: 6, fontSize: 11, color: 'var(--fg-3)' }}>
            Enter, comma or semicolon adds an address. Paste a list to add several.
          </div>

          <div className="label" style={{ margin: '16px 0 6px' }}>
            Access role
          </div>
          <RoleCards idPrefix="invite" value={role} onChange={setRole} />

          {ctx.mode === 'firm' ? (
            <>
              <div className="label" style={{ margin: '16px 0 6px' }}>
                Client access
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
                {(['all', 'selected'] as const).map((s) => (
                  <label key={s} data-testid={`invite-scope-${s}`} style={{ display: 'inline-flex', alignItems: 'center', gap: 7, fontSize: 13, cursor: 'pointer' }}>
                    <input
                      type="radio"
                      name="invite-scope"
                      value={s}
                      checked={scope === s}
                      // Toggling back to `All clients` KEEPS the ticked set — a mis-click must
                      // not destroy a selection the user assembled one checkbox at a time.
                      onChange={() => setScope(s)}
                      style={{ flex: 'none' }}
                    />
                    {s === 'all' ? 'All clients' : 'Selected clients'}
                  </label>
                ))}
              </div>

              {scope === 'selected' && (
                <div style={{ marginTop: 10, border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)', padding: 10 }}>
                  <input
                    type="text"
                    className="pf-input"
                    value={clientQuery}
                    onChange={(e) => setClientQuery(e.target.value)}
                    placeholder="Search clients"
                    aria-label="Search clients"
                    data-testid="invite-client-search"
                    style={{ height: 34, fontSize: 13, marginBottom: 8 }}
                  />
                  {shownClients.length === 0 ? (
                    <div data-testid="invite-client-empty" style={{ padding: '8px 10px', fontSize: 12.5, color: 'var(--fg-3)' }}>
                      {NO_MATCH}
                    </div>
                  ) : (
                    shownClients.map((c) => (
                      <label
                        key={c.id}
                        className="pf-row"
                        data-testid="invite-client-row"
                        style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '6px 10px', borderRadius: 'var(--radius-md)', fontSize: 13, color: 'var(--fg-1)' }}
                      >
                        <input
                          type="checkbox"
                          checked={clientIds.includes(c.id)}
                          // `toggleSelection` (invoices.ts:654) is the nearest shipped helper and
                          // is typed `string[]`; CLIENT_ROSTER ids are numbers, so it cannot be
                          // reused. Filtering never unticks: the search narrows what is SHOWN,
                          // and `clientIds` is untouched by it.
                          onChange={() => setClientIds((cur) => (cur.includes(c.id) ? cur.filter((x) => x !== c.id) : [...cur, c.id]))}
                          style={{ flex: 'none' }}
                        />
                        {c.name}
                      </label>
                    ))
                  )}
                  <div data-testid="invite-client-count" style={{ marginTop: 8, paddingTop: 8, borderTop: '1px solid var(--line-1)', fontSize: 12, color: 'var(--fg-3)' }}>
                    {clientSelectionCount(clientIds.length)}
                    {needsClients && <span style={{ color: 'var(--status-amber-text)' }}> · {NO_CLIENTS_NOTE}</span>}
                  </div>
                </div>
              )}
            </>
          ) : (
            <>
              <WfSelect
                label="Department"
                value={department}
                options={DEPARTMENT_OPTIONS}
                onChange={(v) => setDepartment(v as Department)}
                width="100%"
                marginBottom={16}
              />
              <WfSelect
                label="Approval position"
                value={position}
                options={POSITION_OPTIONS}
                onChange={setPosition}
                width="100%"
              />
              <div data-testid="invite-reviewer-hint" style={{ marginTop: 6, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
                {REVIEWER_HINT}
              </div>
            </>
          )}
        </div>

        <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          <button type="button" onClick={onClose} className="v2-btn v2-btn-ghost pf-btn" data-testid="invite-cancel" style={{ height: 36, fontSize: 13 }}>
            Cancel
          </button>
          <button
            type="button"
            onClick={send}
            disabled={!canSend}
            className="v2-btn v2-btn-primary pf-btn"
            data-testid="invite-send"
            // The app's shipped disabled-primary treatment (CreateUpload.tsx:201-206): the real
            // attribute, plus an inline swap so a dead button is not still painted as the
            // action. Both footer buttons want the pill `.pf-btn` forces, so it is taken.
            style={{ height: 36, fontSize: 13, background: canSend ? 'var(--action)' : 'var(--bg-3)', color: canSend ? 'var(--text-on-dark)' : 'var(--fg-4)', cursor: canSend ? 'pointer' : 'not-allowed' }}
          >
            Send invites
          </button>
        </div>
      </div>
    </div>
  )
}
