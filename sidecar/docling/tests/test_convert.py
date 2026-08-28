"""T-03-6, T-03-7: /v1/read against real fixtures, through the real DocumentConverter.

Needs docling importable -- run only via the Docker `test` stage. Both fixtures live in
testdata/: scanned_ocr_fixture.pdf (raster glyphs, no text layer -- see its generator's
docstring for why internal/extraction/testdata/scanned_invoice.pdf can't serve this) and
native_invoice.pdf (a byte-identical copy of internal/extraction's fixture; test_pins.py
guards it against drift).

Currently red against convert.stub_read, which always returns one fixed "STUB" token and
docling_version "stub" regardless of input -- these assertions target the real fixture content.
"""

from pathlib import Path

import pytest
from docling_core.types.doc.document import CURRENT_VERSION
from fastapi.testclient import TestClient

from app import app

TESTDATA = Path(__file__).parent / "testdata"
PDF_CONTENT_TYPE = "application/pdf"


@pytest.fixture
def client():
    return TestClient(app)


def _all_tokens(body: dict) -> list[dict]:
    return [tok for page in body["pages"] for tok in page["tokens"]]


def test_t03_6_scanned_fixture_ocr_recovers_a_known_string(client):
    raw = (TESTDATA / "scanned_ocr_fixture.pdf").read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 200

    tokens = _all_tokens(resp.json())
    assert len(tokens) >= 20, f"got {len(tokens)} token(s), want at least 20"

    texts = [t["text"] for t in tokens]
    assert "ASCOMPLY" in texts, (
        "known ASCII string missing from OCR output -- a Chinese-model run would still "
        f"return characters (got: {texts!r})"
    )


def test_t03_7_native_pdf_returns_tokens_and_matching_docling_version(client):
    raw = (TESTDATA / "native_invoice.pdf").read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 200

    body = resp.json()
    tokens = _all_tokens(body)
    assert len(tokens) > 0, "native_invoice.pdf carries real text; an empty token list is wrong"
    assert body["docling_version"] == CURRENT_VERSION, (
        f"docling_version {body['docling_version']!r} != installed CURRENT_VERSION {CURRENT_VERSION!r}"
    )
