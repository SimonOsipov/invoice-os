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
  DEPARTMENTS,
  hasDerivableName,
  INVITE_ERROR,
  invitedNotice,
  memberFromInvite,
  mergeChips,
  needsClientPick,
  parseEmailInput,
  type AccessRole,
  type Department,
  type InviteOptions,
} from '../lib/members'
import { INVITE_ROLE_HELPER } from '../lib/roles'
import { useDismiss } from '../lib/useDismiss'
import { ClientAccessPicker, DepartmentField, RoleCards } from './MemberParts'
import { WfSelect, type WfOption } from './WorkflowParts'
import type { PlatformCtx } from '../types'

// The surface's own titles. They stay here rather than joining the four strings this subtask
// moved into lib/members.ts, matching MATRIX_HEADING's posture (MemberRoleMatrix.tsx:19-22):
// §7 names them as the modal's chrome, not as its content.
const TITLE = 'Invite people'
const EYEBROW = "THEY'LL RECEIVE AN EMAIL INVITE"

// A SENTINEL option mapped back to null on the way out — the `'all'`-as-just-another-option
// idiom MembersView.tsx records, where the caller narrows on the way back. `Role.key` is a
// free-form slug (`newRoleKey`), so a role could in principle be keyed `none`; no seeded one
// is, and members.test.ts pins that.
const NO_WF_ROLE = 'none'

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
  // The value `ClientAccessPicker` emits, not its internals. `'all' | number[]` is exactly
  // `InviteOptions['clientAccess']`, so this state IS the field — no scope/ids reassembly on
  // the way out, and no second place that could disagree with what the picker is showing.
  const [clientAccess, setClientAccess] = useState<'all' | number[]>('all')
  const [department, setDepartment] = useState<Department>(DEPARTMENTS[0])
  // Optional and in BOTH modes (`[invite-single-role]`). One select here, the multi-picker in
  // the drawer: an invite is a first guess, and the asymmetry is deliberate.
  const [wfRole, setWfRole] = useState<string | null>(null)

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
  // §7's zero-selected rule, now a lib predicate: `Selected clients` with nothing ticked is an
  // invite that grants access to nothing, which is not a thing to send. The picker renders the
  // amber explanation off the same function, so the disabled button and the sentence that says
  // why can never disagree.
  const needsClients = ctx.mode === 'firm' && needsClientPick(clientAccess)
  const canSend = pending.length > 0 && !needsClients
  const wfRoleOptions: WfOption[] = [{ value: NO_WF_ROLE, label: 'None' }, ...ctx.roles.map((r) => ({ value: r.key, label: r.title }))]

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
        ctx.mode === 'firm' ? { mode: 'firm', role, clientAccess } : { mode: 'inhouse', role, department }
      // `[invite-writes-both-stores]` — the minted ids land in the chosen role in the same
      // commit as the roster; App.tsx is the only place that sees both stores.
      ctx.inviteMembers(ok.map((address) => memberFromInvite(address, opts, ctx.user.name)), wfRole)
      // Only when this modal is actually closing. MembersView renders the flash in normal
      // document flow with no z-index (MembersView.tsx:122-129), so on a PARTIAL send — where
      // §7 keeps the modal open — it paints BEHIND the scrim (zIndex 80, blurred) and its
      // 2600ms timer expires before anyone could close the modal and look. Giving the flash a
      // z-index instead would be worse: a transient success notice stacked on top of a modal
      // the user is still correcting in. Nothing is lost — a partial send already reports
      // itself through the red chips that stay put, which is §7's specified behaviour.
      //
      // `failed` is complete here: the classification loop above has already run to the end.
      // The full-send case is unaffected — it takes the `onClose` branch just below, which
      // batches into this same commit, so the scrim is gone by the time the flash paints.
      if (failed.length === 0) onFlash(invitedNotice(ok.length))
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
          {/* `pf-chipbox` is load-bearing, not decoration: it is the hook for the two
              platform.css rules that move the focus ring off the bare input and onto this box.
              Without them `.asc-app input:focus` (app-layer.css:236) rings the input INSTEAD,
              9px inside the border — two nested rounded rectangles, measured on the deployed
              build and fixed there. `WfAmountInput` is the target behaviour: same inline
              border, ringed as one field. The rules live in the app's own platform.css and not
              in packages/design-tokens/, which is vendored and re-pulled. */}
          <div
            onClick={() => inputRef.current?.focus()}
            className="pf-chipbox"
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
                // The typed draft is MERGED, not replaced. `commit` clears the draft, so pasting
                // over a half-typed address used to discard it silently — contradicting `pending`
                // (:98), where the uncommitted draft already counts toward Send. A space
                // is one of `ADDRESS_SEPARATORS`, so the joined string parses as two addresses,
                // and `mergeChips` de-dupes them if they collide. With an empty draft this is
                // byte-for-byte the previous behaviour.
                const pasted = e.clipboardData.getData('text')
                commit(draft ? `${draft} ${pasted}` : pasted)
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
              {/* `idPrefix="invite"` reproduces every `data-testid` this control shipped with
                  before MEMB-01-07 lifted it into MemberParts — invite-scope-all/-selected,
                  invite-client-search/-empty/-row/-count — so the extraction is invisible to
                  MEMB-01-06's gate items. */}
              <ClientAccessPicker idPrefix="invite" value={clientAccess} onChange={setClientAccess} />
            </>
          ) : (
            <div style={{ marginTop: 16 }}>
              <DepartmentField department={department} onDepartment={setDepartment} />
            </div>
          )}

          {/* BOTH modes. Drawn from `ctx.roles`, so a role created on the Roles tab is
              offerable here without a second list to keep in step. */}
          <div style={{ marginTop: 16 }}>
            <WfSelect
              label="Workflow role"
              value={wfRole ?? NO_WF_ROLE}
              options={wfRoleOptions}
              onChange={(v) => setWfRole(v === NO_WF_ROLE ? null : v)}
              width="100%"
            />
          </div>
          <div data-testid="invite-wfrole-helper" style={{ marginTop: 6, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
            {INVITE_ROLE_HELPER}
          </div>
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
