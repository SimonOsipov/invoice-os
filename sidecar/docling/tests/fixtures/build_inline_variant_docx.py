"""Deterministic boxless_inline_variant.docx generator (EXTR-19-01's fixture A-prime).

Same determinism recipe as build_docx.py -- see its docstring for why `_normalise_zip` is
needed.

The near-miss control for invoice.docx: same inline `Label: value` template, different data,
plus one extra line-item paragraph. That paragraph sits at index 2, strictly INSIDE the anchor
run -- appended after the total it would shift no anchor's raw token ordinal and the control
would degenerate into a tie. TestBoxlessFixtures_TheControlShiftsAnAnchorOrdinal is the guard.

Mirrors fixtures_test.go's -update idiom: `python build_inline_variant_docx.py --update`
regenerates the committed file; a bare run byte-compares and exits non-zero on drift.
"""

import io
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # tests/, for conftest
from build_docx import _normalise_zip
from conftest import repo_root
from docx import Document

FIXTURE = repo_root() / "internal" / "extraction" / "testdata" / "boxless_inline_variant.docx"

# 0921, so this never collides with invoice.docx's 0919 or fixture B's 0920 under
# invoices_tenant_entity_number_uq if they import under one entity. The line item must trip no
# anchorLexicon pattern -- a stray "Total", "Date" or "Tax" in it adds a fourth observation and
# silently breaks EXTR-19-02 AC-2. TestBoxlessFixtures_TheExtraParagraphMatchesNoAnchor guards it.
PARAGRAPHS = [
    "Invoice No: ASC-2026-0921",
    "Issue Date: 03 Sep 2026",
    "Widget x 2 NGN 1,000.00",
    "Total: NGN 7,150.00",
]


def build() -> bytes:
    doc = Document()
    for text in PARAGRAPHS:
        doc.add_paragraph(text)

    raw = io.BytesIO()
    doc.save(raw)
    return _normalise_zip(raw.getvalue())


if __name__ == "__main__":
    built = build()
    if "--update" in sys.argv:
        FIXTURE.write_bytes(built)
        print(f"wrote {FIXTURE} ({len(built)} bytes)")
    else:
        existing = FIXTURE.read_bytes()
        if existing != built:
            sys.exit(
                f"{FIXTURE} does not match its generator: {len(existing)} byte(s) on disk, "
                f"{len(built)} regenerated -- run `python build_inline_variant_docx.py --update`"
            )
        print("boxless_inline_variant.docx matches its generator")
