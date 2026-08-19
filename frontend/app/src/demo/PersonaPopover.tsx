// The "become another member" roster. Explicit props, not ctx -- PersonaFooter does the
// ctx-to-props adaptation in one place.
import type { ApiError, AsyncStatus } from '@invoice-os/api-client'
import { accessRoleLabel, membersSurface, type Member } from '../lib/members'
import { POPOVER_EMPTY, POPOVER_ERROR_TITLE, POPOVER_HEADER, POPOVER_LOADING, POPOVER_NOTE, RETURN_ROW, SEAT_LABEL, SUSPENDED_REASON } from './copy'
import { demoFlaskGlyph, demoLockGlyph, demoTickGlyph } from './glyphs'
import { isSeat } from './identity'

export function PersonaPopover({
  members,
  membersState,
  membersError,
  seatSubject,
  standingIn,
  onSelect,
  onReturn,
}: {
  members: Member[]
  membersState: AsyncStatus
  membersError: ApiError | null
  seatSubject: string | undefined
  standingIn: boolean
  onSelect: (member: Member) => void
  onReturn: () => void
}) {
  const surface = membersSurface(membersState)
  const seatName = members.find((m) => isSeat(m.id, seatSubject))?.name ?? ''

  return (
    <div
      data-testid="persona-popover"
      style={{
        position: 'absolute',
        bottom: 'calc(100% + 6px)',
        left: 0,
        width: 296,
        zIndex: 70,
        background: 'var(--bg-2)',
        border: '1px solid var(--status-amber-border)',
        borderRadius: 'var(--radius-md)',
        boxShadow: '0 -16px 40px -16px oklch(20% .02 210 / 0.28)',
        overflow: 'hidden',
        animation: 'popIn 140ms ease-out',
      }}
    >
      <div style={{ padding: '9px 12px 8px', background: 'var(--status-amber-bg)', borderBottom: '1px solid var(--status-amber-border)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ flex: 'none', color: 'var(--status-amber-text)' }}>{demoFlaskGlyph}</span>
          <span
            data-testid="persona-popover-header"
            className="mono"
            style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.08em', color: 'var(--status-amber-text)' }}
          >
            {POPOVER_HEADER}
          </span>
        </div>
        <div style={{ fontSize: 11, color: 'var(--status-amber-text)', marginTop: 3, lineHeight: 1.45 }}>{POPOVER_NOTE}</div>
      </div>

      {surface === 'loading' && (
        <div data-testid="persona-surface-loading" style={{ padding: '16px 12px', fontSize: 11, color: 'var(--fg-3)', textAlign: 'center' }}>
          {POPOVER_LOADING}
        </div>
      )}

      {surface === 'error' && (
        <div data-testid="persona-surface-error" style={{ padding: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--status-red-text)' }}>{POPOVER_ERROR_TITLE}</div>
          <div style={{ fontSize: 11, color: 'var(--fg-2)', marginTop: 2 }}>{membersError?.message}</div>
        </div>
      )}

      {surface === 'empty' && (
        <div data-testid="persona-surface-empty" style={{ padding: '16px 12px', fontSize: 11, color: 'var(--fg-3)', textAlign: 'center' }}>
          {POPOVER_EMPTY}
        </div>
      )}

      {surface === 'roster' && (
        <div data-testid="persona-row-list" style={{ maxHeight: 'calc(100vh - 240px)', overflowY: 'auto' }}>
          {members.map((m) => {
            const blocked = m.status !== 'active'
            const bits = blocked ? [accessRoleLabel(m.role), m.status] : isSeat(m.id, seatSubject) ? [accessRoleLabel(m.role), SEAT_LABEL] : [accessRoleLabel(m.role)]

            const rowStyle = {
              width: '100%',
              display: 'flex',
              alignItems: 'flex-start',
              gap: 10,
              padding: '9px 12px',
              border: 0,
              textAlign: 'left' as const,
              fontFamily: 'var(--font-sans)',
              background: m.isYou ? 'var(--bg-3)' : 'transparent',
              cursor: blocked ? 'not-allowed' : 'pointer',
            }

            const rowContent = (
              <>
                <span
                  style={{
                    flex: 'none',
                    width: 26,
                    height: 26,
                    borderRadius: 99,
                    display: 'grid',
                    placeItems: 'center',
                    fontSize: 10,
                    fontWeight: 700,
                    background: blocked ? 'var(--bg-3)' : 'var(--slate-800)',
                    color: blocked ? 'var(--fg-4)' : 'var(--text-on-dark)',
                  }}
                >
                  {m.initials}
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span
                    data-testid="persona-row-name"
                    style={{
                      display: 'block',
                      fontSize: 13,
                      fontWeight: 500,
                      color: blocked ? 'var(--fg-4)' : 'var(--fg-1)',
                      whiteSpace: 'nowrap',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                    }}
                  >
                    {m.name}
                  </span>
                  <span
                    data-testid="persona-row-meta"
                    className="mono"
                    style={{
                      display: 'block',
                      fontSize: 10,
                      letterSpacing: '0.04em',
                      marginTop: 1,
                      textTransform: 'uppercase',
                      color: m.status === 'suspended' ? 'var(--status-red-text)' : blocked ? 'var(--fg-4)' : 'var(--fg-3)',
                      ...(blocked ? {} : { whiteSpace: 'nowrap' as const, overflow: 'hidden' as const, textOverflow: 'ellipsis' as const }),
                    }}
                  >
                    {bits.join(' · ')}
                  </span>
                  {m.status === 'suspended' && (
                    <span data-testid="persona-row-reason" style={{ display: 'block', fontSize: 11, color: 'var(--fg-3)', lineHeight: 1.45, marginTop: 4 }}>
                      {SUSPENDED_REASON}
                    </span>
                  )}
                </span>
                {m.isYou && (
                  <span data-testid="persona-row-tick" style={{ flex: 'none', color: 'var(--action)', marginTop: 4 }}>
                    {demoTickGlyph}
                  </span>
                )}
                {blocked && (
                  <span data-testid="persona-row-lock" style={{ flex: 'none', color: 'var(--fg-4)', marginTop: 4 }}>
                    {demoLockGlyph}
                  </span>
                )}
              </>
            )

            return blocked ? (
              <div key={m.id} data-testid="persona-row" style={rowStyle}>
                {rowContent}
              </div>
            ) : (
              <button key={m.id} type="button" data-testid="persona-row" className="pf-menu-item" style={rowStyle} onClick={() => onSelect(m)}>
                {rowContent}
              </button>
            )
          })}
        </div>
      )}

      {standingIn && (
        <button
          type="button"
          data-testid="persona-return-row"
          onClick={onReturn}
          style={{
            width: '100%',
            display: 'block',
            textAlign: 'left',
            border: 0,
            borderTop: '1px solid var(--line-1)',
            background: 'transparent',
            padding: '10px 12px',
            cursor: 'pointer',
            fontFamily: 'var(--font-sans)',
            fontSize: 12,
            fontWeight: 500,
            color: 'var(--action)',
          }}
        >
          {RETURN_ROW.replace('{name}', seatName)}
        </button>
      )}
    </div>
  )
}
