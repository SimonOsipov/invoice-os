// Demo-only footer: the DEMO BUILD marker over the identity row. The flag-off footer
// stays in Sidebar.tsx and is never rendered from here.
import { useCallback, useRef, useState, type ReactNode } from 'react'

import { useDismiss } from '../lib/useDismiss'
import { accessRoleLabel, type Member } from '../lib/members'
import type { PlatformCtx } from '../types'
import { BUSY_NAME, MARKER_LABEL, TRIGGER_BUSY_ROLE, TRIGGER_TITLE } from './copy'
import { demoChevronUpGlyph, demoFlaskGlyph } from './glyphs'
import { isSeat } from './identity'
import { PersonaPopover } from './PersonaPopover'

export function PersonaFooter({ ctx, orgLabel, signOutButton }: { ctx: PlatformCtx; orgLabel: string; signOutButton: ReactNode }) {
  const { user } = ctx
  const [open, setOpen] = useState(false)
  // Local, not lifted: both die with this component on a successful switch's remount
  // (App.tsx's Workspace key), which is the transient-UI clear -- not five manual resets.
  const [busy, setBusy] = useState<string | null>(null)
  const [rowError, setRowError] = useState<{ memberId: string; message: string } | null>(null)
  const wrapperRef = useRef<HTMLDivElement>(null)
  const close = useCallback(() => setOpen(false), [])
  useDismiss(open, close, wrapperRef)

  const current = ctx.members.find((m) => m.isYou) ?? null
  // Amber only when standing in is proven; an unresolved roster reads as the seat,
  // which is what App boots into.
  const standingIn = current != null && ctx.seatSubject != null && !isSeat(current.id, ctx.seatSubject)

  const handleSelect = useCallback(
    async (member: Member) => {
      if (!ctx.becomePersona) return
      setBusy(member.name)
      setRowError(null)
      setOpen(false)
      try {
        await ctx.becomePersona(member, ctx.view)
      } catch (err) {
        // The 700ms floor lives in App, which owns the commit that ends it -- App
        // rejects at round-trip time rather than holding an error the presenter needs now.
        setRowError({ memberId: member.id, message: err instanceof Error ? err.message : String(err) })
        setOpen(true)
      } finally {
        setBusy(null)
      }
    },
    [ctx],
  )

  const handleReturn = useCallback(async () => {
    const seatMember = ctx.members.find((m) => isSeat(m.id, ctx.seatSubject))
    if (!seatMember || !ctx.returnToSeat) return
    setBusy(seatMember.name)
    setOpen(false)
    try {
      // isYou is normalized here: the roster's seat row reads false while standing in
      // (the stand-in is "you" until this resolves), but the member this call names IS you.
      await ctx.returnToSeat(ctx.view, { ...seatMember, isYou: true })
    } finally {
      setBusy(null)
    }
  }, [ctx])

  return (
    <div style={{ flex: '0 0 auto', padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column', gap: 9 }}>
      {/* marker line */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <span style={{ flex: 'none', color: 'var(--status-amber-text)' }}>{demoFlaskGlyph}</span>
        <span className="mono" style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.08em', color: 'var(--status-amber-text)' }}>
          {MARKER_LABEL}
        </span>
        <span style={{ flex: 1 }} />
        <span
          className="mono"
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 5,
            minWidth: 0,
            fontSize: 9,
            fontWeight: 500,
            letterSpacing: '0.06em',
            color: 'var(--fg-4)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
          }}
        >
          {user.verified && user.tenantName ? (
            <>
              <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} title="Tenant verified via /v1/me" />
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{user.tenantName.toUpperCase()}</span>
            </>
          ) : (
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{orgLabel}</span>
          )}
        </span>
      </div>

      {/* identity row */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div ref={wrapperRef} style={{ flex: 1, minWidth: 0, position: 'relative' }}>
          <button
            onClick={() => setOpen((o) => !o)}
            disabled={busy != null}
            data-testid="persona-trigger"
            title={TRIGGER_TITLE}
            style={{
              width: '100%',
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              background: 'var(--bg-1)',
              border: `1px dashed ${open || busy != null ? 'var(--status-amber-text)' : 'var(--status-amber-border)'}`,
              borderRadius: 'var(--radius-sm)',
              padding: '6px 8px',
              cursor: 'pointer',
              textAlign: 'left',
              fontFamily: 'var(--font-sans)',
            }}
          >
            <span
              style={{
                flex: 'none',
                width: 30,
                height: 30,
                borderRadius: 99,
                background: busy != null ? 'transparent' : 'var(--slate-800)',
                color: 'var(--text-on-dark)',
                display: 'grid',
                placeItems: 'center',
                fontSize: 11,
                fontWeight: 600,
              }}
            >
              {busy != null ? (
                <span
                  data-testid="persona-spinner"
                  style={{
                    width: 16,
                    height: 16,
                    borderRadius: 99,
                    border: '2px solid var(--status-amber-border)',
                    borderTopColor: 'var(--status-amber-text)',
                    animation: 'spin 700ms linear infinite',
                  }}
                />
              ) : (
                user.initials
              )}
            </span>
            <span style={{ flex: 1, minWidth: 0 }}>
              <span
                data-testid="persona-name"
                style={{ display: 'block', fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
              >
                {busy != null ? BUSY_NAME.replace('{first name}', busy.split(' ')[0]) : user.name}
              </span>
              <span className="mono" style={{ display: 'flex', alignItems: 'center', gap: 5, fontSize: 10, color: 'var(--fg-3)', marginTop: 1 }}>
                <span
                  data-testid="persona-dot"
                  style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: busy != null || standingIn ? 'var(--status-amber-text)' : 'var(--status-green-text)' }}
                />
                <span data-testid="persona-role">{busy != null ? TRIGGER_BUSY_ROLE : current ? accessRoleLabel(current.role).toUpperCase() : '—'}</span>
              </span>
            </span>
            <span style={{ flex: 'none', color: 'var(--status-amber-text)', transform: open ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}>
              {demoChevronUpGlyph}
            </span>
          </button>
          {open && (
            <PersonaPopover
              members={ctx.members}
              membersState={ctx.membersState}
              membersError={ctx.membersError}
              seatSubject={ctx.seatSubject}
              standingIn={standingIn}
              rowError={rowError}
              onSelect={handleSelect}
              onReturn={handleReturn}
            />
          )}
        </div>
        {signOutButton}
      </div>
    </div>
  )
}
