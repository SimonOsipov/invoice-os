# TypeSafe Jev client

**Audience:** anyone setting the TypeSafe key on a Railway service, anyone debugging a
`jev call` log line, and CHECK-03, CHECK-04 and CHECK-05, the stories that wire this
client.

> `internal/platform/jev/doc_test.go` is this page's doc-sync gate. It reads every variable,
> marker, log key and outcome below from the Go and shell source, never from a retyped
> copy. A rename in code fails that test instead of drifting here. The prose is reviewed by
> hand.

## What it is

`internal/platform/jev` asks TypeSafe's Jev model typed questions. In real mode each `Ask`
sends one `POST` to `https://api.typesafe.ai/v1/systemone` (the `endpoint` const), plus at
most one retry. The request carries `Authorization: Bearer <key>` and
`Content-Type: application/json`. The body is `{"state": …, "model": …, "questions": {…}}`.

The model is `jev-latest`, the `Model` const. It is an alias, and the vendor moves it when a
release ships.
On 2026-09-23 it pointed at `jev-1.13.0`.

The caller supplies `State` as one string and a map of questions keyed by question id. A
request names one `Purpose`: `value_check`, `document_type` or `mapping_check`. Each question
has one of three types:

| Type | The caller sets | Sent as `criteria` | The answer carries |
|---|---|---|---|
| `noul` | `Instructions`, and optionally `True` and `False` | `{"true": …, "false": …}` with only the non-empty keys; no `criteria` when both are empty | `Noul`, the probability of yes, in `[0, 1]` |
| `choice` | `Instructions`, two or more `Options`, and a `Default` | A map of each option's `Name` to its `Description`, `null` for an empty description; `encoding/json` sorts the keys | `Choice`, one of the asked option names, and `Confidence` in `[0, 1]` |
| `score` | `Instructions`, two or more `Options` ordered low to high, and a `Default` | The level descriptions, in `Options` order; a score option's `Name` is never sent | `Score` in `[0, len(Options) − 1]`, and `Confidence` in `[0, 1]` |

`Default` names the option the fake answers. It is never sent.

`Ask` returns a `Response`. `Answers` holds one `Answer` per asked question id. `Usage`
holds `InputTokens` and `OutputTokens`, summed over attempts. An answer id nobody asked is
dropped. A missing `usage` object counts as zero tokens. A missing answer, an answer of the
wrong type, a `null` required field, a value out of range or a malformed `usage` (such as a
fractional `input_tokens`) fails the whole call.

Every error `Ask` returns satisfies `errors.Is(err, jev.ErrCheckSkipped)`. The text names
the outcome only: `jev: check skipped: off`, `jev: check skipped: refused` or
`jev: check skipped: unavailable`. A result that a fake marker produced adds `(fake)` to the
text. When the caller's own context ends, the error also wraps `ctx.Err()`, for example
`jev: check skipped: unavailable: context canceled`. No status code, URL, response body,
state, question text or key reaches the text. On any error the `Response` is empty.

An enabled client refuses a request before it sends anything when:
- the purpose is not one of the three;
- `State` is `""`;
- there are no questions, or a question id is `""`;
- a question's type is not one of the three, or its `Instructions` is `""`;
- a `choice` or `score` has fewer than two options, an empty or repeated option name, or a
  `Default` that names none of its options;
- a `score` level has an empty `Description`.

The refusal is `skipped_refused` with `attempts` `0`, in real and fake mode alike.

`submission`'s extraction worker asks the value questions and the `document_type` question
in one call per extraction attempt, on its Docling text branch only
(`internal/extraction/jevcheck.go`); a retried job asks again. Only CHECK-05 is still to
come: it will call the client from `invoice`'s importer.

## Env knobs

| Variable | Read by | Meaning |
|---|---|---|
| `TYPESAFE_API_KEY` | `FromEnv` in `env.go` (`EnvKey`) | The vendor key. Unset or `""`, with `JEV_FAKE` not true, means the client is off: `Enabled()` returns false, and every `Ask` returns `jev: check skipped: off` having sent nothing. Any other value counts as set, whitespace included. The value is never trimmed. A key holding a control byte other than tab, or DEL, refuses every call before anything is sent. The vendor's own SDK reads the same name. |
| `JEV_FAKE` | `FromEnv` in `env.go` (`EnvFake`) | `true` per `strconv.ParseBool` selects fake mode: no network call, and a result steered by a marker in `State`. Unset or `""` is not fake. Any other unparseable value makes `FromEnv` return an error naming `JEV_FAKE`. The value is never trimmed, so `"true "` is an error. `JEV_FAKE` true with a non-empty `TYPESAFE_API_KEY` makes `FromEnv` return an error naming both variables, and no client. This differs from `AI_FAKE`, which wins over a key. |

`FromEnv` never exits the process. It returns an error in two cases: an unparseable
`JEV_FAKE`, and `JEV_FAKE` true with a key set. The error text never holds the key. A caller
treats that error as fatal at boot. `cmd/submission` does, as it and `cmd/invoice` do for an
`ai.FromEnv` error.

The model, the endpoint, the budget and the retry wait are constants. No variable changes
them.

## Per environment

| Environment | `TYPESAFE_API_KEY` | `JEV_FAKE` | Client | Set by |
|---|---|---|---|---|
| production (persistent) | unset | unset | off | Nobody. No production key exists until the user resolves the data terms (see "Data terms"). `set-ai-fake` refuses this environment's id. |
| `pr-<N>` (ephemeral fork) | `""` on `submission` and `invoice` | `true` on `submission` and `invoice` | fake | `set-ai-fake <env-id>` in `dev-env.yml`'s `prepare-env` job, PR-only, no `continue-on-error`. |
| local compose / developer shell | unset | unset | off | Nobody. |

The key goes on `submission` and `invoice` only.

**Fork rule.** `prepare-env` creates each `pr-<N>` as a fork of the persistent environment,
and the fork copies its variables. A key set on production would therefore reach every fork.
Sealing the key cannot stop that: `audit-sealed-variables` fails `prepare-env` whenever the
source environment holds a sealed variable. `set-ai-fake` is the one defence. It runs on
every PR `prepare-env` run and refuses any environment that is not ephemeral. For each of
`submission` and `invoice` it:
1. upserts `JEV_FAKE=true` and `TYPESAFE_API_KEY=""`, beside the same pair for the AI client;
2. reads `JEV_FAKE` back and fails the job unless it is `true`;
3. re-reads the variable map and fails the job if `TYPESAFE_API_KEY` or
   `OPENROUTER_API_KEY` is anything but absent or `""`.

The key check never prints a value. `set-ai-fake --self-test` runs its fixtures with no
token and no network call.

## Fake mode markers

In fake mode `Ask` sends nothing. The leftmost marker in `State` steers the result:

| Marker in `State` | Fake result |
|---|---|
| *(none)* | No doubt: every `noul` answers `1`; every `choice` answers its `Default` at confidence `1`; every `score` answers its `Default` level's index at confidence `1`. |
| `JEVFAKE-DOUBT` | Every `noul` answers `0`. `choice` and `score` answer as the no-marker row. |
| `JEVFAKE-CHOICE-<base64url option name>` | Every `choice` answers the named option at confidence `1`. `noul` and `score` answer as the no-marker row. A payload that does not decode refuses the call, whatever the questions. A `choice` whose options do not list the name refuses the call. A call with no `choice` question and a payload that decodes answers as the no-marker row. |
| `JEVFAKE-UNAVAILABLE` | `jev: check skipped: unavailable (fake)` at once, with no wait. |
| `JEVFAKE-REFUSED` | `jev: check skipped: refused (fake)` at once. |

The marker is read from `State` only. Question text and option descriptions never steer the
fake. Matching is case-sensitive and needs no word boundary, so `scan-JEVFAKE-DOUBT.pdf`
matches. An `AIFAKE-` marker does not steer this fake. `JEVFAKE-DOUBT` doubts every checked
field of a document, so `jev_doubt_invoice.pdf`, which has one checked field, is the deployed
fixture that uses it.

**Building a choice marker.** Encode the option's `Name` with `base64.RawURLEncoding`: the
URL-safe alphabet, with no padding. For the option `credit note` the marker is
`JEVFAKE-CHOICE-Y3JlZGl0IG5vdGU`. The payload class is `[A-Za-z0-9_-]+`, so the payload ends
at the first character outside that class. A standard-alphabet `+` or `/` therefore cuts the
payload short. The class is greedy: a letter, digit, `_` or `-` right after the payload joins
it. End the marker with a non-word character other than `-`, such as a space, a `.` or a line
break. In `scan-JEVFAKE-CHOICE-Y3JlZGl0IG5vdGU_v2.pdf`, `_v2` joins the payload, so a call
with a `choice` question is refused. The payload needs at least one character: a bare
`JEVFAKE-CHOICE-` is not a marker, so a later marker in `State` can still match.
`jev_receipt_invoice.pdf` is the deployed fixture that uses `JEVFAKE-CHOICE-`: it prints
`JEVFAKE-CHOICE-cmVjZWlwdA`, which answers `receipt`.

Every fake result logs outcome `fake`, `attempts` `1` and `input_tokens` `0`, the marker
errors included: `JEVFAKE-UNAVAILABLE`, `JEVFAKE-REFUSED`, and a `JEVFAKE-CHOICE-` payload
the call refuses, which returns `jev: check skipped: refused (fake)`. Fake mode validates the
request exactly as the real path does. An invalid request is `skipped_refused` with
`attempts` `0` and the text `jev: check skipped: refused`, with no `(fake)`, because the fake
never ran.

The fake ignores the caller's context. A cancelled or expired context in fake mode still
gets the result the marker table names: the fake answers with a nil error, or returns the
marker's error.

## Retries and the budget

One `Ask` has a total budget of `3s` (the `budget` const) and makes at most two attempts.
Each attempt is capped at `(budget − retryWait) / 2`, which is `1.375s`, so a hung first
attempt leaves room for the retry. A fixed wait of `250ms` (the `retryWait` const) comes
before the one retry. The retry is sent only when more than that wait remains before the
deadline. The worst case is `1.375s` + `250ms` + `1.375s`, which is the whole `3s`.

**Retried once:** a transport error, an attempt timeout, HTTP 408, HTTP 429, and HTTP
500–599 (529 included). **Not retried:** 401, 422, every other non-2xx status, a 3xx that
`net/http` hands back without a `Location`, and a 2xx whose body does not decode or fails the
answer check. Any 2xx status is a success once its answers pass the check. `Retry-After` is
not read.

The caller's context wins. When it is cancelled or expires, during an attempt or during the
wait, `Ask` returns at once with `skipped_unavailable`, and the error wraps `ctx.Err()`. A
spent budget does not wrap `context.DeadlineExceeded`: the budget is the client's clock, not
the caller's.

## The log line

One `jev call` line at INFO per `Ask`, on every path, through the logger passed to
`FromEnv`. A nil logger writes nothing. Keys, in emission order (`log.go`):

| Key | Meaning |
|---|---|
| `tenant_id` | From the caller context's identity, via `auth.IdentityFromContext`. Omitted when there is no identity or its tenant is empty; never logged blank. |
| `purpose` | The request's `Purpose`, logged raw: an invalid purpose is logged as sent. |
| `question_count` | The number of questions in the request. |
| `input_tokens` | The vendor's `usage.input_tokens`, summed over attempts, failed attempts included. `0` for `off` and `fake`. |
| `latency_ms` | Whole milliseconds across the entire `Ask`, including the retry wait. |
| `attempts` | `0` for `off`, for a request refused by validation and for a key refused by the header check. `1` for `fake`. Otherwise the attempts entered: a context already cancelled when `Ask` starts logs `1`. |
| `outcome` | One of the five values below. |

Plus slog's own `time`, `level` and `msg`. The line carries no state, question text, option,
answer, key, file name or response body. `output_tokens` is not logged: the vendor does not
bill output tokens. The line is written with a background context, so the platform's
context-aware handler cannot add a second `tenant_id`. The cost is that `request_id` never
appears on this line. In a deployed binary the process logger adds its own base fields,
`service` and `environment`.

### Outcomes

| Outcome | When | Retried | Error text |
|---|---|---|---|
| `ok` | A 2xx whose answers pass the answer check. | – | none |
| `skipped_unavailable` | A transport error, an attempt timeout, 408, 429 or 500–599 once the retry is spent or does not fit. A 2xx whose body does not decode or fails the answer check. The caller's context cancelled or expired. | once, for the first group only | `jev: check skipped: unavailable`, plus the caller's `ctx.Err()` when its context ended |
| `skipped_refused` | 401, 422 and every other non-2xx, including a 3xx with no `Location`. A request refused by validation, or a key holding a byte invalid in a header value: nothing is sent, and `attempts` is `0`. | no | `jev: check skipped: refused` |
| `off` | No key, and `JEV_FAKE` not true. Nothing is sent. | – | `jev: check skipped: off` |
| `fake` | Fake mode answered once validation passed, including the `JEVFAKE-UNAVAILABLE` and `JEVFAKE-REFUSED` errors and a refused `JEVFAKE-CHOICE-` payload. | – | none, or the marker's error with `(fake)` |

## A skipped check changes nothing on screen

`off`, `skipped_refused` and `skipped_unavailable` mean one thing to a caller: skip the check.
So does a `fake` result that returns an error. A caller tests
`errors.Is(err, jev.ErrCheckSkipped)` and carries on as if the check did not exist.
- A skipped check changes nothing on screen. The `jev call` log line is its only trace.
- A skipped check never sends a document to manual entry. This differs from the AI client,
  where a failed read quarantines the document for manual entry.

CHECK-03, CHECK-04 and CHECK-05 keep this rule when they wire the client.

## Data terms

In real mode each `Ask` sends `State` to TypeSafe, and for each question its id, its
`Instructions` and its `criteria` as the type table under "What it is" defines them: a
`noul`'s `True` and `False` text, every `choice` option `Name` with its `Description`, and
every `score` level description. The data terms for that transfer are unresolved. The user owns them. No production
key exists, and none is set until the user resolves them.

The vendor's Legal page (`https://docs.typesafe.ai/legal.md`, read 2026-09-23) lists a Data
Processing Agreement. The listing says the agreement covers how the vendor processes customer
data, including data retention. This page makes no claim about whether that agreement is
adequate.

## Known limitations

1. The value check's threshold `0.5` and its wording were measured on 21 synthetic documents
   (`CHECK-00 Jev Measurement Results`). Re-measure them on real documents before trusting the
   check in production. No versioned id is pinned: `jev-latest` can move under that threshold
   (on 2026-09-23 it pointed at `jev-1.13.0`), and the version that answered is not logged.
   The document-type threshold `0.9` is unmeasured: every answer on the same 21 synthetic
   documents, whose non-invoices announce their type, scored at least `0.9`, so no swept cut
   from `0.30` to `0.90` removed one. It shipped as it is (user decision, 2026-09-24).
2. `skipped_refused` conflates 401 (a revoked key), 422 (a client bug, or a `state` beyond
   the vendor's context limit), a key refused by the header check, and every other
   non-retryable status. Neither the error nor the log names the status. A revoked key
   refuses every call; an over-long `state` refuses only the calls that carry one. The
   context limit is 32k tokens for
   `state` plus the longest question (the vendor's `models.md`). No size guard exists: an
   over-limit document is refused by the vendor, logged `skipped_refused`, and written as
   decided.
3. `skipped_unavailable` conflates a spent budget, a failed answer check and a cancelled
   caller context. The error tells the last one apart: it wraps `ctx.Err()`.
4. The PR key audit reads `submission` and `invoice` only. A key an operator set anywhere
   else would fork unaudited. The `OPENROUTER_API_KEY` audit has the same scope.
5. The `3s` budget and its `1.375s` per-attempt cap are accepted as they are; the measured
   all-attempts latency max was 517 ms (`CHECK-00 Jev Measurement Results`). A timed-out
   attempt may still be processed and billed by the vendor, so a retry can bill a call twice.
6. `cmd/submission` is wired: an unset or empty key boots it, and either `FromEnv` error
   stops it at boot by design. `FromEnv` alone is proved by
   `TestFromEnv_DoesNotExitTheProcess`. `invoice` stays inert until CHECK-05 wires it.
7. A key holding a control byte other than tab, such as a pasted trailing newline, refuses
   every call. `FromEnv` does not trim it.
8. `net/http` follows a 3xx that carries a `Location`: 301, 302 and 303 turn the `POST`
   into a `GET`, and `Authorization` is forwarded only to the same host or a subdomain. The
   response body is read with no size cap. Both match the AI client.
9. `State` holding invalid UTF-8 is not sent byte for byte. `encoding/json` replaces each
   invalid byte with U+FFFD, because JSON cannot carry such bytes.
10. The value questions and the `document_type` question share one call, and the client
    fails the whole call on any unusable answer. So one bad answer of either kind skips both
    checks for that document: it is logged `skipped_unavailable`, and the screen is unchanged.
    `TestAsk_OneUnusableAnswerInAMixedRequestFailsTheWholeCall` proves it.

## See also

- `internal/platform/jev/`: `client.go`, `env.go`, `fake.go`, `log.go`.
- `scripts/ci/railway-env.sh`: `set-ai-fake` and its `--self-test` fixtures.
- `docs/ai-client.md`: the OpenRouter client, whose shape this client follows.
- `docs/add-a-service.md`, section 4: secrets live only in Railway service variables.
