# Mock APP Adapter (M5-03)

**Audience:** anyone running a dev or CI environment against the mock Access Point Provider,
anyone writing a demo or fixture dataset (**M5-13** especially), and anyone allocating a new
scripted outcome. The adapter is `internal/submission`'s `mock`; the outcome table below mirrors
`mockAllocations` in `internal/submission/mock_script.go`, and
`TestMockAdapterDoc_DocumentsEveryAllocation` fails if the two drift. Change the table there and
here in the same PR.

## What this is

The mock APP is a **deterministic** stand-in for the FIRS MBS clearance API. It opens no socket
and reads no clock to decide anything: the outcome of a submission is a pure function of the
invoice's **buyer TIN**, and the identifiers it mints are pure functions of the wire bytes. The
same invoice submitted twice, on two replicas, a week apart, produces byte-identical results.

The buyer TIN is the trigger channel because `Submit` is handed only the marshalled wire
(`Adapter.Submit(ctx, Wire, idemKey)`) — it never sees the `Canonical` — so the trigger has to
round-trip through the BIS envelope. It lands where it belongs:
`AccountingCustomerParty.Party.PartyTaxScheme.CompanyID`.

## The reserved block: `99999999-####`

All 10 000 values of `99999999-####` are **reserved permanently** for scripted outcomes.

The `99999999` prefix is not decorative. The shipped v1 rule set seeds `buyer-tin-format`
(`migrations/20260711121327_seed_mbs_v1.sql:16`) at `error` severity with the pattern
`^[0-9]{8}-[0-9]{4}$`. A trigger TIN that failed that regex would be rejected during validation
and would never reach submission at all, so every trigger has to be a well-formed Nigerian TIN.
The block is the highest 8-digit prefix, which is the least likely to collide with a real one.

Every **allocated** trigger is additionally **Luhn-invalid**. `tools/fixturegen`'s `genBuyerTIN`
(`tools/fixturegen/gen.go:151-164`) is the only TIN generator in the repo and it appends a Luhn
check digit, so it *cannot* mint an allocated trigger. Collision is mechanically impossible, not
merely unlikely.

> **Rule for M5-13 and for any anonymizer of real invoices:** never mint a buyer TIN with the
> `99999999-` prefix. A demo or anonymized dataset that lands one would silently arm a scripted
> outcome — a rejected or permanently-failing invoice with no visible cause. Generators must keep
> minting Luhn-valid TINs, which is what makes the guarantee mechanical.

## Allocation table

| Buyer TIN | Trigger | Result | Synth. HTTP | ReachedWire | In-flight wait |
|---|---|---|---|---|---|
| *(anything else, or absent)* | `accept` | `Accepted{IRN, CSID, QRPayload}` | 200 | true | `Latency` |
| `99999999-0001` | `accept` (explicit) | `Accepted{IRN, CSID, QRPayload}` | 200 | true | `Latency` |
| `99999999-0002` | `reject` | `Rejected{1 Reason}` | 422 | true | `Latency` |
| `99999999-0003` | `pending` | `Pending{Ref, PollAfter}` | 202 | true | `Latency` |
| `99999999-0004` | `unavailable` | `Retryable` | 503 + `Retry-After` | true | `Latency` |
| `99999999-0005` | `slow` | `Accepted{…}` | 200 | true | `4 × Latency` |
| `99999999-0006` | `timeout` | `Retryable` | *(none)* | true | `8 × Latency` |
| `99999999-0007` | `connection` | `Retryable` | *(none)* | **false** | *(none — fails in the connect phase)* |

The match is **exact**: no trimming, no case folding. `99999999-0002 ` with a trailing space, a
stray newline from a CSV import, or a non-breaking space is **not** the reject trigger — it takes
the accept path. This is the opposite ruling from `Select`/`IsProduction`, which normalise
because they guard a fail-closed boot gate; normalising here would *widen* the set of inputs that
arm a scripted outcome inside a running pipeline.

**Which one is "permanently-failing"?** No trigger carries that name. `99999999-0004`
(`unavailable`) **is** the permanently-failing allocation: it returns `Retryable` with a
synthesized 503 on every attempt and never converges, so a River retry budget spends itself out
against it. `99999999-0007` (`connection`) also never converges, but it fails in the **connect
phase** — `Evidence.ReachedWire` is false and `app_exchange.outcome` is `connection_failed`
rather than `sent`. Use `-0004` to exercise retry-and-give-up, `-0007` to exercise "nothing left
the process".

`slow` and `timeout` are separate allocations because they leave different evidence: `slow`
records a 200 and a response body after a long wait; `timeout` records neither while still
reporting that the bytes reached the wire.

## Never allocate

| Value | Why |
|---|---|
| `99999999-0008` | The one **Luhn-valid** value in `99999999-000X` — `tools/fixturegen` can mint it, so allocating it would break the collision guarantee above. |
| `99999999-0009` | Already a live, unrelated `SupplierTIN` literal at `internal/invoice/payload_fingerprint_test.go:68`. |

Both are in `mockNeverAllocate` and behave as `accept`, like every other unallocated value in the
block. A future story adding a trigger takes the next free suffix from `-0010` upwards and must
check the new value is Luhn-invalid first.

## Synthesized identifiers

All three are pure functions of what the adapter was handed. No clock, no randomness, no counter.

| Field | Value | Keyed on |
|---|---|---|
| `IRN` | `<docRef>-FBMOCK01-<YYYYMMDD>` | **document identity**: invoice number + issue date |
| `CSID` | `base64url(sha256(wire))`, unpadded, 43 chars | the **whole wire** |
| `QRPayload` | `base64url({"irn","csid","tin","amt","cur"})` | the whole wire (it carries the CSID) |

Both encodings are `base64.RawURLEncoding` — unpadded, URL-safe — matching the rest of the repo.

**The IRN is identity-keyed, not content-keyed, and that is deliberate.** It changes when the
invoice number or the issue date changes, and is deliberately **stable** across a change to an
amount, a line or a party. The real FIRS MBS IRN is invoice-number + service-id + issue-date and
carries no content digest; the resemblance is the point. Do not "fix" this by hashing the wire
into it — the CSID already covers content.

`docRef` upper-cases the invoice number, strips it to `[A-Z0-9-]`, then truncates to 24
characters (in that order — stripping first would delete every lowercase letter, and sanitising
before truncating means the cut can never split a multi-byte rune). An invoice number that
sanitises to nothing at all degrades to `INV` + the first 8 uppercase hex characters of the wire
digest. An unparseable or absent issue date degrades to `00000000`. The IRN is therefore never
blank, for any document, which contract law **L07** requires.

The QR's `tin` is the **supplier's**, not the buyer's. The buyer TIN is the trigger channel, so
stamping it into the payload would put a reserved trigger value inside every accepted invoice.

The rejection speaks two vocabularies at once, on purpose: the synthesized 422 body names the
field as the APP would (`customer.taxIdentifier`), while the `Reason` handed upward names it as
we do (`buyer.tin` — the path `MBSPayload` emits and the shipped `buyer-tin-format` rule
resolves). Mapping between the two is the adapter's job. That is what lets M5-09 point an
operator at a field that genuinely exists, and lets fixing it make the resubmission accept.

## Env knobs

| Variable | Read by | Meaning |
|---|---|---|
| `APP_ADAPTER` | `cmd/submission/main.go` | Set to `mock` to select this adapter. Unset means no adapter, which is fatal in production. |
| `APP_ADAPTER_MOCK_LATENCY` | `MockConfigFromEnv` in `internal/submission/mock_adapter.go`, called from `cmd/submission/main.go` | Boot-time in-flight latency baseline, default `800ms`. `slow` waits 4×, `timeout` waits 8×. Unparseable or **negative** is a hard boot failure; `0s` is legitimate and means instant. |

There is no runtime control surface — adjusting latency without a redeploy is M5-14's scope.
Registering the mock does **not** make it bootable in production: `productionAdapters` stays
empty, so `APP_ADAPTER=mock` with `ENVIRONMENT=production` terminates the process with
`ErrAdapterNotInProd`.

## The pending handle

The `pending` trigger returns `Pending{Ref, PollAfter}`. The `Ref` is
`mockapp-v1.` + base64url of a compact JSON object carrying the **polls remaining** and the exact
IRN/CSID/QR the eventual accept will return. It is fully self-describing — the adapter keeps no
state between calls, so a worker restart or a different replica converges identically.

Each `Poll` returns a **new** `Pending` whose ref has the count decremented, until it reaches zero
and the accept is returned.

> **The caller must persist the ref from *each* `Pending`** (into `submission_jobs.poll_ref`), not
> re-poll the original forever. A caller that re-polls the first ref never converges.

A ref that was not issued by this adapter — wrong prefix, bad base64, bad JSON, a negative poll
count, or a blank IRN — is rejected with an error wrapping `ErrMockUnknownRef`, and `Poll`
reports `Retryable` with `ReachedWire` false.

## See also

- `migrations/20260711121327_seed_mbs_v1.sql:16` — the v1 rule-set seed that pins `buyer-tin-format`
  to `^[0-9]{8}-[0-9]{4}$`, which is why every trigger TIN is 8 digits, a dash and 4 digits.
- `internal/submission/mock_script.go` — the table, the synthesis and the ref codec.
- `internal/submission/mock_wire.go` — the BIS Billing 3.0 envelope the trigger travels in.
