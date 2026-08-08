// Members (Settings › Members) — types, the memberships wire and read-only derivations.
//
// Identity, access role and status are SERVER truth, read through `listMembers` and written
// through `setMembershipStatus`. Everything else this module still models — invite, remove,
// department, per-person client access — has no endpoint behind it; `MEMBER_UNBACKED` says
// so in one sentence per control.
//
// This module is a function of a PERSON. Anything that is really a function of the ROLE LIST
// — who holds a seat, what a policy step resolves to, how a workspace's coverage reads —
// lives in lib/roles.ts, which imports this one and never the reverse.
//
// Dependency direction is ONE-WAY: members.ts → customers.ts (runtime, `initials`),
// members.ts → portfolio.ts (type-only, `AuthedFetch`). Neither imports this module back.
// members.ts imports NOTHING from types.ts: types.ts already type-imports this module, so a
// back-edge here would be a real cycle rather than a hypothetical one.
//
// Reducers are immutable, and a `Member` is now flat — every value on it is a scalar, so a
// spread is a full copy and nothing can be left aliased.

import { CFG } from '../data'
import type { AuthedFetch } from './portfolio'
import type { AsyncStatus } from '@invoice-os/api-client'
import { initials } from './customers'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Axis A — what a person can DO. Matches the shipped backend enum, three values only. */
export type AccessRole = 'admin' | 'preparer' | 'reviewer'

export type MemberStatus = 'active' | 'invited' | 'suspended'

/** In-house grouping primitive. There is no separate Teams/Groups entity. */
export type Department = 'Finance' | 'Tax & Compliance' | 'Accounts Payable' | 'Executive' | 'Procurement'

/**
 * One flat row per person — exactly what a membership stores. `toMember` is its only
 * producer, and it sets all seven keys from the wire.
 */
export type Member = {
  id: string
  name: string
  initials: string
  /** `null` when the membership row carries no address — render via `emailLabel`. */
  email: string | null
  role: AccessRole
  status: MemberStatus
  isYou: boolean
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Axis A, with the descriptions rendered verbatim in the drawer's role cards. */
export const ACCESS_ROLES: readonly { id: AccessRole; label: string; description: string }[] = [
  { id: 'admin', label: 'Admin', description: 'Full access. Manages members, settings, connectors and certificates.' },
  { id: 'preparer', label: 'Preparer', description: 'Creates, imports and validates invoices. Cannot approve or transmit.' },
  {
    id: 'reviewer',
    label: 'Reviewer',
    description: 'Reviews and signs off on invoices in approval steps. Cannot manage members or settings.',
  },
]

/**
 * The `ACCESS_ROLES` display label for one role id — the Access role column, the drawer and
 * the role picker's meta column all want it, and none may re-case `m.role`, which happens
 * to produce the right string today and diverges the first time a label changes.
 *
 * The fallback exists for the same reason `roleOf`'s does (roles.ts): a persisted
 * id this build does not know must render as SOMETHING rather than crash the row. It is a
 * branch no screenshot can reach, which is exactly why it is spec'd here and not inlined.
 */
export function accessRoleLabel(role: AccessRole): string {
  return ACCESS_ROLES.find((r) => r.id === role)?.label ?? role
}

export const DEPARTMENTS: readonly Department[] = ['Finance', 'Tax & Compliance', 'Accounts Payable', 'Executive', 'Procurement']

/**
 * The "What can each role do?" expander — eight capability rows × the three roles. Labels
 * are §6's copy, kept lowercase exactly as the story writes them; casing at render time is
 * MEMB-01-05's display call, not a fact to bake in here.
 */
export const CAPABILITY_ROWS: readonly { label: string; admin: boolean; preparer: boolean; reviewer: boolean }[] = [
  { label: 'create and edit invoices', admin: true, preparer: true, reviewer: true },
  { label: 'import from file or ERP', admin: true, preparer: true, reviewer: true },
  { label: 'run validation', admin: true, preparer: true, reviewer: true },
  { label: 'approve in approval steps', admin: true, preparer: false, reviewer: true },
  { label: 'transmit to NRS/MBS', admin: true, preparer: false, reviewer: true },
  { label: 'invite and manage members', admin: true, preparer: false, reviewer: false },
  { label: 'manage ERP connectors', admin: true, preparer: false, reviewer: false },
  { label: 'manage signing certificates', admin: true, preparer: false, reviewer: false },
]

/**
 * §6's footnote under the capability matrix, and §6's copy for the firm-only `Client users`
 * placeholder. Both are rendered verbatim by MemberRoleMatrix — MEMB-01-05's AC#1 says so
 * for the footnote and AC#3 for the card — and neither is a derivation of anything.
 *
 * They live here, beside the eight labels QA18 already pins, for the reason task-297's QA
 * moved three display strings out of components: `environment: node` cannot mount a
 * component, so a string authored inside one is a string no spec can hold (§15.8). The
 * footnote is the longest passage in the story, two sentences whose clauses depend on each
 * other, and a fluent paraphrase of it is exactly what a screenshot gate cannot catch.
 *
 * The expander's own header label stays in the component: §6 names it as the affordance,
 * not as prose, and AC#1's verbatim clause covers the rows and the footnote only.
 */
// Reworded one noun at a time — "approval position" → "workflow role" — so §6's two-sentence
// structure and its Reviewer/which-steps distinction both survive the rename.
export const CAPABILITY_FOOTNOTE =
  'Approving also requires a workflow role named by the policy that routes the invoice. A person needs the Reviewer role to act on approval steps at all, and a workflow role to decide which steps.'

export const CLIENT_USERS_COPY =
  'Give a contact at one of your clients read-only access, or approval rights on their own invoices.'

/**
 * The static client roster the invite flow's scoping picker indexes. Ids ARE the CFG indices
 * (../data) — deliberately the static mock roster and not the live portfolio entity list,
 * whose ids are UUID strings a `number[]` cannot address (`[client-roster-is-static]`).
 */
export const CLIENT_ROSTER: readonly { id: number; name: string }[] = CFG.map((c, i) => ({ id: i, name: c.name }))

// ---------------------------------------------------------------------------
// Derivations — all pure, all taking their inputs as arguments
// ---------------------------------------------------------------------------

/**
 * The standing committees and `Preparer` — which here means "whoever raised this document",
 * a relationship rather than a position. A stored value the list does not otherwise carry
 * (`polH1`'s legacy `'Tax Team'`) is appended LAST: it stays selectable without being
 * promoted (Decision `[notify-target-order]`).
 *
 * The departments that used to lead this list are gone with the field. `toMember` never set
 * one, so the dropdown already lost them when the roster went live — this makes the type
 * agree with what the screen has been rendering.
 */
export function inhouseNotifyTargets(current: string): string[] {
  const out: string[] = ['Audit Committee', 'Board', 'Preparer']
  if (current && !out.includes(current)) out.push(current)
  return out
}

/** Active members whose access role is `reviewer` — admins excluded, per §11.3. */
export function delegateCandidates(list: readonly Member[]): string[] {
  return list.filter((m) => m.status === 'active' && m.role === 'reviewer').map((m) => m.name)
}

/** The §9 last-admin lock reads this: at length 1, that admin cannot be demoted or suspended. */
export function activeAdmins(list: readonly Member[]): Member[] {
  return list.filter((m) => m.role === 'admin' && m.status === 'active')
}

// ---------------------------------------------------------------------------
// The invite pipeline, the reducers, the filter and the last-admin guard
// ---------------------------------------------------------------------------
// UNREACHED from the app since the roster went live: no endpoint mints an invite, so the
// modal that consumed this half is gone. Kept, not deleted — the invite surface is MEMB's
// to rebuild, and these are the rules it will need. Nothing below has an app-path caller.

/** One verdict per pasted address, in input order. */
export type InviteVerdict = 'ok' | 'member' | 'invited' | 'malformed'

/**
 * Discriminated on `mode`, so the fork is a compile-time fact and a firm invite's
 * `clientAccess` is REQUIRED rather than optional. `Member` itself carries neither field —
 * this is the argument an invite would take, not the row it would produce.
 */
export type InviteOptions =
  | { mode: 'firm'; role: AccessRole; clientAccess: 'all' | number[] }
  | { mode: 'inhouse'; role: AccessRole; department: Department }

/**
 * Deliberately minimal (Decision `[email-validation-minimal]`): one `@`, a dot in the
 * domain, no whitespace on either side. Same shape as the pattern rules.ts:189 shows the
 * user as display copy, so what the invite box accepts cannot drift from what the rules
 * screen advertises.
 */
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** Comma, semicolon and any whitespace — newlines included — all separate addresses. */
const ADDRESS_SEPARATORS = /[,;\s]+/

/** `. _ - +` all read as word breaks inside the local part. */
const LOCAL_PART_SEPARATORS = /[._+-]+/

/** Splits on comma / semicolon / whitespace / newline; trims, drops empties, de-dupes. */
export function parseEmailInput(raw: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  // `\s` inside the separator class does the trimming for us: whitespace can never survive
  // into a fragment, so the only cleanup left is dropping the empties that repeated or
  // leading/trailing separators leave behind.
  for (const address of raw.split(ADDRESS_SEPARATORS)) {
    if (!address) continue
    // De-duped case-insensitively but stored VERBATIM — the first spelling seen is the one
    // the chip renders, and the address is what will be mailed.
    const key = address.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(address)
  }
  return out
}

export function isValidEmail(value: string): boolean {
  return EMAIL_RE.test(value)
}

/** The local part, split into words. Shared by `nameFromEmail` and `initialsFrom`. */
function localTokens(email: string): string[] {
  return email.split('@')[0].split(LOCAL_PART_SEPARATORS).filter(Boolean)
}

/** §7's "name derived from the local part" — local part, split, capitalised, space-joined. */
export function nameFromEmail(email: string): string {
  return localTokens(email)
    .map((t) => t[0].toUpperCase() + t.slice(1).toLowerCase())
    .join(' ')
}

/**
 * MEMB-01-06's extra chip gate — a local part of only separators derives no name.
 *
 * `isValidEmail` is deliberately minimal, so `'...@x.ng'` and `'-@x.ng'` pass it, classify
 * `ok`, and mint a row whose `name` and `initials` are both empty: a blank Person cell and
 * an empty initials circle in the table. QA35 pins that as RECORDED, NOT ENDORSED, and
 * routes the product call here — the invite modal rejects the chip as `Not a valid email`.
 *
 * Deliberately a SIBLING rather than a fourth branch inside `classifyInvites`: QA35 pins
 * that function returning `['ok']` for exactly this address, and that spec stays literally
 * true — the classifier still says `ok`, the modal declines to mint. Widening `EMAIL_RE`
 * instead is barred twice over: T2.7/T2.8 pin its accept/reject sets, and Decision
 * `[email-validation-minimal]` keeps it matching the pattern rules.ts:189 advertises.
 *
 * Not a validator on its own — `'nope'` has a derivable name and is not an address. It is
 * only ever applied ON TOP of an `ok` verdict, so the composition is strictly stricter than
 * the classifier alone and can never let a bad row through.
 */
export function hasDerivableName(email: string): boolean {
  return localTokens(email).length > 0
}

/**
 * Takes an EMAIL, not a name — a deliberate FORK of `initials(name)` (customers.ts:60-69)
 * rather than a reuse, and emphatically not a silent third variant. Two reasons, both
 * structural: that helper reads a NAME and strips non-alpha, so an email fed to it returns
 * garbage; and Decision `[name-from-local-part]` wants a single-token local part to yield
 * its first TWO letters (`zainab` → `ZA`), where one-letter-per-token gives `Z`. Composing
 * it as `initials(nameFromEmail(x))` fails for that same second reason.
 */
export function initialsFrom(email: string): string {
  const tokens = localTokens(email)
  const source = tokens.length === 1 ? tokens[0] : tokens.map((t) => t[0]).join('')
  return source.slice(0, 2).toUpperCase()
}

export function classifyInvites(existing: readonly Member[], addresses: readonly string[]): InviteVerdict[] {
  return addresses.map((address): InviteVerdict => {
    if (!isValidEmail(address)) return 'malformed'
    // Lower-cased on BOTH sides: the stored spelling is whatever was typed when the row was
    // created, so a raw `===` would let the same person be invited twice. A null-email row
    // can never match a parsed address (`EMAIL_RE` rejects `''`), which is the right verdict.
    const match = existing.find((m) => (m.email ?? '').toLowerCase() === address.toLowerCase())
    if (!match) return 'ok'
    return match.status === 'invited' ? 'invited' : 'member'
  })
}

// Reducers. All three allocate UNCONDITIONALLY (`map` / `filter` / spread), so a miss returns
// a new array holding the same values rather than the input reference — that is the
// `replacePolicy`/`removePolicy` form (workflows.ts:396-402), picked over rules.ts:253-268's
// `return rules as CustomRule[]` because the two house precedents disagree and only the
// always-allocate one satisfies §15.1. Every reducer keys off `id`, never email.

/** The status write's list patch: the SERVER's row replaces the one it answered for. */
export function replaceMember(list: readonly Member[], next: Member): Member[] {
  return list.map((m) => (m.id === next.id ? next : m))
}

export function addMembers(list: readonly Member[], added: readonly Member[]): Member[] {
  return [...list, ...added]
}

export function removeMember(list: readonly Member[], id: string): Member[] {
  return list.filter((m) => m.id !== id)
}

// The ONE definition of "a filter is active". MembersView reads it to choose between the
// roster-of-one empty state and the filtered-to-zero row, and `filterMembers` short-circuits
// on it, so the two cannot disagree about what an empty query is. They did once, and the two
// empty surfaces shadowed each other.
export function isFiltering(query: string, roleFilter: AccessRole | 'all'): boolean {
  return query.trim() !== '' || roleFilter !== 'all'
}

/** Name OR email, case-insensitive substring. `roleFilter: 'all'` disables the role predicate. */
export function filterMembers(list: readonly Member[], query: string, roleFilter: AccessRole | 'all'): Member[] {
  // Not a fast path — this is the pin. Neither predicate live means nobody is excluded, and
  // saying so via `isFiltering` is what stops a caller re-deriving that rule. Copies, per §15.1.
  if (!isFiltering(query, roleFilter)) return list.slice()
  const q = query.trim().toLowerCase()
  return list.filter((m) => {
    if (roleFilter !== 'all' && m.role !== roleFilter) return false
    if (!q) return true
    // `q` is non-empty here, so a null email's `''` never matches.
    return m.name.toLowerCase().includes(q) || (m.email ?? '').toLowerCase().includes(q)
  })
}

/** §9 — true iff `member` is an active admin and they are the only one. */
export function isProtectedAdmin(list: readonly Member[], member: Member): boolean {
  if (member.role !== 'admin' || member.status !== 'active') return false
  return activeAdmins(list).length === 1
}

// ---------------------------------------------------------------------------
// The invite modal's copy and its client-picker derivations
// ---------------------------------------------------------------------------
// The modal that rendered these is gone with the unbacked invite flow; `ClientAccessPicker`
// still reads the picker half. Living here rather than in a component for §15.8's reason:
// vitest is `environment: node`, so a string authored inside a component is a string no
// spec can hold.
//
// The modal's own two title strings stay in the component, matching MATRIX_HEADING's posture
// (MemberRoleMatrix.tsx:19-22): §7 names them as the surface's chrome, not as its content.

/**
 * §7's three inline chip errors, keyed by the verdict that produces them. `ok` is excluded at
 * the TYPE level: a chip that classified `ok` has no error to render, and a `Record` over the
 * whole union would demand a fourth string that means nothing.
 *
 * `malformed` also carries the `hasDerivableName` downgrade. Both failures map to the same
 * string deliberately — to the person typing, "this address cannot become a member" is one
 * fact, and §7 supplies exactly three strings rather than a fourth for the rarer case.
 */
export const INVITE_ERROR: Record<Exclude<InviteVerdict, 'ok'>, string> = {
  member: 'Already a member',
  invited: 'Already invited',
  malformed: 'Not a valid email',
}

/**
 * The chip list's own de-duplication — §7's "an address already chipped does not chip twice",
 * and the duty NOTHING upstream performs. `parseEmailInput` de-dupes only WITHIN one paste
 * (T2.5) and `classifyInvites` has no batch memory at all (QA33), so the chip list is the only
 * place this state exists and this is the only function that enforces it.
 *
 * Keyed on `toLowerCase()`, keeping the FIRST spelling seen — the same key `parseEmailInput`
 * uses and the same case-insensitive comparison `classifyInvites` makes against stored rows. A
 * raw `===` would let `a@x.ng` and `A@x.ng` both chip and both classify `ok`, and the roster
 * would gain the same person twice.
 *
 * It lives here rather than in the modal against that subtask's own "everything else stays
 * component-side" rule, because that rule's stated reason — "its oracle is the deploy gate" —
 * is false for exactly this function: a lower-cased-key collision is invisible in a screenshot.
 * Same argument that pulled QA40-QA46's three derivations out of MEMB-01-04's components.
 */
export function mergeChips(current: readonly string[], added: readonly string[]): string[] {
  const seen = new Set(current.map((c) => c.toLowerCase()))
  const out = [...current]
  for (const address of added) {
    const key = address.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(address)
  }
  return out
}

/**
 * §7's zero-selected state. `Selected clients` with nothing ticked is an invite that grants
 * access to nothing, so `Send invites` is disabled and this says why — INVENTED COPY, since §7
 * specifies the behaviour and supplies no sentence for it.
 */
export const NO_CLIENTS_NOTE = 'Pick at least one client, or switch to All clients.'

/**
 * The client picker's filtered-to-zero line. Deliberately the roster search's sentence with one
 * noun changed (MembersView.tsx:141) — two search boxes on one tab that phrased "nothing
 * matched" differently would read as two different failures.
 */
export const NO_CLIENT_MATCH = 'No clients match this search.'

/**
 * The invite modal's client-picker search. Same rule as `filterMembers` — trimmed,
 * case-insensitive substring (QA36) — so the two search boxes this tab ships cannot disagree
 * about what a trailing space means.
 *
 * Narrows what is SHOWN and nothing else. The ticked set is the caller's state and this
 * function never sees it: a filter that unticked a hidden client would silently revoke access
 * the user had already granted, and the user would never see it happen.
 */
export function filterClientRoster(query: string): { id: number; name: string }[] {
  const q = query.trim().toLowerCase()
  // Copies rather than handing back the module constant, per §15.1 and this module's own
  // docblock: `CLIENT_ROSTER` is readonly at the type level only, nothing freezes it.
  if (!q) return CLIENT_ROSTER.slice()
  return CLIENT_ROSTER.filter((c) => c.name.toLowerCase().includes(q))
}

/**
 * §7's running count under the picker, in the shape the story writes it ("3 of 6 selected").
 *
 * The denominator is the ROSTER's length, never the filtered length: the sentence is about how
 * much access this invite grants, and a search box cannot change that. Typing "lagos" must not
 * make a 3-client invite read "1 of 1 selected".
 */
export function clientSelectionCount(selected: number): string {
  return `${selected} of ${CLIENT_ROSTER.length} selected`
}

/**
 * The flash MembersView raises after a successful send. INVENTED COPY — §7 says the modal
 * closes and says nothing about what confirms it — so it is pinned here rather than left in a
 * component, the same shape and the same reason as `unassignedNotice`'s singular.
 *
 * It exists because every other action on this tab already flashes (MembersTable's Resend
 * invite / Copy invite link), and the one action that actually changes the roster must not be
 * the silent one.
 */
export function invitedNotice(count: number): string {
  return `Invited ${count} ${count === 1 ? 'person' : 'people'}.`
}

// ---------------------------------------------------------------------------
// MEMB-01-07 — the member drawer's copy, and the three facts it must not derive
// ---------------------------------------------------------------------------
// §8's danger-zone copy is the single most important text in this story, and none of it
// existed in the repo before this subtask. It lives here for §15.8's reason and for no
// other: vitest is `environment: node`, so a sentence authored inside `MemberDrawer.tsx`
// is a sentence no spec can hold, and the drawer's oracle is a screenshot — which is
// exactly the gate a fluent paraphrase walks straight through. Same argument that moved
// CAPABILITY_FOOTNOTE (T5.1), NO_CLIENTS_NOTE (T6.12) and QA40-QA46's three display
// derivations into this module.
//
// Every string below was byte-checked against §8/§9 in the vault, not against the
// subtask description that quotes them.

/**
 * What suspension actually does. It no longer claims to block sign-in — the membership
 * endpoint writes a status column and nothing in the auth path reads it yet.
 *
 * Rendered under the suspend control in BOTH of its states: it describes the STATE, not the
 * direction of travel, so it is equally true beside `Reactivate`. The closing clause is
 * shared byte-for-byte with `REMOVE_EXPLANATION`.
 */
export const SUSPEND_EXPLANATION =
  'Removes their approver rights and keeps all history. Sign-in is not blocked yet. Their name stays on every invoice they touched.'

/**
 * §8's `Remove` explanation, verbatim.
 *
 * The semicolon is load-bearing: the second clause is what makes the first survivable, and
 * splitting it into two sentences (or dropping "audit history is never rewritten") is the
 * paraphrase this constant exists to prevent.
 */
export const REMOVE_EXPLANATION =
  'Revokes access permanently. Their name stays on every invoice they touched; audit history is never rewritten.'

/**
 * §8's confirm sentence. A FUNCTION, not a constant — §8 writes it with the member's name
 * interpolated ("Remove {name}? …"), so there is no name-free form of this string.
 */
export function removeConfirmQuestion(name: string): string {
  return `Remove ${name}? Access is revoked immediately. Their name stays on every invoice they touched.`
}

/**
 * §9's explanation, and the tab's most-repeated sentence: the `⋯` menu, the drawer's role
 * cards and the drawer's danger zone all carry it.
 *
 * MOVED here from `MembersTable.tsx:91`, where it was a component-local constant no spec
 * could see. Four call sites across two components is exactly the divergence QA41 caught
 * for `accessRoleLabel` — one copy edited, the other left behind, and a screenshot of
 * either one looking right.
 */
export const PROTECTED_ADMIN_NOTE = "You're the only admin. Promote someone else first."

/**
 * §7's zero-selected rule, as a predicate over the value the client picker EMITS.
 *
 * `'all' | number[]` is a discriminated union, not a convention: `'all'` IS scope-all and an
 * array IS scope-selected. `ClientAccessPicker` reads it to show `NO_CLIENTS_NOTE`.
 *
 * `[]` is representable and meaningful — it is simply not grantable.
 */
export function needsClientPick(access: 'all' | readonly number[]): boolean {
  return access !== 'all' && access.length === 0
}

// ---------------------------------------------------------------------------
// The live wire, the projection and the honest-absence vocabulary
// ---------------------------------------------------------------------------

/** One row of GET/PATCH .../memberships (internal/tenancy/tenancy.go). */
export type MembershipWire = {
  user_id: string
  role: string
  status: string
  display_name: string | null
  email: string | null
}

// The envelope is unwrapped here: it carries exactly one key and no pagination, so unlike
// `listInvoices` there is nothing in it to lose. No try/catch anywhere below — `ApiError`
// propagates unreshaped so the view can render the server's own reason. Neither function is
// ever called with a null base; the caller short-circuits (invoices.ts:1167-1174).
export async function listMembers(f: AuthedFetch, base: string): Promise<MembershipWire[]> {
  const body = await f<{ memberships: MembershipWire[] }>(`${base}/api/tenancy/v1/memberships`)
  return body.memberships
}

/** `invited` is a column value, not a PATCH target — excluded at the type level, not by a 400. */
export function setMembershipStatus(
  f: AuthedFetch,
  base: string,
  userId: string,
  status: Exclude<MemberStatus, 'invited'>,
): Promise<MembershipWire> {
  return f<MembershipWire>(`${base}/api/tenancy/v1/memberships/${userId}`, {
    method: 'PATCH',
    body: { status },
  })
}

/**
 * Sets exactly seven keys and never a mock-only one — a row the server did not state is a
 * row this projection does not invent.
 *
 * `role` is VERBATIM: never re-cased, never defaulted. The cast admits that the server's
 * vocabulary is wider than the union, and `accessRoleLabel`'s `?? role` fallback renders an
 * unknown value rather than crashing. Defaulting would silently grant or remove power, and
 * would empty the delegate picker `delegateCandidates` feeds.
 */
export function toMember(w: MembershipWire, selfSubject: string): Member {
  return {
    id: w.user_id,
    name: w.display_name ?? w.email ?? w.user_id,
    initials: memberInitials(w.display_name, w.email, w.user_id),
    email: w.email,
    role: w.role as AccessRole,
    status: w.status as MemberStatus,
    isYou: w.user_id === selfSubject,
  }
}

/** Composes the two existing helpers rather than adding a third initials variant. */
export function memberInitials(displayName: string | null, email: string | null, userId: string): string {
  if (displayName) return initials(displayName)
  if (email) return initialsFrom(email)
  return userId.slice(0, 2).toUpperCase()
}

/** A membership row can carry no address; a bare `{m.email}` would render nothing at all. */
export function emailLabel(member: Member): string {
  return member.email ?? ABSENT_LABEL
}

/** Null base is `idle`; every other status passes through, so an error can never read as empty. */
export function membersViewState(base: string | null, status: AsyncStatus): AsyncStatus {
  return base == null ? 'idle' : status
}

/** The four surfaces a roster status can take. `idle` and `empty` share one. */
export type MembersSurface = 'loading' | 'error' | 'empty' | 'roster'

/**
 * Which surface a status renders. Shared by the Members and Roles tabs so the two rosters
 * of one tenant cannot disagree on the same screen — and so no caller re-derives "ready"
 * from `members.length`, which reads an errored fetch as an empty workspace.
 */
export function membersSurface(status: AsyncStatus): MembersSurface {
  if (status === 'loading') return 'loading'
  if (status === 'error') return 'error'
  if (status === 'idle' || status === 'empty') return 'empty'
  return 'roster'
}

/** Absence, rendered. ONE em dash for the whole tab, so no two cells can disagree. */
export const ABSENT_LABEL = '—'

/**
 * One sentence per control the roster still offers but no endpoint backs. Plain and
 * server-stated: what is missing, not an apology or a promise.
 */
export const MEMBER_UNBACKED: Record<'invite' | 'remove' | 'role' | 'department' | 'clientAccess', string> = {
  invite: 'There is no invite endpoint yet — nothing mints a token, tracks an expiry, or sends the email.',
  remove: 'Deleting a membership locks that person out on their next request, and nothing undoes it. That decision has not been taken.',
  role: 'The membership endpoint writes status only. Changing someone\'s access role has no server call behind it.',
  department: 'A membership stores a name, an email, an access role and a status. There is no department column.',
  clientAccess: 'Client access is not stored per person — everyone in this workspace sees the same clients.',
}
