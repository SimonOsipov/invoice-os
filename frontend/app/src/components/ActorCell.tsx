// The Who column. Three actor shapes exist in audit_log: a resolved person, the literal
// "system", and free text such as backfill-source-rows (internal/importer/backfill.go).
// A person and System share the round 26px avatar; initials and the bolt tell them apart.
// Free text stays a flat square.
//
// The wire already carries actor_name/actor_kind (internal/actor/resolve.go), so this always
// passes the resolved pair to actorLabel: its APP_PERSONAS fall-through holds both tenants'
// subjects unscoped and would otherwise name another tenant's admin
// (ActorCell.test.tsx: actorCell_alwaysPassesResolvedPair).

import { Icon } from '../icons'
import { actorLabel, type ActorKind } from '../lib/actor'

const SIZE = 26

export interface ActorAvatarStyle {
  width: number
  height: number
  borderRadius: string
  background: string
  color: string
}

export function actorAvatar(kind: ActorKind): ActorAvatarStyle {
  const base = { width: SIZE, height: SIZE }
  if (kind === 'person') {
    return { ...base, borderRadius: '50%', background: 'var(--bg-3)', color: 'var(--fg-2)' }
  }
  if (kind === 'system') {
    return { ...base, borderRadius: '50%', background: 'var(--bg-3)', color: 'var(--fg-3)' }
  }
  // Free text and absent: a flat square with no fill or glyph -- neither a person nor System.
  return { ...base, borderRadius: 'var(--radius-xs)', background: 'transparent', color: 'var(--fg-3)' }
}

function initialsOf(name: string): string {
  const parts = name.split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  const first = parts[0][0] ?? ''
  const last = parts.length > 1 ? (parts[parts.length - 1][0] ?? '') : ''
  return (first + last).toUpperCase()
}

export interface ActorCellProps {
  actor: string
  actor_name: string
  actor_kind: string
  variant?: 'audit' | 'activity'
}

export function ActorCell({ actor, actor_name, actor_kind, variant = 'audit' }: ActorCellProps) {
  const label = actorLabel(actor, { name: actor_name, kind: actor_kind })
  const avatar = actorAvatar(label.kind)

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: variant === 'audit' ? 9 : 8, minWidth: 0 }}>
      <span
        aria-hidden
        className="mono"
        style={{
          ...avatar,
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          flex: '0 0 auto',
          fontFamily: 'var(--font-mono, ui-monospace, monospace)',
          fontSize: 9.5,
          fontWeight: 700,
        }}
      >
        {label.kind === 'person' ? (
          <span data-testid="actor-initials">{initialsOf(label.text)}</span>
        ) : label.kind === 'system' ? (
          <span data-testid="actor-bolt" style={{ display: 'inline-flex' }}>
            <Icon paths={['M13 2 3 14h8l-1 8 10-12h-8z']} size={13} />
          </span>
        ) : null}
      </span>
      <span
        style={{
          fontFamily: label.mono ? 'var(--font-mono, ui-monospace, monospace)' : undefined,
          fontSize: label.mono ? 12 : variant === 'audit' ? 13 : 12.5,
          fontWeight: variant === 'audit' ? 500 : 600,
          color: label.kind === 'raw' ? 'var(--fg-2)' : 'var(--fg-1)',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
      >
        {label.text}
      </span>
    </span>
  )
}
