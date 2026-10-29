// What the privacy page must describe, read from the Go and TS sources that do it.
// ceiling: the CI frontend filter skips Go-only PRs, so these readers run with a frontend change only; add internal/** and migrations/** to the filter if a Go-only PR falsifies a sentence
// A parse miss throws, so a moved fact fails the test instead of emptying the oracle.
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { stripComments } from '@invoice-os/api-client/strip-comments'
import { APP_LABELS, BACKUP_DAYS, DELETED_MARK, DELETION_GRACE_DAYS, EXPORT_LABELS, FILE_RECOVERY_DAYS, PRIVACY_CONTACT } from './Privacy'

const REPO = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..', '..', '..')

export function readSource(rel: string): string {
  return stripComments(readFileSync(join(REPO, rel), 'utf8'))
}

function found<T>(value: T | null | undefined, what: string): T {
  if (value == null) throw new Error(`privacyCodeFacts: ${what} not found`)
  return value
}

const unique = (xs: string[]): string[] => Array.from(new Set(xs)).sort()
const keysOf = (re: RegExp, text: string): string[] => Array.from(text.matchAll(re), (m) => m[1])

// Properties internal/notifications/hubspot.go sends for a registrant or demo booker.
export function hubspotCrmKeys(): string[] {
  const body = found(/func \(h \*HubSpot\) Upsert\b[\s\S]*?\n}\n/.exec(readSource('internal/notifications/hubspot.go'))?.[0], 'HubSpot.Upsert')
  const literal = found(/range map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the Upsert property literal')
  return unique([...keysOf(/"([a-z_]+)"\s*:/g, literal), ...keysOf(/props\["([a-z_]+)"\]\s*=/g, body)])
}

// Properties the browser's HubSpot Forms post sends for a demo booker (hubspot.ts buildSubmission).
export function hubspotFormsKeys(): string[] {
  const list = found(/const mapped[^=]*=\s*\[([\s\S]*?)\n\s*\]\n/.exec(readSource('frontend/landing/src/hubspot.ts'))?.[1], 'the mapped list')
  return unique(keysOf(/\[\s*'([a-z_]+)'\s*,/g, list))
}

// Fields internal/notifications/resend.go sends when it creates a contact.
export function resendContactKeys(): string[] {
  const body = found(/func \(r \*Resend\) Sync\b[\s\S]*?\n}\n/.exec(readSource('internal/notifications/resend.go'))?.[0], 'Resend.Sync')
  const literal = found(/body\s*:=\s*map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the Resend create body')
  return unique([...keysOf(/"([a-z_]+)"\s*:/g, literal), ...keysOf(/body\["([a-z_]+)"\]\s*=/g, body)])
}

// Tag values worker.go gives a contact, in source order.
export function crmTags(): string[] {
  const tags = keysOf(/c\.Tags = append\(c\.Tags, "([^"]+)"\)/g, readSource('internal/notifications/worker.go'))
  if (tags.length === 0) throw new Error('privacyCodeFacts: no tag assignment in worker.go')
  return tags
}

// worker.go skips Resend for anyone neither registered nor ticked.
export function resendSkipsUntickedDemoBooker(): boolean {
  return /if !registered && !consent \{\s*return nil/.test(readSource('internal/notifications/worker.go'))
}

// The region deploy-prod.yml deploys production to.
export function productionRegion(): string {
  return found(/^\s+REGION: (\S+)$/m.exec(readFileSync(join(REPO, '.github/workflows/deploy-prod.yml'), 'utf8'))?.[1], 'REGION in deploy-prod.yml')
}

// GoTrue sends the verification email through this SMTP host (sidecar/auth/Dockerfile).
export function authSmtpHost(): string {
  return found(/GOTRUE_SMTP_HOST=(\S+)/.exec(readFileSync(join(REPO, 'sidecar/auth/Dockerfile'), 'utf8'))?.[1], 'GOTRUE_SMTP_HOST')
}

// True when a landing client sends a consent time of its own; the page may say "ticked" only then.
export function clientsSendConsentTime(): boolean {
  return ['frontend/landing/src/register.ts', 'frontend/landing/src/demoRequest.ts'].some((f) =>
    /marketing_consent_(?!text)\w+|consent(?:ed)?_at|ticked_at/.test(readSource(f)),
  )
}

// The gateway stamps the registration consent time itself, at the register request.
export function gatewayStampsRegisterConsentTime(): boolean {
  return /"at":\s*time\.Now\(\)/.test(readSource('internal/gateway/register.go'))
}

// The demo form awaits the HubSpot Forms post, then the gateway post: the server send follows the browser's.
export function demoPostsToOurServerAfterHubSpot(): boolean {
  return /await trackedHubSpotSubmit[\s\S]*?await sendDemoRequest/.test(readSource('frontend/landing/src/components/DemoLeadForm.tsx'))
}

// notifications queues each delivery as a River job in the intake transaction, so it can be late and is retried.
export function deliveryIsQueued(): boolean {
  return /river\.InsertTx\(/.test(readSource('internal/notifications/store.go'))
}

// A registrant's HubSpot company is the workspace name, not a company field (store.go Registrant).
export function registrantCompanyIsWorkspaceName(): boolean {
  return /company:\s*strings\.TrimSpace\(in\.WorkspaceName\)/.test(readSource('internal/notifications/store.go'))
}

const AI_CLIENTS = ['internal/platform/ai/client.go', 'internal/platform/jev/client.go']

// The host of each OpenRouter `endpoint` constant, de-duplicated.
export function aiProviderHosts(): string[] {
  return unique(AI_CLIENTS.map((f) => found(/\bendpoint\s*=\s*"https:\/\/([^\/"]+)/.exec(readSource(f))?.[1], `endpoint in ${f}`)))
}

// The vendor prefix of each client's `Model` constant (`~` dropped), in client order.
export function aiModelVendors(): string[] {
  return AI_CLIENTS.map((f) => found(/\bModel\s*=\s*"~?([^\/"]+)\//.exec(readSource(f))?.[1], `Model in ${f}`))
}

// Non-test newsletter*.go files of internal/notifications, comments stripped, joined.
export function newsletterGoFiles(): string[] {
  const files = readdirSync(join(REPO, 'internal/notifications')).filter((f) => /^newsletter.*\.go$/.test(f) && !f.endsWith('_test.go'))
  if (files.length === 0) throw new Error('privacyCodeFacts: no newsletter*.go found')
  return files.sort()
}
function newsletterGo(): string {
  return newsletterGoFiles().map((f) => readSource(`internal/notifications/${f}`)).join('\n')
}

// Hours a pending blog request lives (newsletter.go Subscribe).
export function newsletterPendingHours(): number {
  return Number(found(/INSERT INTO blog_subscription_requests[\s\S]*?expires_at\)[\s\S]*?now\(\) \+ interval '(\d+) hours'/.exec(newsletterGo())?.[1], 'the request expiry interval'))
}

// The SubscribeIntake fields.
export function newsletterIntakeFields(): string[] {
  const body = found(/type SubscribeIntake struct \{([^}]*)\}/.exec(newsletterGo())?.[1], 'SubscribeIntake')
  return keysOf(/^\s*(\w+)\s+\S+\s*$/gm, body).sort()
}

// The Resend contact property keys SyncBlog writes.
export function newsletterResendPropertyKeys(): string[] {
  const body = found(/func \(r \*BlogResend\) SyncBlog\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'BlogResend.SyncBlog')
  const literal = found(/props\s*:=\s*map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the SyncBlog property literal')
  return unique(keysOf(/"([a-z_]+)"\s*:/g, literal))
}

// SyncBlog adds the contact to the Blog segment, opts it in to the Blog topic, and sends neither unsubscribe nor opt_out.
export function syncBlogAddsSegmentAndTopic(): boolean {
  const body = found(/func \(r \*BlogResend\) SyncBlog\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'BlogResend.SyncBlog')
  return /"\/segments\/"\+url\.PathEscape\(r\.keys\.SegmentID\)/.test(body) && /"id": r\.keys\.TopicID, "subscription": "opt_in"/.test(body)
}

// Confirm consumes the request once, only while unexpired, and answers ErrLinkInvalid otherwise.
export function confirmLinkIsSingleUseAndExpires(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Confirm\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Confirm')
  return /DELETE FROM blog_subscription_requests WHERE token_hash = \$1 AND expires_at > now\(\)/.test(body) && /ErrNoRows\) \{\s*return "", ErrLinkInvalid/.test(body)
}

// A second sign-up overwrites topics and frequency and replaces the earlier token (one pending row per email).
export function resubscribeReplacesRequest(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Subscribe\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Subscribe')
  return /ON CONFLICT \(email\) DO UPDATE SET\s*category_ids = EXCLUDED\.category_ids, frequency = EXCLUDED\.frequency, token_hash = EXCLUDED\.token_hash/.test(body)
}

// Confirm overwrites a known subscriber's topics and frequency.
export function confirmOverwritesSubscriber(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Confirm\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Confirm')
  return /ON CONFLICT \(email\) DO UPDATE SET\s*category_ids = EXCLUDED\.category_ids, frequency = EXCLUDED\.frequency/.test(body)
}

// Confirm stores the confirmation time on the subscriber row.
export function confirmStoresConfirmedAt(): boolean {
  return /INSERT INTO blog_subscribers \([^)]*confirmed_at\)\s*VALUES \([^)]*now\(\)\)/.test(newsletterGo())
}

// The confirmation mail goes through the accountmail Sender, which the Resend sender implements against api.resend.com.
export function confirmationMailGoesThroughResend(): boolean {
  const handler = newsletterGo()
  const sender = readSource('internal/accountmail/sender.go')
  return (
    /Sender\s+accountmail\.Sender/.test(handler) &&
    /RenderBlogConfirm\(/.test(handler) &&
    /n\.Sender\.Send\(/.test(handler) &&
    /ResendBaseURL\s*=\s*"https:\/\/api\.resend\.com"/.test(sender) &&
    /func \(r \*Resend\) Send\(/.test(sender)
  )
}

// True when a non-test newsletter*.go file names HubSpot (worker.go is the control that does).
export function newsletterCodeNamesHubSpot(): boolean {
  return /hubspot/i.test(newsletterGo())
}
export function workerNamesHubSpot(): boolean {
  return /hubspot/i.test(readSource('internal/notifications/worker.go'))
}

// contacts.md tells staff to put the unsubscribe merge tag in every blog broadcast.
export function rulePutsUnsubscribeInEveryBroadcast(): boolean {
  return readFileSync(join(REPO, '.claude/rules/contacts.md'), 'utf8').includes('Put `{{{RESEND_UNSUBSCRIBE_URL}}}` in every blog broadcast.')
}

// The blog confirm page tells the subscriber every email has a one-click unsubscribe.
export function confirmPageSaysOneClickUnsubscribe(): boolean {
  return readFileSync(join(REPO, 'frontend/blog/src/components/Confirm.tsx'), 'utf8').includes('Every email has a one-click unsubscribe.')
}

// The pinned Blog chat paragraph; Privacy.render.test.tsx pins it in place, Privacy.claims.test.tsx reads its HubSpot sentence.
export const BLOG_CHAT_PARAGRAPH =
  "If you have allowed analytics, the Blog shows HubSpot's live chat, which loads only after you choose Accept, sends HubSpot, about its own pages, what is described under Live chat below, and is removed, with HubSpot's cookies, when you choose Reject on any of our sites."

// The five labels of APP_LABELS, read from the app files that render them.
export function appLabels(
  src: { account: string; workspace: string; data: string; glyphs: string } = {
    account: readSource('frontend/app/src/lib/accountDeletion.ts'),
    workspace: readSource('frontend/app/src/lib/workspaceDeletion.ts'),
    data: readSource('frontend/app/src/data.tsx'),
    glyphs: readSource('frontend/app/src/glyphs.tsx'),
  },
): typeof APP_LABELS {
  return {
    page: found(/ACCOUNT_PAGE\s*=\s*\{\s*title:\s*'([^']+)'/.exec(src.account)?.[1], 'ACCOUNT_PAGE title') as 'Your account',
    deleteAccount: found(/DELETE_ACCOUNT\s*=\s*\{\s*title:\s*'([^']+)'/.exec(src.account)?.[1], 'DELETE_ACCOUNT title') as 'Delete my account',
    settings: found(/NAV_SETTINGS\b[^=]*=\s*\{\s*id:\s*'settings',\s*label:\s*'([^']+)'/.exec(src.glyphs)?.[1], 'NAV_SETTINGS label') as 'Settings',
    workspaceTab: found(/\{\s*id:\s*'workspace',\s*label:\s*'([^']+)'/.exec(src.data)?.[1], 'the workspace settings tab') as 'Workspace',
    deleteWorkspace: found(/DELETE_WORKSPACE\s*=\s*\{[^}]*?button:\s*'([^']+)'/.exec(src.workspace)?.[1], 'DELETE_WORKSPACE button') as 'Delete workspace',
  }
}

// The sidebar name card opens the account page and is labelled with it.
export function sidebarOpensAccountPage(
  src: string = readSource('frontend/app/src/components/Sidebar.tsx'),
): boolean {
  const at = src.indexOf('data-testid="identity-card-account"')
  const card = at < 0 ? '' : src.slice(src.lastIndexOf('<button', at), src.indexOf('style=', at))
  return card.includes("ctx.nav('account')") && card.includes(`aria-label="${APP_LABELS.page}"`)
}

// Sentences the page states about deletion; Privacy.render.test.tsx pins each verbatim on the page.
export const DELETION_SENTENCES: readonly { id: string; text: string }[] = [
  { id: 'W1', text: `Email ${PRIVACY_CONTACT} and tell us what you want done, and we will act on it. That works also if you cannot sign in to an account, or never had one.` },
  { id: 'W2', text: `If you can sign in, you can delete your account yourself: open ${APP_LABELS.page}, from your name at the foot of the sidebar, and choose ${APP_LABELS.deleteAccount}.` },
  { id: 'W3', text: 'If you have registered or booked a demo, the records we hold are in HubSpot, in Resend and on our own server, and we can delete them.' },
  { id: 'A1', text: `You can delete your account yourself. Open ${APP_LABELS.page}, from your name at the foot of the sidebar, and choose ${APP_LABELS.deleteAccount}.` },
  { id: 'A2', text: 'If another member is still active in your workspace, your place in it ends at once and we queue the erasure of your records.' },
  { id: 'A3', text: 'The erasure deletes your sign-in account. Our own server also asks HubSpot to delete your contact permanently, together with the deals filed under it, and asks Resend to delete your contact. It deletes the copies of your registration and demo details that our own server holds under your email address, and it ends your subscription to the Blog newsletter if that address has one.' },
  { id: 'A4', text: `Your name stays in the audit log and in the history of each invoice of your workspace, the record of who did what, with ${DELETED_MARK} after it. If we never held a name for you, the history says Deleted member. Your membership record keeps your name and drops your email address.` },
  { id: 'A5', text: `An admin can delete the whole workspace. In ${APP_LABELS.settings}, under ${APP_LABELS.workspaceTab}, the admin chooses ${APP_LABELS.deleteWorkspace} and types the workspace name to confirm.` },
  { id: 'E1', text: `In the dialog that opens, ${EXPORT_LABELS.button} starts the download of one ZIP file, and the admin ticks “${EXPORT_LABELS.tick}” before the deletion can be confirmed. The ZIP holds the evidence bundle of every company in the workspace over all time, every original uploaded document and the audit log. While the workspace is locked, the notice offers the admin ${EXPORT_LABELS.button} again.` },
  { id: 'A6', text: `The workspace is then locked for ${DELETION_GRACE_DAYS} days. Nobody can create or change anything in it. While it is locked, a member cannot delete their own account. Members see the date it will be deleted and can sign out. Any admin can cancel the deletion until the ${DELETION_GRACE_DAYS} days end. Then our server deletes every invoice, file and member of the workspace, and erases the records of each member still in it, as described above.` },
  { id: 'A7', text: `If you are the only active member of a workspace, deleting your account deletes the workspace instead. Your account then goes when the workspace is deleted, ${DELETION_GRACE_DAYS} days later, not at once.` },
  { id: 'R1', text: 'This section covers the main copies in our own systems. It says how long they last after an account or a workspace is deleted.' },
  { id: 'R2', text: `When an admin deletes a workspace, it and everything in it stay for ${DELETION_GRACE_DAYS} days. Then our server deletes them.` },
  { id: 'R3', text: `Database backups: our database keeps backups, and a log of recent changes, for ${BACKUP_DAYS} days. Data that has been deleted can stay in a backup for up to ${BACKUP_DAYS} more days.` },
  { id: 'R4', text: `Stored files: the storage that holds uploaded documents keeps a deleted file for ${FILE_RECOVERY_DAYS} days before it is gone for good. A file can stay in storage for up to ${FILE_RECOVERY_DAYS} more days after it is deleted.` },
  { id: 'R5', text: 'Sign-in record: our sign-in service writes a record when an account is deleted. The record includes the email address of the account. We have set no time after which it is removed.' },
  { id: 'R7', text: 'Staff log: our staff action log keeps one entry for the deletion of a workspace: the workspace identifier, the date the deletion was due, and the number of members and files removed. That entry is not deleted.' },
]

// The body of one Go func, from its signature to the closing brace in column 0.
const goFunc = (src: string, signature: RegExp, what: string): string =>
  found(new RegExp(`${signature.source}[\\s\\S]*?\\n}\\n`).exec(src)?.[0], what)

// The erase calls Account.DeleteUser; DeleteUser sends a DELETE with no body, so no soft-delete flag.
export function eraseDeletesSigninAccount(
  erase: string = readSource('internal/notifications/erase.go'),
  admin: string = readSource('internal/platform/auth/gotrueadmin/gotrueadmin.go'),
): boolean {
  const del = goFunc(admin, /func \(c \*Client\) DeleteUser\b/, 'gotrueadmin DeleteUser')
  return erase.includes('w.Account.DeleteUser(ctx, uid)') && /c\.call\(ctx, http\.MethodDelete, userID\)/.test(del) && !/soft/i.test(del)
}

// The HubSpot step reads the contact's deals, deletes each, then POSTs gdpr-delete.
export function eraseAsksHubSpotToDeleteContactAndDeals(
  src: string = readSource('internal/notifications/vendor_erase.go'),
): boolean {
  const contact = goFunc(src, /func \(h \*HubSpot\) EraseContact\b/, 'HubSpot.EraseContact')
  const deal = goFunc(src, /func \(h \*HubSpot\) eraseDeal\b/, 'HubSpot.eraseDeal')
  return (
    contact.includes('/associations/deals') &&
    contact.includes('h.eraseDeal(') &&
    /http\.MethodPost, h\.baseURL\+hubspotContacts\+"\/gdpr-delete"/.test(contact) &&
    deal.includes('http.MethodDelete, dealURL')
  )
}

export function eraseAsksResendToDeleteContact(
  src: string = readSource('internal/notifications/vendor_erase.go'),
): boolean {
  return /http\.MethodDelete, r\.baseURL\+"\/contacts\/"/.test(goFunc(src, /func \(r \*Resend\) EraseContact\b/, 'Resend.EraseContact'))
}

const sweepDeletes = (src: string): string => goFunc(src, /func \(w \*EraseWorker\) sweep\b/, 'EraseWorker.sweep')

export function eraseDeletesBlogSubscription(src: string = readSource('internal/notifications/erase.go')): boolean {
  const sweep = sweepDeletes(src)
  return sweep.includes('DELETE FROM blog_subscribers WHERE email = $1') && sweep.includes('DELETE FROM blog_subscription_requests WHERE email = $1')
}

export function eraseDeletesRegistrationCopies(src: string = readSource('internal/notifications/erase.go')): boolean {
  const sweep = sweepDeletes(src)
  return sweep.includes('DELETE FROM registrant_handoffs WHERE') && sweep.includes('DELETE FROM contacts WHERE email = $1')
}

const removedName = (src: string): string => goFunc(src, /func RemovedName\b/, 'actor.RemovedName')

// The suffix RemovedName appends to a stored name.
export function deletedMark(src: string = readSource('internal/actor/actor.go')): string {
  return found(/\+ " (\(deleted\))"/.exec(removedName(src))?.[1], 'the RemovedName suffix')
}

// The text RemovedName gives a member with no stored name.
export function deletedFallback(src: string = readSource('internal/actor/actor.go')): string {
  return found(/Label\{Text: "([^"]+)", Kind/.exec(removedName(src))?.[1], 'the RemovedName fallback')
}

// Resolve labels a removed membership with RemovedName and every other with Name.
export function resolveMarksRemovedMembers(src: string = readSource('internal/actor/resolve.go')): boolean {
  return /if status == "removed" \{\s*out\[subject\] = RemovedName\(displayName\)\s*continue\s*\}\s*out\[subject\] = Name\(/.test(src)
}

export function endMembershipDropsEmail(src: string = readSource('internal/tenancy/store.go')): boolean {
  return /UPDATE memberships SET status = 'removed', email = NULL\b/.test(src)
}

export function onlyActiveMemberDeletesWorkspace(src: string = readSource('internal/tenancy/account_deletion.go')): boolean {
  return /case heir == nil:\s*return OutcomeDeleteWorkspace/.test(src)
}

// The purge reads every non-removed member, then queues an erasure for each.
export function purgeErasesEveryNonRemovedMember(src: string = readSource('internal/workspacepurge/purge.go')): boolean {
  const body = goFunc(src, /func \(p \*Purger\) eraseMembers\b/, 'Purger.eraseMembers')
  const read = body.indexOf("FROM memberships WHERE status <> 'removed'")
  return read > -1 && body.indexOf('notifications.EnqueueErasure(', read) > read
}

// The purge deletes the tenant's files before it purges the rows.
export function purgeDeletesFilesThenRows(src: string = readSource('internal/workspacepurge/purge.go')): boolean {
  const files = src.indexOf('document.DeleteTenantObjects(')
  const rows = src.indexOf('p.purgeRows(')
  return files > -1 && rows > files && src.includes('public.purge_tenant(')
}

// A write in a locked workspace gets ErrWorkspaceLocked; a read-only transaction passes.
export function lockRefusesWrites(src: string = readSource('internal/platform/db/tenant.go')): boolean {
  return /case locked && opts\.AccessMode != pgx\.ReadOnly:\s*return ErrWorkspaceLocked/.test(src)
}

// The body both cancel routes, the button and the emailed link, run.
function cancelBody(src: string): string {
  const button = goFunc(src, /func \(s \*Store\) CancelWorkspaceDeletion\b/, 'Store.CancelWorkspaceDeletion')
  const link = goFunc(src, /func \(s \*Store\) CancelWorkspaceDeletionByLink\b/, 'Store.CancelWorkspaceDeletionByLink')
  const body = goFunc(src, /func \(s \*Store\) cancelScheduledDeletion\b/, 'Store.cancelScheduledDeletion')
  const delegates = button.includes('return s.cancelScheduledDeletion(ctx, nil)') && link.includes('return s.cancelScheduledDeletion(ctx, &token)')
  return delegates ? body : ''
}

// Cancel gates on the admin role and never on who scheduled the deletion.
export function anyAdminCancels(src: string = readSource('internal/tenancy/workspace_deletion.go')): boolean {
  const cancel = cancelBody(src)
  return cancel.includes('role != "admin"') && !cancel.includes('deletion_scheduled_by')
}

// DeleteMyAccount runs in a read-write request transaction, which a locked workspace refuses.
export function deleteAccountRefusedWhileLocked(
  src: string = readSource('internal/tenancy/account_deletion.go'),
  tenant: string = readSource('internal/platform/db/tenant.go'),
  mapper: string = readSource('internal/tenancy/tenancy.go'),
): boolean {
  const body = goFunc(src, /func \(s \*Store\) DeleteMyAccount\b/, 'Store.DeleteMyAccount')
  return (
    body.includes('db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {') &&
    !body.includes('ReadOnly') &&
    lockRefusesWrites(tenant) &&
    /errors\.Is\(err, db\.ErrWorkspaceLocked\):\s*return http\.StatusForbidden/.test(mapper)
  )
}

// DeleteMyAccount ends the membership and queues the erasure through the same tx, inside one tenant transaction.
export function deleteAccountEndsMembershipAndQueuesErasureInOneTx(
  src: string = readSource('internal/tenancy/account_deletion.go'),
): boolean {
  const body = goFunc(src, /func \(s \*Store\) DeleteMyAccount\b/, 'Store.DeleteMyAccount')
  const open = body.indexOf('db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {')
  const end = body.indexOf('endMembership(ctx, tx, caller.Subject)')
  const enqueue = body.indexOf('notifications.EnqueueErasure(ctx, tx,')
  return open > -1 && end > open && enqueue > end
}

// The JSX opening tag that carries the confirm testid: from its "<" to the first ">" outside braces.
function confirmTag(dialog: string): string {
  const at = dialog.indexOf('data-testid="delete-workspace-confirm"')
  if (at < 0) return ''
  const start = dialog.lastIndexOf('<', at)
  let depth = 0
  for (let i = start + 1; i < dialog.length; i++) {
    if (dialog[i] === '{') depth++
    else if (dialog[i] === '}') depth--
    else if (dialog[i] === '>' && depth === 0) return dialog.slice(start, i + 1)
  }
  return ''
}

// The Confirm button of the delete-workspace dialog is disabled until the typed text equals the name.
export function deleteWorkspaceDialogNeedsTypedName(
  dialog: string = readSource('frontend/app/src/components/DeleteWorkspaceDialog.tsx'),
  lib: string = readSource('frontend/app/src/lib/workspaceDeletion.ts'),
): boolean {
  const match = found(/export function nameMatches\b[^{]*\{([\s\S]*?)\n\}/.exec(lib)?.[1], 'nameMatches')
  return (
    /return typed === name\b/.test(match) &&
    /canConfirm = exported && nameMatches\(typed, name\)/.test(dialog) &&
    /data-testid="delete-workspace-confirm"/.test(dialog) &&
    confirmTag(dialog).includes('disabled={!canConfirm}')
  )
}

// The lock notice shows the due date and a Sign out button that no admin gate wraps.
export function deletionNoticeShowsDateAndSignOut(
  notice: string = readSource('frontend/app/src/components/WorkspaceDeletionNotice.tsx'),
  lib: string = readSource('frontend/app/src/lib/workspaceDeletion.ts'),
): boolean {
  const adminOnly = found(/\{isAdmin && \([\s\S]*?\n\s*\)\}/.exec(notice)?.[0], 'the admin-only block')
  return (
    notice.includes('deletionNoticeBody(formatDueDate(dueAt)') &&
    /onClick=\{onSignOut\}[\s\S]*?\{DELETION_NOTICE\.signOut\}/.test(notice) &&
    !adminOnly.includes('onSignOut') &&
    /signOut: 'Sign out'/.test(lib)
  )
}

// The clear of a schedule maps the purge-started trigger error to ErrDeletionUnderway, which answers 409.
export function cancelRefusedOnceThePurgeStarted(
  src: string = readSource('internal/tenancy/workspace_deletion.go'),
  mapper: string = readSource('internal/tenancy/tenancy.go'),
  migrations: string[] = migrationUps(),
): boolean {
  const cancel = cancelBody(src)
  const clear = goFunc(src, /func clearDeletionSchedule\b/, 'clearDeletionSchedule')
  const guard = lastDefinition(migrations, /CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(?:public\.)?tenants_purge_guard\(\)[\s\S]*?\$\$[\s\S]*?\$\$/i, 'the tenants_purge_guard function')
  return (
    cancel.includes('clearDeletionSchedule(ctx, tx)') &&
    /ConstraintName == "tenant_deletion_underway"[\s\S]*?return ErrDeletionUnderway/.test(clear) &&
    /errors\.Is\(err, ErrDeletionUnderway\):\s*return http\.StatusConflict/.test(mapper) &&
    /OLD\.deletion_purge_started_at IS NOT NULL[\s\S]*?CONSTRAINT = 'tenant_deletion_underway'/.test(guard)
  )
}

// The text of the last migration that matches, so a later CREATE OR REPLACE wins.
function lastDefinition(migrations: string[], re: RegExp, what: string): string {
  let last: string | undefined
  for (const sql of migrations) last = re.exec(sqlCode(sql.split('-- +goose Down')[0]))?.[0] ?? last
  return found(last, what)
}

// The days the tenants_deletion_guard trigger adds to the schedule, per its last definition.
export function graceDays(migrations: string[] = migrationUps()): number {
  const fn = lastDefinition(migrations, /CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(?:public\.)?tenants_deletion_guard\(\)[\s\S]*?\$\$[\s\S]*?\$\$/i, 'the tenants_deletion_guard function')
  return Number(found(/deletion_scheduled_at \+ interval '(\d+) days'/.exec(fn)?.[1], 'the deletion interval'))
}

// The two labels of EXPORT_LABELS, read from the app's EXPORT_COPY.
export function exportLabels(src: string = readSource('frontend/app/src/lib/workspaceExport.ts')): typeof EXPORT_LABELS {
  const copy = found(/EXPORT_COPY\s*=\s*\{([\s\S]*?)\}\s*as const/.exec(src)?.[1], 'EXPORT_COPY')
  return {
    button: found(/button:\s*'([^']+)'/.exec(copy)?.[1], 'EXPORT_COPY button') as 'Download export',
    tick: found(/tick:\s*'([^']+)'/.exec(copy)?.[1], 'EXPORT_COPY tick') as 'I downloaded the export',
  }
}

// The entries the workspace ZIP gets, in write order, read from AssembleWorkspace.
export function exportZipEntries(src: string = readSource('internal/archive/workspace.go')): string[] {
  const body = found(/func \(s \*WorkspaceStore\) AssembleWorkspace\b[\s\S]*?\n}\n/.exec(src)?.[0], 'AssembleWorkspace')
  const hits = [/writeEntityStages\([^"\n]*"(companies\/)"/.exec(body)?.[1], /WriteBodyFrom\("(documents\/)"/.exec(src)?.[1], /streamCSV\("(audit_log\.csv)"/.exec(body)?.[1]]
  return hits.filter((x): x is string => x !== undefined)
}

// Every company, every document, no date window, one .zip.
export function exportCoversAllTime(src: string = readSource('internal/archive/workspace.go')): boolean {
  const body = found(/func \(s \*WorkspaceStore\) AssembleWorkspace\b[\s\S]*?\n}\n/.exec(src)?.[0], 'AssembleWorkspace')
  const firstDocs = found(/func selectDocumentPage\b[\s\S]*?cur == nil \{\s*rows, err = tx\.Query\(ctx, cols\+`([^`]*)`/.exec(src)?.[1], 'the first documents page')
  return (
    /var allTime = Request\{From: time\.Time\{\}, To: time\.Date\(9999,/.test(src) &&
    /req := allTime\b/.test(body) &&
    /SELECT id::text, name, tin FROM business_entities ORDER BY/.test(src) &&
    !/WHERE/.test(firstDocs) &&
    /\+ "\.zip"\)/.test(body)
  )
}

// The delete dialog's button starts the export and its confirm waits on the tick.
export function dialogStartsExportAndAsksTick(
  dialog: string = readSource('frontend/app/src/components/DeleteWorkspaceDialog.tsx'),
): boolean {
  const button = found(/<button[^>]*data-testid="delete-workspace-export"[^>]*>[\s\S]*?<\/button>/.exec(dialog)?.[0], 'the export button')
  const label = found(/<label[^>]*>[\s\S]*?<\/label>/.exec(dialog.slice(dialog.lastIndexOf('<label', dialog.indexOf('data-testid="delete-workspace-exported"'))))?.[0], 'the tick label')
  return (
    button.includes('onClick={exp.start}') &&
    button.includes('{EXPORT_COPY.button}') &&
    label.includes('type="checkbox"') &&
    label.includes('checked={exported}') &&
    label.includes('{EXPORT_COPY.tick}') &&
    /canConfirm = exported && nameMatches/.test(dialog)
  )
}

// The lock notice offers the export to admins only.
export function noticeOffersExportToAdmins(
  notice: string = readSource('frontend/app/src/components/WorkspaceDeletionNotice.tsx'),
): boolean {
  const adminBlock = /\{isAdmin && \(\s*<button\s+onClick=\{exp\.start\}[\s\S]*?<\/button>\s*\)\}/.exec(notice)?.[0] ?? ''
  return adminBlock.includes('{EXPORT_COPY.button}') && adminBlock.includes('data-testid="deletion-notice-export"')
}

// DEL-06 merged: the app has the export module. The page may mention the export only then.
export function workspaceExportIsMerged(): boolean {
  return existsSync(join(REPO, 'frontend/app/src/lib/workspaceExport.ts'))
}

// The GoTrue release the sidecar pins; R5 describes what that release's adminUserDelete writes.
export function gotrueVersion(
  src: string = readFileSync(join(REPO, 'sidecar/auth/Dockerfile'), 'utf8'),
): string {
  return found(/^FROM ghcr\.io\/supabase\/auth:v(\d+\.\d+\.\d+)@/m.exec(src)?.[1], 'the GoTrue tag')
}

const PRUNE_ROOTS = ['internal', 'cmd', 'migrations', 'sidecar', '.github', 'db', 'scripts', 'tools']
const SKIP_DIRS = new Set(['node_modules', '.venv', '__pycache__', '.git'])

function nonTestFiles(rel: string): string[] {
  return readdirSync(join(REPO, rel), { withFileTypes: true }).flatMap((e) => {
    const next = `${rel}/${e.name}`
    if (e.isDirectory()) return SKIP_DIRS.has(e.name) ? [] : nonTestFiles(next)
    return /_test\.go$|\.test\.[jt]sx?$/.test(e.name) ? [] : [next]
  })
}

// No non-test file in this repo names the table GoTrue writes the deletion record to, so nothing reads or prunes it.
export function signinScanFiles(): string[] {
  const roots = PRUNE_ROOTS.filter((r) => existsSync(join(REPO, r))).flatMap(nonTestFiles)
  const top = readdirSync(REPO).filter((f) => f === 'Makefile' || /^docker-compose.*\.ya?ml$/.test(f))
  return [...roots, ...top]
}

const hashCode = (t: string): string => t.replace(/^[ \t]*#.*$/gm, '').replace(/\s#.*$/gm, '')

function codeOf(file: string, text: string): string {
  if (/\.sql$/.test(file)) return sqlCode(text)
  if (/\.(?:sh|ya?ml|mk)$|(?:^|\/)(?:Makefile|Dockerfile[^/]*)$/.test(file)) return hashCode(text)
  if (/\.(?:go|[cm]?[jt]sx?)$/.test(file)) return stripComments(text)
  return text
}

// Pairs of [file, text]; a file with a NUL byte is binary and is skipped.
export function signinScanTexts(): [string, string][] {
  return signinScanFiles().flatMap((f): [string, string][] => {
    const buf = readFileSync(join(REPO, f))
    return buf.includes(0) ? [] : [[f, buf.toString('utf8')]]
  })
}

export function nothingPrunesSigninAuditLog(texts: Iterable<[string, string]> = signinScanTexts()): boolean {
  for (const [file, text] of texts) if (/audit_log_entries/i.test(codeOf(file, text))) return false
  return true
}

// The facts R7 states: the purge's staff row carries the event and exactly these payload keys.
export function staffLogEntryFacts(
  src: string = readSource('internal/workspacepurge/purge.go'),
): { event: string; keys: string[] } {
  const call = found(/audit\.RecordStaff\([\s\S]*?\n\t\t\}\)/.exec(src)?.[0], 'the RecordStaff call')
  const payload = found(/Payload:\s*map\[string\]any\{([^}]*)\}/.exec(call)?.[1], 'the staff payload')
  return {
    event: found(/Event:\s*"([^"]+)"/.exec(call)?.[1], 'the staff event'),
    keys: Array.from(payload.matchAll(/"(\w+)":/g), (m) => m[1]).sort(),
  }
}

// Every migration's Up section, in timestamp order.
export function migrationUps(): string[] {
  return readdirSync(join(REPO, 'migrations'))
    .filter((f) => f.endsWith('.sql'))
    .sort()
    .map((f) => readFileSync(join(REPO, 'migrations', f), 'utf8'))
}

const sqlCode = (sql: string): string => sql.replace(/\/\*[\s\S]*?\*\//g, '').replace(/--.*$/gm, '')

// After the last migration, the append-only function raises and both triggers refuse UPDATE, DELETE and TRUNCATE.
export function staffLogIsAppendOnly(migrations: string[] = migrationUps()): boolean {
  let raises = false
  let rowTrigger = false
  let truncTrigger = false
  for (const sql of migrations) {
    const up = sqlCode(sql.split('-- +goose Down')[0])
    if (!/staff_audit_log/.test(up)) continue
    const fn = /FUNCTION\s+(?:public\.)?staff_audit_log_append_only\(\)[\s\S]*?\$\$[\s\S]*?\$\$/.exec(up)?.[0]
    if (fn) raises = /RAISE EXCEPTION 'staff_audit_log is append-only/.test(fn)
    if (/DROP\s+FUNCTION\s+(?:IF EXISTS\s+)?(?:public\.)?staff_audit_log_append_only/i.test(up)) raises = false
    if (/BEFORE UPDATE OR DELETE ON public\.staff_audit_log[\s\S]*?EXECUTE FUNCTION public\.staff_audit_log_append_only\(\)/.test(up)) rowTrigger = true
    if (/BEFORE TRUNCATE ON public\.staff_audit_log[\s\S]*?EXECUTE FUNCTION public\.staff_audit_log_append_only\(\)/.test(up)) truncTrigger = true
    if (/DROP\s+TRIGGER\s+(?:IF EXISTS\s+)?staff_audit_log_no_update_delete\b/i.test(up)) rowTrigger = false
    if (/DROP\s+TRIGGER\s+(?:IF EXISTS\s+)?staff_audit_log_no_truncate\b/i.test(up)) truncTrigger = false
    if (/ALTER\s+TABLE\s+(?:ONLY\s+)?public\.staff_audit_log\s+DISABLE\s+TRIGGER/i.test(up)) rowTrigger = truncTrigger = false
    if (/DROP\s+TABLE\s+(?:IF EXISTS\s+)?(?:public\.)?staff_audit_log\b|ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?staff_audit_log\s+RENAME\s+TO/i.test(up)) raises = rowTrigger = truncTrigger = false
  }
  return raises && rowTrigger && truncTrigger
}

// Sentences the page states about sign-in records; Privacy.render.test.tsx pins each verbatim on the page.
export const ACCOUNT_RECORD_SENTENCES: readonly { id: string; text: string }[] = [
  { id: 'P1', text: 'Each time someone signs in to your account, fails to sign in with a wrong password, is blocked after too many attempts, signs out, or asks for or completes a password reset, our own server records it in the audit log of your workspace, with the IP address and the browser and operating system.' },
  { id: 'P2', text: "Your workspace's admins see these records for every member. Every other member sees only their own. ASComply's support staff can also read them." },
  { id: 'P3', text: 'These records stay with the audit log, which keeps every record until the workspace is deleted.' },
  { id: 'P4', text: 'Our own server also keeps the list of browser and operating-system pairs your account has signed in with. The first pair is recorded without an email. When you later sign in with a pair that is not on the list, we email you through Resend; that email carries the browser, the operating system, the IP address and the time. The erasure of your account deletes the list.' },
]

// The six account event names, sorted.
export function accountEventNames(src: string = readSource('internal/audit/account_events.go')): string[] {
  const list = found(/var AccountEvents = \[\]string\{([^}]*)\}/.exec(src)?.[1], 'AccountEvents')
  const names = unique(keysOf(/"(account\.[a-z_]+)"/g, list))
  if (names.length === 0) throw new Error('privacyCodeFacts: no account event names')
  return names
}

// Payload keys the audit job writes; empty unless Work records that payload.
export function accountEventPayloadKeys(src: string = readSource('internal/accountevents/audit_job.go')): string[] {
  const body = found(/type auditPayload struct \{([^}]*)\}/.exec(src)?.[1], 'auditPayload')
  const work = found(/func \(w \*AuditWorker\) Work\b[\s\S]*?\n}\n/.exec(src)?.[0], 'AuditWorker.Work')
  if (!/audit\.Record\(ctx, tx, a\.UserID, a\.Event, p\)/.test(work)) return []
  return unique(keysOf(/json:"([a-z_]+)"/g, body))
}

// In the reader's scope switch, a company set or a non-admin gets a self-bound account arm; an admin with no set adds none.
export function accountRowsAreSelfOnly(src: string = readSource('internal/audit/filter.go')): boolean {
  const sw = found(/switch \{\s*case f\.scope\.set:[\s\S]*?\n\t\}\n/.exec(src)?.[0], 'the scope switch')
  const setCase = found(/case f\.scope\.set:([\s\S]*?)case !f\.scope\.admin:/.exec(sw)?.[1], 'the set case')
  const nonAdmin = found(/case !f\.scope\.admin:([\s\S]*)$/.exec(sw)?.[1], 'the non-admin case')
  return (
    /names := bind\(AccountEvents\)/.test(setCase) &&
    /a\.event = ANY\("\+names\+"\) AND a\.actor = "\+bind\(f\.scope\.self\)/.test(setCase) &&
    /names := bind\(AccountEvents\)/.test(nonAdmin) &&
    /a\.event <> ALL\("\+names\+"\) OR a\.actor = "\+bind\(f\.scope\.self\)/.test(nonAdmin)
  )
}

// After the last migration, staff_reader can select audit_log.payload.
export function staffReadsAuditPayload(migrations: string[] = migrationUps()): boolean {
  let granted = false
  for (const sql of migrations) {
    const up = sqlCode(sql.split('-- +goose Down')[0])
    for (const m of up.matchAll(/\b(GRANT|REVOKE)\s+SELECT\s*\(([^)]*)\)\s+ON\s+(?:TABLE\s+)?(?:public\.)?audit_log\s+(?:TO|FROM)\s+staff_reader\b/gi)) {
      if (/\bpayload\b/.test(m[2])) granted = m[1].toUpperCase() === 'GRANT'
    }
  }
  return granted
}

// Every DELETE FROM or TRUNCATE of audit_log in migrations/, both goose sections, as [file, statement].
export function auditLogDeletes(
  files: [string, string][] = readdirSync(join(REPO, 'migrations'))
    .filter((f) => f.endsWith('.sql'))
    .sort()
    .map((f): [string, string] => [f, readFileSync(join(REPO, 'migrations', f), 'utf8')]),
): [string, string][] {
  return files.flatMap(([f, sql]) =>
    Array.from(sqlCode(sql).matchAll(/(?:DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:ONLY\s+)?(?:"?public"?\.)?"?audit_log\b[^;]*/gi), (m): [string, string] => [f, m[0].replace(/\s+/g, ' ')]),
  )
}

// The new-device mail shows device, IP and time; the mover queues it for a sign-in; the worker records the first pair silently (`!had`) and mails only a later new pair, through the Resend sender.
export function newDeviceMailFacts(
  tpl: string = readFileSync(join(REPO, 'internal/accountmail/new_device.html'), 'utf8'),
  render: string = readSource('internal/accountmail/accountmail.go'),
  worker: string = readSource('internal/accountevents/new_device.go'),
  sender: string = readSource('internal/accountmail/sender.go'),
  mover: string = readSource('internal/accountevents/mover.go'),
  workerMain: string = readSource('cmd/worker/main.go'),
): boolean {
  const details = found(/\{\{-?\s*define "details"\}\}([\s\S]*?)\{\{end\}\}/.exec(tpl.replace(/\{\{\/\*[\s\S]*?\*\/\}\}/g, ''))?.[1], 'the details block')
  const view = found(/v := newDeviceView\{([\s\S]*?)\n\t\}/.exec(render)?.[1], 'the new-device view')
  const work = found(/func \(w \*NewDeviceWorker\) Work\b[\s\S]*?\n}\n/.exec(worker)?.[0], 'NewDeviceWorker.Work')
  const guard = work.indexOf('if !had || !inserted {')
  return (
    />Device<\/td><td[^>]*>\{\{\.Device\}\}/.test(details) &&
    />IP address<\/td><td[^>]*>\{\{\.IPValue\}\}/.test(details) &&
    />Time<\/td><td[^>]*>\{\{\.When\}\}/.test(details) &&
    /IPValue: m\.IP/.test(view) &&
    /When: m\.At/.test(view) &&
    /Sender\s+accountmail\.Sender/.test(worker) &&
    guard > -1 &&
    guard < work.indexOf('w.Sender.Send(') &&
    /QueryRow\(ctx, recordPair,/.test(work) &&
    work.indexOf('recordPair') < guard &&
    /const recordPair = `WITH had AS \(SELECT EXISTS \(SELECT 1 FROM account_known_devices WHERE user_id = \$1::uuid\) AS v\),\s*ins AS \(INSERT INTO account_known_devices \(user_id, browser, os\)/.test(worker) &&
    /ResendBaseURL\s*=\s*"https:\/\/api\.resend\.com"/.test(sender) &&
    /func \(r \*Resend\) Send\(/.test(sender) &&
    /n > 0 && h\.event == "account\.signed_in"/.test(mover) &&
    /newDeviceJob\(h\)/.test(mover) &&
    /NewDeviceWorker\{Pool: pool, Sender: nl\.Sender,/.test(workerMain)
  )
}

// Both erase workers delete the person's account_known_devices rows in the statement list they execute.
export function eraseDeletesKnownDevices(
  erase: string = readSource('internal/notifications/erase.go'),
  byID: string = readSource('internal/notifications/erase_by_id.go'),
): boolean {
  const del = 'DELETE FROM account_known_devices WHERE user_id::text = $1'
  const a = found(/deletes := \[\]del\{[\s\S]*?for _, d := range deletes \{[\s\S]*?tx\.Exec\(ctx, d\.sql/.exec(erase)?.[0], 'the erase delete list')
  const b = found(/for _, d := range \[\]struct \{[\s\S]*?tx\.Exec\(ctx, d\.sql/.exec(byID)?.[0], 'the erase-by-id delete list')
  return a.includes(del) && b.includes(del)
}

// Go files outside tests that DELETE FROM or TRUNCATE audit_log, with the count of files scanned.
export function auditLogGoDeletes(files?: [string, string][]): { hits: string[]; scanned: number } {
  const all: [string, string][] =
    files ??
    ['internal', 'cmd'].flatMap((dir) =>
      (readdirSync(join(REPO, dir), { recursive: true }) as string[])
        .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
        .map((f): [string, string] => [`${dir}/${f}`, stripComments(readFileSync(join(REPO, dir, f), 'utf8'))]),
    )
  const re = /(?:DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:ONLY\s+)?(?:"?public"?\.)?"?audit_log\b|TRUNCATE\s+[^`;"]*?\baudit_log\b/i
  return { hits: all.filter(([, src]) => re.test(src)).map(([f]) => f).sort(), scanned: all.length }
}

// The gateway's non-test source, joined.
export function gatewaySource(): string {
  return readdirSync(join(REPO, 'internal/gateway'))
    .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .sort()
    .map((f) => readSource(`internal/gateway/${f}`))
    .join('\n')
}

// The account events the gateway records through recordAccountEvent, sorted.
export function accountEventsRecorded(src: string = gatewaySource()): string[] {
  const consts = new Map(Array.from(src.matchAll(/\b(Event\w+)\s*=\s*"(account\.[a-z_]+)"/g), (m) => [m[1], m[2]] as const))
  if (consts.size === 0) throw new Error('privacyCodeFacts: no gateway event constants')
  const used = keysOf(/recordAccountEvent\([^)]*AccountEvent\{Event: (Event\w+)/g, src)
  return unique(used.map((c) => consts.get(c) ?? c))
}

// The request reader's admin flag comes from the member's role, and the seam gives an admin the full client scope.
export function adminFollowsTheRole(
  store: string = readSource('internal/audit/store.go'),
  seam: string = readSource('internal/platform/db/tenant.go'),
): boolean {
  return (
    /coalesce\(\(SELECT m\.role = 'admin' FROM memberships m WHERE m\.user_id = \$1::uuid\), false\)/.test(store) &&
    /f\.scope\.admin = admin \|\| inProcess/.test(store) &&
    /f\.scope\.self = id\.Subject/.test(store) &&
    /CASE WHEN t\.kind <> 'firm' OR m\.role = 'admin' OR m\.all_clients THEN '\*'/.test(seam)
  )
}

// The staff route returns every event of every tenant with its payload: no event filter in the last staff_read_audit_log.
export function staffSearchReadsAccountRows(
  migrations: string[] = migrationUps(),
  reader: string = readSource('internal/audit/staff_read.go'),
  main: string = readSource('cmd/invoice/main.go'),
): boolean {
  let fn = ''
  for (const sql of migrations) {
    const m = /CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+public\.staff_read_audit_log\b[\s\S]*?END \$\$;/i.exec(sqlCode(sql.split('-- +goose Down')[0]))
    if (m) fn = m[0]
  }
  if (fn === '') throw new Error('privacyCodeFacts: staff_read_audit_log not found')
  return (
    /a\.event, a\.actor, m\.display_name, m\.email, a\.payload\s+FROM public\.audit_log a/.test(fn) &&
    !/account/i.test(fn) &&
    /member_email, payload\s+FROM public\.staff_read_audit_log\(/.test(reader) &&
    /"GET \/v1\/staff\/audit-log", audit\.StaffSearchHandler/.test(main)
  )
}
