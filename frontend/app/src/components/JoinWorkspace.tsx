import { useState } from 'react'

import { BrandMark } from '../icons'
import { accessRoleLabel, type AccessRole } from '../lib/members'
import { nameError, trimName, validName } from '../lib/pendingInvite'
import type { PendingInvite } from '../lib/sessionHandoff'
import { radioPaint } from './MemberParts'

// Passed as `joining` while Create my own workspace runs; invite ids are UUIDs.
export const CREATING_OWN = 'create-own'

const BUTTON = { alignSelf: 'flex-start', height: 34, padding: '0 12px', fontSize: 13 } as const
const WRAP = { overflowWrap: 'anywhere' } as const
const INPUT = { width: '100%', boxSizing: 'border-box' } as const
const LABEL = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, color: 'var(--fg-3)' } as const
const NO_WORKSPACE_LINE = 'This account has no workspace yet. If you were invited, open the invite link in your email.'
const FIRST_WORKSPACE_LINE = 'This account has no workspace yet. Create one below to get started. If you were invited, open the invite link in your email.'
const CREATE_FAILED = 'We could not create your workspace. Try again.'
type Kind = 'firm' | 'in_house'
// Labels match RegisterModal.tsx.
const KINDS: { value: Kind; label: string }[] = [
  { value: 'firm', label: 'For clients — an accounting or tax firm' },
  { value: 'in_house', label: 'For our own company — in-house' },
]

const inviteLine = (i: PendingInvite) => {
  const role = accessRoleLabel(i.role as AccessRole)
  return i.inviter ? `${i.inviter} invited you as ${role}.` : `You are invited as ${role}.`
}

// Empty `invites` renders the no-workspace screen: Create a workspace, never a silent provision.
// `firstWorkspace` (an account with no answers) adds the create copy and a kind choice, and freezes the values once a Create is sent.
export function JoinWorkspace({ invites, joining, notice, firstWorkspace = false, createFailed = false, defaultName = '', defaultWorkspace = '', onJoin, onSignOut, onCreateOwn, onCreate }: {
  invites: PendingInvite[]
  joining: string | null
  notice?: string | null
  firstWorkspace?: boolean
  createFailed?: boolean
  defaultName?: string
  defaultWorkspace?: string
  onJoin: (id: string, name: string) => void
  onSignOut: () => void
  onCreateOwn?: () => void
  onCreate?: (workspaceName: string, displayName: string, kind?: Kind) => void
}) {
  const [name, setName] = useState(defaultName)
  const [workspace, setWorkspace] = useState(defaultWorkspace)
  const [kind, setKind] = useState<Kind | ''>('')
  const [sent, setSent] = useState(false)
  const several = invites.length > 1
  const empty = invites.length === 0
  const busy = joining !== null
  const [nameTouched, setNameTouched] = useState(false)
  // A prefilled invalid name explains itself with no edit.
  const nameMessage = nameTouched || name !== '' ? nameError(name) : null
  const nameInput = (testId: string, label: string, value: string, set: (v: string) => void, error?: string | null) => (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <label style={LABEL}>
        {label}
        <input
          data-testid={testId}
          className="pf-input"
          value={value}
          onChange={(e) => set(e.target.value)}
          disabled={busy || sent}
          style={INPUT}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? 'join-name-error' : undefined}
        />
      </label>
      {error && (
        <div id="join-name-error" data-testid="join-name-error" style={{ fontSize: 11.5, color: 'var(--status-red-text)' }}>
          {error}
        </div>
      )}
    </div>
  )
  const setNameEdited = (v: string) => {
    setNameTouched(true)
    setName(v)
  }
  const joinButton = (i: PendingInvite, marginTop: number) => (
    <button
      onClick={() => onJoin(i.id, trimName(name))}
      disabled={busy || !validName(name)}
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
            {empty ? (firstWorkspace ? 'Create your workspace' : 'No workspace yet') : several ? 'Choose a workspace to join' : `Join ${invites[0].workspace}`}
          </h1>
          <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)', ...WRAP }}>
            {empty ? (firstWorkspace ? FIRST_WORKSPACE_LINE : NO_WORKSPACE_LINE) : several ? `You are invited to ${invites.length} workspaces. An account can belong to only one.` : inviteLine(invites[0])}
          </div>
          {notice && (
            <div role="status" data-testid="join-notice" style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)', ...WRAP }}>
              {notice}
            </div>
          )}
          {empty ? (
            <>
              {nameInput('join-workspace-name', 'Workspace name', workspace, setWorkspace)}
              {nameInput('join-name', 'Your name', name, setNameEdited, nameMessage)}
              {firstWorkspace && (
                <fieldset disabled={busy || sent} style={{ border: 0, margin: 0, padding: 0, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
                  <legend style={{ padding: 0, marginBottom: 6, fontSize: 12.5, color: 'var(--fg-3)', ...WRAP }}>How will this workspace file invoices?</legend>
                  {KINDS.map((k) => (
                    <label
                      key={k.value}
                      style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--fg-1)', cursor: 'pointer', ...WRAP, ...(busy || sent ? { opacity: 0.45, cursor: 'not-allowed' } : null) }}
                    >
                      <input type="radio" name="join-kind" value={k.value} checked={kind === k.value} onChange={() => setKind(k.value)} style={{ ...radioPaint(kind === k.value), margin: 0 }} />
                      {k.label}
                    </label>
                  ))}
                </fieldset>
              )}
              <button
                data-testid="join-create"
                onClick={() => {
                  if (firstWorkspace && kind !== '') {
                    setSent(true)
                    onCreate?.(workspace.trim(), trimName(name), kind)
                  } else {
                    onCreate?.(workspace.trim(), trimName(name))
                  }
                }}
                disabled={busy || !onCreate || !validName(workspace) || !validName(name) || (firstWorkspace && kind === '')}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ ...BUTTON, marginTop: 4 }}
              >
                {joining === CREATING_OWN ? 'Creating…' : 'Create a workspace'}
              </button>
              {firstWorkspace && createFailed && (
                <div role="alert" data-testid="join-create-error" style={{ fontSize: 11.5, color: 'var(--status-red-text)', ...WRAP }}>
                  {CREATE_FAILED}
                </div>
              )}
            </>
          ) : (
            <>
              {nameInput('join-name', 'Your name', name, setNameEdited, nameMessage)}
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
            </>
          )}
          {!empty && onCreateOwn && (
            <button onClick={onCreateOwn} disabled={busy} className="v2-btn v2-btn-ghost pf-btn" style={{ ...BUTTON, marginTop: several ? 0 : 2 }}>
              {joining === CREATING_OWN ? 'Creating…' : 'Create my own workspace'}
            </button>
          )}
          <button onClick={onSignOut} disabled={busy} className="v2-btn v2-btn-ghost pf-btn" style={{ ...BUTTON, marginTop: empty || onCreateOwn || several ? 0 : 2 }}>
            Sign out
          </button>
        </div>
      </div>
    </div>
  )
}
