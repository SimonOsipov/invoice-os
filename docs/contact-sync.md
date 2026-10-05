# Contact sync (`notifications`)

Every verified registrant and every demo booker reaches HubSpot (sales record) and Resend (email list).
Signup and the demo form never wait on either vendor.

## Flow

```
GET  /auth/verify  → GoTrue /verify 200 ─┬─ 303 to the landing (never waits)
                                         └─ background hand-off
POST /auth/sign-in → GoTrue /token 200 ──┬─ {code} (never waits)
                                         └─ background hand-off, only when user_metadata.registration is set
POST /contacts/demo-request ─ one call, no retry ─┐
hand-off ─ POST notifications /internal/contacts/registrants ─┤  (X-Gateway-Token)
           POST notifications /internal/contacts/demo-requests ◀┘
notifications intake ─ one transaction: merge the contacts row + queue one River job per undelivered destination
River queue `contacts`, kind `contact_deliver` ─ worker ─ HubSpot, Resend ─ stamps *_delivered_at
GET /api/notifications/v1/contacts/me ─ the caller's own row
```

Code: `internal/gateway/contacts.go`, `internal/notifications/` (`intake.go`, `store.go`, `worker.go`, `hubspot.go`, `resend.go`, `mode.go`), `cmd/notifications/main.go`.

## Modes and variables

Notifications reads these variables at boot (`internal/notifications/mode.go`). Values are not trimmed. No error text carries a value.

| Variable | Use |
|---|---|
| `CONTACTS_FAKE` | boolean (`strconv.ParseBool`); anything else refuses to boot |
| `HUBSPOT_TOKEN` | HubSpot private-app token |
| `RESEND_API_KEY` | Resend API key |
| `RESEND_SEGMENT_ID` | id of segment "Registered" |
| `RESEND_TOPIC_ID` | id of topic "Marketing" |
| `RAILWAY_ENVIRONMENT_NAME` | decides the posture; `pr-<n>` (or `<prefix>-pr-<n>`) is `preview` |
| `GATEWAY_TOKEN` | required; the intake routes accept only the gateway |

| Mode | When | Worker | Effect |
|---|---|---|---|
| `fake` | posture `preview`, whatever else is set (the keys are discarded and no error is raised) | runs | records a delivery with `delivery_mode = fake`; no network call |
| `fake` | `CONTACTS_FAKE=true` and none of the four keys set | runs | same |
| `real` | all four keys set, `CONTACTS_FAKE` not true | runs | calls HubSpot and Resend; records `delivery_mode = real` |
| `off` | none of the four keys set, `CONTACTS_FAKE` not true | not started | intake still queues; jobs wait and deliver when keys arrive |
| refuses to boot | `CONTACTS_FAKE=true` with any key set; 1-3 keys set; `CONTACTS_FAKE` not a boolean | - | the process exits; the error names the variables, not the values |

Outside `preview`, a refusal applies in every posture, local included.
Preview forks inherit production's keys and never use them.

The mode shows as `contacts` on `/healthz` and on `GET /healthz/fleet` (the `notifications` entry).

## Sending rule

| Email | Send to |
|---|---|
| Product and service email | segment "Registered" |
| Marketing email | topic "Marketing" only |

The segment holds every registrant, ticked or not. Only the topic holds consent.

Resend sync (`resend.go`):
- Reads `GET /contacts/{email}` first and creates only on 404.
- Adds the contact to the segment when it carries the tag `registered`.
- Sets the topic to `opt_in` once, for a ticked person (`resend_opt_in_sent_at` records it).
- Never sends `unsubscribed`, `opt_out`, or a second `opt_in`, so a person who unsubscribed stays unsubscribed.
- A demo booker who did not tick gets no Resend job.

HubSpot sync (`hubspot.go`):
- `PATCH /crm/v3/objects/contacts/{email}?idProperty=email`; on 404, `POST /crm/v3/objects/contacts`.
- Sends `firstname`, `lastname`, `company` when non-empty.
- Sends property `ascomply_contact_tags` as `;registered`, `;demo_request` or `;registered;demo_request`. It sends nothing for the property when the row has no tag.
- A person who registers and books a demo is one contact with both options.

A registrant's `display_name` splits on the first run of whitespace into first and last name; `workspace_name` is `company`.

## Hand-off from the gateway

| Rule | Value |
|---|---|
| Triggers | every `GET /auth/verify` that GoTrue answers 200; every `POST /auth/sign-in` 200 whose user has `user_metadata.registration` |
| Attempts | 3: at once, after 5 s, after 30 s (`handOffDelays` in `contacts.go`) |
| Call timeout | 5 s per attempt |
| After the third failure | one WARN `contacts: registrant hand-off failed` with `user_id`, `attempts`, `status`; nothing retries it |
| Gateway stops mid hand-off | the hand-off is lost |
| Recovery | that person's next sign-in hands them off again |
| Staff, founder, API-made accounts | no `registration` metadata, so sign-in never hands them off |
| Demo route | public `POST /contacts/demo-request` on the gateway; one call, no retry; sink failure answers 502 and logs WARN `contacts: demo request hand-off failed` |
| Consent | `marketing_consent_text` on register and on the demo route; blank or whitespace-only is 400; 1 to 500 characters |

`ceiling:` the demo route is public and unthrottled; any address can be submitted with a tick. No marketing email goes to demo-route contacts until a double opt-in step exists.

## Proxy refusal of `internal` paths

- The gateway answers 404 to `/api/<service>/internal/...`. It tests the raw first segment and the cleaned first segment (`internal/gateway/gateway.go`), because a CONNECT path is not cleaned by the mux.
- The notifications intake routes answer 404 to any request that carries `X-User-ID`, which every proxied request has.
- The intake routes also need `X-Gateway-Token`.

## Retry, ERROR log, discard

| Item | Behaviour |
|---|---|
| Queue / kind | `contacts` / `contact_deliver`, 2 workers |
| Args | `{email, destination, version}`; `destination` is `hubspot` or `resend` |
| Retry | River's default policy: 25 attempts, `attempt^4` seconds, about three weeks. Any non-nil worker error retries |
| Version guard | a job whose `version` differs from the row's returns without a vendor call; a newer version has its own job. A delivery is recorded only `WHERE version = <version read>`; a miss is not an error. Delivery runs in one transaction under `pg_advisory_xact_lock` on (destination, email), so two versions of one person never overlap and the second reads the fresh row |
| Log, HTTP 4xx other than 408 and 429 | ERROR `contacts: <destination> rejected the delivery` with `destination`, `status`. The job still retries, so each attempt logs again |
| Log, any other failure | WARN `contacts: <destination> delivery failed` with `destination`, `status` (0 is no response) |
| Sentry | River reports only the final attempt |
| After the last attempt | job state `discarded`; the row keeps a NULL `*_delivered_at` |
| Unknown `destination` | cancelled |
| Email or name | never in a log line or an error |

Re-trigger rule: any later intake for that email (the person's next sign-in if registered, or another demo request) re-queues every destination whose delivery time is NULL. Unique jobs ignore `completed` ones, so a re-queue is never blocked by a finished job.
A discarded delivery for a person who never returns stays undelivered. `ceiling:` revisit when Sentry shows a discarded `contact_deliver` job.

Find a discarded job (production, as the Postgres superuser; see Erase):

```sql
SELECT id, args->>'destination', attempted_at FROM river_job WHERE kind = 'contact_deliver' AND state = 'discarded';
```

## The `contacts` table

One row per email, lower-cased and trimmed by Postgres (`migrations/20261004122344_contacts.sql`). No `tenant_id`, no RLS.

| Column | Meaning |
|---|---|
| `email` | primary key |
| `registered_at`, `demo_requested_at` | set once |
| `marketing_consent_text`, `marketing_consented_at` | set together, once; the sentence the person saw and its time |
| `version` | +1 whenever a fact that was NULL becomes set |
| `hubspot_delivered_at`, `resend_delivered_at` | NULL means undelivered; cleared when `version` changes |
| `resend_opt_in_sent_at` | the topic opt-in was sent |
| `delivery_mode` | `real` or `fake`, from the last delivery |

Merge rule (`store.go`):
- A set fact never changes. A trigger raises on any update of it, the table owner included.
- A later unticked form never removes a tick.
- A name or company merges; a non-empty new value wins.
- `invoice_app` has `SELECT, INSERT, UPDATE`. Only the table owner can `DELETE`.

## Read a person's state

Signed in as that person, with a tenant-scoped token (the gateway answers 403 to a tenant-less one; sign in again after the workspace exists):

```
GET /api/notifications/v1/contacts/me
```

| Field | Meaning |
|---|---|
| `email`, `tags` | `tags` holds `registered` and/or `demo request` |
| `marketing_eligible` | they ticked |
| `hubspot.delivered_at`, `resend.delivered_at` | null until delivered |
| `resend.applies` | true for a registrant or a ticked person |
| `mode` | `real`, `fake` or null before the first delivery |

404 when no row exists for the token's email.

Operator read (production, see Erase for the connection):

```sql
SELECT email, registered_at, demo_requested_at, marketing_consented_at, version,
       hubspot_delivered_at, resend_delivered_at, delivery_mode
FROM contacts WHERE email = lower('<address>');
```

## Erase a person

Production Postgres is private-only. Run as the superuser inside the Postgres container.

1. `railway ssh --service Postgres`, then `psql` against `invoice_os`.
2. Delete the row and its queued or kept jobs:
   ```sql
   DELETE FROM river_job WHERE kind = 'contact_deliver' AND args->>'email' = lower('<address>');
   DELETE FROM contacts WHERE email = lower('<address>');
   ```
3. HubSpot: delete the contact in the portal (Contacts, then delete).
4. Resend: delete the contact in the dashboard.

A registrant who signs in again is handed off again and the row returns. Cut the account off first (`docs/identity-provider.md`, "Cutting an account off").
A running job for a deleted row does nothing: the worker returns when it finds no row.

## Owed after merge (U1 to U4)

Production writes are the user's. Project and production environment:

```
P=9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3
E=6c864094-6a06-452f-8495-be77d8a94fe7
```

Until U3, production runs `off` and queues every contact.

**U1 - HubSpot.** In portal 148915098:
- Create contact property `ascomply_contact_tags`, type multiple checkboxes, with options value `registered` / label "registered" and value `demo_request` / label "demo request".
- Create a private app with scopes `crm.objects.contacts.read` and `crm.objects.contacts.write`.
- Keep its token.

**U2 - Resend.**
- Create segment "Registered".
- Create topic "Marketing" with `default_subscription: opt_out` and visibility public. The default cannot be changed later.
- Create an API key with full access.
- Keep the key, the segment id and the topic id.
- Plan contact limit: not recorded. Read it on the Resend plan page and write it here.

**U3 - Railway production, `notifications` service.** After the code reaches production.

Before U3:
- The user or legal approves the wording of the marketing checkbox and the product-email notice.
- A per-IP limit exists on `POST /contacts/demo-request`.
- `GATEWAY_TOKEN` is set on `notifications`.

Write all four variables in one command with skip-deploys. A partial set makes notifications refuse to boot. Read the secrets from a password manager into the shell first (`read -rs HUBSPOT_TOKEN`, and so on) so they stay out of history.

```
railway variables -p "$P" -e "$E" -s notifications --skip-deploys \
  --set "HUBSPOT_TOKEN=$HUBSPOT_TOKEN" \
  --set "RESEND_API_KEY=$RESEND_API_KEY" \
  --set "RESEND_SEGMENT_ID=$RESEND_SEGMENT_ID" \
  --set "RESEND_TOPIC_ID=$RESEND_TOPIC_ID"
railway variables -p "$P" -e "$E" -s notifications --json | jq -r 'keys[] | select(test("^(HUBSPOT_TOKEN|RESEND_)"))'
# expected: four names: HUBSPOT_TOKEN, RESEND_API_KEY, RESEND_SEGMENT_ID, RESEND_TOPIC_ID
railway redeploy -s notifications -p "$P" -e "$E" -y
```

Health check, after the redeploy:

```
curl -sS https://api.ascomply.com/healthz/fleet | jq -r '.services[] | select(.name == "notifications") | .contacts'
# expected: real
```

`off` means the keys did not reach the running container. A refusal to boot shows as `notifications` down and the fleet answers 503 (hence no `-f`).

**U4 - Production proof.**
1. Book one demo on www.ascomply.com with the marketing box ticked. In HubSpot, read the contact: it carries "demo request" (HubSpot MCP `search_crm_objects`, read-only). In Resend, the contact is opted in to topic "Marketing".
2. After production signup opens: register one real person and verify the email. HubSpot shows "registered"; Resend shows segment "Registered", and topic "Marketing" opt-in only if they ticked.
3. As that registrant, with a tenant-scoped token (see Read a person's state), `GET /api/notifications/v1/contacts/me` shows `mode: "real"` and both delivery times.
4. Book a second demo with the same address. Read `ascomply_contact_tags` again: both options, each once.
