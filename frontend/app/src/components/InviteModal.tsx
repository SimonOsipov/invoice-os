// Stub: the invite modal lands in RESEND-07-03.
import type { AccessRole, Member } from '../lib/members'

export type InviteModalProps = {
  /** The roster with pending invites; chips are judged against it. */
  existing: readonly Member[]
  onSend: (emails: readonly string[], role: AccessRole) => Promise<void>
  /** Must be stable: it is a `useDismiss` dependency. */
  onClose: () => void
}

export function InviteModal(_props: InviteModalProps): null {
  return null
}
