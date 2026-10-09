---
paths:
  - "internal/platform/db/**"
  - "cmd/*/main.go"
  - "internal/tenancy/**"
  - "internal/**/handlers.go"
---
# Tenant seam

- Reach tenant data in a request through `WithinRequestTenantTx` or `WithinRequestTenantTxOpts`.
- Acquire no database handle outside the seam. A bypass needs an `scPoolAllowlist` entry with a reason.
- Call `WithinTenantTx` only from a worker, a boot-time seeder, an operator CLI or an exempt method. Each needs an `scCoreAllowlist` entry with a reason.
- These `tenancy.Store` methods skip the gate: `Me`, `ProvisionWorkspace`, `AcceptInvitation`, `AcceptInvitationByID`, `MyPendingInvitations` and `PreviewInvitation`.
- Call `invitee_account_state` only through `accountState` in `internal/tenancy/store.go`.
- Call `accountState` only from `PreviewInvitation` and `ListInvitations`, with an address read from an `invitations` row in the same transaction. `TestInviteeAccountStateCalledOnlyByTheStore` fails otherwise.
- Guard `AcceptInvitationByID` and `MyPendingInvitations` with the email of the identity header only. The caller has no membership.
- Scope an exemption to one func, never to a file that holds gated methods.
- Give every new route an entry in `scRouteVerdicts` in `internal/platform/db/seam_coverage_test.go`.
- Set the verdict to `covered` or `exempt`. Give an `exempt` entry a reason.
- Queue the `set_config` statement before the membership SELECT. The SELECT reads through the tenant that `set_config` sets.
- Refuse a caller with no membership row or an inactive row with `ErrNotActiveMember`.
- Answer `ErrNotActiveMember` with 403 and `NotActiveMemberMessage`. Never answer 401.
- Answer 401 only for `ErrNoTenant`. The SPA signs the user out on every 401.
- Map both `ErrNoTenant` and `ErrNotActiveMember` in every handler error mapper.
- Refuse a request with a malformed tenant id with `ErrNoTenant` before the first statement.
- Skip the membership lookup for a subject that is not a UUID. Only in-process actors reach that arm.
- Never assert the query plan of the `memberships` status lookup.
- Reword `NotActiveMemberMessage`, `NOT_ACTIVE_MEMBER_MESSAGE` in `authedFetch.ts` and `NOT_ACTIVE_MESSAGE` in `e2e/api/suspension.spec.ts` in one commit.
- Source the refusal message from `db.NotActiveMemberMessage`. Never retype it as a literal in Go.
- Build a DB test identity from a seeded membership row, never from a bare UUID.
