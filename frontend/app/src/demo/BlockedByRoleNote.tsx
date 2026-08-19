// Demo-only. Names the person whose access role is why Approve is disabled.
import { accessRoleLabel, type Member } from '../lib/members'
import { BLOCKED_BY_ROLE_ACTION, BLOCKED_BY_ROLE_PREFIX } from './copy'
import { demoLockGlyph } from './glyphs'

export function BlockedByRoleNote({ member }: { member: Member }) {
  const prefix = BLOCKED_BY_ROLE_PREFIX.replace('{name}', member.name).replace(
    '{role}',
    accessRoleLabel(member.role),
  )
  return (
    <div
      data-testid="persona-blocked-note"
      style={{ display: 'flex', gap: 8, alignItems: 'flex-start', background: 'var(--bg-3)', borderRadius: 8, padding: '10px 11px' }}
    >
      <span style={{ flex: 'none', color: 'var(--fg-4)', marginTop: 2 }}>{demoLockGlyph}</span>
      <span style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
        {`${prefix} ${BLOCKED_BY_ROLE_ACTION}`}
      </span>
    </div>
  )
}
