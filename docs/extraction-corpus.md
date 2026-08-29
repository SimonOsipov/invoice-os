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
| `corpus_stacked_labels.pdf` | `below` — the only layout where *every* label's value sits under it, 16pt down at the same `x`, invoice number and date and total included. A label reaches its own group's values within 0.027 normalised and the next group's label no closer than 0.087, a 3.3x margin, so a `below` rule anchored on a label cannot span two groups. `corpus_two_column.pdf` stacks its two party blocks the same way, so `below` reaches the name fields there too; what is unique here is that nothing else in this layout is inline. Its bare TINs are not unique either — `corpus_split_labels.pdf` carries one in each page half as well. | 13 | 1109 |
| `corpus_two_column.pdf` | Column bands. Supplier labels centre at X 0.15–0.21 (band 0), buyer labels at 0.68–0.74 (band 2). It is the only layout whose anchor labels reach the right-hand third at all, and so the only one whose fingerprint carries a band above 1; `TestCorpus_TwoColumnPartiesLandInTheOuterBands` enforces both halves of that. Both TINs sit inside a longer token (`TIN: 99999999-0401` and `TIN: 99999999-0402`, both at `Y0` 0.2341), so neither format-only sweep can fire and neither is separated by page half. Under Tier-1 they are not separated by label either: the `supplier_tin` pattern's party word is optional, so a bare `TIN` label matches it and `supplier_tin` collects **both**, while `buyer_tin` — whose party word is required — is unreachable on this layout. That is a Tier-1 accuracy defect, not a corpus defect; EXTR-04-09 owns it, and widening the lexicon would change every stored document's fingerprint. | 10 | 1031 |
| `corpus_ambiguous_date.pdf` | `12/03/2026` — both components at most 12 and no month name, so `ShapeDate` returns both readings and `issue_date` keeps two candidates. The one layout whose expectation row carries two values. | 6 | 873 |
| `corpus_totals_block.pdf` | The lexicon overlap: `Sub-total` matches both `subtotal` and `\btotal\b`, because `-` is a non-word character, so one label mints a candidate for two fields. Right-aligned split totals. The VAT label carries no percentage — a `7.5%` remainder would mint a spurious amount candidate. | 9 | 938 |

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

Then regenerate as above and commit the new `.pdf` with its generator. Every value in the new
`corpusExpect` row must be readable out of the new fixture:
`TestCorpus_EveryExpectedValueAppearsInItsFixture` runs each token's word runs through the
field's shape and fails on a row naming a value the bytes do not carry.

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
