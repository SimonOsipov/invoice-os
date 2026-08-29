"""The PDF backend is a pinned choice, not a default.

PdfFormatOption's default is ThreadedDoclingParseDocumentBackend (the v1 parser). On a
scanned page it hands the OCR stage no bitmap region, so /v1/read returns a document with
ZERO tokens for a scan RapidOCR reads perfectly when called directly -- a silent
"unreadable" verdict on exactly the documents this service exists to read.

Needs docling importable -- run only via the Docker `test` stage.
"""

import pytest
from conftest import repo_root
from docling.backend.docling_parse_v4_backend import DoclingParseV4DocumentBackend
from docling.datamodel.base_models import InputFormat
from fastapi.testclient import TestClient

import convert
from app import app

PDF_CONTENT_TYPE = "application/pdf"

# The dense fixture is the only committed scan with enough OCR-able content to tell a
# working backend from a broken one; the 4x4-image fixtures yield zero tokens either way.
DENSE = ("internal", "extraction", "testdata", "dense_invoice.pdf")


@pytest.fixture
def client():
    return TestClient(app)


def test_backend_is_pinned_not_defaulted():
    opt = convert._construct_converter().format_to_options[InputFormat.PDF]
    assert opt.backend is DoclingParseV4DocumentBackend, (
        f"the PDF backend is {opt.backend}, want DoclingParseV4DocumentBackend -- "
        "falling back to the library default silently stops OCR on scanned pages"
    )


def test_a_scanned_page_reaches_the_wire_with_text(client):
    # The regression this file exists for. Under the library default this returns 0 tokens.
    fixture = repo_root().joinpath(*DENSE)
    resp = client.post("/v1/read", content=fixture.read_bytes(), headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 200

    tokens = [t for page in resp.json()["pages"] for t in page["tokens"]]
    assert len(tokens) >= 40, (
        f"dense_invoice.pdf yielded {len(tokens)} token(s), want at least 40 -- it is an "
        "image-only page whose glyphs RapidOCR reads; zero means the OCR stage saw no bitmap"
    )

    # Not just any text: the document's own identifiers must survive OCR, so a backend that
    # returns plausible noise cannot pass.
    joined = " ".join(t["text"] for t in tokens)
    for want in ("INVOICE", "KADUNA", "30154829"):
        assert want in joined, f"OCR output does not contain {want!r}: {joined[:200]!r}"
