import { APP_PERSONAS } from '../auth'

export type ActorKind = 'absent' | 'system' | 'person' | 'raw'

export interface ActorLabel {
  text: string
  mono: boolean
  kind: ActorKind
}

/**
 * Renders a stored actor for display. Endpoints whose wire carries `actor_name`/`actor_kind`
 * resolve the name server-side (internal/actor/resolve.go); pass that pair as `resolved`.
 *
 * A resolved pair is authoritative and APP_PERSONAS is never consulted below it -- that
 * table holds BOTH tenants' subjects unscoped, so a fall-through names the other tenant's
 * admin to a viewer with no sight of that tenant (actor.test.ts:
 * actorLabel_neverConsultsPersonasWhenTheServerAnswered).
 *
 * The `system` / persona / verbatim rungs remain for the callers whose wire shape carries
 * no resolved pair, which is why the parameter is optional.
 */
export function actorLabel(subject: string | null, resolved?: { name: string; kind: string }): ActorLabel {
  if (subject === null) return { text: 'Not recorded', mono: false, kind: 'absent' }

  if (resolved != null) {
    // An empty name is the server failing to answer, not an answer: the subject stands in.
    if (resolved.name === '') return { text: subject, mono: true, kind: 'raw' }
    // Both wire mirrors widen actor_kind to `string` (lib/invoices.ts:323), so narrow here.
    const kind = resolved.kind === 'system' || resolved.kind === 'person' ? resolved.kind : 'raw'
    return { text: resolved.name, mono: kind === 'raw', kind }
  }

  if (subject === 'system') return { text: 'System', mono: false, kind: 'system' }
  const persona = Object.values(APP_PERSONAS).find((p) => p.subject === subject)
  if (persona) return { text: `${persona.name} · ${persona.org}`, mono: false, kind: 'person' }
  return { text: subject, mono: true, kind: 'raw' }
}
