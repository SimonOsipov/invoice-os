// The invite accept page's client and the landing's read of `?invite=`.
import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { appBase } from './auth'

export const INVITE_PARAM = 'invite'

export type InviteOutcome = 'already-member' | 'invalid' | 'other-address'

const OUTCOMES: readonly string[] = ['already-member', 'invalid', 'other-address']

export const INVITE_NOTICES: Record<InviteOutcome, string> = {
  'already-member': 'You already belong to a workspace. An account can belong to only one.',
  invalid: 'This invite is no longer valid. Ask your workspace admin for a new one.',
  'other-address': 'This invite was sent to a different email address. Open the invite link from that email and sign in with the invited address.',
}

export const ROLE_LABELS: Record<string, string> = { admin: 'Admin', preparer: 'Preparer', reviewer: 'Reviewer' }

export type InvitationPreview = { workspace: string; role: string; email: string }

// Exactly one recognised value reads as an outcome; anything else, repeats included, is none.
export function readInviteOutcome(search: string): InviteOutcome | null {
  const values = new URLSearchParams(search).getAll(INVITE_PARAM)
  return values.length === 1 && OUTCOMES.includes(values[0]) ? (values[0] as InviteOutcome) : null
}

function base(): string {
  const b = gatewayBase()
  if (!b) throw new ApiError('malformed', 'gateway not configured')
  return b
}

export const previewInvitation = (token: string) =>
  apiFetch<InvitationPreview>(`${base()}/auth/invitation`, { method: 'POST', body: { token } })

export async function registerInvitee(token: string, password: string): Promise<void> {
  await apiFetch<unknown>(`${base()}/auth/invitation/register`, { method: 'POST', body: { token, password } })
}

export function inviteSignInUrl(token: string | null): string | null {
  const app = appBase()
  return app && token ? `${app}?auth=start#invite=${token}` : null
}
