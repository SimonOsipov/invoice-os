import { BrandMark } from '../icons'
import { accessRoleLabel, type AccessRole } from '../lib/members'
import type { PendingInvite } from '../lib/sessionHandoff'

// Passed as `joining` while Create my own workspace runs; invite ids are UUIDs.
export const CREATING_OWN = 'create-own'

const BUTTON = { alignSelf: 'flex-start', height: 34, padding: '0 12px', fontSize: 13 } as const
const WRAP = { overflowWrap: 'anywhere' } as const

const inviteLine = (i: PendingInvite) => {
  const role = accessRoleLabel(i.role as AccessRole)
  return i.inviter ? `${i.inviter} invited you as ${role}.` : `You are invited as ${role}.`
}

export function JoinWorkspace({ invites, joining, onJoin, onSignOut, onCreateOwn }: {
  invites: PendingInvite[]
  joining: string | null
  onJoin: (id: string) => void
  onSignOut: () => void
  onCreateOwn?: () => void
}) {
  const several = invites.length > 1
  const busy = joining !== null
  const joinButton = (i: PendingInvite, marginTop: number) => (
    <button
      onClick={() => onJoin(i.id)}
      disabled={busy}
      aria-label={joining === i.id ? undefined : `Join ${i.workspace}`}
      className="v2-btn v2-btn-primary pf-btn"
      style={{ ...BUTTON, marginTop }}
    >
      {joining === i.id ? 'Joining…' : 'Join'}
    </button>
  )
  return (
    <div
      className="asc-app"
      style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <div data-testid="join-screen" style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>ASComply</span>
        </div>
        <div style={{ padding: '28px 20px', display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h1 style={{ margin: 0, fontSize: 14, fontWeight: 600, color: 'var(--fg-1)', ...WRAP }}>
            {several ? 'Choose a workspace to join' : `Join ${invites[0].workspace}`}
          </h1>
          <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)', ...WRAP }}>
            {several ? `You are invited to ${invites.length} workspaces. An account can belong to only one.` : inviteLine(invites[0])}
          </div>
          {several ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {invites.map((i) => (
                <div key={i.id} data-testid="join-invite" style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 12, display: 'flex', flexDirection: 'column', gap: 4, ...WRAP }}>
                  <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{i.workspace}</div>
                  <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>{inviteLine(i)}</div>
                  {joinButton(i, 6)}
                </div>
              ))}
            </div>
          ) : (
            joinButton(invites[0], 10)
          )}
          {onCreateOwn && (
            <button onClick={onCreateOwn} disabled={busy} className="v2-btn v2-btn-ghost pf-btn" style={{ ...BUTTON, marginTop: several ? 0 : 2 }}>
              {joining === CREATING_OWN ? 'Creating…' : 'Create my own workspace'}
            </button>
          )}
          <button onClick={onSignOut} disabled={busy} className="v2-btn v2-btn-ghost pf-btn" style={{ ...BUTTON, marginTop: onCreateOwn ? 0 : several ? 0 : 2 }}>
            Sign out
          </button>
        </div>
      </div>
    </div>
  )
}
