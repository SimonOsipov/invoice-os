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
byte-compares, so neither side can drift alone. The generator imports stdlib plus
`internal/extraction` itself, for the specs that read a built page back through the reader, and
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
| `corpus_inline_labels.pdf` | `same_token` for all ten fields — every `Label: value` is one token. Carries no bare-TIN token, so the format-only sweep deliberately cannot fire here. | 11 | 1117 |
| `corpus_split_labels.pdf` | `right` — label and value on one baseline as two tokens, ~0.15 normalised apart. Its buyer TIN sits at `Y0` 0.53, in the page's lower half — geography the retired banded sweeps read and nothing reads now; what routes a party-less TIN is the party block it stands in (**Party blocks** below). Its date, `15/04/2026`, has a day above 12 and is deliberately unambiguous. | 21 | 1429 |
| `corpus_stacked_labels.pdf` | `below` — the only layout where *every* label's value sits under it, 16pt down at the same `x`, invoice number and date and total included. Every value `corpusExpect` requires from a `below` rule here sits at most 0.009111 normalised under its label, and the next group's label is no closer than 0.087010, so a `below` rule anchored on a label cannot span two groups. Since EXTR-16 a bare label is not a value, so what bounds the dial is the next group's *value*: 0.107212 down, an 11.77x window, with the buyer's name 0.321571 down behind it. (The widest *intra-group* gap is 0.026631, a party block's TIN line that no expectation requires; `TestCorpus_StackedValuesSitBelowTheirLabels` asserts that one and the 0.087010 separation, and `TestTier1_DialsStayInsideTheirMeasuredWindow` asserts the window.) `corpus_two_column.pdf` stacks its two party blocks the same way, so `below` reaches the name fields there too; what is unique here is that nothing else in this layout is inline. Its bare TINs are not unique either — `corpus_split_labels.pdf` carries one in each page half as well. | 13 | 1109 |
| `corpus_two_column.pdf` | Column bands. Supplier labels centre at X 0.15–0.21 (band 0), buyer labels at 0.68–0.74 (band 2). It is the only layout whose anchor labels reach the right-hand third at all, and so the only one whose fingerprint carries a band above 1; `TestCorpus_TwoColumnPartiesLandInTheOuterBands` enforces both halves of that. Both TINs sit inside a longer token (`TIN: 99999999-0401` and `TIN: 99999999-0402`, both at `Y0` 0.2341), so the format-only sweep cannot fire on either. What separates them is the **party block**: the stacked `Supplier` and `Buyer` headings open one block each, and the party-less `TIN` label inside each token resolves to the field that block's party owns, so `supplier_tin` reads `99999999-0401` and `buyer_tin` reads `99999999-0402`. Until EXTR-22 neither was separated at all — `supplier_tin`'s party word was optional, so a bare `TIN` label matched it and `supplier_tin` collected **both**, while `buyer_tin`, whose party word is required, was unreachable here. EXTR-04-09 measured that gap and carried it forward rather than closing it, because closing it meant widening the shared anchor lexicon, which is an input to **both** layout identities and so needs a `FingerprintVersion` **and** a `BoxlessFingerprintVersion` bump — the pair of bumps EXTR-22 made. | 10 | 1031 |
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
fails on any expected value the shipped set cannot reach. A pair the shipped set cannot reach is
exempted by name in `t1aGaps`, and every exemption is asserted **still unreached**, so closing one
is a deliberate diff rather than a silent pass.

`t1aGaps` is **empty**. Its one entry was `corpus_two_column.pdf` / `buyer_tin`, and it closed when
EXTR-22 bound each party's TIN to the heading that owns it — see that layout's row above and
**Party blocks** below. An empty list is not a dead list: the declaration and its two oracles stay,
so the next unreachable pair is recorded rather than absorbed.

## Party blocks

A page is partitioned into **party blocks** before any rule runs, and a rule anchored on a
party-**less** TIN label is routed to the field that block's party owns instead of to a fixed
field. Four rules are party-scoped and no others: `bare_tin`'s three relations
(`t1.tin.same_token`, `t1.tin.right`, `t1.tin.below`) and the format-only `t1.tin.sweep`. Their
`Field` is `partyField`'s `PartyUnknown` fallback rather than a party name, and the key carries the
neutral role — a party-scoped rule keyed `t1.supplier_tin.*` would read as a supplier rule that
sometimes files under the buyer.

`internal/extraction/party.go` holds the whole partition: `Party`, `partyHeadings`, `partyHeading`,
`partyOrder` and `partyField`, and nothing else. `Resolve` computes one partition per page, beside
the per-page label array, and reads it where a party-scoped rule mints its candidate.

**A party heading opens a block, and the block runs to the next heading in page order.** A heading
is a token whose text one — and only one — of the two party vocabularies matches: the
`supplier_name` and `buyer_name` entries of `anchorLexicon`, read by **id** and never re-spelled
in `party.go`, because a forked copy of a pattern drifts from the fingerprint in silence
(`TestPartyHeading_ReadsTheLexiconAndNotACopy`). Since EXTR-26, `supplier_name` alone carries
the lexicon's only **position-anchored** arm, `^\s*from\b\s*(?:[:.\-–—]|$)` — no `(?m)` flag, so
`^` is start-of-text and not start-of-line, and the trailing terminator is required, not optional
(**The `From` guard** below). Every token from the heading onwards belongs to
that party until another heading opens the next block, so a heading belongs to its own block and
the last block on a page runs to the end of it
(`TestPartyOrder_AssignsEveryTokenToTheHeadingBeforeIt`). Order is the page's own token order and
nothing else — there is no sort, and no reading-order reconstruction.

**A token both vocabularies match heads neither.** `Supplier / Buyer` names both parties, and a
heading that names both names none: the partition keeps whichever block was already open rather
than guessing (`TestPartyHeading_ATokenNamingBothPartiesHeadsNeither`,
`TestPartyOrder_ABothPartiesTokenDoesNotEndTheBlock`). Since the `From` guard, the rule also
reaches `From: Billed to`, `From — Invoice to` and `From. Deliver to` — tokens that headed the
**buyer** block before the `From` arm, because only `buyer_name` matched them; now both vocabularies
do, and the heading opens neither (`TestParty_AFromTokenThatAlsoNamesTheBuyerHeadsNeither`). A
token that repeats the party already
heading the block — `Supplier TIN` after `Supplier` — opens a block of the same party, which is
the same block; it neither resets nor toggles one
(`TestPartyOrder_ARepeatedHeadingOfOnePartyDoesNotDisturbTheBlock`).

**Before the first heading the party is `PartyUnknown`, and `PartyUnknown` falls back to the
supplier.** That fallback is not a default chosen here; it is exactly what a party-less TIN label
meant before EXTR-22, which is what makes the change **monotone**: a page carrying no heading at
all reads as it always did (`TestPartyOrder_TokensBeforeTheFirstHeadingAreUnknown`,
`TestPartyField_IsTotalOverEveryParty`, which also holds `partyField` total over a `Party` value no
constant names yet). The fallback is load-bearing on a shipped arrangement:
`wild_two_party_bare_tin.pdf` carries **no supplier heading at all** — every heading on it is a
buyer heading, and the first is `Invoice to` at token 6 — so its entire supplier reading rests on
the fallback. (The other two, `Customer No.` at token 7 and `Buyer's Signature` at token 11, repeat
the party already heading the block and so open the same block; they are still headings, and the
count in **Why a heading is not outranked** below counts them.)
`TestPartyOrder_InvoiceToHeadsTheBuyerBlock` asserts both halves, the index of the first heading and
the absence of any `PartySupplier` on the page, so the fallback cannot be dropped while its only
shipped user is still shipping.

**A block cannot reach the next page.** `partyOrder` takes one `TokenPage`, so a heading in the
last token of page 1 owns nothing on page 2 and every page begins at `PartyUnknown`. The bound is
structural rather than a convention anyone has to remember, and
`TestPartyOrder_OwnershipNeverCrossesAPage` holds it from both sides: the two pages walked
separately, and the same tokens joined into one page as the control that proves the partition
partitions at all.

**The partition reads no box.** Token order and token text, nothing else — so a boxless read,
where every token carries the zero box, partitions **identically** to the same tokens with real
geometry (`TestPartyOrder_ABoxlessPagePartitionsTheSame`, with a non-degenerate control so two
all-`PartyUnknown` slices cannot compare equal and pass). `TestParty_UsesNoMapAndNoGeometry` parses
`party.go` and scans it for the two things it must not contain — a map type, whose iteration order
is not deterministic, and any read of a token's `Region` — with a needle and a control for each
scan, so an all-clear cannot be a broken scan.

This is the reason the party block, and not a page band, is what scopes a party-less TIN. A
bounded `PageBand` fails closed on a token with no usable box (`inBand`), by design, so the banded
sweeps EXTR-04 shipped could match **nothing at all** on a boxless read. The partition has no such
floor: it reads what a DOCX still carries, which is arrival order.

### What the last block swallows

The last block on a page owns everything after its heading, the totals included. On
`wild_ruled_lines_totals.pdf` the buyer block opens at token 5 and runs to token 33 — **29
tokens**, the whole line-item table and `Sub-total`, `VAT` and `Total` with it
(`TestPartyOrder_TheLastBlockOnAPageOwnsTheTotals`, which asserts the run and then names those
three tokens inside it, so a block called totals-owning that owns no total is a red test).

That is harmless **only because no amount field is party-scoped**. Party scoping is set on the
party-less TIN rules and nowhere else, so nothing today asks which party owns `Total`. The day a
rule does — a per-party subtotal, an amount split by side — it inherits a partition in which the
buyer owns the entire totals block, and it will look correct on every layout whose buyer block
happens to come second. This paragraph is the warning that the partition was never designed to
answer that question.

### Why a heading is not outranked

`anchorOutranked` is what stops a **rule** anchoring on a narrow label sitting inside a wider one:
`Supplier` inside `Supplier TIN: 99999999-0101` owns nothing on that token, so no `supplier_name`
rule may fire there. `partyHeading` deliberately does **not** consult it, and must not.

Measured over the eleven scored layouts: adding the qualifier silences **14 of the 31 party
headings** and moves **18** token assignments. **Twelve** of the fourteen are `Supplier TIN: …` /
`Buyer TIN …`-shaped tokens whose party-name span sits strictly inside the party-TIN span; the other
two are `wild_two_party_bare_tin.pdf`'s `Customer No.` and `Buyer's Signature`, silenced by the
owning phrases of **Labels that own their token** below rather than by a TIN span — and those two
move nothing, because both repeat the party already heading their block. On most of the rest a
sibling heading — the plain `Supplier:` line — catches the block a token or two later,
which is worse than it sounds: the party TIN token itself falls outside its own block. On
`corpus_totals_block.pdf` it is fatal outright, because that page's **only** heading is the token
`Supplier TIN: 99999999-0601`; silence it and the page has no block at all, and all seven tokens
from that heading to the end of the page read `PartyUnknown`.

The two questions are simply different. *Does this label own this token's value* is what outranking
answers. *Which party does this token belong to* is what a heading answers — and a token that
spells out `Supplier TIN` names the supplier louder, not more quietly.

### The `From` guard

`supplier_name` gained one alternation arm, `^\s*from\b\s*(?:[:.\-–—]|$)` — the lexicon's only
position-anchored pattern. `^` is Go's start-of-text (no `(?m)` flag is set anywhere this pattern
compiles), so the arm only ever opens a token, never a sentence mid-flight, and it must close on
one of five printable separators or end-of-token; ASCII whitespace may lead the terminator, and
nothing else — not even a non-breaking space — satisfies it
(`TestAnchorLexicon_TheFromArmsTerminatorSet`).

Three committed refusal sets hold the guard down:

* **Prose, five mid-sentence and four leading, refuses outright** — `Freight from Lagos`,
  `From Lagos to Abuja` and seven more like them match nothing
  (`TestAnchorLexicon_FromInProseNamesNobody`).
* **A mid-sentence `from` followed by a real terminator still refuses**, because only the leading
  `^` stops it — `Period from:`, `Goods shipped from-` and five more carry a terminator and match
  nothing (`TestAnchorLexicon_AMidSentenceFromWithATerminatorNamesNobody`).
* **Leading prose loses no TIN.** Under an anchored but unterminated `^\s*from\b`, this exact page —
  `Billed to` / `Enugu Ceramics Limited` / `From Lagos to Abuja` / `TIN 99999999-1302` — files
  `buyer_tin` as `supplier_tin` and fabricates `supplier_name = "Lagos to Abuja"`, measured. The
  shipped arm never reaches that token, so `buyer_tin` stays `99999999-1302`
  (`TestParty_ALeadingProseFromLeavesTheBuyerTINAlone`).

Differential oracle: `TestAnchorLexicon_TheFromArmIsTheOnlyThingSupplierNameGained` derives the
pre-arm pattern from the shipped one and diffs it against a generated probe set wide enough that
a silent change to the supplier/seller/vendor arm cannot hide in it, so every span the arm adds,
keeps or moves is attributed to the arm itself and not to the rewritten parenthesisation around
it.

**The guard's cost, characterised rather than left silent:**

* An uncolonned `From Kaduna Advisory Partners` in one token reads no supplier at all
  (`TestAnchorLexicon_AnUncolonnedFromNamesNoSupplier`).
* `From: 01 Aug 2026` names the date `01 Aug 2026` as the supplier, and `From – To` names `To`
  (`TestParty_ATerminatedFromOnADateRangeNamesTheDate`).
* A non-breaking space before the terminator refuses, and so does `From` with the name on a second
  line of the same token: `$` is end-of-text, not end-of-line
  (`TestAnchorLexicon_TheFromArmsTerminatorSet`).
* A second `From` heading silently drops the first block's name; only the newer one decides, with
  no alternative recorded (`TestParty_ASecondFromHeadingTakesTheBlockFromTheFirst`).
* A learned label derives the unguarded `(?i)\bFrom\b` — `LearnRule`'s `learnedLabel` rebuilds a
  rule from the anchor's own matched text, which carries no position
  (`TestLearn_ALearnedFromLabelLosesTheLeadingGuard`; the `ceiling:` beside the entry in
  `anchor.go`).

**A one-off, uncommitted control, named so it is not mistaken for a shipped oracle**: the fully
unanchored `\bfrom\b` misfile was a Stage-1 mutation, never a committed test. On the token
`Services rendered from 01 Aug to 31 Aug 2026` it files the buyer's TIN as `supplier_tin` and
fabricates `supplier_name = "01 Aug to 31 Aug 2026"`, measured;
`TestParty_AMidSentenceFromDoesNotOpenASupplierBlock` is the test that reds under it. The anchored,
unterminated misfile above is pinned by the committed test named there.

**The positive cases.** A leading `From` opens a supplier block wherever it sits on the page —
leading the page (`TestParty_ALeadingFromOpensASupplierBlock`) or behind an already-open buyer
block, stranding neither party's fields
(`TestParty_AFromHeadingBehindTheBuyerBlockOpensASupplierBlock`). And a suffixed buyer heading —
`Billed to`, `Billing to`, `Delivered to` — moves the bare TIN behind it to the buyer exactly as
`Bill to` always did; a spelling outside the closed set leaves it with the supplier
(`TestPartyOrder_ASuffixedBuyerHeadingMovesTheBareTIN`).

## Labels that own their token

Seven entries in `anchorLexicon` exist to be **labels and nothing else**. `party_ref`
(`Customer No.`, `Client Code`) and `signature` (`Buyer's Signature`) sit over the party
vocabulary; `reg_identifier` (`VAT REG NO`, `VAT REGISTRATION NUMBER`), `rc_number` (`RC NO`,
`CAC NUMBER`) and `doc_title` (`TAX INVOICE`, `VAT INVOICE`) sit over the amount vocabulary;
`due_date` (`Due Date`, `PAST DUE DATE`) sits over `issue_date`'s bare `date`, and
`withholding_tax` (`Withholding tax 10%`, `With holding tax`) sits over `vat`'s bare `tax`, by
strict containment. None of the seven carries
a Tier-1 rule; none of them resolves a field. Each exists so that a token
spelling one of those phrases is claimed **whole** by a label, which is what stops a narrower entry
inside it — `buyer_name`'s bare `Buyer` inside `Buyer's Signature`, `vat`'s bare `VAT` inside
`VAT REG NO`, its bare `TAX` inside `TAX INVOICE` — from anchoring its own rule there and reading
whatever stands beside it as a party name or an amount.

An entry that resolves nothing and suppresses nothing is indistinguishable from a **dead entry**
that ships in the fingerprint and does no work — measured: one was added, declared owning, and
passed the whole extraction suite. Three declarations close that:

* `t1OwningPhraseIDs` names every entry carrying no Tier-1 rule, so an entry that resolves nothing
  must **say** so, and one that later gains rules stops being exempt.
* `t1PrintedPhraseIDs` names the subset that no rule-bearing entry sits inside, so suppression
  cannot be what they are for. They earn their place by being **printed on a shipped layout**,
  proved through `AnchorObservations` in the end-to-end package rather than asserted here.
  `rc_number` is the only entry on that arm; `reg_identifier`, `doc_title` and `withholding_tax`
  earn theirs by containing `vat`. The declaration is read by source scan, so it must stay on one
  line.
* `alBareTokenCases` carries one row per owning phrase, each with a paired positive (the owner must
  match its own whole label) and a refusal (the owner must not match the bare token it protects).
  It is set-equality pinned against `t1OwningPhraseIDs`, so neither list can grow or shrink alone.

The bare-token row is the one that catches a **loosening**. Containment cannot see a phrase that
degenerates to exactly its victim's span, because `anchorOutranked` requires a **strictly** wider
one — measured, a `doc_title` broken that way passed all eleven other guards and only that row
red.

`wild_rc_due_naira.pdf` is the shipped instance. Its token `RC NUMBER: RC-000142` sits between
`Sub-total` and `VAT` on the page, and since EXTR-22 it reads as a **label**. It is not suppressed,
not displaced and not corrected: nothing rewrites it, nothing hides it, that layout's `vat` cell
still reads `187.50` unchanged, and the RC line still displaces nothing. All that changed is that
the phrase now has an owner, so no amount rule can anchor on the bare `RC` or on the `NUMBER`
inside it.

An owning phrase is one defence against a word inside prose; a positional guard —
`supplier_name`'s `From` arm, in **Party blocks** above — is a second kind, refusing a token by
where the word sits rather than by what claims it.

### A label may carry a verb inflection

Two entries admit one enumerated tail between the words of their `<verb> to` arm: the constant
`alSuffix`, `(?:ed|ing|s|d)?` (`anchor.go:121`), sits after `bill`, `sold`, `invoice` and
`deliver`. `buyer_name` (`Billed to`, `Billing to`, `Bills to`, `Delivered to`, `Invoiced to`)
and `buyer_tin` (`Billed to TIN`, `Invoiced to TIN`) both carry it, and no other entry does.

The set is closed at four tails. `es`, `er` and `ment` are all refused — `Billeder to`,
`Billments to` match nothing — because the rule names a morphological class, the four verbs
above inflecting, and is not a stemmer over arbitrary verbs. A noun-noun entry takes no tail at
all: `sub total`, `grand total`, `amount due` and `invoice date` refuse every one of the four,
because an intervening suffix between two nouns spells nothing in English.

Decoration needs no rule on this path, because header-field matching is unanchored substring
search over raw token text: a trailing colon, a currency symbol or a percentage cannot break a
match that never anchored to the token's edges
(`TestAnchorLexicon_ADecoratedLabelStillNamesItsOwnField`). The `From` arm is the one exception: it
anchors to the token's start and requires a terminator (**The `From` guard**).

The set elides no letter, so it has an edge. `Invoicing to` misses and `Invoiceing to` matches —
the tail is a blind concatenation onto the stem, not a real elision of the trailing `e`
(`TestAnchorLexicon_TheClosedSetMissesTheElidingInflection`). The `\b` after `to` still
fires: `Billed total` is no buyer label, because `to` must end a word
(`TestAnchorLexicon_TheTrailingBoundaryStillFiresAfterTo`).

### The rightward label boundary

A rightward read stops at an intervening label. When a labelled token sits between the anchor and
the value, inside the anchor's own band, the pair is refused — a label owns what follows it, so the
read may not reach past one to take a value that label introduces. `crossesALabel` is the
predicate; `labelTokens` is the per-page precompute it reads.

**The corpus does not exercise it, and the honest denominator is eight, not fourteen.** Only **8 of
the 14** layouts admit a rightward anchor/value pair at all — **35** pairs in total — and the other
six read nothing rightward, so the predicate is never called on them. Those six are named in
`bdSilentLayouts` rather than left implicit, because "the boundary moved nothing across all fourteen
layouts" is a claim whose effective denominator is eight. Of the 35 pairs, **none** crosses a label,
which is why the boundary removes zero candidates on the shipped corpus. Read that zero as "no
shipped arrangement puts a label in the corridor", never as "the boundary does nothing".

Two properties of the predicate have no behavioural oracle and are held by structural ones
instead. The per-page precompute must not degenerate into a per-pair lexicon scan: the two produce
a byte-identical candidate list on every arrangement, differing only by orders of magnitude in
cost, so `TestResolve_TheBoundaryPredicateScansNoLexicon` and
`TestResolve_TheBoundaryPredicateCallsNothingThatReadsTheLexicon` are the **only** things standing
between the shipped shape and a silent regression — the second bounds the transitive closure,
because a predicate that calls a helper that reads the lexicon names no banned identifier. And each
page must be read against **its own** label array, which no single-page arrangement can see
(`TestResolve_TheBoundaryReadsEachPagesOwnLabels`). Do not sweep any of the three.

## Doubt on a header field

A value supported only by an **uncorroborated adjacent match** presents as doubtful rather than
decided, for four fields declared in `doubtfulFields` in `reconcile.go` — `buyer_tin`,
`buyer_name` and `vat`, which EXTR-22 owns, and `total`, which EXTR-23 added. *Adjacent* means the
value was read `right` of or `below` its anchor rather than out of the anchor's own token;
*uncorroborated* means such a head at the generic tier, and on these four fields it competes with
**every** reading of its field rather than only the equal-standing ones (D-14). A head that so meets
a second, distinct value reads `ReasonAmbiguous`, carries the competitor as an alternative, and
**keeps its own value**.

The scope list is pinned in both directions. Removing a member is caught by that member's own
oracle, and **adding** one is caught by nothing unless the partition is pinned over the whole
vocabulary — measured: adding `subtotal`, and adding an inert misspelling, both survived the entire
suite. A ten-field behavioural partition and an order-blind source-level set pin close it, and both
are needed.

`dtAmbiguous` pins every cell the fourteen layouts read as ambiguous, eight in all
(`TestEndToEnd_TheDoubtfulCellsAreExactlyThePinnedEight`). One is `corpus_ambiguous_date.pdf`'s
`issue_date`, a tie between two readings of one printed date that predates the doubt. The other
seven are doubtful cells in this scope.

Five of the seven are `buyer_name`, and the doubt moves none of their values (the same spec checks
every scoped value per layout). Widening the scope to `total` changed none of the five: no shipped
layout reaches a second distinct reading of `total` under an adjacent generic head. Under the
widening the head competes with *every* reading of its field, at any tier and any distance, so the
group it would have to tie with is not the narrow one.

The other two are `total`, on the ruled table and its as-printed sibling. They come from EXTR-29's
found total, not from the widening: the value is the printed `8600.00` that arithmetic found, and
the anchored `1000.00` is kept as its alternative (**What EXTR-29 changed**, under **End-to-end
field accuracy**).

| Layout | Field | Value | The alternative offered |
|---|---|---|---|
| `corpus_stacked_labels.pdf` | `buyer_name` | `Honeywell Group` | `99999999-0302` |
| `corpus_two_column.pdf` | `buyer_name` | `Honeywell Group` | `TIN: 99999999-0402` |
| `wild_two_party_bare_tin.pdf` | `buyer_name` | `Honeywell Group` | `TIN:` |
| `wild_ruled_lines_totals.pdf` | `total` | `8600.00` | `1000.00` |
| `wild_scanned_no_number.pdf` | `buyer_name` | `7 AWOLOWO ROAD, IKOYI` | `TIN: 99999999-1202` |
| `wild_two_party_bare_tin_asprinted.pdf` | `buyer_name` | `Honeywell Group` | `TIN:` |
| `wild_ruled_lines_totals_asprinted.pdf` | `total` | `8600.00` | `1000.00` |

**Four of the five `buyer_name` alternatives are TIN fragments offered under a *name* field, and that is
accepted rather than overlooked.** An ambiguous field renders a chip picker and **no free-text
input** (`ExtractionFields.tsx` gates the picker on `reason === 'ambiguous'` and a non-empty
alternatives list), so a reviewer correcting the doubtful buyer name on
`wild_two_party_bare_tin.pdf` is offered `TIN:` as one of exactly two choices. Filtering an
implausible alternative away would hide the doubt this feature exists to surface, and it would also
move the count — a cell whose only alternative is filtered falls back to `ReasonNone` — so it is a
different story's scope. It is recorded here because it is invisible from the code: nothing in
`reconcile.go` says an alternative must be a plausible name. Alternative-chip plausibility, and
free-text entry on a doubtful field, are **owed**.

**A reason moves no stored value.** `documentCreateInput` maps `f.Value` and never `f.Reason`, so a cell
flagged `ambiguous` still writes its value to the `invoices` row — which is also why
`corpus_ambiguous_date.pdf`'s long-standing ambiguous `issue_date` has always scored as a hit.
(The importer reads a reason in exactly one place, `isPoorScan`, and that predicate is over the
whole field set rather than one field, so no header field's reason can reach it.) When EXTR-22
measured it, over 110 cells before and after, four lines change and every one of them is a reason
or an alternative; the value column diffs to nothing. EXTR-29's found total does move a value,
because it changes the rank-0 reading: the ruled pair's `invoices` total goes from `1000.00` to
`8600.00` (see "What EXTR-29 changed"). Which value gets filed is decided one rung earlier: the read of
`extraction_field_results` carries `candidate_rank = 0`, and without it a rank-1 alternative would
file `TIN:` on the invoice. The doubt makes that predicate load-bearing.

**`wild_scanned_no_number.pdf` is a live trap for the next author.** It is image-only: pdfium reads
**zero tokens** off it, so a pdfium-sourced walk over "all fourteen layouts" compares an empty set
against an empty expectation on that row and calls it agreement. It must be sourced from its
committed `wild_scanned_no_number.docling.json` golden — 17 tokens on page 1 — and any walk
claiming fourteen rows must prove each row was read, with a zero token count as a fatal rather than
a pass.

## Tier-1 recall and the floor

Re-measured 2026-09-10 on `feature/extr-26-a-label-is-read-by-its-word`: the shipped Tier-1 set reaches
**44 of 44** of the (layout, field) pairs `corpusExpect` names — **1.0000**. The denominator is
the pairs the table actually asserts, so a field absent from a row is not counted and the
ambiguous-date row's two accepted readings are one pair, not two.

That number is **recall**. A pair is a **hit** when the expected value appears **anywhere**
among that field's candidates; which of them the pipeline goes on to decide is not read here at
all. It is **not** end-to-end accuracy. EXTR-04 shipped it as the headline accuracy figure and
it never was that: recall is monotone in the candidate list, so through the EXTR-04 era it read
43/44 while every party name on every layout decided a label. What the pipeline decides is
recorded under **Tier-1 decision rate** below. The two numbers coincide today; they are still
two measures.

| Layout | Hits | Pairs |
|---|---|---|
| `corpus_inline_labels.pdf` | 10 | 10 |
| `corpus_split_labels.pdf` | 10 | 10 |
| `corpus_stacked_labels.pdf` | 7 | 7 |
| `corpus_two_column.pdf` | 7 | 7 |
| `corpus_ambiguous_date.pdf` | 5 | 5 |
| `corpus_totals_block.pdf` | 5 | 5 |

| Field | Hits | Pairs |
|---|---|---|
| `invoice_number` | 6 | 6 |
| `issue_date` | 5 | 5 |
| `supplier_tin` | 6 | 6 |
| `supplier_name` | 5 | 5 |
| `buyer_tin` | 4 | 4 |
| `buyer_name` | 4 | 4 |
| `currency` | 2 | 2 |
| `subtotal` | 3 | 3 |
| `vat` | 3 | 3 |
| `total` | 6 | 6 |

No pair is missed. Every value `corpusExpect` names is reachable by the shipped set, and
`t1aGaps` is empty.

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

Re-measured 2026-09-10 on `feature/extr-26-a-label-is-read-by-its-word`, over the same 44 pairs the
recall rate scores: the pipeline **decides** the value `corpusExpect` names on **44 of 44** —
**1.0000**. It read 30 of 44 — 0.6818 — before EXTR-16, and every one of the 13 pairs it gained
moved for the same two reasons: a Tier-1 rule no longer anchors on a token another lexicon entry
matches more widely, and a value one lexicon entry matches whole is no longer a name or an
invoice number. The layouts that moved are `corpus_inline_labels.pdf`, `corpus_split_labels.pdf`,
`corpus_stacked_labels.pdf`, `corpus_two_column.pdf`, `corpus_ambiguous_date.pdf` and
`corpus_totals_block.pdf` — all six. EXTR-22 took the 44th, `corpus_two_column.pdf` /
`buyer_tin`, by binding each party's TIN to the heading that owns it.

`internal/extraction/accuracy_test.go` pins it as `tier1DecisionHits` / `tier1DecisionPairs`,
and `TestTier1Accuracy_DecisionRateOverTheCorpus` re-measures it. Unlike the floor above this is
a **measurement, not a ratchet**: move the pin to what is measured and say which layouts moved.
Its mutilation control — dropping every `invoice_number` rule must lower it, 44 to 38 — is what
stops it becoming a second number blind to rank.

The two numbers now agree on this corpus, so neither the shipped rate nor the mutilation cut can
still tell a decision measure from a candidate-containment one.
`TestTier1Accuracy_TheDecisionRateIsNotTheRecallRate` carries that job in a **decoy rule set**: a
`same_token` rule that reads `corpus_totals_block.pdf`'s Sub-total amount token whole and files
it under `total`. It sits at Distance 0 and out-ranks the layout's real total, which `Resolve`
still reaches — so recall holds at 44/44 while the decision rate falls to 43/44.

**False decisions: 0.** A false decision is a field decided with a value its layout never
prints. `corpusExpect` names no such field on that layout, so no hit and no miss is scored for
it and the recall rate cannot see it at all. `corpus_totals_block.pdf` / `supplier_name` was the
one such row — that page prints `Supplier TIN: 99999999-0601` and no party name, and the residue
after the label was decided as the supplier's name. It now reads `missing`. `acFalseDecided` is
empty and `TestCorpusDoc_RecordsTheDecisionRate` asserts this section carries no
false-decision row, so a new fabrication is red rather than green.

## Wired-path decision rate

Re-measured 2026-09-10 on `feature/extr-26-a-label-is-read-by-its-word`, over the same 44 pairs the
two rates above score. Driven through `ExtractWorker.Work` — the real document store, the real
transaction boundaries, the fingerprint hoist, the learned-rule lookup and the rank-0 encoding —
and scored from the rows read back out of `extraction_field_results` rather than from a
`Reconcile` return value, the pipeline decides **44 of 44** — **1.0000**. That is the in-process
decision rate, unmoved.

`TestRLS_WiredPathRateMatchesTheInProcessDecisionRate` asserts the *equality* rather than pinning
a second number, so a stage the in-process harness skips fails here instead of being absorbed.
`TestRLS_WiredPathMissesExactlyTheRecordedGaps` compares the missed set to `t1aGaps` by identity
in both directions, because a count alone cannot tell one inherited gap from a new gap plus a new
hit. `t1aGaps` is empty and so is the missed set, so the anti-vacuity floor sits on the
denominator — the walk must score every pair — and the hits assertion carries the claim.

The corpus runs through a real `DoclingReader` replaying the six committed `corpus_*.docling.json`
goldens — the reader the deployed worker uses when `EXTRACTOR=docling` — and a second time
through `PDFiumReader`, the reader the in-process harness reads. Both decide 44 of 44 and miss
the same, empty set; `TestRLS_WiredPathReadsTheSameThroughBothReaders` compares the two missed
sets by identity, not by rate.

Two preconditions are asserted rather than assumed. The denominator is `tier1DecisionPairs`, and
the walk is scoped to `HeaderFields`, so the `line_items` block row every layout writes is not
counted as a field. And every layout's tenant is served **zero** learned rules on the baseline
run, with a seeded run beside it as the positive control — a rule store that served nothing to
anyone would satisfy the zero half alone.

**The line-item figures.** The report carries `line items reached / expected` and
`line items priced / reached` beside the header number, as two raw counts and a slash — never a
computed ratio. `documentCreateInput` groups `line_items[N].<role>` extraction fields onto
`invoice.CreateInput.LineItems`, so an extracted line does reach the invoice. The corpus-wide
figures below still read **0 on every layout**, for a text-seam reason spelled out under
[Line-item outcome](#line-item-outcome): this walk reads all but one layout through pdfium, which
extracts no line row.

Because no `corpus_*` layout carries a table, their denominators are 0 too, so those rows render
`0/0` — which is indistinguishable from a satisfied denominator. The report therefore prints a
`NO LINE SIGNAL` line whenever every reached row has a zero denominator, and stops printing it
the moment a table-bearing layout joins the scored set.
`TestEndToEnd_AnEmptyLineCorpusSaysSoInWords` holds both directions. Read the zeros as "not
measured here", never as coverage.

`TestRLS_EndToEndTheWorkerWroteLineRowsAndTheInvoiceGotThem` and
`TestRLS_EndToEndInvoiceReadMatchesAnyRankOnAZeroAttritionFixture` (`endtoend/lines_db_test.go`)
drive the one committed table-bearing fixture, `rich_invoice.pdf`, through the real import path:
the invoice reads back 4 line(s), 3 priced, matching what the worker extracted. That fixture has
zero attrition, so it cannot show the invoice-read score diverging from an any-rank extraction
read — a fixture with a rejected/quarantined line is still owed for that.

**The rank control.** On this corpus the rank-0 rate and the any-rank rate both read 44 of 44, so
the shipped number alone cannot tell a decision measure from a candidate-containment one. That is
the EXTR-16 defect, and `TestRLS_WiredPathRankingDecoyMovesTheRate` is what stops it recurring
here: a **learned** `AnchorRule`, written through the real rule store for
`corpus_totals_block.pdf`'s fingerprint, whose label matches both amount tokens on that layout and
files them under `total`. `TierLearned` out-ranks `TierGeneric`, the two candidates tie at
Distance 0, and `compareRegions` hands rank 0 to the Sub-total amount above — so the wired rate
falls to **43 of 44** while the layout's real total, still reached, is stored one rank down.
Scored at *any* rank the same run reads 44 of 44, and the spec asserts that difference. A
single-amount decoy does not discriminate: `decideField` keeps an alternative only at the head's
tier *and* distance, so a lone `TierLearned` match leaves the real total with no row at all and
both scorers read 43.

**This section has no honest oracle, and is recorded as having none.**
`TestCorpusDoc_RecordsTheWiredPathRate` compares this prose to a Go constant, so it fails when
someone edits the page — never when the wired measurement itself is wrong. The measurement's real
reader is the `Report the wired-path decision rate` step in `.github/workflows/ci.yml`. This
package's DB-backed suite runs through a gate that pipes `go test -json` into a file and discards
a passing test's buffered log, so the report reaches CI only from a step of its own, which greps
its own output for the marker; `TestRLS_WiredPathTheCIStepsRunFilterNamesARealTest` keeps that
step's `-run` filter from rotting into one that matches nothing.

EXTR-22 closed the last reach limit by widening an EXISTING anchor pattern, which sits behind
both fingerprint producers, so it bumped `FingerprintVersion` **and** `BoxlessFingerprintVersion`
together. The bump is a namespace decision taken **by policy**, not one forced by a measured
digest shift: EXTR-26 widened six shared patterns the same way and measured it directly — 0 of 72
pinned digests moved (`84a08f9c`) — and still bumped both levers. The rule is that a widened
shared pattern bumps both; the bump, not the widening, is what retires every stored rule. A NEW
rule-less owning-phrase entry needs neither bump — it resets only pages that print the phrase. A
reach limit closed by another route needs neither bump either.

## End-to-end field accuracy

Re-measured 2026-09-14 on `feature/extr-29-a-total-no-label-points-at-can-still-be-found`: a document goes in at the
extraction worker and an `invoices` row comes out the other side, and that row carries the value
the page prints on **75 of 112** cells — **0.6696**. Fourteen layouts, eight written fields each.
This is the first number on this page measured **end to end**: not what Tier-1 can reach, not
what the pipeline decides, but what a user would find in the database.

The three rates above it all read 44 of 44. This one does not, and the gap is the point. Recall
(`## Tier-1 recall and the floor`) scores the candidate list; the decision rate
(`## Tier-1 decision rate`) scores rank 0; the wired-path rate (`## Wired-path decision rate`)
scores `extraction_field_results`. None of them reads the `invoices` row, and none of them scores
the five arrangements added by EXTR-21 — so all three read 44/44 while eleven observed defects
sat between the decision and the row.

**This number is bad on purpose.** EXTR-21 fixes none of those defects; it builds the oracle
EXTR-22…EXTR-28 are graded against. Every one of the 37 misses is named cell by cell, with its
reason, in `eeAbsentCells` (13 cells the page carries no value for) and `eeRealMisses` (24 cells
the page does carry and the row does not). Nothing is hidden behind the green.

### Per layout

| Layout | Hits | Cells |
|---|---|---|
| `corpus_inline_labels.pdf` | 8 | 8 |
| `corpus_split_labels.pdf` | 8 | 8 |
| `corpus_stacked_labels.pdf` | 5 | 8 |
| `corpus_two_column.pdf` | 5 | 8 |
| `corpus_ambiguous_date.pdf` | 3 | 8 |
| `corpus_totals_block.pdf` | 4 | 8 |
| `wild_two_party_bare_tin.pdf` | 8 | 8 |
| `wild_ruled_lines_totals.pdf` | 8 | 8 |
| `wild_rc_due_naira.pdf` | 8 | 8 |
| `wild_stacked_borderless.pdf` | 2 | 8 |
| `wild_scanned_no_number.pdf` | 0 | 8 |
| `wild_two_party_bare_tin_asprinted.pdf` | 8 | 8 |
| `wild_ruled_lines_totals_asprinted.pdf` | 8 | 8 |
| `wild_stacked_borderless_asprinted.pdf` | 0 | 8 |

`wild_scanned_no_number.pdf` scores a full **0 / 8**. The page prints no invoice number at all,
so the import quarantines the document and writes no `invoices` row — the six cells OCR reads
cleanly off its committed golden score nothing. They reach an invoice only when the operator
supplies the number (`POST /v1/imports/document/invoice`). A quarantined layout stays in the denominator;
dropping it would flatter the rate by the exact amount the defect costs.
`wild_stacked_borderless_asprinted.pdf` scores **0 / 8** the same way: no lexicon entry reads its
letter-spaced invoice-number label, so it quarantines too.

### Per field

| Field | Hits | Cells |
|---|---|---|
| `invoice_number` | 12 | 14 |
| `issue_date` | 10 | 14 |
| `buyer_tin` | 10 | 14 |
| `buyer_name` | 9 | 14 |
| `currency` | 7 | 14 |
| `subtotal` | 8 | 14 |
| `vat` | 8 | 14 |
| `total` | 11 | 14 |

`currency` at 7 of 14 is still the worst field on the corpus, and its seven misses split four ways:
three layouts print the value inside a total with no label to anchor it, one prints no currency at
all, one offsets the value from a colon-less label, and the last two are the quarantined pages.
`buyer_tin` reads 10 of 14: EXTR-22 bound each party's TIN to the heading that owns it, and the
four cells left are two layouts that carry no buyer block at all and the two quarantined pages.

### What EXTR-23 changed

*Historical, recorded when EXTR-23 merged. EXTR-29 later found the ruled-table total this
subsection records as missed (`TestRLS_EndToEndScoresTheCorpus`); see **What EXTR-29 changed**.*

Nothing on this page, and that is the finding rather than an omission. EXTR-23 ships an arithmetic
referee: when a `total` cell arrives ambiguous and exactly one of its competing readings equals the
decided `subtotal` plus the decided `vat` to within a kobo, that reading is taken and the doubt is
removed. Two readings inside that tolerance pick nothing, and no reading is ever condemned. It
moved no cell: `total` stayed 8 of 11 in the per-field table, no per-layout row moved, and the
headline it left behind was 57 hits, against EXTR-21's frozen baseline of 53. **Do not read
"EXTR-23 merged" as "the ruled-table total is fixed".** It is not.

The referee is **inert on this corpus because no arrangement reaches two competing readings**, not
because the mechanism cannot reach the defect. Measured across all fourteen layouts, `Resolve` emits
zero or one `total` candidate and never two, so the tie the referee breaks never occurs here.

`wild_ruled_lines_totals.pdf/total` is the cell it was written for, and it stays in `eeRealMisses`.
The reason that constant carries — the Total label continuing on the last data row's baseline — is
true but narrower than the geometry. The printed `8,600.00` is not out-ranked; it is **unreachable
by every shipped relation**. `Total` sits at `x=[0.621190,0.663190] y=[0.448717,0.459808]` and
`8,600.00` at `x=[0.817739,0.892562] y=[0.524702,0.537566]`, so `right` fails on a y-overlap of
`-0.064894`, and `below` fails on an x-overlap of `-0.154549` **and** a gap of `0.064894` against
a `0.06` dial. An overlap is not a distance, so no dial widening reaches that value; finding it
needs a new candidate *source*, which is EXTR-29's scope and not this story's.

The mechanism is therefore graded by `internal/extraction/endtoend/total_test.go` — the exact
per-layout candidate count, plus two planted controls that drive the same instrument to a positive
— and by the unit specs in `reconcile_total_test.go` and `reconcile_total_adversarial_test.go`. It
is not graded by a moved score, and this subsection is where that is recorded rather than inferred
from a number that did not change.

### What EXTR-25 changed

EXTR-25 ships `t1.currency.sweep`, a shape-only Tier-1 rule that reads `NGN` off a bare ₦ with no
label to corroborate it. It moves **exactly one cell**: `wild_rc_due_naira.pdf/currency`, which
takes that layout to 8 of 8 and `currency` to 5 of 11, and the headline from 57 hits to 58. Every
other per-layout and per-field figure above is unchanged, and `eeBaselineHits` is untouched.

The rule is a fallback tier, so a labelled reading always outranks it
(`TestResolve_ALabelledCurrencyBeatsABareSymbol`). It is also blind: nothing ties the symbol to an
amount, so a ₦ in a footer, a street name or a reference code resolves the field just as readily,
and an unlabelled invoice priced in another currency that carries one ₦ reads `NGN` with no doubt
attached. Those readings are pinned in `internal/extraction/sweep_qa_test.go` rather than left to
be found on a tenant's document.

EXTR-25 also ships a `due_date` owning phrase: `(?i)\bdue\s*date\b` refuses to anchor the issue
date. It moves no cell, and **not** because the corpus is silent on it: `wild_rc_due_naira.pdf`
prints "Due Date" above a second date (`internal/extraction/fixtures_test.go:798`), and the entry
does remove that token's competing `2026-08-07` read. The printed issue date was already winning
that contest on distance, so the decided value — and every figure above — is the same before and
after. This score therefore does not grade the refusal; its oracles are
`TestResolve_ADueDateNoLongerContestsTheIssueDate` and
`TestWildLayouts_TheRCLayoutDoesNotReproduceItsDateOrVATDefect`, and both red the moment the entry
is removed.

### What EXTR-26 changed

Nothing on this page moved, and the headline stayed at **58 of 88**. EXTR-26 widened six shared
header-field patterns — the party suffix (`buyer_name`, `buyer_tin`), the `From` guard,
`Taxable amount`, `Amount payable` and `Issued` — and added one rule-less owning phrase,
`withholding_tax`. It moved no cell because no arrangement then scored printed a spelling the pre-story
lexicon missed. Two goldens print a suffix-arm spelling that already matched before EXTR-26:
`Invoice to` (`wild_two_party_bare_tin.pdf`) and `BILL TO` (`wild_scanned_no_number.pdf`, the
quarantined 0-of-8 page). Measured, not assumed, on the docling side: the suffix adds no anchor on
any lexicon-friendly docling golden (`TestAnchorLexicon_TheSuffixAddsNoAnchorOnTheCommittedFixtures`), the
three advisory arms add none on the eleven lexicon-friendly arrangements
(`TestAnchorLexicon_TheAdvisoryArmsAddNoAnchorOnTheScoredArrangements`), and the `From` arm adds
none on any lexicon-friendly docling golden
(`TestAnchorLexicon_TheFromArmAddsNoAnchorOnTheCommittedArrangements`). This walk reads all but
one layout through pdfium, not its golden, so the unmoved 58 is the pdfium-side proof.
`withholding_tax` is listed in `wildUnprintedPhraseIDs`: no scored layout prints the phrase
either. The words EXTR-26 newly matches are printed on the advisory fixtures, which carry no golden
and sit outside this ratchet — see **The advisory arrangements** below — and on the as-printed
siblings, which are scored.

### What EXTR-29 changed

The headline moved from 73 hits to 75, on exactly two cells: `wild_ruled_lines_totals.pdf/total`
and `wild_ruled_lines_totals_asprinted.pdf/total` now file the printed `8600.00`, and
`eeRealMisses` loses those two entries (`TestRLS_EndToEndScoresTheCorpus`). Both layouts read 8 of
8 and `total` reads 11 of 14 in the tables above; every other row is unchanged
(`TestRLS_EndToEndDocRecordsTheMeasuredTables`). EXTR-21's frozen baseline stays 53
(`TestRLS_EndToEndNoBaselineHitRegresses`).

EXTR-29 adds `findTotal` to `Reconcile`, after EXTR-23's referee. It runs only when:

- `subtotal` and `vat` are both decided (`TestReconcile_AnUndecidedAddendFindsNothing`);
- at least one anchored `total` reading exists, so a total no label reaches stays missing
  (`TestReconcile_AMissingTotalIsNotFilledByArithmetic`);
- no anchored `total` reading already equals `subtotal + vat` within a kobo, because EXTR-23
  decides that case (`TestReconcile_ACorroboratedAnchoredTotalIsNeverReDecided`);
- no `total` reading comes from a learned rule
  (`TestReconcile_ALearnedTotalIsNeverOverriddenByArithmetic`).

It then counts the tokens on every page (`TestReconcile_FindTotalReadsEveryPage`; through the
worker, `TestRLS_EndToEndAFoundTotalOnALaterPageReachesTheInvoiceRow`) that carry a usable box and
read as one amount through `ShapeAmount` (`TestReconcile_AFoundTotalIsReadThroughTheAmountShape`)
equal to `subtotal + vat`. The tolerance is one kobo, inclusive, so `8,600.01` beside `8,600.00`
is a second match (`TestReconcile_TwoAmountsInsideOneKoboFindNothing`). The decided `subtotal` and
`vat` cells' own tokens do not count, matched by box
(`TestReconcile_AnAddendsOwnTokenIsNeverItsTotal`). Exactly one match becomes the total. No match
finds nothing (`TestReconcile_AWithholdingNetPayableIsNotCondemned`, arm 1). Two or more find
nothing either, and a grand total printed twice is two matches
(`TestReconcile_TwoUnrelatedBalancingAmountsFindNothing`,
`TestReconcile_AGrandTotalPrintedTwiceFindsNothing`). A document the pass cannot help keeps
EXTR-23's header results exactly, with no new reason code
(`TestReconcile_ADocumentFindTotalCannotHelpIsLeftExactlyAsEXTR23LeftIt`).

A found total presents doubtful (0.6d gate, Q1): reason `ambiguous` at rank 0 with the found value,
and the anchored reading kept as its alternative. On the ruled table that is `8600.00` over
`1000.00`, under the pdfium reader and the docling golden alike, and the `invoices` row takes
`8600.00` (`TestRLS_EndToEndTheFoundTotalReachesTheInvoiceRow`). Extraction review renders an
ambiguous field as chips with no input (`ExtractionFields.test.tsx`, "renders one chip per
candidate and no input, and its neighbour keeps its input"), so `total` on such a document is a
chooser where it was an input. The doubt counts once toward the audit row's `flagged_count`
(`TestReconcile_AFoundTotalIsCountedOnceByTheFlaggedCount`), and `flagged_count` equals the rank-0
rows carrying a reason (`TestRLS_ExtractWorkerAuditCountsAgreeWithTheRowsItWrote`).

It fires on no other scored arrangement. With pages, only the ruled pair's header results move,
under the pdfium walk and under every layout's docling golden
(`TestEndToEnd_FindTotalMovesExactlyTheRuledPair`). A token without a usable box is not evidence
(`usableBox` in `resolve.go`, `TestReconcile_ATokenWithNoUsableBoxIsNotEvidence`), and a DOCX
reading carries the zero box on every token (`TestDoclingGolden_BoxlessPairIsBoxless`), so the pass
is structurally silent on a DOCX.

On a withholding-tax invoice (0.6d gate, Q2, Option A) no withholding label holds the pass off. If
the gross `subtotal + vat` is printed once and the anchored total is the net payable, the gross is
found and the net payable becomes its alternative. If the gross is not printed, the net payable
stays decided (`TestReconcile_AWithholdingNetPayableIsNotCondemned`). No scored layout prints a
withholding phrase (`wildUnprintedPhraseIDs`), so that in-memory spec is the only grade.

**Known limit.** With `vat` at `0.00`, a second printed copy of the subtotal value counts: only the
decided subtotal's own box is left out, and the copy equals `subtotal + vat`. If the anchored total
fails the identity, that copy is found and presented doubtful. The pass reads positions, not
labels, so the copy and a real grand total are the same input. The arm "vat 0.00 with a printed
grand total: that token is found" of `TestReconcile_AnAddendsOwnTokenIsNeverItsTotal` files exactly
that shape.

On `advisory_dense.pdf`, the dense register's replica, the pass is unnecessary. The referee already
decides the anchored `3426476.50`, which equals `3187420.00 + 239056.50`
(`TestAdvisory_TheDenseInvoiceLetsTheRefereeDecide`, run without pages), and a balancing anchored
reading holds `findTotal` off (`TestReconcile_ACorroboratedAnchoredTotalIsNeverReDecided`).

**Two synthetic arrangements are not evidence about real documents.** The move above is one ruled
geometry printed with two label sets. On the two real documents re-measured since EXTR-22, the
boxed-header invoice and the dense register (production read on `d36be21e`, 2026-09-09, no
oracle), the pass is either unnecessary or unable to fire. Whether arithmetic finds a total on a
real document is unmeasured.

### Moving the figure

The number lives in `internal/extraction/endtoend/score_test.go` as two pinned integers,
`eeCorpusHits` and `eeCorpusCells`; `eeCorpusFloor` is their quotient and never a decimal literal
beside them.

1. Re-measure: `go test -p 1 -count=1 -v -run TestRLS_EndToEnd ./internal/extraction/endtoend/`.
   The report prints all three tables on this page. CI prints it too, from the
   `Report the end-to-end field accuracy` step — the gated step runs this package through
   `rlsgate`, which deletes a passing test's output.
2. Edit `eeCorpusHits` to the measured value. `eeCorpusCells` moves only when a layout or a
   written field is added.
3. Move every cell that changed between `eeRealMisses` and the hits, with a written reason per
   cell. `TestRLS_EndToEndScoresTheCorpus` compares the measured miss set against
   `eeAbsentCells ∪ eeRealMisses` in **both** directions, so a cell that now hits reds by name
   and so does a cell pinned as neither.
4. Update all three tables on this page **in the same commit**, or
   `TestRLS_EndToEndDocRecordsTheMeasuredTables` fails: it parses every row and compares it
   against a live walk, so a table that sums correctly with the numbers in the wrong rows is
   still red, and `TestCorpusDoc_RecordsTheEndToEndProcedure` fails on the headline and the rate.

### Why the number may only go up

`TestRLS_EndToEndMeetsTheFloor` admits less than one cell of slack in either direction: the pin
must be neither below nor above what the walk measured. An unrecorded **improvement** is
therefore as red as a regression — deliberately, because an improvement that ships without its
number being recorded is the exact failure this measurement exists to close. The pin is set to
what was measured, never to a target, and it may **only go up**.

Lowering it is not impossible, only loud. It takes four edits in one commit — the constant,
`eeRealMisses` with a reason per cell, the per-layout table and the per-field table — and the
live walk must then agree that those cells really do miss. What no test can catch is an author
who lands all four with plausible reasons, having actually broken extraction. That is a
regression accepted in review, not one accepted in silence, and this paragraph is what a
reviewer is pointed at.

### What this number cannot see

Like the recall rate, it is **monotone** in both distance dials: widening a dial only adds
candidates, so a sloppier rule set can only raise it. The dial window itself is guarded
elsewhere (`TestTier1_DialsStayInsideTheirMeasuredWindow`), and this figure's non-vacuity rests
on the mutilation controls instead — `eeCutScore` is what the same corpus scores with every
`invoice_number` rule removed, and `eeDecoyBaseHits` is the ranking decoy's base. Read the
distance dial guarantees off those, never off this rate.

It also cannot see a defect that lives only in a real document. The corpus is fourteen **synthetic
arrangements** — six `corpus_*` layouts, the five `wild_*` reproductions and three as-printed siblings — not the five source
mock-ups. A production read that fails on paper texture, a scanner's skew or a vendor's
unmodelled block moves nothing here. The manual production pass that read 18 of 40 fields is not
reproducible in this repo and never will be.

**Three claims in this section have no honest oracle, and are recorded as having none.** Why each of
the 24 real misses exists is prose here and pinned in `eeRealMisses`, which carries its own weld
to the walk — a second copy would be a competing source of truth. The cause of the permanent
line-item zero is source fact, stated below rather than scanned for. And the 18-of-40 production
pass above is unrepeatable. Everything else in these two sections is parsed and compared against a live
measurement.

## Line-item outcome

Measured in the same walk. `reached` is how many `line_items` rows the layout's document produced
on the invoice; `expected` is how many its committed golden carries; `priced` is how many of the
reached rows hold a unit price.

| Layout | Reached | Expected | Priced |
|---|---|---|---|
| `corpus_inline_labels.pdf` | 0 | 0 | 0 |
| `corpus_split_labels.pdf` | 0 | 0 | 0 |
| `corpus_stacked_labels.pdf` | 0 | 0 | 0 |
| `corpus_two_column.pdf` | 0 | 0 | 0 |
| `corpus_ambiguous_date.pdf` | 0 | 0 | 0 |
| `corpus_totals_block.pdf` | 0 | 0 | 0 |
| `wild_two_party_bare_tin.pdf` | 0 | 0 | 0 |
| `wild_ruled_lines_totals.pdf` | 0 | 3 | 0 |
| `wild_rc_due_naira.pdf` | 0 | 0 | 0 |
| `wild_stacked_borderless.pdf` | 0 | 0 | 0 |
| `wild_scanned_no_number.pdf` | 0 | 0 | 0 |
| `wild_two_party_bare_tin_asprinted.pdf` | 0 | 0 | 0 |
| `wild_ruled_lines_totals_asprinted.pdf` | 0 | 3 | 0 |
| `wild_stacked_borderless_asprinted.pdf` | 0 | 0 | 0 |

**0 of 6** lines reach any invoice, on the two scored layouts that carry a table at all. That
zero is a TEXT-SEAM outcome, not a wiring one. `documentCreateInput` groups
`line_items[N].<role>` extraction fields onto `invoice.CreateInput.LineItems`, and
`invoice.Store.Create` writes one `line_items` row per entry, so an extracted line does reach the
invoice. This walk reads every layout except `wild_scanned_no_number.pdf` through pdfium
(`eeOptsFor`, `score_db_test.go`), and pdfium extracts no line row from any of them.
`TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling` runs
`wild_ruled_lines_totals.pdf` through both readers in one test: **0 of 3** under pdfium, **3 of 3
reached / 2 priced** under the docling golden, which is what deployed extraction uses. Read this
table's zero as "pdfium read no table here", never as "lines do not reach invoices".

Every other row reads `0 / 0`, which is indistinguishable from a satisfied denominator, so the
report prints a `NO LINE SIGNAL` line whenever every reached row has a zero denominator. Read
those zeros as "not measured here", never as coverage.

The zeros also read the same for a scorer that cannot see `line_items` at all, so the same run
scores a control invoice built from two `LineItemInput` entries, one of them priced: it reads
**2 / 1**. `TestRLS_EndToEndTheLineScorerReadsLinesWhenTheyExist` re-derives both numbers off the
control itself, so a control cut down to one entry cannot leave the pins asserting nothing.

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

The single `-update` flag is `fxUpdate` in `fixtures_test.go`. Do not add a second
`flag.Bool("update", …)` anywhere in the package — a duplicate flag name panics the test binary
at registration, before any test runs.

## Adding a layout

Nine edits, no new test:

1. A builder plus an `fxCorpus` entry in `fixtures_test.go`. The files go **flat** in
   `testdata/` with a `corpus_` prefix — `TestFixtures_MatchTheirGenerator` counts
   non-directory `.pdf` entries, so a subdirectory sits outside its floor.
2. A `corpusExpect` row in `corpus_test.go`.
3. The file name in `corpusLayouts`, the hard-coded set `TestCorpus_HasAllSixNamedLayouts`
   pins. It is hard-coded rather than derived from `fxCorpus` precisely so it can see a
   missing layout; update its expected count with it.
4. The layout's token count in `corpusTokenFloor`, in `corpus_adversarial_test.go`. That table
   must name every layout, so a new one without an entry fails rather than going unmeasured.
5. The `.pdf` name in `requiredPDFs` and the matching `.docling.json` in `requiredGoldens`, in
   `internal/extraction/endtoend/harness_db_test.go`. Those two lists are that package's own —
   `corpusLayouts` is an unexported identifier in a `_test.go` file of a different package and
   is unreachable from there. `TestEndToEnd_AbsentFixtureFatalsRatherThanSkips` compares them
   against what is committed, so a layout missing from them is a red test.
6. An `expectByLayout` row in `internal/extraction/endtoend/score_test.go`, carrying one key per
   `writtenFields` entry — an empty list where the layout prints nothing, plus its reason in
   `eeAbsentCells`. Move `eeLayoutCount`, `eeWrittenCells` and `eeCorpusHits` with it, and
   `eeCutReach` in `mutilation_db_test.go` — the mutilation cut's reach is taken over the same
   cells, so a new layout moves it too.
   `TestEndToEnd_TheScoredSetIsTheRequiredSet` makes edit 5 without this one a red test, so a
   layout cannot be registered on disk and go unscored end to end.
7. An `eeLinesExpected` row in `internal/extraction/endtoend/lines_test.go`, holding how many
   line rows the layout's golden carries. The map must hold exactly one key per `expectByLayout`
   row, and `TestEndToEnd_TheExpectedLineCountIsTakenOffTheGoldens` re-derives every value from
   that layout's own golden, so a hand-guessed count is a red test. A layout that carries a
   table also clears the `NO LINE SIGNAL` note off the report — see **The line-item figures**.
8. A row in **each** of the three tables above — the per-layout and per-field tables under
   **End-to-end field accuracy**, and the table under **Line-item outcome** — plus the
   re-measured `eeCorpusHits`. `TestRLS_EndToEndDocRecordsTheMeasuredTables` compares every row
   against a live walk and both accuracy tables against `eeCorpusHits`/`eeCorpusCells`, so a
   table left short a row, or summing right with the numbers in the wrong rows, is a red test.
   Follow **Moving the figure** in that section; the number may only go up.
9. A new `wild_*` arrangement declares a `wildPairs` row: its as-printed sibling, or an exemption with the reason.
   `TestWildPairs_EveryArrangementDeclaresASiblingOrAnExemption` makes a missing row a red test, and
   **The as-printed siblings** below records the pair.

Every value in the new row must also be reachable by `Tier1Rules`, or the pair goes in `t1aGaps`
in `tier1_adversarial_test.go` with the reason. An unreachable expectation with no entry there
is a red test, which is the point.

Then regenerate as above and commit the new `.pdf` with its generator. Every value in the new
`corpusExpect` row must be readable out of the new fixture:
`TestCorpus_EveryExpectedValueAppearsInItsFixture` runs each token's word runs through the
field's shape and fails on a row naming a value the bytes do not carry.

**Not every committed fixture is a layout.** `learned_two_party.pdf` is generated and
byte-compared exactly like the six layouts, and it is deliberately named *outside* the `corpus_`
prefix so that none of the nine edits above apply to it. Do not add a `corpusExpect` row, a
`corpusLayouts` entry or a `corpusTokenFloor` entry for it by reflex — the **Learned rules**
section below says why. `rich_invoice.pdf` (EXTR-18-01) follows the same pattern for a different
reason: a ruled table plus a deliberately inconsistent totals block, exercised by
`TestFixtures_RichInvoice*` in `fixtures_test.go`, not the anchor-rule corpus. The eight
`wild_*.pdf` arrangements (EXTR-21-06, EXTR-21-07, and EXTR-33's three as-printed siblings) are the same category again: generated, byte-compared and
scored by `expectByLayout`, but outside every `corpus_` ratchet, so edits 2, 3 and 4 above do not
apply to them.

The three advisory arrangements (`advisory_register.pdf`, `advisory_register_unspaced.pdf`,
`advisory_dense.pdf` — **The advisory arrangements** below) join this list: generated and
byte-compared like every other fixture, but outside `requiredPDFs`, `expectByLayout` and every
`corpus_` ratchet. The spec that reads `wildUnprintedPhraseIDs`
(`endtoend/wild_adversarial_test.go:456`) walks `expectByLayout` only, so it never reads them.
Registering either register fixture in `expectByLayout` must remove `withholding_tax` from that
list in the same commit, because both print the withholding line; `advisory_dense.pdf` prints
none. `reg_identifier` left the list that way when `wild_two_party_bare_tin_asprinted.pdf`, which
prints `VAT Reg. No:`, was registered. `dense_invoice.pdf` (pre-existing, unrelated to EXTR-26) belongs on this "not a layout" list
too and has never been added; named here, not fixed.

This also deviates from **When a client's invoice fails to extract** step 4 below, which asks a
reproduction to add its `corpusExpect` row and watch it fail: the advisory reproductions
deliberately carry none, because a scored row would move the denominators EXTR-26's no-regression
claim is measured against, and the dense source's scored arrangement belongs to EXTR-33 (below).

**The EXTR-33 collision.** `advisory_dense.pdf`'s source, NG-4, is scored on
`wild_ruled_lines_totals_asprinted.pdf`, and EXTR-26 fixed its `Taxable amount` subtotal (record (a)
under **The as-printed siblings**). Its total missed for the ruled twin's own geometric reason,
because `TOTAL DUE (NGN)` does not fit at the twin's `Total` position, and EXTR-29's arithmetic
pass now finds it (`TestRLS_EndToEndScoresTheCorpus`).
`advisory_register.pdf`'s source, NG-3, is divergence (c)'s source, scored on
`wild_stacked_borderless_asprinted.pdf`, which quarantines. No scored arrangement exists at the
register's own geometry, and that gap is **owed and unowned**.

## The as-printed siblings

The five documents the `wild_*` arrangements were drawn from are **synthetic mock-ups, not client
documents**, so transcribing their printed labels verbatim does not breach the 100%-synthetic rule
at the top of this page. Labels and column headers cross into the repository; the documents' bytes
never do, and neither does any address, TIN, account number or invoice number printed on them.
Short supplier names in the file names below cross, and so do the full names the advisory
arrangements print verbatim: `Okonkwo Advisory Partners`, `Honeywell Group Nigeria Plc`, `Access
Bank Plc`, `Sahara Telecoms Nigeria Plc`, `Bello Construction Nigeria Ltd`, `United Bank for Africa
Plc` and `Cisco ISR 4331`, all in `fixtures_test.go`. `Honeywell Group` itself predates EXTR-33 as
the corpus's own generic buyer name. The two invoices **The advisory arrangements** transcribes are
NG-3 and NG-4 below.

The sources live outside the repository, in the project vault at
`Simon Vault/Projects/ASComply Africa/User Stories/EXTR/sources/`:

- NG-1 `NG-Invoice-1-Adeola-Steel-classic.pdf`
- NG-2 `NG-Invoice-2-Rivers-Energy-boxed-header.pdf`
- NG-3 `NG-Invoice-3-Okonkwo-Advisory-minimal.pdf`
- NG-4 `NG-Invoice-4-Sahara-Telecoms-dense.pdf`
- NG-5 `NG-Invoice-5-Ibadan-Goods-scan.pdf`

No test reads that folder, so the inventories below are the durable record of what each source
prints. `TestCorpusDoc_EachPrintedLabelIsInItsSourceInventory` holds each label a sibling prints to
its source's inventory; nothing checks the other labels.

### The pairs

Each lexicon-friendly `wild_*` arrangement declares a `wildPairs` row in
`internal/extraction/endtoend/goldens_test.go`: its as-printed sibling, or an exemption with the
reason. `TestWildPairs_EveryArrangementDeclaresASiblingOrAnExemption` refuses an arrangement that
declares neither. The scores are in **Per layout** above.

| Lexicon-friendly | As-printed | Source | Divergence | Verdict |
|---|---|---|---|---|
| `wild_two_party_bare_tin.pdf` | `wild_two_party_bare_tin_asprinted.pdf` | `NG-Invoice-2-Rivers-Energy-boxed-header.pdf`, signature label from NG-5 | (d) | not reproduced |
| `wild_ruled_lines_totals.pdf` | `wild_ruled_lines_totals_asprinted.pdf` | `NG-Invoice-4-Sahara-Telecoms-dense.pdf` | (a), (b) | (a) subtotal fixed by EXTR-26, total found by EXTR-29; (b) fixed by EXTR-24 |
| `wild_stacked_borderless.pdf` | `wild_stacked_borderless_asprinted.pdf` | `NG-Invoice-3-Okonkwo-Advisory-minimal.pdf` | (c) | reproduces |
| `wild_rc_due_naira.pdf` | exempt: no vocabulary divergence was measured on its source, NG-1 | `NG-Invoice-1-Adeola-Steel-classic.pdf` | none measured | — |
| `wild_scanned_no_number.pdf` | exempt: its source has no text layer; the OCR path is out of scope | `NG-Invoice-5-Ibadan-Goods-scan.pdf` | none | — |

`wild_rc_due_naira.pdf` has no sibling because no failure was measured on NG-1, although its labels
differ from the twin's (`Invoice Date:`, `RC No:`, `Subtotal`, `VAT @ 7.5%`, `Total Due`,
`BILL TO`). `wild_scanned_no_number.pdf` has none because NG-5 is image-only: its failure is an
absence of text, not of vocabulary.

### Source label inventories

Labels and column headers only, as each source prints them.

- **NG-1 Adeola Steel (classic):**
  - Header: `TAX INVOICE` (letter-spaced), `Tel:`, `RC No:`, `TIN:`, `Invoice No:`, `Invoice Date:`, `Payment Terms:`, `Due Date:`
  - Parties: `BILL TO`, `DELIVER TO`, `TIN:`, `RC No:`, `Attn:`
  - Columns: `#`, `Description`, `Qty`, `Unit Price (₦)`, `Amount (₦)`
  - Totals: `Subtotal`, `VAT @ 7.5%`, `WHT @ 5% (on services)`, `Total Due`
  - Footer: `Bank:`, `Account Name:`, `Account No:`, `Amount in words:`
  - Amounts are ₦-joined; WHT is parenthesised.
- **NG-2 Rivers Energy (boxed header):**
  - Header: `Sales Invoice`, `Invoice No.`, `Invoice Date`, `Customer No.`, `Purchase Order`, `Due Date`, `Page`, `Tel`, `Fax`, `VAT Reg. No:`, `RC No:`, `Currency: NGN`
  - Parties: `INVOICE TO:`, `SITE:`, `TIN:`
  - Columns: `Code`, `Description`, `Qty`, `Unit Price`, `Line Total`, `VAT Rate`
  - Totals: `Net Amount`, `VAT @ 7.5%`, `Total NGN`
  - Footer: `FOR BANK TRANSFERS`, `Bank Name:`, `Account No:`, `Account Name:`, `Sort Code:`, `Branch:`, `Currency: NAIRA`
- **NG-3 Okonkwo Advisory (minimal, two pages):**
  - Header: `Invoice`, `INVOICE NUMBER`, `ISSUED`, `DUE`, `CURRENCY`
  - Parties: `FROM`, `BILLED TO`, `TIN`, `RC`
  - Totals: `Subtotal`, `VAT 7.5%`, `Withholding tax 10%`, `Amount payable`
  - Spacing: `INVOICE NUMBER`, `ISSUED`, `DUE`, `CURRENCY`, `FROM` and `BILLED TO` print letter-spaced, the rest unspaced. R0 letter-spaces only `I N V O I C E   N U M B E R` and `I S S U E D`. Amounts are ₦-joined.
- **NG-4 Sahara Telecoms (dense):**
  - Title: `MONTHLY SERVICE INVOICE`
  - Header strip, labels above values: `INVOICE NUMBER`, `BILL PERIOD`, `INVOICE DATE`, `PAYMENT DUE`, `ACCOUNT NUMBER`, `CURRENCY`
  - Parties: `SUPPLIER`, `CUSTOMER`, `TIN:`, `RC:`, `VAT Registration:`, `Contact:`
  - Summary box: `SUMMARY`, `Previous balance`, `Current charges`, `VAT`, `Total payable`
  - Columns: `Item`, `Service description`, `Reference`, `Qty`, `Unit rate ₦`, `Amount ₦`, `VAT ₦`
  - Totals: `Taxable amount`, `VAT @ 7.5%`, `Previous balance`, `TOTAL DUE (NGN)`
  - Footer: `Payment instructions.`
- **NG-5 Ibadan Goods (scan, image only; read off the render):**
  - Header: `Tel:`, `Mobile:`, `RC No.`, `TIN:`, `SALES INVOICE` (letter-spaced), `Invoice No.:`, `Order Ref.:`, `Date:`, `Terms:`
  - Parties: `CUSTOMER:`, `TIN:`
  - Columns: `S/N`, `DESCRIPTION OF GOODS`, `QTY`, `RATE (N)`, `AMOUNT (N)`
  - Totals: `SUB-TOTAL`, `V.A.T. @ 7.5%`, `DELIVERY`, `TOTAL`
  - Footer: `Amount in words:`, `Prepared by`, `Customer's Signature`, stamp `PAID`

### Label tables

A sibling keeps its twin's geometry and swaps a twin token's label for the source's printed label
only when all four hold:

1. The source prints a label for the same role.
2. It takes the twin's token form: joined to its value in one token, or split into two.
3. It names the party wherever the twin's label names it.
4. The built page reads the declared token count and text under pdfium.

Otherwise the twin's label is kept, and the table names the clause that failed. Values stay the
twin's synthetic values, except the invoice number, which is the twin's plus 100. The "pdfium reads"
value of every row that is not kept, minus the `Invoice No:` rows, is `wildAsPrintedLabels` in
`internal/extraction/endtoend/goldens_test.go`; `TestCorpusDoc_CarriesEachSiblingsPrintedLabels`
holds each one to exactly one token on its sibling's page.

**`wild_two_party_bare_tin_asprinted.pdf`** (source NG-2; 19 tokens)

| Twin token | Sibling token | pdfium reads | Source / reason kept |
|---|---|---|---|
| `INVOICE` | `Sales Invoice` | `Sales Invoice` | NG-2 |
| `Invoice No: INV-2101` | `Invoice No: INV-2201` | same | label kept: NG-2 prints `Invoice No.` with its value in a separate column (clause 2) |
| `Invoice Date: 2026-06-11` | kept | same | NG-2 prints `Invoice Date` with a separate value (clause 2) |
| `TIN:` (supplier) | `VAT Reg. No:` | `VAT Reg. No:` | NG-2, as two tokens, the twin's form; joined to its value it loses `supplier_tin` |
| `Invoice to` | `INVOICE TO:` | `INVOICE TO:` | NG-2 |
| `Customer No.` | `Customer No.` | same | NG-2, already as printed |
| `TIN:` (buyer) | `TIN:` | same | NG-2, already as printed |
| `Buyer's Signature` | `Customer's Signature` | `Customer's Signature` | NG-5: the twin's signature label paraphrases NG-5, and NG-2 prints none |
| `Currency: NGN` | `Currency: NGN` | same | NG-2, already as printed |
| `Sub-total` | `Net Amount` | `Net Amount` | NG-2 |
| `VAT` | `VAT @ 7.5%` | `VAT @ 7.5%` | NG-2 |
| `Total` | `Total NGN` | `Total NGN` | NG-2 |

Not transcribed, because nothing at the twin's geometry carries them: `Purchase Order`, `Due Date`,
`Page`, `Tel`, `Fax`, `RC No:`, `SITE:`, the columns `Code` / `Description` / `Qty` / `Unit Price` /
`Line Total` / `VAT Rate`, and the `FOR BANK TRANSFERS` block.

**`wild_ruled_lines_totals_asprinted.pdf`** (source NG-4; 34 tokens)

| Twin token | Sibling token | pdfium reads | Source / reason kept |
|---|---|---|---|
| `INVOICE` | `MONTHLY SERVICE INVOICE` | same | NG-4 |
| `Invoice No: INV-2102` | `Invoice No: INV-2202` | same | label kept: NG-4 prints `INVOICE NUMBER` above its value (clause 2) |
| `Invoice Date: …` | kept | same | NG-4's `INVOICE DATE` sits above its value (clause 2) |
| `Supplier TIN: …` / `Buyer TIN: …` | kept | same | NG-4 prints bare `TIN:` under `SUPPLIER` / `CUSTOMER` headings (clause 3) |
| `Supplier: …` / `Buyer: …` | kept | same | NG-4's headings sit above the names (clause 2) |
| `Currency: NGN` | kept | same | NG-4's `CURRENCY` sits above `NGN` (clause 2) |
| `S/N` | `Item` | `Item` | NG-4 |
| `DESCRIPTION OF GOODS` | `Service description` | `Service description` | NG-4 |
| `QTY` | `Qty` | `Qty` | NG-4 |
| `RATE (N)` | `Unit rate ₦` | `Unit rate ₦` | NG-4 |
| `Amount ₦` | `Amount ₦` | same | NG-4, already as printed |
| `Sub-total` | `Taxable amount` | `Taxable amount` | NG-4 |
| `VAT` | `VAT @ 7.5%` | `VAT @ 7.5%` | NG-4 |
| `Total` | kept | `Total` | `TOTAL DUE (NGN)` merges with row 3's amount into one token (clause 4) |

Not transcribed: `BILL PERIOD`, `PAYMENT DUE`, `ACCOUNT NUMBER`, the `SUMMARY` box (`Previous balance`,
`Current charges`, `VAT`, `Total payable`), the `Reference` and `VAT ₦` columns, `Previous balance`
in the totals block, `RC:`, `VAT Registration:`, `Contact:`, `Payment instructions.` and
`TOTAL DUE (NGN)`.

**`wild_stacked_borderless_asprinted.pdf`** (source NG-3; 21 tokens)

| Twin token | Sibling token | pdfium reads | Source / reason kept |
|---|---|---|---|
| `INVOICE` | `Invoice` | `Invoice` | NG-3 |
| `Invoice No` | `I N V O I C E N U M B E R` | `I N V O I C E N U M B E R` | NG-3, letter-spaced with one space between words (below) |
| `Issue Date` | `I S S U E D` | `I S S U E D` | NG-3 |
| `Buyer` | `BILLED TO` | `BILLED TO` | NG-3 |
| `Buyer TIN` / `Supplier TIN` | kept | same | NG-3 prints `TIN` joined to its value (clause 2) |
| `Supplier` | `FROM` | `FROM` | NG-3 |
| `Currency` | `CURRENCY` | `CURRENCY` | NG-3 |
| `Sub total` | `Subtotal` | `Subtotal` | NG-3 |
| `VAT` | `VAT 7.5%` | `VAT 7.5%` | NG-3 |
| `Total` | `Amount payable` | `Amount payable` | NG-3 |

Not transcribed: `DUE` and its date, `RC`, `Withholding tax 10%` (no row at the twin's geometry), the
service lines, and the page-2 payment text.

**One space, not three.** The sibling letter-spaces exactly the two labels R0 letter-spaces, and
prints `FROM`, `BILLED TO` and `CURRENCY` unspaced as R0 does, although NG-3 letter-spaces them. R0
prints `I N V O I C E   N U M B E R` with three spaces between words; with that string the pinned
docling image keeps the gap pdfium collapses, and `TestWildGoldens_DescribeTheirPDF` fails on the
token. With one space both readers read `I N V O I C E N U M B E R`, so the sibling prints one.
pdfium reads the same text from either string, so the scored outcome does not change.

### What each divergence did

Each record carries the production symptom measured on build `d36be21e` on 2026-09-09, its verdict,
and the probe lines (**The retro-proof** below). `70cc8dcd` is the base this story branched from.

#### (a) `wild_ruled_lines_totals`: the totals labels

NG-4 prints `Taxable amount` / `VAT @ 7.5%` / `TOTAL DUE (NGN)`. On `d36be21e`, subtotal read
nothing and the invoice filed a total of `1,250,000.00`, line item one's amount, against a printed
`3,426,476.50`.

Verdict: **fixed by EXTR-26**.

The subtotal half is fixed. `Taxable amount` decides nothing before the EXTR-26 merge and decides
the subtotal from it on:

```
d36be21e  wild_ruled_lines_totals_asprinted.pdf  decide subtotal "<nil>" reason="missing"
cf68c9ad  wild_ruled_lines_totals_asprinted.pdf  decide subtotal "<nil>" reason="missing"   (EXTR-25 merge)
d6db0e8d  wild_ruled_lines_totals_asprinted.pdf  decide subtotal "8000.00" reason=""        (EXTR-26 merge)
70cc8dcd  wild_ruled_lines_totals_asprinted.pdf  decide subtotal "8000.00" reason=""
```

The total half does not reproduce NG-4's mechanism. `TOTAL DUE (NGN)` does not fit at the twin's
`Total` position, where pdfium merges it with row 3's amount, so the sibling keeps `Total`. Its total
missed for the ruled twin's own geometric reason, the label continuing on the last data row's
baseline. EXTR-29's arithmetic pass now files the printed `8,600.00` on both
(`TestRLS_EndToEndScoresTheCorpus`).

#### (b) `fxWildRuledHeader`: the index column's header

NG-4 heads its index column `Item`; the twin heads it `S/N`. `liLexicon` maps `item` to the
description role, and on `d36be21e` twelve line items filed as `01`..`12`.

Verdict: **fixed by EXTR-24**.

`LineItems` over `wild_ruled_lines_totals_asprinted.docling.json` (header `Item` /
`Service description` / `Qty` / `Unit rate ₦` / `Amount ₦`) files the index as the description
before the EXTR-24 merge `8acf0879` and the description from it on. `item` has been a weak header
since: it still takes the description when no strong description column exists, a shape neither
pair member prints. pdfium yields no tables, so only a docling golden shows (b).

```
d36be21e  LineItems -> 3 line(s) desc=["1" "2" "3"]
8acf0879  LineItems -> 3 line(s) desc=["Steel Rods" "Cement Bags" "Roofing Sheets"]   (EXTR-24 merge)
70cc8dcd  LineItems -> 3 line(s) desc=["Steel Rods" "Cement Bags" "Roofing Sheets"]
```

#### (c) `wild_stacked_borderless`: the letter-spaced invoice-number label

On `d36be21e`, NG-3 resolved no invoice number and quarantined; the twin resolves `INV-2104` and
cannot reproduce that.

Verdict: **reproduces**.

The sibling prints `I N V O I C E N U M B E R` directly above its value, at the twin's position, and
no lexicon entry reaches a letter-spaced word. The document quarantines and scores 0 / 8, while the
twin reads its number on the same geometry:

```
d36be21e  wild_stacked_borderless_asprinted.pdf  decide invoice_number "<nil>" reason="missing"
d36be21e  wild_stacked_borderless.pdf            decide invoice_number "INV-2104" reason=""
70cc8dcd  wild_stacked_borderless_asprinted.pdf  decide invoice_number "<nil>" reason="missing"
70cc8dcd  wild_stacked_borderless.pdf            decide invoice_number "INV-2104" reason=""
```

EXTR-31 Core AC-4 claims this quarantine, and the sibling's eight `eeRealMisses` entries name
EXTR-31. EXTR-31 plans to widen the value offset, but the measured cause is the label's
letter-spacing, so geometry alone may not deliver its AC-4. Letter-spaced label matching is owed and
not created (**Core AC-5 is not delivered on the real register**).
`TestWildLayouts_TheStackedSiblingReadsNoInvoiceNumber` pins the missing candidate.

#### (d) `wild_two_party_bare_tin`: the supplier's registration number as the buyer's TIN

Re-measured after EXTR-22 merged, NG-2 still files the supplier's `VAT Reg. No` as the buyer's TIN,
and the buyer's printed TIN goes unread.

Verdict: **not reproduced**.

At the twin's geometry with NG-2's labels, both TINs bind to their own party on both builds, and the
sibling scores 8 / 8:

```
d36be21e  wild_two_party_bare_tin_asprinted.pdf  decide supplier_tin "99999999-0801"  buyer_tin "99999999-0802"
70cc8dcd  wild_two_party_bare_tin_asprinted.pdf  decide supplier_tin "99999999-0801"  buyer_tin "99999999-0802"
```

The production failure needs something the twin's geometry does not carry. pdfium splits the real
NG-2 page per glyph, so docling's tokenisation of it cannot be measured here, and the cause is
unmeasured. `TestWildLayouts_TheTwoPartySiblingDoesNotReproduceTheRegNoAsBuyerTIN` pins the
non-reproduction and fails once the sibling starts reproducing it.

### The retro-proof

EXTR-24 and EXTR-26 had merged before the siblings existed, so no committed test can show (a)'s
subtotal or (b) red. The lines above come from a throwaway probe, run once per build in a scratch
directory outside the repository and never committed:

```
mkdir -p <scratch>/<build>
git archive <build> | tar -x -C <scratch>/<build>
cp internal/extraction/testdata/wild_*_asprinted.pdf \
   internal/extraction/testdata/wild_ruled_lines_totals_asprinted.docling.json \
   <scratch>/<build>/internal/extraction/testdata/
# <scratch>/<build>/internal/extraction/endtoend/zz_retro04_test.go: per sibling PDF, the PDFium
# reader -> Resolve(Tier1Rules) -> Reconcile; LineItems over eeGoldenPages(the ruled sibling golden)
go -C <scratch>/<build>/internal/extraction/endtoend test -count=1 -run '^TestZZRetro04$' -v .
```

Builds: `d36be21e` (EXTR-23 merge, the production pass), `8acf0879` (EXTR-24), `cf68c9ad`
(EXTR-25), `d6db0e8d` (EXTR-26) and `70cc8dcd`.

### A paired twin's spec and its sibling

`TestWildPairs_ATwinHitIsASiblingHitOrAnOwnedMiss` holds the expectations mechanically: for every
pair and written field, a twin hit is a sibling hit or an `eeRealMisses` entry that names its owner.

A spec that asserts a paired twin's resolved value either asserts the sibling too, or this section
names the sibling cell's owner. These assert mechanisms on a twin's exact tokens and are not
extended to its sibling:

- `TestWildLayouts_TheRuledTableReproducesACompetingTotal`
- `TestWildLayouts_TheRuledTableTotalStillTakesTheLineAmount`
- `TestWildLayouts_ALabelledCurrencyStillHeadsTheRuledTable`
- `TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling`
- `TestLineItems_TheRuledWildFixtureNowMapsRateAndAmount`
- `TestEndToEnd_TheStackedBorderlessArrangementResolvesItsOffsetTotal`
- `TestWildLayouts_TheTwoPartyTINsBindToTheirOwnParty`
- `TestWildLayouts_TheTwoPartyBuyerNameIsTheName`

The sibling cells that miss, and their owners: every cell of `wild_stacked_borderless_asprinted.pdf`,
EXTR-31. The two-party sibling misses nothing, and since EXTR-29 neither does the ruled sibling. A
new spec on a paired twin joins this list or asserts the sibling.

## The advisory arrangements

Three fixtures transcribe two of the Nigerian invoice mock-ups, NG-3 and NG-4, TINs swapped into the reserved block.
`advisory_register.pdf` (R0, faithful, two pages) and `advisory_register_unspaced.pdf` (R1, exactly
one declared transformation) transcribe the advisory-firm register EXTR-26 was written against;
`advisory_dense.pdf` (D0, faithful) transcribes the dense telecoms invoice. The `wild_*` layouts
are the precedent these depart from: they copied a source document's geometry and rewrote its labels
to something a template would print, which tamed the text layer. R0 and D0 keep both the geometry
and the printed words; only the bytes are generated.

### Letter-spacing is a second text-layer taming axis

R0's header labels print letter-spaced — `I S S U E D`, `I N V O I C E   N U M B E R` — and no
lexicon entry reaches a letter-spaced word. That evidence is the **local docling 1.10.0 canary,
not the deployed sidecar**. R1 exists to isolate that one variable: it unspaces those two labels
and changes nothing else, so `issue_date` resolves on R1 while `total` stays missing on both, and
the two blockers below are measured apart. `Amount payable`
is evidenced **split**, with no `(NGN)` beside it, by the same local canary
(`TestAdvisory_TheFixturesTokeniseTheWayTheyDeclare` pins each newly matched label's declared
tokenisation — split label-and-value or joined into one token — against the built PDF).

### The joined label-and-value token

D0's summary block prints `Total payable   ₦3,426,476.50` as **one token**. `total` matches only
the bare `Total`; the residue `payable   ₦3,426,476.50` goes to `sameTokenValue`, which hands it
to `ShapeAmount`, and `ShapeAmount` rejects it — `isLabelSep` trims only `:`, `-`, `–`, `—` and
whitespace, none of which opens the residue. The same joined token still anchors a `below`
candidate at row 1's competing `1,250,000.00`, so it is not inert: it feeds the competing total
the referee must overrule. `sameTokenValue`'s residue handling is a **value-normaliser** gap,
not a label-matching one, and it is **owed**, not fixed here
(`TestAdvisory_AJoinedLabelAndValueResolvesNothing`).

### Core AC-5 is not delivered on the real register

Through the local docling canary, `invoice_number`, `issue_date`, `subtotal`, `vat` and `total`
all decide `missing` on the real advisory register. Two blockers, both **owed**:

1. The header labels are letter-spaced (above).
2. The totals sit 0.3932–0.5880 right of their labels — pdfium R0's own `Amount payable` gap
   measures 0.4604 — against `tier1MaxDistanceRight = 0.35` (`tier1.go:43`).

Neither letter-spaced label matching nor a far-right totals column exists; both follow-ups are
**owed and not created**. On the faithful fixture, `issue_date` and `total` are known gaps by
name, recorded as gaps and never counted as passes
(`TestAdvisory_TheRegisterFilingBlockersAreKnownGapsByName`).

## Learned rules

A learned rule is one tenant's answer to "this producer puts the buyer's TIN *there*". It is
derived from one correction — a **pointed** one, or a **typed** one on either layout identity —
stored against the layout's fingerprint, and read back on every later document of that layout. It
is the tenant-specific tier; Tier-1 is generic except its one shape-only naira fallback
(`t1.currency.sweep`). A typed correction on a geometric layout learns only through a
self-check: the rule derived from the token the value names must re-read page 1 as exactly that
value. A pointed correction is never checked (**How a typed value finds its token** below).

### The two layout identities

A rule is keyed by a layout **fingerprint**, and two producers make one, in two disjoint
namespaces. Which one a job takes is decided when it is extracted, by whether the format returns
usable geometry.

* **The geometric identity, `v3:` today.** `Fingerprint` hashes `<label>:<band>` elements: which
  anchor labels page 1 carries, and which vertical third — left, middle or right — each one's
  box centres in. They are hashed in reading order, top edge then left edge. This is what a PDF
  gets.
* **The boxless identity, `b3:` today.** `BoxlessFingerprint` hashes `<label>:<placement>`
  elements, where placement is `w` when the lexicon match is the whole token, `l` when it leads
  the token, and `i` when it sits inside one. It is what a DOCX gets: every token a DOCX read
  returns carries the zero box, so no `band` can be computed and no reading order can be
  recovered from geometry. The elements are hashed in the order the tokens arrive — nothing
  sorts, and that order is the whole signal.

**Read a prefix as a namespace, never as a fixed string.** A prefix is its lever's current value
plus a colon, so it moves every time the lever does. The first generation of each spelled `v1:`
and `b1:`; EXTR-22 widened the shared anchor lexicon and both stepped together to their second
generation, `v2`/`b2`; EXTR-26 widened it again and both stepped to `v3:` and `b3:`. Prose, a test
needle or an operator runbook that hard-codes a generation is one bump from being wrong, and this
page has been exactly that.

The two can never collide **within the current generation**: they differ on byte 0, so one
`layout_fingerprint` column holds both, and `IsBoxlessFingerprint` tells them apart by prefix —
true only for the current generation, the way `fingerprint.go:174-178`'s own comment on that
function now says. A key stored under a retired prefix reads false from that classifier, the same
as any other non-match: `AnchorRulesFor` (`anchor_store.go:29`) matches by exact equality, so a
row keyed under a retired generation is never selected again, raises no error and is logged
nowhere — and it cannot be deleted either, because `invoice_app` holds `INSERT` and `SELECT` only
(**A rule is scoped to one tenant and one layout fingerprint** below). A correction is keyed to
its job's stored key, whatever the generation. On a retired geometric key, a pointed correction
and a typed one that names exactly one page-1 token and passes the self-check each still write a
rule under that key, which nothing will ever select again, and each still record an
`AnchorLearned` event for a rule no future extraction can reach
(`TestRLS_ATypedCorrectionOnARetiredGenerationJobWritesARuleNoExtractionSelects`). A DOCX job
keyed outside the current boxless namespace never reaches the boxless branch, because
`IsBoxlessFingerprint` reads false there, and its anchors carry the zero box, so no page is read
and nothing is learned (`TestRLS_ATypedCorrectionOnAV1KeyedBoxlessJobLearnsNothing` re-keys a DOCX
job into the geometric namespace).

Each namespace carries its **own** invalidation lever, and bumping one clears **only its own**
class. Bumping `FingerprintVersion` retires every geometric rule and leaves every boxless rule
readable under its unchanged key; bumping `BoxlessFingerprintVersion` does the reverse. So
`FingerprintVersion` is no longer the single lever it was before EXTR-19 — an operator who bumps
it and expects every stored rule gone is wrong. WIDENING an existing shared-anchor-lexicon pattern
is the one case that needs **both**, because `anchorLabelMatchers` is an input to both producers;
EXTR-22 is the change that proved it, stepping both levers in one commit — true of EXTR-22
specifically, not a rule about commits in general. EXTR-26 widened six patterns across three
commits and stepped both levers in a fourth, all inside one merge; deploy runs on merge, so the
merge is the unit that matters, not any one commit inside it. A NEW rule-less
owning-phrase entry needs neither: EXTR-25-04 added `due_date` and moved neither lever, because a
new entry resets only the pages that print the phrase rather than invalidating every stored rule.

### How a rule is derived

`LearnRule` (`internal/extraction/learn.go`) takes the corrected field, a box — the one the
reviewer dragged, or the box of the token a typed value names (**How a typed value finds its
token** below) — and the anchor observations the job's own read recorded. It considers every anchor
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
  When a pointed correction derives a rule, this label is also what lands in the correction row's
  `anchor_label`, overwriting whatever the wire sent. When no rule is derived, the client's trimmed
  value stands unchanged — see `TestRLS_APointedCorrectionThatAnchorsToNothingCommitsWithoutARule`.
  A typed correction never writes the derived label there, on either identity
  (`TestRLS_ATypedGeometricLearningWritesNoAnchorLabel`,
  `TestRLS_ABoxlessLearnedCorrectionWritesNoAnchorLabel`).

The worked example, measured off `learned_two_party.pdf`. Both party blocks are stacked
(`label` / `name` / bare TIN) in page 1's top half, so the page reads `Supplier`,
`Adeyemi Trading Limited`, `99999999-0701`, `Buyer`, `Honeywell Group`, `99999999-0702`. Since
EXTR-22 **Tier-1 alone already reaches the buyer's TIN**: the `Buyer` token opens a party block,
the party-scoped sweep names `99999999-0702` by format inside it, and the field decides
`99999999-0702` with no alternatives — as `supplier_tin` decides `99999999-0701`. Before EXTR-22 it
decided `missing`, because the two banded sweeps split the page by half rather than by party and
both party blocks sat in the same half.

That makes this a **sharper** example than the one it replaces, not a weaker one. The learned rule
has to beat a real generic candidate carrying the same value rather than fill a void, so the only
thing that says it fired is the **tier**: `TierLearned` outranks `TierGeneric` whatever the values
are. `TestLearnedTwoParty_Tier1BindsTheBuyerTINAndTheLearnedRuleStillOutranksIt` asserts rank 0 on
tier and rule id and never on value, precisely because a value-only assertion would pass whether
the learned rule fired or not.

A reviewer points at the bare token `99999999-0702`. The nearest qualifying anchor is
`buyer_name` / `"Buyer"`, `below` at a gap of `0.026525`; `"Supplier"` is also below, at
`0.140267`, and is dropped for exceeding the `0.06` dial. The derived body is:

```json
{"label":"(?i)\\bBuyer\\b","relation":{"kind":"below","max_distance":0.03},"shape":"tin"}
```

On the next document of that layout the rule matches one token, `relatedTokens` reaches the two
tokens under it, `ShapeTIN` rejects the party name, and `buyer_tin` resolves to `99999999-0702`
at rank 0 as a `TierLearned` candidate, one rank above Tier-1's own reading of the same value and
with no alternatives — `TestRLS_TheSecondDocumentOfTheSameLayoutResolvesTheLearnedBuyerTIN`.

### Which correction produces a rule

A correction carries a method, and the job carries a layout. The method picks the learner and the
layout's namespace picks the arm; a correction that reaches no row below writes **zero rules**:

| Method | Job's `layout_fingerprint` | Derivation | Extra input |
|---|---|---|---|
| `pointed`, with a region | any key, provided the job recorded a layout | `LearnRule` | the box, against `layout_anchors` |
| `typed` | a **boxless** key, `b3:` today — the identity written for a format with no page images | `LearnBoxlessRule` | the page-1 token text in `layout_tokens` |
| `typed` | any other key, provided `layout_anchors` holds at least one usable box | `LearnTypedRule`: locate, then `LearnRule`, then the self-check | page 1 re-read from the stored document, against `layout_anchors` |
| `chosen`, `undone` | either | none | — |

Read the boxless row as *which namespace the job took*, never as two fixed bytes: the boxless
prefix spelled `b1:` before EXTR-22, `b2` before EXTR-26, and spells `b3:` now, and
`IsBoxlessFingerprint` is what tells the two namespaces apart — see **The two layout identities**
above.

The method decides the learner and the namespace decides the arm, so no one correction enters two
rows. A `typed` correction on a **PDF** — a geometric key — reaches `LearnTypedRule` only when the
job's stored layout carries a usable anchor box, and it writes a rule only when its value names
exactly one page-1 token and the self-check passes. **How a typed value finds its token** below
names every refusal. On `learned_typed_total.pdf` a typed `total` of `14800000.00` learns the same
rule that pointing at `₦14,800,000.00` learns
(`TestRLS_ATypedCorrectionTeachesThePointedRuleOnAGeometricLayout`). Its label is `VAT`, not
`Total`: `betterAnchor` picks the smallest gap, and the `VAT inclusive` token stands nearer the
amount than `Total` does.

```json
{"label":"(?i)\\bVAT\\b","relation":{"kind":"right","max_distance":0.13},"shape":"amount"}
```

A `pointed` correction on a **DOCX** writes zero rules — every box it could anchor to is the zero
box, so `LearnRule` finds no relation to record. The pointed gate reads the box, never the
namespace: a job that took the boxless identity while carrying real geometry can still learn from
a pointed correction (`TestRLS_ABoxlessIdentityOverRealGeometryStoresUsableAnchors`). An `undone`
correction writes **zero rules** either way.

What the boxless path derives is always a **`same_token`** rule. `same_token` is the only
relation available without geometry — there is no box to stand `right` of or `below` — so a DOCX
layout that puts a value in a paragraph of its own is structurally underivable, however many
times it is corrected. For each page-1 token and each lexicon matcher that hits it,
`LearnBoxlessRule` builds the rule body that hit would produce and keeps it only when the label's
own token, read back under the field's shape, gives the typed value.

The second refusal is the **ambiguous** derivation, and it is judged on the derived *body*, never
on the hit count. The bodies are deduplicated: when exactly one distinct body survives the rule
is written, and when two survive the function refuses rather than guess which the reviewer meant.
A total printed twice identically therefore derives — both hits spell the same body — while
`Total: 300.00` and `Amount Due: 300.00` on one page do not, because they anchor on two different
labels.

The boxless gate has a third conjunct: the job must have stored its page-1 tokens. A job
extracted **before** the `layout_tokens` migration carries SQL NULL there and learns nothing, and
so does one whose tokens were over the storage cap. The loop therefore closes only for documents
extracted after that column started being written; an older DOCX teaches nothing however many
times it is corrected.

A correction that **anchors to nothing** — an empty corner of the page, a box no anchor
observation stands in a relation to, a job that recorded no layout at all, a typed value no
page-1 token carries — still commits the correction and still teaches nothing. That is an
**honest refusal**, not an error: the reviewer's edit is recorded and applied, and the system
declines to generalise from an input it cannot interpret. `LearnRule` and `LearnBoxlessRule`
report `ok=false`, `LearnTypedRule` returns a verdict other than `TypedLearned`, and the request
answers `201` exactly as it otherwise would.

### How a typed value finds its token

A typed correction names a value, not a place. On a geometric layout the route turns that value
into a place, then hands the place to the same `LearnRule` a pointed correction reaches.
`LearnTypedRule` (`internal/extraction/learn.go`) runs three clauses in order and stops at the
first refusal.

1. **Locate.** Only page 1 is searched — the fingerprint, the stored anchors and the learner are
   page 1 only (`TestPageOneReader_StopsAfterPageOne`,
   `TestLearnTypedRule_ATokenOffPageOneDerivesNothing`). A token names the value when its whole text, or the remainder after an
   anchor-lexicon label inside it (`sameTokenValue`, the raw form `Resolve` reads there), shares a
   reading with the typed value under the field's shape. Both sides pass through that shape, so a
   typed `14800000.00` names the printed `₦14,800,000.00`
   (`TestLearnTypedRule_FindsThePrintedNairaAmountThroughTheShape`), so does a typed
   `₦14,800,000.00` (`TestLearnTypedRule_NormalisesTheTypedSideToo`), and a typed `3225.00` names
   `Total: NGN 3,225.00` through its remainder (`TestLearnTypedRule_ReadsTheRemainderAfterALabel`).
   Nothing beyond the shape canonicalises: a typed `3225` names no `3,225.00` token, because the
   amount shape keeps exactly the fraction digits it is given
   (`TestLearnTypedRule_AddsNoToleranceTheShapeLacks`). No token gives `TypedNoToken`
   (`TestLearnTypedRule_RefusesAValueNoTokenCarries`). Two or more distinct tokens give
   `TypedSeveralTokens`, and one token that two labels read counts once
   (`TestLearnTypedRule_RefusesAValuePrintedOnSeveralTokens`).
2. **Derive.** `LearnRule` runs, unchanged, over that token's box and the job's stored anchors. Its
   refusal gives `TypedNotDerived` (`TestLearnTypedRule_RefusesWhenNoAnchorRelatesToTheToken`).
3. **Self-check.** The derived rule is re-applied alone, never with Tier-1, to the same page-1
   tokens through `Resolve`. The rule is learned only when that yields exactly one candidate and
   the candidate equals the typed value under the field's shape; anything else gives
   `TypedSelfCheckRefused`
   (`TestLearnTypedRule_TheSelfCheckRefusesOneCandidateThatIsNotTheTypedValue`,
   `TestLearnTypedRule_TheSelfCheckRefusesEveryReachableMisfire`). Only
   `TypedLearned` writes a rule, and the zero verdict is `TypedNoToken`, so an unset verdict
   refuses (`TestLearnTypedRule_TheZeroVerdictRefuses`).

Where the self-check passes, the typed rule is the rule pointing at the same token teaches, body,
label and anchor alike (`TestLearnTypedRule_WhereTheSelfCheckPassesItTeachesThePointedRule`). A
pointed correction is not checked: it keeps teaching every rule `LearnRule` derives, including the
misfires the self-check refuses
(`TestRLS_OnTheSplitLayoutPointingTeachesTheTotalAndTypingItRefuses`) — see **When a learned rule
misfires** below.

The page comes from the stored document, read again inside the correction request, before its
transaction, through the same audited opener the extraction worker uses. Each read writes one
`document.read` audit row attributed to the operator (`TestStoreGet_WritesReadAudit`,
`TestSubmissionMain_WiresTheCorrectionRouteAndItsCollaborators`,
`TestRLS_ATypedCorrectionTeachesThePointedRuleOnAGeometricLayout`). The route reads only for a
`typed` correction on a job the caller can see whose stored layout is not boxless and carries at
least one usable anchor box (`TestRLS_ATypedCorrectionOnAJobWithNoUsableAnchorReadsNothing`,
`TestRLS_ChosenAndUndoneNeverReadTheDocumentAndTeachNothing`,
`TestRLS_ATypedCorrectionOnABoxlessJobNeverReadsTheDocumentAndStillLearns`). Another tenant's job
answers `404` and reads no document (`TestRLS_ATypedCorrectionOnAnotherTenantsJobReadsNoDocument`).

The read is bounded by `pageOneReadTimeout` (5 s), because it borrows an instance from the PDFium
pool the extraction queue also uses, sized to that queue's workers
(`TestPDFiumMaxTotalMatchesTheQueueWorkerCount`,
`TestPageOneReader_AnExhaustedPoolReturnsAtItsDeadline`). An open error, a read error, a document
with no page 1, or the expired bound learns nothing, and the correction still commits with `201`
(`TestPageOneReader_PassesTheOpenErrorThrough`,
`TestRLS_AFailedPageReadCommitsTheCorrectionAndTeachesNothing`,
`TestRLS_APageReadIsBoundedAndItsExpiryCommitsTheCorrection`). A read error writes one WARN naming
the job and the field, never the typed value
(`TestRLS_AFailedPageReadCommitsTheCorrectionAndTeachesNothing`). A refusal at any clause writes no
WARN and no `extraction.anchor.learned` row, a refusal before the self-check writes no log line at
all, and the correction commits with `201`
(`TestRLS_ATypedRefusalBeforeTheSelfCheckRecordsTheCorrectionAndLogsNothing`,
`TestRLS_ATypedTINCorrectionOnTheTwoColumnLayoutRefusesAtTheSelfCheck`). An undecodable stored
`layout_anchors` column is a different posture: the correction aborts with `500` and commits
nothing (`TestRLS_AnUndecodableLayoutAnchorsColumnAbortsATypedCorrection`). A learned rule is
written in the correction's own transaction, so a failed rule write rolls back the correction, the
invoice field and the audit row with it
(`TestRLS_AFailedTypedRuleWriteRollsBackTheCorrectionTheInvoiceAndTheAudit`). The correction row's
`anchor_label` never carries the derived label (`TestRLS_ATypedGeometricLearningWritesNoAnchorLabel`).

The vehicle is the `learned_typed_total.pdf` / `learned_typed_total_twin.pdf` pair: one
arrangement whose `total` Tier-1 leaves missing on both documents
(`TestLearnedTypedTotal_Tier1LeavesTotalMissingOnBothDocuments`). A typed `total` on the first
teaches the rule that makes the twin decide its own `9250000.00`, and deleting that rule turns the
next document back to `missing` (`TestRLS_ATypedCorrectionTeachesTheNextDocumentOfThatLayout`,
`TestRLS_DeletingTheTypedRuleRedsTheNextDocument`). A boxless layout keeps its own typed path,
above, with no page read and no self-check.

### Undo does not un-teach

This is the sharp edge of the feature, and it is a decision (D-17), not an oversight.

A rule written by a correction that was later **undone stays live**. It keeps firing on every
later document carrying that layout fingerprint. The undo revises what one field on one document
says; it does not withdraw the claim about *where that field lives on this layout*.

The only way to displace a live rule is a second correction that derives a different one for the
same field, such as a **second pointed correction** on a geometric layout, pointing at a
distinguishing label, or a second `typed` correction on a boxless layout, retyping a value some
other page-1 token carries. Displacement is by **ordering**, never by
deletion: the `extraction_anchor_rules` table is **append-only** by grant — `invoice_app` holds
`INSERT` and `SELECT` and no `UPDATE` or `DELETE` — so after a superseding correction **both rows
remain**, and `AnchorRulesFor` returns them newest-first. `Resolve` lets the first rule that
produces anything for a field claim it; an older rule is outranked, never erased.

Two tests hold this:
`TestRLS_AnUndoDoesNotUnteachAndAPointedCorrectionSupersedes` proves the undo leaves the rule
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

Both oracles read the geometric namespace, and the layout one says in its own comment that its
cross-layout zero would survive the fingerprint gate being removed. The boxless equivalents
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
`TestRLS_TheTwoColumnLayoutRegressesFromACorrectReadingToAWrongOne`.

That layout prints the supplier and buyer blocks side by side, each ending in a token spelled
`TIN: 99999999-04NN`. **Since EXTR-22 Tier-1 alone reads `buyer_tin` correctly**, as
`99999999-0402`, because the `Buyer` heading opens the block that token stands in — and that makes
this misfire strictly *worse* to look at, not better: what turns a correct field into a wrong one
is a reviewer pointing at a field that was already right.

A reviewer points at the buyer's own token `TIN: 99999999-0402`. The best anchor is the `bare_tin`
lexicon entry matching the party-less word `TIN` **inside that same token**, so the derived rule
is:

```json
{"label":"(?i)\\bTIN\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}
```

That label matches **both** party blocks, and a learned rule is never party-scoped — party scoping
would overrule the very reviewer who pointed at the token, which is another story's decision. With
the rule live `buyer_tin` has **three** candidates: **two from that one rule**, both `TierLearned`
at distance 0, plus Tier-1's own correct reading a tier below. The decision comes out
`99999999-0401` — **the supplier's TIN** — flagged `ambiguous` with `99999999-0402` as the
alternative. The layout has gone from a correct value to a confidently decided **wrong** one.

Both learned candidates come from the *same* rule, so newest-rule-wins cannot rescue it, and both
outrank the correct generic reading by tier. The tie between them is broken in `compareRegions`:
the two tokens share a baseline, so their `Y0` is bit-identical (`0.23407067192925346`), and `X0`
decides — `0.1179 < 0.6539` hands the field to the supplier.

The remedy today is a **second pointed correction** on a distinguishing label — a token whose
text tells the two blocks apart — which prepends a superseding rule. Widening the anchor lexicon
so that `TIN` alone no longer anchors is **not** a remedy here: it is what Tier-1 already does, and
the learned rule outranks Tier-1 regardless. It is also the expensive lever: WIDENING AN EXISTING
pattern requires a `FingerprintVersion` **and** a `BoxlessFingerprintVersion` bump, by policy, in
both namespaces — not because a stored rule's own key is measured to move. A widening shifts a
page's digest only when that page's tokens print a spelling the pattern newly matches; across the
six patterns EXTR-26 widened, that was zero of 72 measured digests (`84a08f9c`), and both levers
still bumped, because the scheme invalidates by rule, not by observed drift. A NEW rule-less
owning-phrase entry needs neither bump. Since
EXTR-19-02 the same `anchorLabelMatchers` feed `BoxlessFingerprint` too, so a widened pattern and
a new entry each move a boxless key only on a page that prints a spelling they newly match.

That misfire belongs to the pointed arm, and it stays: a pointed correction is not checked, so
pointing still teaches the `TIN` rule above. It is one instance of a wider class. Learned rules
skip the outranking check Tier-1 labels pass, so a learned `Total` also fires inside `Sub-total`
and a learned `Buyer` inside `Buyer TIN`.

A typed correction refuses that class at the self-check.
`TestLearnTypedRule_TheSelfCheckRefusesEveryReachableMisfire` types the correct value on seven
rows — `total` on `corpus_split_labels.pdf`, `corpus_totals_block.pdf` and `rich_invoice.pdf`;
`buyer_name` on `corpus_split_labels.pdf` and `corpus_inline_labels.pdf`; `buyer_tin` on
`corpus_two_column.pdf` and `advisory_register.pdf` — and asserts that `LearnRule` derives a rule
over the token that prints the value and that the self-check refuses it. Through the route, typing the buyer's
`99999999-0402` on `corpus_two_column.pdf` records the correction and writes no rule
(`TestRLS_ATypedTINCorrectionOnTheTwoColumnLayoutRefusesAtTheSelfCheck`), so typing is no remedy
for this layout either. On `corpus_split_labels.pdf` the same `2,150.00` token teaches the `Total`
rule when pointed at and nothing when typed
(`TestRLS_OnTheSplitLayoutPointingTeachesTheTotalAndTypingItRefuses`). That divergence is the
chosen trade: where the self-check refuses, typing teaches less than pointing.

The self-check also refuses three rows, and that is the accepted cost: `total` on
`corpus_inline_labels.pdf`, whose learned rule keeps the next read at `1075.00` with no reason, and
the ambiguous numeric `issue_date` on `corpus_ambiguous_date.pdf` and on `rich_invoice.pdf`, under
either reading (`TestLearnTypedRule_TheAcceptedCostRefusals`).

Two limits stay open. The self-check reads page 1 only, the page it located the token on, so a
rule that misfires only because of a later page's tokens passes it; no committed fixture has that
shape. And the realistic exposure — a document where Tier-1 decides the field wrong while the right
value sits within reach of its label, and the reviewer types that value — is unmeasured.

### learned_two_party.pdf is not a corpus layout

`learned_two_party.pdf` is generated by `fxBuildLearnedTwoParty` and byte-compared by
`TestFixtures_MatchTheirGenerator` and `TestFixtures_GeneratorIsDeterministic` like every other
fixture. It is deliberately named **outside** the `corpus_` prefix, and therefore sits outside
`corpusExpect`, `corpusLayouts`, `corpusTokenFloor`, the Tier-1 recall rate and the Tier-1
decision rate.

That is the point: it is a **vehicle for the learning chain**, not a measured arrangement, and
adding it to the corpus would move `EXTR-04`'s accuracy ratchet — which must keep measuring the
same six layouts it has always measured — for a fixture nobody grades. Its own reserved-TIN scan
is `TestCorpus_TheLearnedRuleFixtureUsesOnlyFreeReservedTINs`, because
`TestCorpus_UsesOnlyFreeReservedTINs` quantifies over `corpusLayouts` and cannot see it.

`learned_typed_total.pdf` and `learned_typed_total_twin.pdf` are the second vehicle, for the typed
path, and sit outside the same ratchets for the same reason. `fxBuildLearnedTypedTotal` generates
both, `TestFixtures_MatchTheirGenerator` byte-compares them like every other fixture, and neither
carries a `corpus_` or `wild_` prefix, so no `corpusExpect`, `corpusLayouts`, `corpusTokenFloor` or
`expectByLayout` row names them. They print no TIN, so no reserved-TIN scan applies. Learning
leaves every field but `total` unchanged on the twin
(`TestLearnedTypedTotal_TheAmountTokenTeachesARuleTheTwinReads`).

One consequence worth recording, and it inverted at EXTR-22. `supplier_tin` used to read
`ambiguous` here, because `t1.supplier_tin.sweep` was banded to page 1's top half and claimed
**both** bare TINs, and the buyer's TIN was unreachable behind it. Both party blocks still sit in
the top half, but the half decides nothing now — the party block does — so Tier-1 alone decides
`supplier_tin` = `99999999-0701` and `buyer_tin` = `99999999-0702`, each with no alternatives.
The fixture keeps its job either way, and the job got harder rather than easier: the rule a pointed
correction teaches here must now outrank a *correct* generic reading instead of filling a gap,
which is what makes it a real test of tier precedence. All of that is still invisible to every
accuracy number, precisely because this is not a corpus layout.

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
