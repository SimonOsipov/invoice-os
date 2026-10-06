// Red-phase stub: the executor replaces every body.
export const PENDING_INVITE_KEY = 'invoice-os.pendingInvite'

export function readInviteFragment(_hash: string): string | null {
  return null
}

export function holdPendingInvite(_token: string | null, _now: number = Date.now()): void {}

export function consumePendingInvite(_now: number = Date.now()): string | null {
  return null
}
