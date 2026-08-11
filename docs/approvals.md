# Approval Policies

**Audience:** anyone publishing, verifying or troubleshooting an approval policy in a
deployed environment, and anyone building the layer that consumes one. This page
specifies what publishing a policy *does today*, how to prove it happened, and which
parts of the approvals story are **designed but not yet built** — so a reader can tell
the two apart without reading the code.

> **Read this first.** Publishing a policy today **seals and activates a configuration
> record and nothing else**. It does not hold, block, gate or arm a single invoice. No
> approval run is ever created, and the invoice transmit path never consults a policy, so
> a tenant with a published policy and a tenant with none behave **identically**. The
> enforcing engine is separate future work; it is called out by name in every section
> where the distinction matters.

The API is six routes (`cmd/invoice/main.go:187-192`):

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/v1/approval-policies` | list the tenant's live policies |
| `POST` | `/v1/approval-policies` | create a policy and its empty draft version 1 |
| `GET` | `/v1/approval-policies/{id}` | read one policy |
| `PUT` | `/v1/approval-policies/{id}/draft` | replace the open draft's whole step tree |
| `POST` | `/v1/approval-policies/{id}/publish` | seal the draft and activate it |
| `DELETE` | `/v1/approval-policies/{id}` | soft-delete the policy |

Reads are open to any caller holding a tenant claim. Every **write** requires an
**active admin** — `requireActiveAdmin` (`internal/approval/store.go:455`) is the first
statement inside each write transaction, so a suspended or non-admin caller is refused
with nothing written. It answers `403 only an admin can change approval policies`.

**Arrived here from an error message?** Go straight to the [error
reference](#appendix--error-reference) at the foot of this page, which lists every
response these endpoints can return and what to do about each.

---

## 1. What publish does

`POST /v1/approval-policies/{id}/publish` reads no body at all. `published_by` is taken
from the caller's own verified subject and `published_at` from `now()`; neither can be
supplied by the client.

Publish resolves the policy's **one unsealed version** — the predicate is `NOT sealed`,
never "the active version", because a policy may hold sealed versions while some *other*
policy owns the tenant's active slot. If there is no unsealed version the call is
`409 this policy has no unpublished changes`.

Two gates run before anything is written. Both answer `409`:

| Condition | Response |
|---|---|
| an `approval` step names a workflow role that is absent or soft-deleted | `an approval step names a workflow role that no longer exists` |
| a `condition` step has nothing in **either** of its two lanes | `a condition must have at least one step in one of its two lanes` |

An **empty** policy — no steps at all — passes both gates and is publishable.

The write is two statements, in this order:

```sql
-- 1. release the tenant's active slot. No policy predicate; RLS is the tenant scope.
UPDATE approval_policy_versions SET is_active = false WHERE is_active;

-- 2. seal and claim the slot, in the same transaction.
UPDATE approval_policy_versions
   SET sealed = true, is_active = true, published_at = now(), published_by = $caller
 WHERE id = $version;
```

**The first statement is tenant-wide, and that is the single most surprising thing on
this page.** It carries no policy predicate. The index behind it is:

```
approval_policy_versions_one_active
  ON approval_policy_versions USING btree (tenant_id) WHERE is_active
```

Keyed on `tenant_id` alone — **not** `(tenant_id, policy_id)`. A tenant therefore has at
most **one** active version across **all** of its policies. Publishing policy B silently
deactivates whatever version policy A had active. This is intended (see §6), but an
operator who publishes a second policy expecting both to be live will not get an error —
they will get a quietly deactivated first policy.

Deactivation is **not** un-publishing. The version that lost the slot keeps `sealed =
true` and keeps its original `published_at` and `published_by`. Only `is_active` flips.

### Recovering from an accidental deactivation

**There is no undo, and there is no re-activate.** The only statement in the codebase that
sets `is_active = true` is the publish seal itself, and it can only ever target a version
resolved by `NOT sealed`. **No API path re-activates a version that has already been
sealed** — which is every version that was ever live. Restoring policy A therefore means
publishing it *again*, as a **new** version.

The obvious workaround does not work: **deleting policy B does not bring A back.** The
deactivation in `DELETE` is policy-scoped (§9), so it clears B's own `is_active` and
touches nothing else. The tenant is then left with **zero** active versions — strictly
worse than before.

The actual procedure:

1. **Read A's step tree out of the database.** No endpoint returns a sealed version's
   steps (§10), so this cannot be done through the API:

   ```sql
   SET app.current_tenant = '<tenant-uuid>';   -- omit only as a superuser; see §3

   SELECT coalesce(parent.ord, s.ord) AS group_ord,
          s.branch, s.ord, s.kind,
          s.workflow_role_key, s.sla_hours, s.cond_op, s.cond_amount,
          s.id, s.parent_step_id
     FROM approval_policy_steps s
     JOIN approval_policy_versions v ON v.id = s.version_id
     LEFT JOIN approval_policy_steps parent ON parent.id = s.parent_step_id
    WHERE v.tenant_id = '<tenant-uuid>'
      AND v.policy_id = '<policy-A-uuid>'
      AND v.version   = <the version that was active>
    ORDER BY group_ord, s.parent_step_id NULLS FIRST, s.branch, s.ord;
   ```

   The `SET` is not optional for a non-superuser connection, and it must be a plain `SET`
   rather than `SET LOCAL` — §3 explains why, and the same trap applies here.

   Reading the output: `group_ord` collects each root step together with the lane steps
   belonging to it, so a `condition` row is immediately followed by its own `then` and
   `else` rows and nothing else. A row with a null `branch` is a root step; a row with a
   `branch` belongs to the step whose `id` equals its `parent_step_id`.

   > **Match lanes by `parent_step_id`, never by adjacency.** The `id`/`parent_step_id`
   > pair is the only reliable link. A policy with two `condition` steps produces two sets
   > of `then`/`else` rows that are otherwise indistinguishable — and attaching a lane to
   > the wrong threshold silently inverts a real approval rule. That is why both id columns
   > are selected even though nothing else in this page needs them.

2. **Re-author that tree** through `PUT /v1/approval-policies/{A}/draft`. This mints a
   fresh version numbered `max + 1` and **copies nothing** from the sealed one — every
   step has to be sent again.

3. **Publish it.** `POST /v1/approval-policies/{A}/publish`. This deactivates B in turn,
   which is the intended outcome here.

Afterwards A governs again, but as a **new version number** with a new `published_at` and
`published_by` naming whoever performed the recovery. The originally-active version stays
`sealed = t, is_active = f` for good. That is by design — the history of what was in force
and who put it there is preserved rather than rewritten — but it does mean an accidental
publish is permanently visible in the version list.

`published_at` is `now()`, which in Postgres is the **transaction** timestamp, so it is
identical to the `created_at` of the audit row written in the same transaction. That
equality is a useful correlation handle when reading the two tables side by side.

**Two admins publishing at once produce two different `409`s, and the likelier case is the
confusing one.** Which you get depends on whether they were publishing the *same* policy:

| Race | Loser's `409` | Why |
|---|---|---|
| the **same** policy | `this policy has no unpublished changes` | The two serialise on the policy row's `FOR UPDATE`. The loser then re-resolves its draft on a fresh snapshot, finds the version already sealed, and reports the same thing it would report if there had been nothing to publish at all. |
| **different** policies | `another version was published first — reload the policy and try again` | Both pass their own draft resolution and then collide on the tenant's single active slot. |

Read the first row carefully: a lost same-policy race is **indistinguishable by message**
from publishing a policy that had no pending edits. An admin who is told "no unpublished
changes" immediately after clicking Publish has most likely just lost a race to a
colleague — their draft is now sealed and live, published under the *other* admin's name.
Check `published_by` (§3) before assuming the click did nothing.

Neither `409` is ever retried automatically: a retry would publish a step tree the caller
never re-validated.

The audit write is the **last** statement in the transaction, so a failing audit rolls
the seal back — a sealed version without its audit row cannot exist.

---

## 2. A tenant with no active policy

**Every invoice transmits as soon as it validates.** Approval policies do not
participate in the transmit path at all. The only gates on transmission are the two
capability checks that guard the two ways an invoice can be transmitted — the
single-invoice `TransitionHandler` (`internal/invoice/handlers.go:669`) and the batch
`BatchSubmitHandler` (`:1124`, behind `POST /v1/invoices/submissions`). Both apply the
same `isApprover` test, admitting an `admin` or a `reviewer` and refusing a `preparer`
with the same message. No production code path
anywhere reads `approval_runs`, `approval_run_steps` or `approval_decisions` — the three
tables are referenced only by the test-database reset list and by tests — and
`approval_policy_versions` is read only by the policy endpoints themselves.

**An active but EMPTY policy behaves identically.** A policy with zero steps is
publishable, so a tenant can hold an active, sealed, correctly-published version that
constrains nothing. Today that tenant and a tenant with no policy at all are
indistinguishable in behaviour.

They are, however, perfectly distinguishable **in the data**:

| State | `approval_policy_versions WHERE is_active` | steps of that version |
|---|---|---|
| no active policy | 0 rows | — |
| active but empty policy | 1 row | 0 rows |
| active policy with rules | 1 row | 1 or more rows |

> **Carry this forward.** The arming engine's predicate must ask **both** questions.
> "Does this tenant have an active version?" and "does that active version have steps?"
> are different questions, and only the second one should arm a run. A predicate that
> stops at the first will arm every invoice in a tenant whose admin published an empty
> policy — turning a no-op configuration into a total transmit freeze. The two states
> are identical in behaviour today precisely *because* nothing consumes them yet; that
> stops being true the moment the engine lands.

---

## 3. How to verify an activation

> **Set the tenant GUC first, or both queries below lie to you.** All three approval
> tables and `audit_log` are `FORCE ROW LEVEL SECURITY`, and **neither `invoice_app` nor
> `invoice_migrator` is a superuser or holds `BYPASSRLS`** — `FORCE` subjects the table's
> owner too, so being the migrator buys you nothing here. Measured on a database holding
> hundreds of published policies: as `invoice_migrator` with no GUC set, both queries
> below return **`(0 rows)`**.
>
> That is the worst possible failure mode for this section — an operator checking whether
> a publish landed reads an empty result and concludes it did not. Nothing is wrong; the
> rows are simply invisible.
>
> Run this once, at the top of your session, before either query:
>
> ```sql
> SET app.current_tenant = '<tenant-uuid>';   -- required for EVERY non-superuser role
> ```
>
> **Use plain `SET`, not `SET LOCAL`.** `SET LOCAL` is scoped to a transaction, and
> outside one it is discarded with only a warning — the query then runs with no tenant and
> returns `(0 rows)`, which is the very failure this box exists to prevent:
>
> ```
> WARNING:  SET LOCAL can only be used in transaction blocks
> ```
>
> If you do want transaction scope, both statements must be inside the same transaction:
> `BEGIN; SET LOCAL app.current_tenant = '…'; SELECT …; COMMIT;`
>
> Only a superuser or a `BYPASSRLS` connection may skip the setting entirely.

Two queries.

**a. The version state.** `sealed` and `is_active` are the two booleans that matter, and
they are not the same fact:

```sql
SELECT p.name, v.version, v.sealed, v.is_active, v.published_at, v.published_by
  FROM approval_policy_versions v
  JOIN approval_policies p ON p.id = v.policy_id
 WHERE v.tenant_id = '<tenant-uuid>'
   AND p.deleted_at IS NULL
 ORDER BY p.name, v.version DESC;
```

```
          name           | version | sealed | is_active |         published_at          |  published_by
-------------------------+---------+--------+-----------+-------------------------------+-----------------
 Doc verification policy |       1 | t      | t         | 2026-08-11 04:32:33.279542+00 | ops@example.com
```

A correctly published and in-force version reads `sealed = t`, `is_active = t`, and a
non-null `published_at`/`published_by`. Two rows to recognise on sight:

- `sealed = t, is_active = f` — a **previously** published version that has since been
  superseded, either by a newer version of the same policy or by a publish on a
  *different* policy. Its `published_at`/`published_by` are its original ones and are not
  evidence that it is in force.
- `sealed = f` — an open **draft**. Never in force. `is_active` cannot be true here; the
  database refuses it (see §4).

At most one row in the whole result can carry `is_active = t`.

**b. The audit row.** Every publish writes one, in the same transaction:

```sql
SELECT created_at, actor, event, payload
  FROM audit_log
 WHERE tenant_id = '<tenant-uuid>'
   AND event = 'approval_policy.published'
 ORDER BY id DESC
 LIMIT 5;
```

```
          created_at           |      actor      |           event           |                               payload
-------------------------------+-----------------+---------------------------+---------------------------------------------------------------------
 2026-08-11 04:32:33.279542+00 | ops@example.com | approval_policy.published | {"version": 1, "policy_id": "c60196b7-6c5f-4901-a388-b296d32a32fb"}
```

The payload is exactly two keys, `policy_id` and `version`. `actor` is the publishing
caller's subject. `created_at` equals the version's `published_at` — both are the same
transaction's `now()` — which is how you tie an audit row to the version it sealed when
a policy has several.

The other three events on the same policy carry the same two payload keys:
`approval_policy.created`, `approval_policy.updated`, `approval_policy.deleted`.

`audit_log` is append-only and permanently retained: it holds no `UPDATE`/`DELETE` grant
for the application role and carries triggers refusing both, plus `TRUNCATE`.

---

## 4. Immutability

**A published version can never be edited, unsealed or deleted.** Once `sealed = true`,
that version's step tree is frozen for good. Changing a policy always means writing a
*new* version: the draft `PUT` forks a fresh version numbered `max + 1` when no open
draft exists, and it copies no steps from the sealed one.

This is enforced in the database, not in application code, so it holds against psql and
against any future service just as it holds against the API. What an operator actually
sees depends on **which role the failing connection holds**, because a missing grant
fires *before* a trigger ever runs:

| Attempt | As `invoice_app` (every service binary) | As a role holding `DELETE` (migrator, superuser) |
|---|---|---|
| `UPDATE` a sealed version's step content | `23001` content lock | same |
| `INSERT` a step under a sealed version | `23001` content lock | same |
| `DELETE` a sealed version's step | `23001` content lock | same |
| `UPDATE ... SET sealed = false` (unseal) | `23001` seal guard | same |
| `DELETE` a sealed version | **`42501` permission denied** | `23001` seal guard |
| `DELETE` the parent policy (cascades into the version) | **`42501` permission denied** | `23001` seal guard |
| `TRUNCATE approval_policy_steps` | **`42501` permission denied** | `23001` truncate lock |

A genuine no-op `UPDATE` of a sealed step — one that changes no column — still passes.
All fourteen columns are compared, so any real change is caught.

> **The `23001` errors carry no constraint name.** They are raised by PL/pgSQL functions,
> so the `CONSTRAINT NAME` field of the error is empty and there is nothing to grep for.
> Match on the message text instead. There are four:
> - `steps of a sealed approval policy version are immutable: ...`
> - `a sealed approval policy version cannot be deleted (version=N)`
> - `a sealed approval policy version cannot be unsealed (version=N)`
> - `approval_policy_steps is protected by the policy immutability lock: TRUNCATE is not permitted`

The remaining two SQLSTATEs **do** carry a constraint name, which is the fastest way to
identify them:

| SQLSTATE | Constraint | Meaning |
|---|---|---|
| `23514` | `approval_policy_versions_active_is_sealed` | something tried to activate a version that is not sealed. `CHECK (NOT is_active OR sealed)` — active always implies sealed, so an active-but-unsealed row cannot exist even transiently. |
| `23505` | `approval_policy_versions_one_active` | a second version tried to become active in the same tenant. `ON (tenant_id) WHERE is_active` — see §1 and §6. |
| `23505` | `approval_policy_versions_one_draft` | a second *unsealed* version was created for one policy. `ON (tenant_id, policy_id) WHERE NOT sealed` — a policy has at most one open draft, which is what makes "the draft" an unambiguous target for the `PUT`. |
| `23514` | `approval_policies_scope_check` | a scope outside the single permitted value was written. See §6. |

Note the two `23505` indexes have **different keys**: one active per *tenant*, one draft
per *policy*. A tenant can hold many open drafts at once — one per policy — but only one
active version in total.

A practical consequence worth knowing before you try it: because a sealed version cannot
be deleted even by a cascade, a tenant that has ever published a policy **cannot be
deleted** by an ordinary `DELETE FROM tenants`. Tearing one down requires suppressing
triggers and deleting the children bottom-up.

---

## 5. The publish sweep

> ### NOT IMPLEMENTED. This section describes future work.
>
> **Nothing in this section is shipped behaviour.** The arming sweep belongs to the
> arming-engine story (**APPR-06**), which is not built. Publishing a policy today writes
> rows to `approval_policy_versions` and `audit_log` and **nothing else**. It creates no
> `approval_runs`, `approval_run_steps` or `approval_decisions` row — those three tables
> exist and no production code path reads or writes them at all. That is pinned, not
> assumed: `TestPublish_CreatesNoApprovalRun` publishes a real policy over a validated
> backlog and asserts all three tables are still empty. There is no cap to hit and no
> `409` to receive, because there is no sweep. The contract below is recorded so it is
> fixed before it is built — not because any of it exists.

The designed contract is:

- **Synchronous.** Publishing arms the tenant's whole validated backlog **in the same
  transaction** as the seal. When the call returns `200`, the policy is in force
  immediately and completely — there is no window in which a policy is published but only
  partly applied.
- **Capped at 5,000 invoices.** Above that the publish **fails with a `409`** and seals
  nothing; the transaction rolls back whole, leaving the draft open and the previously
  active version untouched.
- **The `409` names this page** as the operator path, which is why the remedy is written
  here rather than in the error string.

**The remedy, when a publish is refused for the cap.** The cap is a deliberate ceiling on
transaction size, not a licence limit, and it is not raisable by configuration — there is
no toggle.

The intended operator path is to **reduce the validated backlog below 5,000 and publish
again**: only invoices sitting at `validated` are swept, so transmitting or returning part
of the backlog moves them out of the sweep's range. It requires no code change. **This
path is derived from the designed cap check and has never been exercised** — there is no
sweep to refuse a publish yet, so treat it as the intended shape rather than a tested
runbook, and confirm it against the implementation once that lands.

Beyond that there is no operator remedy: escalate. Moving the sweep to a background pass
is a reserved design option and a code change, to be taken up only if a real tenant
approaches the cap.

The cap was sized against a planning-time figure of roughly 250 invoices for the largest
tenant — about 5% of it. That figure was taken when the decision was made and has not been
re-measured here; re-check it against production before treating the headroom as current.

One correction worth recording, because the obvious rationale for the cap is wrong: a
partially-armed tenant is **not** a leak. The gate the engine will apply is *positive* —
an invoice is blocked unless an approval run has cleared it — so an invoice the sweep has
not yet reached is **blocked, not released**. A partial sweep is therefore safe, and the
synchronous choice rests on operator predictability, not on a correctness cliff.

---

## 6. Scope: one active policy per tenant

**A tenant has exactly one active policy version at a time**, across all of its policies.
Publishing a second policy deactivates the first, without warning (§1). The mechanism is
the `approval_policy_versions_one_active` index keyed on `tenant_id` alone.

Consequently, routing different invoices to different policies is **not possible**, and
the `scope` column reflects that honestly. It accepts exactly one value:

```
approval_policies_scope_check
  CHECK (scope = 'All invoices'::text)
```

`scope` defaults to `All invoices`, and the API normalises an absent or empty scope to
that value before the value ever reaches SQL. Any other string is a `400 invalid
request`; a value that somehow reached the column directly would be a `23514`.

Five further scope options exist in the product's scope dropdown:

- `Foreign-currency invoices`
- `Document type · B2G`
- `Capex & fixed assets`
- `Consumer invoices (B2C)`
- `Credit notes & adjustments`

**The server already refuses every one of them**, by the rule just above. None has any
backing invoice classification, so a policy carrying one would route nothing while
appearing to route something.

> **Not yet removed from the editor — and nothing rejects them there.** The scope dropdown
> still offers all six options: `WF_SCOPE_OPTIONS`
> (`frontend/app/src/lib/workflows.ts:107-114`) still declares them and
> `WorkflowBuilder.tsx:56` still maps the whole list into the rendered select. Three of
> them also still appear as scopes on mock policy data.
>
> Selecting one and saving is **not** refused, because **nothing is sent**. The builder is
> not wired to this API at all — the SPA makes no call to `/v1/approval-policies` anywhere,
> and its `publishPolicy` is a local object transform. The screen accepts an unstorable
> scope and displays it as published. The server's refusal is real but only reachable by
> calling the API directly; the editor will not start meeting it until **APPR-09** wires
> the builder to the server.
>
> Deleting them from the palette is **APPR-10's unbuilt work**, under the rule that **a
> control which fails invisibly is removed, while a control that announces its own
> disabled state is kept and labelled.** A scope dropdown looks identical whether or not
> it routes anything, which puts these five in the first category. Until that lands, the
> `CHECK` above is the *storage* truth and the dropdown is not.

Amount-threshold `condition` steps are unaffected and remain the supported way to express
escalation, because they read the invoice's real total rather than an unpopulated
classification.

Per-scope routing — invoice classification plus policy matching — is deferred to a future
epic. When it lands it is a **migration**, not a configuration change: the `CHECK` above
is a storage-layer lock on the decision, deliberately placed so the decision cannot be
quietly reversed by editing a dropdown.

---

## 7. Known limitation: policies are keyed per workspace

**Approval policies are keyed per WORKSPACE, not per client company.** One policy set
governs every client company a practice manages.

For an in-house company this is invisible — the workspace *is* the company. For an
accounting firm it is a real limitation: **a firm cannot yet run different sign-off rules
per client.** A practice that wants a two-step sign-off for one client and a one-step
sign-off for another cannot express it. The firm's single active policy applies to every
client's invoices alike.

Per-client policy scoping is a schema and builder change beyond the current epic. It is
deliberately deferred rather than half-built, and should be revisited if a pilot practice
asks for it.

---

## 8. Capability matrix

What each access role can do. This is the reference copy: the repo ships no release-notes
artefact, so this page is the matrix's only home. The row labels are the ones rendered in
the product, lowercase as authored (`frontend/app/src/lib/members.ts:91-99`).

The **Enforced where** column is the load-bearing one, and it distinguishes two different
kinds of "no":

- **not enforced** — the endpoint exists and serves every role alike. The product shows
  the control, and the server will not stop anyone from using it. This is the one that
  matters for access control: the restriction shown on screen is decorative.
- **no server surface** — the capability has no endpoint at all. Nothing to enforce,
  because nothing is reachable. Not an access-control gap; an unbuilt feature.

Read either as a statement of fact, never as "presumably enforced somewhere".

| Capability | Admin | Preparer | Reviewer | Enforced where |
|---|:---:|:---:|:---:|---|
| create and edit invoices | ✓ | ✓ | ✓ | not enforced |
| import from file or ERP | ✓ | ✓ | ✓ | not enforced |
| run validation | ✓ | ✓ | ✓ | not enforced |
| approve in approval steps | ✓ | — | ✓ | **not enforced yet** — the approve/reject seam is unbuilt (APPR-07) |
| transmit to NRS/MBS | ✓ | — | ✓ | **server-enforced, both doors** — `internal/invoice/handlers.go:669` (single) and `:1124` (batch, `POST /v1/invoices/submissions`). Both apply `isApprover` = admin or reviewer; a preparer gets `403` either way. |
| invite and manage members | ✓ | — | — | **partly server-enforced** — the *manage* half is admin-only at `internal/tenancy/store.go:140` (`PATCH /v1/memberships/{user_id}`). The *invite* half has **no server surface**. |
| manage ERP connectors | ✓ | — | — | **no server surface** — no endpoint exists |
| manage signing certificates | ✓ | — | — | **no server surface** — no endpoint exists |

**Two rows are server-enforced today**, and one of those only in half. Any wider claim —
that approvals are enforced, or that the matrix as a whole is backed — is aspirational
until the engine lands. The code is the authority for this table; if the two disagree,
the code is right and this table is stale.

Separately, and not in the matrix: **every approval-policy write requires an active
admin** (`internal/approval/store.go:455`). Reading policies requires only a tenant
claim.

---

## 9. Deletion

**Deletion is soft, and only soft.** `DELETE /v1/approval-policies/{id}` stamps
`approval_policies.deleted_at` and does not remove a row from any table. In the same
transaction it also deactivates the version that policy was governing with:

```sql
UPDATE approval_policy_versions SET is_active = false WHERE policy_id = $1 AND is_active;
```

That second statement matters. Without it a soft-deleted policy would keep holding the
tenant's active slot — invisible to every read, but still the tenant's governing policy.
Note the contrast with publish: this deactivation is **policy-scoped**, whereas publish's
is tenant-wide.

**Everything else survives.** Every version row, every step row and every decision row
remains exactly as it was. Deactivating is not un-publishing: `sealed`, `published_at`
and `published_by` are untouched, so the historical record of what was published, by
whom, and when stays intact and readable.

**A policy is not a decision.** Deleting a policy must never be a route to erasing the
record of who approved what. Three independent mechanisms guarantee it:

- `approval_decisions` holds `GRANT SELECT, INSERT` for the application role — **no
  `UPDATE`, no `DELETE`**. A decision, once written, cannot be altered or removed by any
  service. This grant *is* the retention mechanism; there is no trigger and no purge job.
- `approval_runs → approval_policy_versions` is `ON DELETE RESTRICT`. A policy version
  that governed a run cannot be removed out from under the evidence of that approval.
- Hard delete is impossible regardless: the application role holds **no `DELETE` grant**
  on `approval_policies` or on `approval_policy_versions` (attempting it is `42501`, §4),
  and a sealed version blocks even a cascaded delete from a role that does hold one.

Approval decisions are retained **permanently**, matching the audit log's posture. There
is no TTL, no archival table, no purge job and no deletion endpoint.

> **Open, and not an engineering question.** The Nigerian FIRS/NRS statutory retention
> requirement is **unconfirmed**. Permanent retention is a safe default, not a verified
> compliance position. This must be settled before general availability.

Re-deleting an already-deleted policy is a `404`, not a second stamp: `deleted_at IS
NULL` is both the existence predicate and the idempotency mechanism.

---

## 10. Handed forward

Two gaps that are real, verified, and deliberately left for the stories that follow.

### The in-force step tree is not obtainable from the API

Once a draft is open, `GET /v1/approval-policies/{id}` returns the **draft's** steps, not
the active sealed version's. The response's `steps` always belongs to the policy's
**highest-numbered** version, and creating a draft mints a version numbered `max + 1` —
so from the moment a draft exists, `steps` describes an unpublished proposal. **The
sealed active version's step tree has no endpoint at all.**

What the response *does* carry is `versions[]`, newest first, each entry with `version`,
`sealed`, `is_active`, `published_at`, `published_by`. So a reader can always tell
**which** version governs — the one with `is_active: true` — but cannot fetch **what** it
says.

Two consequences, both correctness-relevant:

- **The policy builder (APPR-09) must not label the returned `steps` as "currently
  active".** The test is `versions[0].sealed`: when it is `false` the tree on screen is an
  unpublished draft, whatever the `is_active` flags further down the list say. Presenting
  a draft as the live rule set would tell an admin their unsaved intentions are already
  governing invoices.
- **The arming engine (APPR-06) must read the active tree from the database inside its
  own transaction**, not from this endpoint. Sourcing it here would arm runs against a
  draft — a correctness bug, and a silent one, since a draft tree is structurally valid
  and would produce plausible-looking runs enforcing rules nobody published.

The natural fix is a dedicated read of the active version's tree. It is not in scope here
and has no owner yet.

### No `updated` timestamp exists

The policy list renders `Updated {policy.updated}` for each row
(`frontend/app/src/components/WorkflowsView.tsx:109`), but there is no such value on the
server. `approval_policies` has exactly six columns — `id`, `tenant_id`, `name`, `scope`,
`created_at`, `deleted_at` — with **no** `updated_at`, and nothing on the wire carries an
update time. The field is satisfied from placeholder data.

Anything that surfaces a real modification time needs a new column and a new wire field
first. This is handed to APPR-09, which points the builder at the server.

---

## Appendix — error reference

Every response the policy endpoints can return, and what to do about it. Look up the
message, not the status: several distinct causes share a status code.

| Status | Message | What it means | What to do |
|---|---|---|---|
| `400` | `invalid request` | A value was rejected before it reached the database. The complete set of causes: an empty name; an unrecognised scope (§6); an unknown step `kind`; a `condition` nested inside another step; a **non**-`condition` step carrying `then`/`else` children; a `cond_op` outside `> >= < <=`; a `cond_amount` that is not a decimal number, has more than two decimal places, or is too large for `numeric(14,2)`; a negative or oversized `sla_hours`; a `condition` step missing its operator or amount; a `notify` step missing its target or channel; or a NUL byte in any text field. | Fix the offending field. The message is deliberately non-specific, so work down the causes in this cell — nothing else produces it. |
| `400` | `invalid request body` | The JSON did not decode, or the body exceeded the 64 KiB cap. | Check the payload is well-formed. A very large policy tree can genuinely hit the cap. |
| `400` | `steps must be an array of approval steps` | A draft `PUT` omitted `steps` entirely. | Send `steps` explicitly. **`"steps": []` clears the tree** — that is a real operation, which is why an absent key is refused rather than treated as empty. |
| `401` | `unauthorized` | No verified identity, or no tenant claim on the request. | An authentication problem, not a policy one. |
| `403` | `only an admin can change approval policies` | The caller is not an **active** admin. A suspended admin and an invited-but-not-active admin are both refused. | Have an active admin perform the change, or reactivate the membership. Reads need no admin role. |
| `404` | `approval policy not found` | No such policy in this tenant — or it is soft-deleted, or the id is not a well-formed uuid. All three are deliberately one answer, so the endpoint cannot be used to probe which ids exist. | Confirm the id and that the policy is not deleted (`deleted_at IS NULL`). |
| `409` | `this policy has no unpublished changes` | Either there was genuinely no open draft, **or** a concurrent publish of the same policy won the race. | Check `published_by` and `published_at` (§3) before assuming nothing happened — see §1. |
| `409` | `an approval step names a workflow role that no longer exists` | An `approval` step references a workflow role that has been deleted, or carries no role key at all. | Restore the role, or edit the step to name a live one, then publish again. A role deleted *after* a publish leaves the sealed version active with that step unsatisfiable. |
| `409` | `a condition must have at least one step in one of its two lanes` | A `condition` step has both lanes empty, so it branches nowhere. | Put at least one step in the `then` or `else` lane, or remove the condition. |
| `409` | `another version was published first — reload the policy and try again` | A concurrent publish of a **different** policy took the tenant's single active slot. | Reload, confirm which policy is now active (§3), and decide whether yours should replace it. Do not blind-retry — see §1. |
| `500` | `internal server error` | Anything unmapped. A `23001`, `23514` or `42501` reaching the client arrives here, because those carry no sentinel the API layer can translate. | Read the service log for the underlying SQLSTATE, then §4. A `23001` or `42501` means something attempted a write the immutability lock or the grant matrix forbids, which is a bug, not an operator error. |

For failures seen directly in `psql` rather than through the API, the SQLSTATE tables in
§4 are the lookup: `23001` (immutability, no constraint name — match the message text),
`23514` and `23505` (both carry a constraint name), and `42501` (a missing grant, which
fires *before* any trigger).
