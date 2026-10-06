// The Members screen's invite modal (§5): addresses as chips, one role, send the valid ones.

import { useCallback, useState } from 'react'

import { toApiError } from '@invoice-os/api-client'
import { closeGlyph } from '../glyphs'
import { chipVerdicts, INVITE_ERROR, mergeChips, parseEmailInput, serverRefusedAddresses, type AccessRole, type Member } from '../lib/members'
import { useDismiss } from '../lib/useDismiss'
import { RoleCards } from './MemberParts'

export type InviteModalProps = {
  /** The roster with pending invites; chips are judged against it. */
  existing: readonly Member[]
  onSend: (emails: readonly string[], role: AccessRole) => Promise<void>
  /** Must be stable: it is a `useDismiss` dependency. */
  onClose: () => void
}

export function InviteModal({ existing, onSend, onClose }: InviteModalProps) {
  const [chips, setChips] = useState<string[]>([])
  const [refused, setRefused] = useState<string[]>([])
  const [draft, setDraft] = useState('')
  const [role, setRole] = useState<AccessRole>('preparer')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const closeIfIdle = useCallback(() => {
    if (!sending) onClose()
  }, [sending, onClose])
  useDismiss(true, closeIfIdle)

  const verdicts = chipVerdicts(existing, chips, refused)
  const hasOk = verdicts.includes('ok')
  const canSend = (hasOk || draft.trim() !== '') && !sending

  function commit(text: string) {
    setChips((cur) => mergeChips(cur, parseEmailInput(text)))
    setDraft('')
  }

  const okOf = (list: string[], refusedList: string[]) => {
    const v = chipVerdicts(existing, list, refusedList)
    return list.filter((_, i) => v[i] === 'ok')
  }

  async function send() {
    if (sending) return
    const all = mergeChips(chips, parseEmailInput(draft))
    setChips(all)
    setDraft('')
    let sent = okOf(all, refused)
    if (sent.length === 0) return // unsendable chips stay red for correction
    setSending(true)
    setError(null)
    try {
      try {
        await onSend(sent, role)
      } catch (err) {
        // Layer 2: the server named addresses the client accepted. Mark them, re-send once.
        const bad = serverRefusedAddresses(toApiError(err).message, all)
        if (bad.length === 0) throw err
        const nextRefused = [...refused, ...bad]
        setRefused(nextRefused)
        sent = okOf(all, nextRefused)
        if (sent.length > 0) await onSend(sent, role)
      }
      const left = all.filter((c) => !sent.includes(c))
      setChips(left)
      if (left.length === 0) onClose()
    } catch (err) {
      setError(toApiError(err).message)
    } finally {
      setSending(false)
    }
  }

  return (
    <div
      onClick={closeIfIdle}
      data-testid="invite-modal"
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Invite people"
        style={{ width: 640, maxWidth: '100%', maxHeight: '86vh', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div className="card-title">Invite people</div>
            <div className="mono" style={{ marginTop: 3, fontSize: 11, letterSpacing: '0.04em', textTransform: 'uppercase', color: 'var(--fg-3)' }}>
              They'll receive an email invite
            </div>
          </div>
          <button
            type="button"
            onClick={closeIfIdle}
            className="pf-btn"
            aria-label="Close"
            data-testid="invite-modal-close"
            style={{ flex: 'none', width: 34, height: 34, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px 18px' }}>
          <div className="label" style={{ marginBottom: 6 }}>Emails</div>
          <div
            className="pf-chipbox"
            data-testid="invite-chipbox"
            style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6, minHeight: 44, padding: 6, border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)' }}
          >
            {chips.map((chip, i) => {
              const verdict = verdicts[i]
              const bad = verdict !== 'ok'
              return (
                <span
                  key={chip}
                  data-testid="invite-chip"
                  data-verdict={verdict}
                  title={chip}
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 6, maxWidth: '100%', minWidth: 0, height: 28, padding: '0 6px 0 10px', borderRadius: 'var(--radius-pill)', fontSize: 12.5, border: `1px solid ${bad ? 'var(--status-red-border)' : 'var(--line-2)'}`, background: bad ? 'var(--status-red-bg)' : 'var(--bg-2)', color: bad ? 'var(--status-red-text)' : 'var(--fg-1)' }}
                >
                  <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{chip}</span>
                  {bad && (
                    <span data-testid="invite-chip-error" style={{ flex: 'none', fontSize: 11.5, fontWeight: 600 }}>
                      {INVITE_ERROR[verdict]}
                    </span>
                  )}
                  <button
                    type="button"
                    aria-label={`Remove ${chip}`}
                    disabled={sending}
                    onClick={() => setChips((cur) => cur.filter((c) => c !== chip))}
                    style={{ flex: 'none', width: 18, height: 18, padding: 0, border: 'none', background: 'transparent', color: 'inherit', cursor: sending ? 'not-allowed' : 'pointer', display: 'grid', placeItems: 'center' }}
                  >
                    {closeGlyph}
                  </button>
                </span>
              )
            })}
            <input
              type="text"
              data-testid="invite-modal-input"
              aria-label="Emails"
              value={draft}
              disabled={sending}
              placeholder={chips.length === 0 ? 'name@company.ng, another@company.ng' : ''}
              onChange={(e) => setDraft(e.target.value)}
              onPaste={(e) => {
                e.preventDefault()
                const { selectionStart: from, selectionEnd: to } = e.currentTarget
                commit(draft.slice(0, from ?? draft.length) + e.clipboardData.getData('text') + draft.slice(to ?? draft.length))
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
                  e.preventDefault()
                  commit(draft)
                } else if (e.key === 'Backspace' && draft === '') {
                  setChips((cur) => cur.slice(0, -1))
                }
              }}
              style={{ flex: 1, minWidth: 160, height: 28, border: 'none', outline: 'none', background: 'transparent', fontSize: 13, color: 'var(--fg-1)' }}
            />
          </div>

          <div className="label" style={{ margin: '16px 0 6px' }}>Access role</div>
          <RoleCards value={role} onChange={setRole} idPrefix="invite" />
        </div>

        <div style={{ flex: 'none', padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          {error && (
            <div
              data-testid="invite-modal-error"
              style={{ marginBottom: 10, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}
            >
              {error}
            </div>
          )}
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <div style={{ flex: 1 }} />
            <button
              type="button"
              onClick={closeIfIdle}
              disabled={sending}
              className="v2-btn v2-btn-ghost pf-btn"
              data-testid="invite-modal-cancel"
              style={{ height: 36, fontSize: 13 }}
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void send()}
              disabled={!canSend}
              className="v2-btn v2-btn-primary pf-btn"
              data-testid="invite-modal-send"
              style={{
                height: 36,
                fontSize: 13,
                background: canSend ? 'var(--action)' : 'var(--bg-3)',
                color: canSend ? 'var(--text-on-dark)' : 'var(--fg-4)',
                cursor: canSend ? 'pointer' : 'not-allowed',
              }}
            >
              {sending ? 'Sending…' : 'Send invites'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
