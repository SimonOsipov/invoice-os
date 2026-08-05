import { APP_PERSONAS } from '../auth'

/**
 * `uploaded_by` is a GoTrue subject uuid and this system has no users table, so a name is
 * only producible where the subject matches a known persona. Everything else renders the
 * raw subject, in mono, rather than a fabricated identity.
 */
export function actorLabel(subject: string | null): { text: string; mono: boolean } {
  if (subject === null) return { text: 'Not recorded', mono: false }
  const persona = Object.values(APP_PERSONAS).find((p) => p.subject === subject)
  if (persona) return { text: `${persona.name} · ${persona.org}`, mono: false }
  return { text: subject, mono: true }
}
