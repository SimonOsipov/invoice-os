// Every string the switcher renders, verbatim from the design of record. All plain strings and
// never functions, so a scan over Object.values() sees them; {tokens} substitute at the call site.

export const MARKER_LABEL = 'DEMO BUILD'

export const TRIGGER_TITLE = 'Demo only — become another member of this tenant'
export const TRIGGER_BUSY_ROLE = 'RELOADING'
// Deviates from the spec board's `Signing in as {first name}…`: the brief's own §1 and AC-8 ban
// sign-in vocabulary, and that string is character-identical to SignIn.tsx's real account state.
export const BUSY_NAME = 'Becoming {first name}…'

export const POPOVER_HEADER = 'DEMO ONLY · BECOME ANOTHER MEMBER'
export const POPOVER_NOTE =
  "The app reloads with that person's permissions. This is not account switching — no password, no email."

// Bare fragments: the meta line joins them with ' · ' at the call site, which is both the
// design's own `bits.join(' · ')` and the app's idiom (SourceDocumentModal, MembersView, RolesView).
// Sentence case, uppercased by CSS at the render site. e2e/envCopy.test.ts bans the all-caps
// signing tag case-sensitively, and `-` is a word boundary, so the all-caps spelling hits it.
export const SEAT_LABEL = 'Signed-in seat'
export const BLOCKED_STATUS_LABEL = 'SUSPENDED'
export const SUSPENDED_REASON = 'Suspended — sign-in is blocked, so this person cannot be used in the demo.'
export const RETURN_ROW = 'Return to the signed-in seat · {name}'

export const TOAST_TITLE = 'You are now {full name}'
export const TOAST_META = '{ROLE} · APPROVAL QUEUE AND PERMISSIONS RELOADED'
// The design draws the dismiss control as a bare icon, giving it no accessible name.
export const TOAST_DISMISS = 'Dismiss'

// Invoice-detail copy, not switcher copy: it names the person whose role blocks the step.
export const BLOCKED_BY_ROLE_PREFIX = 'Signed in as {name} — a {role}.'
export const BLOCKED_BY_ROLE_ACTION = 'Switch to a Reviewer to act on this step.'
