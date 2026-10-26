import type { Me } from '../auth'
import type { AuthedFetch } from './portfolio'
import { BASE64URL_43, SIGN_IN_STATE_TTL_MS } from './signInState'

export const DELETE_WORKSPACE = {
  title: 'Delete this workspace',
  body: 'The workspace locks for 30 days. Then we delete every invoice, file and member.',
  button: 'Delete workspace',
  label: 'Type the workspace name to confirm',
  cancel: 'Cancel',
} as const

export const DELETE_ADMIN_ONLY = 'Only an admin can delete the workspace.'

// Not derived from WORKSPACE_LOCKED_MESSAGE: the wire sentence and the notice may diverge (SUSPENDED_NOTICE rule).
export const DELETION_NOTICE = {
  title: 'This workspace is scheduled for deletion',
  cancel: 'Cancel deletion',
  signOut: 'Sign out',
} as const

// Odd indexes are the date and the name; the notice bolds them.
export function deletionNoticeBody(date: string, name: string): string[] {
  return ['On ', date, ' we delete every invoice, file and member of ', name, '.']
}

export function formatDueDate(iso: string, timeZone?: string): string {
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'long', year: 'numeric', timeZone }).format(
    new Date(iso),
  )
}

export function nameMatches(typed: string, name: string): boolean {
  return typed === name
}

export async function scheduleWorkspaceDeletion(f: AuthedFetch, base: string, workspaceName: string): Promise<string> {
  const r = await f<{ deletion_due_at: string }>(`${base}/api/tenancy/v1/workspace/deletion`, {
    method: 'POST',
    body: { workspace_name: workspaceName },
  })
  return r.deletion_due_at
}

export async function cancelWorkspaceDeletion(f: AuthedFetch, base: string): Promise<void> {
  await f<unknown>(`${base}/api/tenancy/v1/workspace/deletion/cancel`, { method: 'POST' })
}

export async function readWorkspaceDeletion(
  f: AuthedFetch,
  base: string,
): Promise<{ dueAt: string | null; name: string; isAdmin: boolean }> {
  const me = await f<Me>(`${base}/api/tenancy/v1/me`)
  return { dueAt: me.tenant.deletion_due_at ?? null, name: me.tenant.name, isAdmin: me.user.role === 'admin' }
}

export const CANCEL_LINK_KEY = 'invoice-os.cancelDeletionLink'
const CANCEL_LINK_SCHEMA_VERSION = 1

// The token of `#cancel-deletion=<43 base64url>` given exactly once, else null.
export function readCancelLinkFragment(hash: string): string | null {
  const tokens = new URLSearchParams(hash.replace(/^#/, '')).getAll('cancel-deletion')
  return tokens.length === 1 && BASE64URL_43.test(tokens[0]) ? tokens[0] : null
}

// Held in this tab across the sign-in bounce; null removes it. Warns, never throws.
export function holdCancelLink(token: string | null, now: number = Date.now()): void {
  try {
    if (token === null) sessionStorage.removeItem(CANCEL_LINK_KEY)
    else sessionStorage.setItem(CANCEL_LINK_KEY, JSON.stringify({ v: CANCEL_LINK_SCHEMA_VERSION, t: token, at: now }))
  } catch (e) {
    console.warn(`[workspaceDeletion] failed to store cancel link at "${CANCEL_LINK_KEY}":`, e)
  }
}

// The held token while live, else null; leaves the key alone.
export function peekCancelLink(now: number = Date.now()): string | null {
  try {
    const raw = sessionStorage.getItem(CANCEL_LINK_KEY)
    if (raw == null) return null
    const p = JSON.parse(raw) as { v?: unknown; t?: unknown; at?: unknown } | null
    if (
      p == null ||
      p.v !== CANCEL_LINK_SCHEMA_VERSION ||
      typeof p.t !== 'string' ||
      !BASE64URL_43.test(p.t) ||
      typeof p.at !== 'number' ||
      Number.isNaN(p.at)
    ) {
      return null
    }
    return p.at <= now && now - p.at < SIGN_IN_STATE_TTL_MS ? p.t : null
  } catch (e) {
    console.warn(`[workspaceDeletion] failed to read cancel link at "${CANCEL_LINK_KEY}":`, e)
    return null
  }
}

export async function cancelWorkspaceDeletionByLink(f: AuthedFetch, base: string, token: string): Promise<void> {
  await f<unknown>(`${base}/api/tenancy/v1/workspace/deletion/cancel-by-link`, { method: 'POST', body: { token } })
}
