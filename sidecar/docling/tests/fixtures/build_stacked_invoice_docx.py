"""Deterministic boxless_stacked_invoice.docx generator (EXTR-19-01's fixture B).

Same determinism recipe as build_docx.py -- see its docstring for why `_normalise_zip` is
needed.

Carries invoice.docx's three anchor labels in the same order, and differs from it only in
structure: each label sits alone on its paragraph with its value on the next. Every DOCX token
reads with the zero box, so Fingerprint cannot tell this apart from invoice.docx --
TestBoxlessFixtures_CollideUnderTheGeometricFingerprint pins that, and EXTR-19-02's
BoxlessFingerprint is what must split them.

Mirrors fixtures_test.go's -update idiom: `python build_stacked_invoice_docx.py --update`
regenerates the committed file; a bare run byte-compares and exits non-zero on drift.
"""

import io
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # tests/, for conftest
from build_docx import _normalise_zip
from conftest import repo_root
from docx import Document

FIXTURE = repo_root() / "internal" / "extraction" / "testdata" / "boxless_stacked_invoice.docx"

# 0920, so this never collides with invoice.docx's 0919 under
# invoices_tenant_entity_number_uq if both import under one entity. Every string here was run
# against all ten anchorLexicon patterns: the three labels hit exactly one each, the three
# values none.
PARAGRAPHS = [
    "Invoice No",
    "ASC-2026-0920",
    "Issue Date",
    "14 Aug 2026",
    "Total",
    "NGN 4,300.00",
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
                f"{len(built)} regenerated -- run `python build_stacked_invoice_docx.py --update`"
            )
        print("boxless_stacked_invoice.docx matches its generator")
