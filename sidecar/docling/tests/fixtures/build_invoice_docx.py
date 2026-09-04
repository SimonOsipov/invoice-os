"""Deterministic invoice.docx generator (EXTR-18-04's Python half).

Same determinism recipe as build_docx.py -- see its docstring for why `_normalise_zip` (fixed
date_time, sorted members, ZIP_DEFLATED level 9) is needed. Reused here rather than duplicated.

Content is three plain paragraphs, not a table: docling's DOCX path emits one token per
paragraph (no word-split), which is what resolves invoice_number/issue_date/total by identity.
"14 Aug 2026" is deliberate -- normalizeDate (shapes.go) only accepts the abbreviated month.

FIXTURE lives cross-tree under internal/extraction/testdata/, not alongside this generator --
resolved via conftest.repo_root() the same way test_fixtures.py already reads native_invoice.pdf
across the docling:test image's mount.

Mirrors fixtures_test.go's -update idiom: `python build_invoice_docx.py --update` regenerates
the committed file; a bare run byte-compares and exits non-zero on drift.
"""

import io
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # tests/, for conftest
from build_docx import _normalise_zip
from conftest import repo_root
from docx import Document

FIXTURE = repo_root() / "internal" / "extraction" / "testdata" / "invoice.docx"

# 0919, not 0918 (Fixture 1's table_invoice.docx has no invoice number of its own), so this
# never collides under invoices_tenant_entity_number_uq if both import under one entity.
PARAGRAPHS = [
    "Invoice No: ASC-2026-0919",
    "Issue Date: 14 Aug 2026",
    "Total: NGN 4,300.00",
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
                f"{len(built)} regenerated -- run `python build_invoice_docx.py --update`"
            )
        print("invoice.docx matches its generator")
