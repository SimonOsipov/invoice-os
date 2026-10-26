import type { AuthedFetch } from './portfolio'

export const ACCOUNT_PAGE = { title: 'Your account' } as const

export const DELETE_ACCOUNT = {
  title: 'Delete my account',
  buttonLeave: 'Delete my account',
  buttonWorkspace: 'Delete my account and workspace',
  labelPhrase: 'Type "delete my account" to confirm',
  cancel: 'Cancel',
} as const

export const DELETE_ACCOUNT_PHRASE = 'delete my account'

// Go: internal/tenancy/account_deletion.go AccountDeletionOutcome constants.
export type AccountDeletionOutcome = 'leave' | 'hand_on' | 'delete_workspace'

export interface AccountDeletionPreview {
  outcome: AccountDeletionOutcome
  heirName: string | null
}

// Odd indexes are the name; the card bolds them. A null heir gives one part.
export function accountDeletionBody(
  { outcome, heirName }: AccountDeletionPreview,
  workspaceName: string | null,
): string[] {
  const workspace = workspaceName ?? 'this workspace'
  switch (outcome) {
    case 'leave':
      return ['Your account goes at once. Your name stays in the history of ', workspace, ', marked (deleted).']
    case 'hand_on':
      return heirName === null
        ? ['You are the last admin. Admin moves to the member who joined first.']
        : ['You are the last admin. Admin moves to ', heirName, ', who joined first.']
    case 'delete_workspace':
      return ['You are the last member. ', workspace, ' will be deleted in 30 days.']
  }
}

export function phraseMatches(typed: string): boolean {
  return typed === DELETE_ACCOUNT_PHRASE
}

export async function readAccountDeletion(f: AuthedFetch, base: string): Promise<AccountDeletionPreview> {
  const r = await f<{ outcome: AccountDeletionOutcome; heir_name?: string | null }>(
    `${base}/api/tenancy/v1/me/deletion`,
  )
  return { outcome: r.outcome, heirName: r.heir_name ?? null }
}

export async function deleteMyAccount(
  f: AuthedFetch,
  base: string,
  outcome: 'leave' | 'hand_on',
): Promise<{ outcome: AccountDeletionOutcome }> {
  const r = await f<{ outcome: AccountDeletionOutcome }>(`${base}/api/tenancy/v1/me/deletion`, {
    method: 'POST',
    body: { outcome },
  })
  return { outcome: r.outcome }
}
