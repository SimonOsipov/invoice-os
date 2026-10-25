import type { Me } from '../auth'
import type { AuthedFetch } from './portfolio'

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

export function deletionNoticeBody(date: string, name: string): string {
  return `On ${date} we delete every invoice, file and member of ${name}.`
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
