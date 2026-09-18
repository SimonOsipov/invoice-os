# AI client

**Audience:** anyone setting the OpenRouter key on a Railway service, anyone debugging an
`ai call` log line, and AIR-03, AIR-05, and AIR-07 — the stories that will call this client.

> **One caller today.** `submission`'s extraction worker calls `FromEnv` and calls the
> client on a text document when the client is enabled
> (`TestRLS_ExtractWorkerWithTheAIOffWritesTodaysRows` pins the off case). AIR-07 wires
> the `invoice` importer next, and AIR-05 fills the `FakeHint` channel described below.
> `doc_test.go`'s `TestAIDoc_*` suite is this page's doc-sync gate — every name below is
> parsed out of the Go and shell source, never retyped, so a rename in code fails this
> page's test rather than drifting silently, the same convention `docs/mock-app-adapter.md`
> follows for its own allocation table.

## What it is

One POST per `Call` to OpenRouter's chat-completions endpoint (`endpoint`, a constant, not
a variable — `https://openrouter.ai/api/v1/chat/completions`), using the model
`google/gemini-3.5-flash-lite` (the `Model` const). The request asks for `json_schema`
output with `strict: true`; the schema and its name come from the caller. The body also
sends `provider.require_parameters: true` and `temperature: 0`, and does **not** send the
deprecated `usage: {include: true}` field.

Input is `Text`, `Pages`, or both. Each page is a PNG, base64-encoded with standard
(padded) encoding and sent as `data:image/png;base64,<data>` — a text part, when present,
always comes first in the message, with the image parts following it. `Call` returns the
parsed answer as `map[string]any`, decoded with `json.Number` so integer and enum
comparisons stay exact. The client holds its own `&http.Client{}` with no `Timeout` set;
the context passed to each attempt is the clock instead.

## Env knobs

| Variable | Read by | Meaning |
|---|---|---|
| `OPENROUTER_API_KEY` | `FromEnv` in `env.go` | Unset or `""`, with `AI_FAKE` not true, means the client is off: `Enabled()` returns false and every `Call` returns `ErrOff` having sent nothing. Any other value counts as set, whitespace included — the value is never trimmed or validated as a plausible key shape. |
| `AI_FAKE` | `FromEnv` in `env.go` | `true` per `strconv.ParseBool` selects fake mode — no network call, an answer steered by a marker instead — and it wins over a key: with `AI_FAKE=true`, a set `OPENROUTER_API_KEY` is never used for the wire request. Unset or `""` is not fake. Any other unparseable value makes `FromEnv` return an error naming `AI_FAKE`; `FromEnv` never exits the process. The value is never trimmed, so `"true "` (trailing space) is an error. |

Model, endpoint and the retry budget are constants — there is no knob for any of them.

## Per environment

| Environment | OPENROUTER_API_KEY | AI_FAKE | Set by |
|---|---|---|---|
| production (persistent) | set by an operator, unsealed | unset | An operator, directly in the Railway dashboard. `railway-env.sh set-ai-fake` refuses to run against this environment's id. |
| `pr-<N>` (ephemeral fork) | `""` on both `submission` and `invoice` | `true` on both `submission` and `invoice` | `set-ai-fake <env-id>` in `dev-env.yml`'s `prepare-env` job, PR-only, no `continue-on-error`. Each write is independently re-read; the job fails if either service still holds a usable key. |
| local compose / developer shell | unset | unset | Nobody — the client is off. |

## Fake mode markers

| Marker in the input | Fake answer |
|---|---|
| *(none)* | Every top-level property of the caller's schema, blank (`null`) |
| `AIFAKE-UNAVAILABLE` | `ErrUnavailable` at once — no request is sent and no wait happens (`attempts` is still logged as `1`, the fake answer itself) |
| `AIFAKE-ANSWER-<base64url>` | That decoded JSON object, after the same schema check a real answer gets |

The marker is searched in `Request.Text` first, then in `Request.FakeHint`; `Request.System`
is never scanned. The first match in a field wins. Matching is case-sensitive with no word
boundary, so `scan-AIFAKE-UNAVAILABLE.pdf` still matches. The payload after
`AIFAKE-ANSWER-` is `base64.RawURLEncoding` (unpadded), payload class `[A-Za-z0-9_-]+`, so
the match stops at the first character outside that class. Trailing `=` padding is
therefore harmless — what is left is exactly the unpadded encoding and still decodes — but
a standard-alphabet `+` or `/` cuts the payload mid-string and the call errors
(`TestFake_BadAnswerIsNotUnavailable`, case `std_encoding_breaks_urlsafe_decode`). A bad
payload after decoding is a plain error that is not `ErrUnavailable`. Fake mode validates
the request exactly as the real path does; a `FakeHint` with no `Text` and no `Pages` is
still an invalid request.

## Document reading

`submission`'s extraction worker calls `Call` once per extraction job attempt that reaches
the text read -- never per line item, never for a document with no text layer (AIR-05); a
River retry re-runs the job and calls again. A blank answer, the fake's own default, leaves
the engine's own reading of every field untouched -- that is what the fake answers whenever
the document text carries no `AIFAKE` marker. The steered reading only happens when the
fixture's `AIFAKE-ANSWER-` marker is present: AIR-03-05's fixture carries one and answers
`invoice_number` `20417`, `buyer_tin` `87654321-0002` and `buyer_name` `ZENITH HOLDINGS
LIMITED`; its e2e spec pins how those three land beside the engine's own reading.

When `Call` returns any error except `ErrOff` or a cancelled or expired caller context, the
worker writes one `document_ai_reading` row (value null, reason `unreadable`) and no engine
reading, and the importer quarantines the document with no reading; the person enters it by
hand (AIR-04). On a `pr-<N>` deploy, `AIFAKE-UNAVAILABLE` printed on the document forces this
path: `ai_unavailable_invoice.pdf` and AIR04-E2E-01 pin it. The fake logs outcome `fake` for
that call. Production logs `unavailable` when the budget is spent, or `refused`.

## Retries and the budget

One `Call` has a 15s budget (the `budget` const), not an attempt-count limit. **Retried:**
a transport error, an attempt timeout, HTTP 429, HTTP 500–599, and any 2xx response whose
body will not decode, carries a non-null `error` object, has no `choices`, is not exactly
one JSON object, or fails the caller's schema. **Not retried:** every other non-2xx status
— HTTP 408 included — and any 3xx; those return `refused` after exactly one request, and no
response body text ever reaches the returned error.

Backoff starts at 250ms and doubles each attempt, capped at 2s. The loop stops once the
time left before the budget's deadline is no more than the next wait, which against a
permanently failing endpoint works out to 10 attempts and 13.75s slept in total
(`TestCall_BackoffIsCappedSoTheBudgetFitsTenAttempts` pins both numbers). The budget, not
an attempt count, ends the loop, and the exhausted call satisfies
`errors.Is(err, ErrUnavailable)`. The caller's context always wins over the budget: with a
cancelled context, or a deadline shorter than 15s, the call returns as soon as that
context ends, carrying `context.Canceled` / `context.DeadlineExceeded`, neither of which
matches `ErrUnavailable`.

## The log line

One `ai call` line at INFO, written once per `Call`, on every path, whatever the attempt
count. Keys, in emission order (`log.go`):

| Key | Meaning |
|---|---|
| `tenant_id` | From the caller context's identity, via `auth.IdentityFromContext`. Omitted entirely when there is no identity or its tenant is empty — never logged as a blank string. |
| `model` | Always the `Model` const. |
| `purpose` | `document` or `spreadsheet`, logged raw — an invalid purpose is still logged as what was asked for. |
| `input_tokens` | OpenRouter's `usage.prompt_tokens`, summed over every attempt, zero when no response carried usage. |
| `output_tokens` | OpenRouter's `usage.completion_tokens`, summed the same way. |
| `cost` | OpenRouter's `usage.cost`, summed the same way. |
| `latency_ms` | Whole milliseconds across the entire `Call`, including every retry wait. |
| `attempts` | `0` for `off` and a validation `refused` (nothing was ever sent), `1` for `fake`, the real attempt count otherwise. |
| `outcome` | One of the five values below. |

Plus slog's own `time`, `level` and `msg`. The line carries no prompt, text, fake hint,
page bytes, answer, schema name, file name or error body. It is written with a background
context so the platform's context-aware log handler cannot attach a second `tenant_id`;
the cost of that choice is that `request_id` never appears on this line.

In a deployed binary the process logger adds its own base fields, `service` and
`environment`, to every line it writes — this one included. How wide a deployed line ends
up is the process logger's business, not this package's, so this page does not say.

### Outcomes

| Outcome | Meaning |
|---|---|
| `ok` | A 2xx answer that passed the schema check. |
| `unavailable` | The retry budget was spent, or the caller's context was cancelled or expired. |
| `refused` | An invalid request (nothing was ever sent) or a non-retryable HTTP status. |
| `off` | No key and not fake — nothing was sent. |
| `fake` | Fake mode answered, including by returning `ErrUnavailable` via `AIFAKE-UNAVAILABLE`. |

## Where the key lives

Only in the Railway service variable set of each service that calls the client —
`docs/add-a-service.md`, section 4, "Secrets live only in Railway service variables":
never in the repo, never in `railway.json`, never as a real value in an example file (the
Go binaries ship no `.env.example`; that is the M2-12 decision recorded in the same
section).

And never as a **sealed** variable: `audit-sealed-variables` fails `prepare-env` whenever
the source environment holds any sealed variable, because `prepare-env` forks the source
environment's variables into every `pr-<N>`. So the production key has to stay unsealed,
which means every fork inherits it — which is exactly why `set-ai-fake` runs and blanks
it before any forked service deploys.

## Known limitations

1. **One binary wired.** `submission` calls `FromEnv` and reads through the client.
   Its two deployed checks are recorded on PR #247's deploy-gate run 35341117286
   (`dev-env.yml`): the `prepare-env` step "Force AI fake mode and blank the AI key
   in the fork" read `submission.OPENROUTER_API_KEY is empty` and
   `submission.AI_FAKE = true`, the fleet health gate passed, and the Railway
   deploy log for the submission instance on `pr-247` carried `ai call` lines with
   `purpose: document` and `outcome: fake`. `invoice` (AIR-07) and the `FakeHint`
   channel (AIR-05) still owe the same two checks for their own wiring.
2. **`unavailable` conflates two causes.** A spent budget and a cancelled-or-expired
   caller context both log it. The returned error distinguishes them —
   `errors.Is(err, ErrUnavailable)` is true only for the spent budget — but the log line
   alone cannot.
3. **`FakeHint` has no deployed channel.** The document opener drops the upload filename
   today, so only `Text` can steer a deployed fake. AIR-05 owns the channel; until then
   `FakeHint` is a test-only field.
4. **Railway does store an empty-string variable value** — measured, not assumed. On the
   first `prepare-env` run of a ready PR, both services' re-read verdicts came back
   `is empty` rather than `is absent`, so `variableUpsert` accepted `""` and persisted it.
   The verdict in `ai_key_verdict` passes on either shape, absent or exactly `""`, so no
   code depends on which one Railway chooses; a rejection would have failed `prepare-env`
   loudly rather than deploying a fork with an inherited key.
5. **(operator-facing)** The verdict matches the exact variable name. A key stored under
   a different case reads as absent and passes, which is correct because the Go client
   reads the exact name too.

## See also

- `internal/platform/ai/` — `client.go`, `env.go`, `fake.go`, `log.go`, `schema.go`.
- `scripts/ci/railway-env.sh` — `set-ai-fake` and its `--self-test` fixtures.
- `docs/add-a-service.md`, section 4 — the secrets rule and the M2-12 `.env.example`
  decision.
