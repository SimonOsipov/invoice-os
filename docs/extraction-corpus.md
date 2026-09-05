# The extraction corpus

Six synthetic invoice PDFs under `internal/extraction/testdata/`, named `corpus_*.pdf`, are the
golden corpus the Tier-1 anchor rules are measured against. Each one arranges its fields
differently, and carries a different subset of them, so a rule that only works on one
arrangement fails visibly on another. Two layouts carry all ten fields; the other four carry
between four and seven. This page records what each layout exercises, how to regenerate the
bytes, how to scrub an anonymised real document into a new layout, and who answers when a
client's invoice fails to extract.

**The corpus is 100% synthetic and must stay that way.** No real client document may be
committed to this repository, in any form, scrubbed or not. None has been supplied, and
EXTR-04's constraint forbids it. What a real failure contributes is its *arrangement* —
reproduced as a generator entry — never its bytes.

The corpus is generated, not drawn. `internal/extraction/fixtures_test.go` holds a builder per
layout and the committed `.pdf` beside it; `TestFixtures_MatchTheirGenerator` regenerates and
byte-compares, so neither side can drift alone. The generator is stdlib-only, and
`TestFixtures_GeneratorIsDeterministic` builds each layout twice in one process, so nothing in
it may read a clock, a random source, or a map in iteration order.

## The six layouts

One `fxLine` is one PDF `Tj` operator is one pdfium token. That is the whole lever the author
has over token granularity: a label and its value on one `fxLine` arrive as a single token, and
on two `fxLine` values as two. pdfium appends a trailing space to a text rect that is followed
by another on the same line, so a split label reads `"Invoice No "` and not `"Invoice No"`;
compare trimmed.

| Layout | What it exercises | Tokens | Bytes |
|---|---|---|---|
| `corpus_inline_labels.pdf` | `same_token` for all ten fields — every `Label: value` is one token. Carries no bare-TIN token, so the format-only sweeps deliberately cannot fire here. | 11 | 1117 |
| `corpus_split_labels.pdf` | `right` — label and value on one baseline as two tokens, ~0.15 normalised apart. Its buyer TIN sits at `Y0` 0.53, in the lower half the buyer sweep needs. Its date, `15/04/2026`, has a day above 12 and is deliberately unambiguous. | 21 | 1429 |
| `corpus_stacked_labels.pdf` | `below` — the only layout where *every* label's value sits under it, 16pt down at the same `x`, invoice number and date and total included. Every value `corpusExpect` requires from a `below` rule here sits at most 0.009111 normalised under its label, and the next group's label is no closer than 0.087010, so a `below` rule anchored on a label cannot span two groups. Since EXTR-16 a bare label is not a value, so what bounds the dial is the next group's *value*: 0.107212 down, an 11.77x window, with the buyer's name 0.321571 down behind it. (The widest *intra-group* gap is 0.026631, a party block's TIN line that no expectation requires; `TestCorpus_StackedValuesSitBelowTheirLabels` asserts that one and the 0.087010 separation, and `TestTier1_DialsStayInsideTheirMeasuredWindow` asserts the window.) `corpus_two_column.pdf` stacks its two party blocks the same way, so `below` reaches the name fields there too; what is unique here is that nothing else in this layout is inline. Its bare TINs are not unique either — `corpus_split_labels.pdf` carries one in each page half as well. | 13 | 1109 |
| `corpus_two_column.pdf` | Column bands. Supplier labels centre at X 0.15–0.21 (band 0), buyer labels at 0.68–0.74 (band 2). It is the only layout whose anchor labels reach the right-hand third at all, and so the only one whose fingerprint carries a band above 1; `TestCorpus_TwoColumnPartiesLandInTheOuterBands` enforces both halves of that. Both TINs sit inside a longer token (`TIN: 99999999-0401` and `TIN: 99999999-0402`, both at `Y0` 0.2341), so neither format-only sweep can fire and neither is separated by page half. Under Tier-1 they are not separated by label either: the `supplier_tin` pattern's party word is optional, so a bare `TIN` label matches it and `supplier_tin` collects **both**, while `buyer_tin` — whose party word is required — is unreachable on this layout. That is a Tier-1 accuracy defect, not a corpus defect. EXTR-04-09 measured it and carried it forward rather than closing it: widening the lexicon would change every stored document's fingerprint, so the fix needs a `FingerprintVersion` bump. | 10 | 1031 |
| `corpus_ambiguous_date.pdf` | `12/03/2026` — both components at most 12 and no month name, so `ShapeDate` returns both readings and `issue_date` keeps two candidates. The one layout whose expectation row carries two values. | 6 | 873 |
| `corpus_totals_block.pdf` | The lexicon overlap: `Sub-total` matches both `subtotal` and `\btotal\b`, because `-` is a non-word character. The `subtotal` entry claims the wider span of that token, so since EXTR-16 the `total` rule does not anchor there and the overlap mints one candidate, not two. Right-aligned split totals. The VAT label carries no percentage — a `7.5%` remainder would mint a spurious amount candidate. | 9 | 938 |

Every TIN is drawn from the free part of the reserved `99999999-` block: `-0101`, `-0102`,
`-0201`, `-0202`, `-0301`, `-0302`, `-0401`, `-0402`, `-0501`, `-0601`. The whole block is
reserved by `internal/submission/mock_script.go`, and the suffixes `-0001` through `-0009` are
spoken for inside it: `-0001`…`-0007` are the mock adapter's scripted submission outcomes and
`-0008`/`-0009` are its never-allocate pair. A corpus TIN in that range would make an
extraction fixture double as a submission trigger.
`TestCorpus_UsesOnlyFreeReservedTINs` enforces both halves of that rule.

## What Tier-1 must produce

`corpusExpect` in `internal/extraction/corpus_test.go` is the expectation table: one row per
layout, mapping a `HeaderFields` key to every value that is a correct answer. Values are the
normalised forms the shapes emit — amounts `-?\d+(\.\d{1,2})?`, dates `2006-01-02`, currency
upper-cased.

A field absent from a row is **not** asserted. The corpus measures Tier-1's reach, and
pretending an unreached field is a pass would inflate it. `corpus_stacked_labels.pdf` and
`corpus_two_column.pdf` therefore expect seven fields, not ten. Candidate *rank* is not
asserted in the table either; EXTR-04-09 owns the match semantics and the accuracy floor, and
that floor's constant lives with the test that enforces it, not here.

`TestTier1_ReachesEveryCorpusExpectation` resolves every layout against `Tier1Rules` alone and
fails on any expected value the shipped set cannot reach. One pair is exempt and listed in
`t1aGaps`: `corpus_two_column.pdf` / `buyer_tin`, per that layout's row above. The exemption is
asserted still-unreached, so closing it is a deliberate diff rather than a silent
pass.

## Tier-1 recall and the floor

Measured 2026-08-29 on `feature/extr-04-anchor-rules-and-field-resolution`: the shipped Tier-1
set reaches **43 of 44** of the (layout, field) pairs `corpusExpect` names — **0.9773**. The
denominator is the pairs the table actually asserts, so a field absent from a row is not counted
and the ambiguous-date row's two accepted readings are one pair, not two.

That number is **recall**. A pair is a **hit** when the expected value appears **anywhere**
among that field's candidates; which of them the pipeline goes on to decide is not read here at
all. It is **not** end-to-end accuracy. EXTR-04 shipped it as the headline accuracy figure and
it never was that: recall is monotone in the candidate list, so it read 43/44 while every party
name on every layout decided a label. What the pipeline decides is recorded under **Tier-1
decision rate** below. The two numbers coincide today; they are still two measures.

| Layout | Hits | Pairs |
|---|---|---|
| `corpus_inline_labels.pdf` | 10 | 10 |
| `corpus_split_labels.pdf` | 10 | 10 |
| `corpus_stacked_labels.pdf` | 7 | 7 |
| `corpus_two_column.pdf` | 6 | 7 |
| `corpus_ambiguous_date.pdf` | 5 | 5 |
| `corpus_totals_block.pdf` | 5 | 5 |

| Field | Hits | Pairs |
|---|---|---|
| `invoice_number` | 6 | 6 |
| `issue_date` | 5 | 5 |
| `supplier_tin` | 6 | 6 |
| `supplier_name` | 5 | 5 |
| `buyer_tin` | 3 | 4 |
| `buyer_name` | 4 | 4 |
| `currency` | 2 | 2 |
| `subtotal` | 3 | 3 |
| `vat` | 3 | 3 |
| `total` | 6 | 6 |

The single miss is `corpus_two_column.pdf` / `buyer_tin`, the pair `t1aGaps` records: that
layout's bare `TIN` labels match the `supplier_tin` pattern, whose party word is optional, and
never `buyer_tin`, whose party word is required. It is a Tier-1 lexicon defect carried forward,
not a corpus defect — closing it changes `anchorLexicon`, which is an input to `Fingerprint`.

### Moving the floor

The floor lives in `internal/extraction/accuracy_test.go` as two pinned integers,
`tier1RecallHits` and `tier1RecallPairs`; `tier1RecallFloor` is their quotient.

1. Re-measure: `go test -count=1 -v -run TestTier1Accuracy ./internal/extraction/`. The report
   prints the two tables above. CI prints it too, from the `go` job's own reporting step — the
   gated step runs this package through `rlsgate`, which deletes a passing test's output.
2. Edit `tier1RecallHits` and `tier1RecallPairs` to the measured values.
3. Update both tables in this section **in the same commit**, or
   `TestCorpusDoc_RecordsTheMeasuredFloor` (per layout) and
   `TestCorpusDoc_ThePerFieldTableMatchesTheMeasurement` (per field) fail: each parses its own
   table's rows and compares them against a live measurement, so a table that sums correctly
   with the numbers in the wrong rows is still red.
4. If a recorded gap closed, drop it from `t1aGaps` in the same commit, or both
   `TestTier1_ReachesEveryCorpusExpectation` and
   `TestTier1Accuracy_TheMissedPairsAreExactlyTheRecordedGaps` fail.

### Why it may only go up

`TestTier1Accuracy_FloorIsNotVacuous` admits less than one pair of slack: the floor must exceed
`rate - 1/total`. So an improvement that is not recorded is a red test, and a lowered floor is a
regression somebody accepted in silence — the one move a ratchet exists to prevent. The floor is
set to what was measured, never to a target.

The asymmetry between the two oracles is deliberate. `t1aGaps` excuses a pair from the per-pair
test `TestTier1_ReachesEveryCorpusExpectation`. It excuses **nothing** from the rate. Adding a
layout whose fields Tier-1 cannot reach lowers the rate and `TestTier1Accuracy_MeetsTheFloor`
stays red until the rules are fixed — which is exactly what
`## When a client's invoice fails to extract` step 4 below asks for. There is no exemption hatch
in the rate by design.

One thing the rate can **never** catch: an over-wide distance dial. Widening a dial only adds
candidates, so the rate is monotone non-decreasing in both — it goes up as the rules get
sloppier. `TestTier1_DialsStayInsideTheirMeasuredWindow` is the only guard on that side, and it
names the wrong candidate each widened dial produces.

Since EXTR-16 a bare label is not a value, and both upper bounds used to rest on one. The
`below` bound was re-measured on the same layout against the next group's *value*, 0.107212 down
— see the `corpus_stacked_labels.pdf` row above. The `right` bound had no corpus instance left
at all: `Buyer` was the only token ever reachable rightward from `Supplier`, and widening `right`
to 0.9 adds no candidate on any layout for any field. It is therefore measured on `acRightColumnPage`,
a synthetic page carrying `corpus_two_column.pdf`'s own column edges with the buyer's *name*
where the label stood; `right_merge` re-reads both edges off the real file, so the fixture cannot
drift into a gap nobody measured. **Adding a layout whose right column holds a party name would
put this bound back on the corpus.**

## Tier-1 decision rate

Measured 2026-08-31 on `feature/extr-16-the-ranking-defect`, over the same 44 pairs the recall
rate scores: the pipeline **decides** the value `corpusExpect` names on **43 of 44** —
**0.9773**. It read 30 of 44 — 0.6818 — before EXTR-16, and every one of the 13 pairs it gained
moved for the same two reasons: a Tier-1 rule no longer anchors on a token another lexicon entry
matches more widely, and a value one lexicon entry matches whole is no longer a name or an
invoice number. The layouts that moved are `corpus_inline_labels.pdf`, `corpus_split_labels.pdf`,
`corpus_stacked_labels.pdf`, `corpus_two_column.pdf`, `corpus_ambiguous_date.pdf` and
`corpus_totals_block.pdf` — all six. The one remaining miss is the unreachable `t1aGaps` pair,
the same pair recall misses.

`internal/extraction/accuracy_test.go` pins it as `tier1DecisionHits` / `tier1DecisionPairs`,
and `TestTier1Accuracy_DecisionRateOverTheCorpus` re-measures it. Unlike the floor above this is
a **measurement, not a ratchet**: move the pin to what is measured and say which layouts moved.
Its mutilation control — dropping every `invoice_number` rule must lower it, 43 to 37 — is what
stops it becoming a second number blind to rank.

The two numbers now agree on this corpus, so neither the shipped rate nor the mutilation cut can
still tell a decision measure from a candidate-containment one.
`TestTier1Accuracy_TheDecisionRateIsNotTheRecallRate` carries that job in a **decoy rule set**: a
`same_token` rule that reads `corpus_totals_block.pdf`'s Sub-total amount token whole and files
it under `total`. It sits at Distance 0 and out-ranks the layout's real total, which `Resolve`
still reaches — so recall holds at 43/44 while the decision rate falls to 42/44.

**False decisions: 0.** A false decision is a field decided with a value its layout never
prints. `corpusExpect` names no such field on that layout, so no hit and no miss is scored for
it and the recall rate cannot see it at all. `corpus_totals_block.pdf` / `supplier_name` was the
one such row — that page prints `Supplier TIN: 99999999-0601` and no party name, and the residue
after the label was decided as the supplier's name. It now reads `missing`. `acFalseDecided` is
empty and `TestCorpusDoc_RecordsTheDecisionRate` asserts this section carries no
false-decision row, so a new fabrication is red rather than green.

## Wired-path decision rate

Measured 2026-09-03 on `feature/extr-17-the-pipeline-runs-end-to-end`, over the same 44 pairs the
two rates above score. Driven through `ExtractWorker.Work` — the real document store, the real
transaction boundaries, the fingerprint hoist, the learned-rule lookup and the rank-0 encoding —
and scored from the rows read back out of `extraction_field_results` rather than from a
`Reconcile` return value, the pipeline decides **43 of 44** — **0.9773**. That is the in-process
decision rate, unmoved.

`TestRLS_WiredPathRateMatchesTheInProcessDecisionRate` asserts the *equality* rather than pinning
a second number, so a stage the in-process harness skips fails here instead of being absorbed.
The single miss is the same `t1aGaps` pair, `corpus_two_column.pdf` / `buyer_tin`, and
`TestRLS_WiredPathMissesExactlyTheRecordedGaps` compares the missed set to `t1aGaps` by identity
in both directions: `43 == 43` also holds for a path that misses a *different* pair, so a count
cannot tell one inherited gap from a new gap plus a new hit.

The corpus runs through a real `DoclingReader` replaying the six committed `corpus_*.docling.json`
goldens — the reader the deployed worker uses when `EXTRACTOR=docling` — and a second time
through `PDFiumReader`, the reader the in-process harness reads. Both decide 43 of 44 and miss
the same pair; `TestRLS_WiredPathReadsTheSameThroughBothReaders` compares the two missed sets by
identity, not by rate.

Two preconditions are asserted rather than assumed. The denominator is `tier1DecisionPairs`, and
the walk is scoped to `HeaderFields`, so the `line_items` block row every layout writes is not
counted as a field. And every layout's tenant is served **zero** learned rules on the baseline
run, with a seeded run beside it as the positive control — a rule store that served nothing to
anyone would satisfy the zero half alone.

**The rank control.** On this corpus the rank-0 rate and the any-rank rate both read 43 of 44, so
the shipped number alone cannot tell a decision measure from a candidate-containment one. That is
the EXTR-16 defect, and `TestRLS_WiredPathRankingDecoyMovesTheRate` is what stops it recurring
here: a **learned** `AnchorRule`, written through the real rule store for
`corpus_totals_block.pdf`'s fingerprint, whose label matches both amount tokens on that layout and
files them under `total`. `TierLearned` out-ranks `TierGeneric`, the two candidates tie at
Distance 0, and `compareRegions` hands rank 0 to the Sub-total amount above — so the wired rate
falls to **42 of 44** while the layout's real total, still reached, is stored one rank down.
Scored at *any* rank the same run reads 43 of 44, and the spec asserts that difference. A
single-amount decoy does not discriminate: `decideField` keeps an alternative only at the head's
tier *and* distance, so a lone `TierLearned` match leaves the real total with no row at all and
both scorers read 42.

**This section has no honest oracle, and is recorded as having none.**
`TestCorpusDoc_RecordsTheWiredPathRate` compares this prose to a Go constant, so it fails when
someone edits the page — never when the wired measurement itself is wrong. The measurement's real
reader is the `Report the wired-path decision rate` step in `.github/workflows/ci.yml`. This
package's DB-backed suite runs through a gate that pipes `go test -json` into a file and discards
a passing test's buffered log, so the report reaches CI only from a step of its own, which greps
its own output for the marker; `TestRLS_WiredPathTheCIStepsRunFilterNamesARealTest` keeps that
step's `-run` filter from rotting into one that matches nothing.

The remaining miss is a Tier-1 **reach** limit, not a storage or a ranking one. Closing it by
widening the anchor lexicon needs a `FingerprintVersion` bump, because the lexicon is an input to
the fingerprint; closing it by another route — a pointed correction on a distinguishing label —
needs none.

## Regenerating

```
go test ./internal/extraction/ -run TestFixtures -update
```

`-update` rewrites every `.pdf` under `testdata/` from its generator. Read the byte diff before
committing.

**Keep `-update` after the package path.**
`go test -run TestFixtures -update ./internal/extraction/` regenerates nothing: `go test` reads
`-update` as its own flag, takes the path as a test-binary argument, and runs whatever package
is in the working directory instead. It exits 0, so the mistake reads as a pass.

**A generator change requires a regenerate in the same commit.**
`TestFixtures_MatchTheirGenerator` compares the committed bytes against a fresh build, so an
edited builder without regenerated bytes is a red test, and so is a hand-edited PDF.

The single `-update` flag is registered at `fixtures_test.go:23`. Do not add a second
`flag.Bool("update", …)` anywhere in the package — a duplicate flag name panics the test binary
at registration, before any test runs.

## Adding a layout

Four edits, no new test:

1. A builder plus an `fxCorpus` entry in `fixtures_test.go`. The files go **flat** in
   `testdata/` with a `corpus_` prefix — `TestFixtures_MatchTheirGenerator` counts
   non-directory `.pdf` entries, so a subdirectory sits outside its floor.
2. A `corpusExpect` row in `corpus_test.go`.
3. The file name in `corpusLayouts`, the hard-coded set `TestCorpus_HasAllSixNamedLayouts`
   pins. It is hard-coded rather than derived from `fxCorpus` precisely so it can see a
   missing layout; update its expected count with it.
4. The layout's token count in `corpusTokenFloor`, in `corpus_adversarial_test.go`. That table
   must name every layout, so a new one without an entry fails rather than going unmeasured.

Every value in the new row must also be reachable by `Tier1Rules`, or the pair goes in `t1aGaps`
in `tier1_adversarial_test.go` with the reason. An unreachable expectation with no entry there
is a red test, which is the point.

Then regenerate as above and commit the new `.pdf` with its generator. Every value in the new
`corpusExpect` row must be readable out of the new fixture:
`TestCorpus_EveryExpectedValueAppearsInItsFixture` runs each token's word runs through the
field's shape and fails on a row naming a value the bytes do not carry.

**Not every committed fixture is a layout.** `learned_two_party.pdf` is generated and
byte-compared exactly like the six layouts, and it is deliberately named *outside* the `corpus_`
prefix so that none of the four edits above apply to it. Do not add a `corpusExpect` row, a
`corpusLayouts` entry or a `corpusTokenFloor` entry for it by reflex — the **Learned rules**
section below says why. `rich_invoice.pdf` (EXTR-18-01) follows the same pattern for a different
reason: a ruled table plus a deliberately inconsistent totals block, exercised by
`TestFixtures_RichInvoice*` in `fixtures_test.go`, not the anchor-rule corpus.

## Learned rules

A learned rule is one tenant's answer to "this producer puts the buyer's TIN *there*". It is
derived from a single pointed correction, stored against the layout's fingerprint, and read back
on every later document of that layout. It is the tenant-specific tier; Tier-1 stays generic.

### How a rule is derived

`LearnRule` (`internal/extraction/learn.go`) takes the corrected field, the box the reviewer
dragged, and the anchor observations the job's own read recorded. It considers every anchor
observation **on the corrected box's page**, keeps the ones that stand in a geometric relation to
that box, and picks the single best one with `betterAnchor`.

* **The relation** is one of `same_token`, `right` or `below`. `same_token` means the value lives
  inside the anchor token's own text; `right` and `below` mean it sits beside or under it, with
  at least half the shorter span overlapping on the off-axis.
* **`max_distance`** is the measured edge gap, **rounded up** to two decimals, then capped at the
  Tier-1 dial for that relation — `0.35` for `right`, `0.06` for `below`, `0` for `same_token`
  (D-6). Rounding up is what absorbs the difference between the reviewer's drag box and the token
  box; the cap is what stops one wide drag from teaching a page-wide rule.
* **The label** is the anchor's own matched text, put through `regexp.QuoteMeta` and wrapped in
  `(?i)` with `\b` word boundaries where the outer bytes allow one (D-3, precision over recall).
  When a rule is derived, this label is also what lands in the correction row's `anchor_label`,
  overwriting whatever the wire sent. When no rule is derived, the client's trimmed value stands
  unchanged — see `TestRLS_APointedCorrectionThatAnchorsToNothingCommitsWithoutARule`.

The worked example, measured off `learned_two_party.pdf`. Both party blocks are stacked
(`label` / `name` / bare TIN) in page 1's **top** half, so `t1.buyer_tin.sweep` — which is banded
to page 1's bottom half — cannot reach the buyer's TIN, and the `buyer_tin` lexicon needs a party
word beside a TIN word, which a bare number does not carry. Tier-1 alone therefore returns
**zero** `buyer_tin` candidates and decides `missing`.

A reviewer points at the bare token `99999999-0702`. The nearest qualifying anchor is
`buyer_name` / `"Buyer"`, `below` at a gap of `0.026525`; `"Supplier"` is also below, at
`0.140267`, and is dropped for exceeding the `0.06` dial. The derived body is:

```json
{"label":"(?i)\\bBuyer\\b","relation":{"kind":"below","max_distance":0.03},"shape":"tin"}
```

On the next document of that layout the rule matches one token, `relatedTokens` reaches the two
tokens under it, `ShapeTIN` rejects the party name, and `buyer_tin` resolves to `99999999-0702`
as a `TierLearned` candidate with no alternatives —
`TestRLS_TheSecondDocumentOfTheSameLayoutResolvesTheLearnedBuyerTIN`.

### Only a pointed correction produces a rule

A correction carries a method. A **`typed`** correction — a reviewer retyping a value — writes
the correction row and **zero rules**. An **`undone`** correction likewise writes **zero rules**.
Only `pointed`, where the reviewer drew a box on the page, teaches anything, because only a box
carries the geometry a rule is derived from.

A pointed correction that **anchors to nothing** — an empty corner of the page, a box no anchor
observation stands in a relation to, a job that recorded no layout at all — still commits the
correction and still teaches nothing. That is an **honest refusal**, not an error: the reviewer's
edit is recorded and applied, and the system declines to generalise from a gesture it cannot
interpret. `LearnRule` reports `ok=false` and the request answers `201` exactly as it otherwise
would.

### Undo does not un-teach

This is the sharp edge of the feature, and it is a decision (D-17), not an oversight.

A rule written by a correction that was later **undone stays live**. It keeps firing on every
later document carrying that layout fingerprint. The undo revises what one field on one document
says; it does not withdraw the claim about *where that field lives on this layout*.

The only way to displace a live rule is a **second pointed correction** on the same field,
pointing at a distinguishing label. Displacement is by **ordering**, never by deletion: the
`extraction_anchor_rules` table is **append-only** by grant — `invoice_app` holds `INSERT` and
`SELECT` and no `UPDATE` or `DELETE` — so after a superseding correction **both rows remain**,
and `AnchorRulesFor` returns them newest-first. `Resolve` lets the first rule that produces
anything for a field claim it; an older rule is outranked, never erased.

Two tests hold this:
`TestRLS_AnUndoDoesNotUnteachAndOnlyAPointedCorrectionSupersedes` proves the undo leaves the rule
both present and *firing*, and that a later pointed correction prepends a superseding row;
`TestRLS_ASecondPointedCorrectionSupersedesTheFirstOnTheThirdDocument` proves that when **both**
rules are live and resolve **different** values, the newer one decides — with the reversed
ordering as the control.

### A rule is scoped to one tenant and one layout fingerprint

Both boundaries are enforced, and neither is advisory.

* **Layout.** A rule is stored under the fingerprint of the layout it was learned on. A different
  layout computes a different fingerprint, so the rule is **never even loaded** for it — the
  read is `WHERE tenant_id = $1 AND layout_fingerprint = $2`, and a rule under another key does
  not come back. This is the mechanism; "the rule happens to match nothing over there" is a
  content coincidence and is not what keeps layouts apart.
* **Tenant.** A rule belongs to the tenant whose reviewer taught it. A **different tenant**
  handed a byte-identical document, computing an identical fingerprint, loads zero rules and
  reads the document exactly as it did before. Row-level security on
  `extraction_anchor_rules` is what holds it, so the isolation survives a query that forgets to
  filter.

`TestRLS_ALearnedRuleIsNeverLoadedUnderAnotherLayoutsFingerprint` and
`TestRLS_TheLearnedRuleDoesNotLeaveItsTenant` are the oracles, each with the paired positive
control that makes its zero mean something. Neither one can see row-level security being turned
off, though: measured, both still pass with the policy disabled, because the store's own
`tenant_id` predicate filters the row. The oracle for the mechanism is
`TestRLS_ExtractionAnchorRulesCrossTenantSelectRefused`, which reds when the policy is dropped.

Both oracles read the `v1:` namespace, and the layout one says in its own comment that its
cross-layout zero would survive the fingerprint gate being removed. The `b1:` boxless equivalents
do not have that weakness — `TestRLS_ABoxlessLearnedRuleDoesNotReachAnotherLayout` and
`TestRLS_ABoxlessRuleAtTheSharedEmptyIdentityStaysInsideItAndItsTenant` put the same token on
both layouts, so each zero reds when `AND layout_fingerprint = $2` is dropped. Their tenant halves
inherit the same blindness to the policy: measured, they pass with either the predicate or the
policy removed, and red only when both are.

### When a learned rule misfires

**The response path is the corpus owner** — the `## Owner` section at the foot of this document.
A learned rule is tenant data, not shipped code: it cannot be fixed by a deploy, and there is no
admin screen that edits or retracts one. Bring the layout, the tenant and the field to the owner.

The canonical misfire is `corpus_two_column.pdf`, and it is asserted rather than hidden —
`TestRLS_TheTwoColumnLayoutGetsWorseBeforeItGetsBetter`.

That layout prints the supplier and buyer blocks side by side, each ending in a token spelled
`TIN: 99999999-04NN`. Under Tier-1 alone `buyer_tin` reads **`missing`** — one honest gap.
A reviewer points at the buyer's own token `TIN: 99999999-0402`; the best anchor is the
`supplier_tin` lexicon entry matching the bare word `TIN` **inside that same token**, so the
derived rule is:

```json
{"label":"(?i)\\bTIN\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}
```

That label matches **both** party blocks. With the rule live, `buyer_tin` gets **two candidates
from one rule**, both at distance 0, and the decision comes out `99999999-0401` — **the
supplier's TIN** — flagged `ambiguous` with `99999999-0402` as the alternative. The layout has
gone from one honest `missing` to a confidently decided **wrong** value.

Both candidates come from the *same* rule, so newest-rule-wins cannot rescue it. The tie is
broken in `compareRegions`: the two tokens share a baseline, so their `Y0` is bit-identical
(`0.23407067192925346`), and `X0` decides — `0.1179 < 0.6539` hands the field to the supplier.

The remedy today is a **second pointed correction** on a distinguishing label — a token whose
text tells the two blocks apart — which prepends a superseding rule. Widening the anchor lexicon
so that `TIN` alone no longer anchors is **not** a remedy: the lexicon is an input to **both**
fingerprints, so changing it invalidates every stored rule for every tenant and requires a
`FingerprintVersion` **and** a `BoxlessFingerprintVersion` bump. Since EXTR-19-02 the same
`anchorLabelMatchers` feed `BoxlessFingerprint`, so a lexicon change moves every `b1:` key too.

### learned_two_party.pdf is not a corpus layout

`learned_two_party.pdf` is generated by `fxBuildLearnedTwoParty` and byte-compared by
`TestFixtures_MatchTheirGenerator` and `TestFixtures_GeneratorIsDeterministic` like every other
fixture. It is deliberately named **outside** the `corpus_` prefix, and therefore sits outside
`corpusExpect`, `corpusLayouts`, `corpusTokenFloor`, the Tier-1 recall rate and the Tier-1
decision rate.

That is the point: it exists to be a layout Tier-1 **cannot** fully read, and adding it to the
corpus would move `EXTR-04`'s accuracy ratchet — which must keep measuring the same six layouts
it has always measured — for a fixture whose whole purpose is a gap. Its own reserved-TIN scan
is `TestCorpus_TheLearnedRuleFixtureUsesOnlyFreeReservedTINs`, because
`TestCorpus_UsesOnlyFreeReservedTINs` quantifies over `corpusLayouts` and cannot see it.

One consequence worth recording: `supplier_tin` reads `ambiguous` on this fixture, because
`t1.supplier_tin.sweep` is banded to page 1's top half and claims **both** bare TINs. That is the
cost of putting both party blocks in the top half, it is what makes the buyer's TIN unreachable,
and it is invisible to every accuracy number precisely because this is not a corpus layout. Do
not "fix" it.

## Scrubbing an anonymised real document

A real invoice that failed to extract is a source of *layout information*, not of bytes. It is
scrubbed and reproduced as a generator entry; it is never committed.

1. **Replace every TIN** with a value from the free part of the reserved block — `99999999-0101`
   onward. Never `99999999-0001` through `99999999-0009`: those are
   `internal/submission/mock_script.go`'s scripted outcomes and its never-allocate pair.
2. **Replace every party name and address**, including any that appear only in a footer, a logo
   caption, or a bank-details block. The corpus uses `Adeyemi Trading Limited` and
   `Honeywell Group`, echoing the dev seed's cast; neither is a live customer.
3. **Replace every other identifier**: invoice numbers, purchase-order references, account
   numbers, phone numbers, email addresses.
4. **Strip PDF metadata** — the `/Info` dictionary, the file `/ID`, and any XMP packet. The
   generator emits none of these, which is one reason the reproduction is safer than the
   original.
5. **Reproduce it as a generator entry**, not as committed client bytes. Transcribe the
   *arrangement* — which labels appear, where, and at what token granularity — into `fxLine`
   values. Nothing from the original file is copied.
6. **Record the scrubber and the date** in the generator's comment, so provenance is reviewable
   in the diff.
7. **A second person confirms** the reproduction carries nothing identifying before it is
   committed.

> The EXTR-04 design document's §10 says to use "the reserved `99999999-000N` block" for
> scrubbed TINs. That is wrong and contradicts the story's own AC-6: `-0001`…`-0009` are the
> reserved suffixes that must be avoided. The free part of the block, `-0101` onward, is
> correct. `TestCorpus_UsesOnlyFreeReservedTINs` fails on the design document's advice.

## When a client's invoice fails to extract

1. Retrieve the document from `documents` / `DOCUMENT_BUCKET`. Do not attach it to an issue and
   do not commit it.
2. Read its layout: which labels the producer emits, whether label and value share a token, and
   which page band each block sits in. The token dump the corpus tests use is the same reader
   the pipeline uses.
3. Scrub and reproduce the failing arrangement as a new synthetic layout, per the section above.
4. Add its `corpusExpect` row and watch it fail. A reproduction that passes before the fix has
   not reproduced the failure.
5. Fix the rule, and keep the layout in the corpus so the regression cannot return.

If the fix would only be correct for that one tenant's producer, it is a **learned rule**
(EXTR-14) and not a Tier-1 change. Tier-1 is the generic tier; a rule that needs a tenant's
context does not belong in it.

## Owner

**Owner: the maintainer of `internal/extraction/` — today the repository owner, `SimonOsipov`,
sole reviewer of every extraction PR.** Reassign by editing this line; there is no `CODEOWNERS`
file to keep in sync.

This is an **assumption recorded here, not an agreement anyone made.** The EXTR-04 decision log
lists the corpus owner as owed, and no one has accepted the role. It is written down because a
response path with no name on it is a response path nobody runs; treat the line as a default to
be corrected, not as a fact.
