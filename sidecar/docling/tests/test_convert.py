"""T-03-6, T-03-7: /v1/read against real fixtures, through the real DocumentConverter.
T-04-9, T-04-17: page ordering and the whole-corpus box-count floor (EXTR-03-04).

Needs docling importable -- run only via the Docker `test` stage. Fixtures live in testdata/:
scanned_ocr_fixture.pdf (raster glyphs, no text layer -- see its generator's docstring for
why internal/extraction/testdata/scanned_invoice.pdf can't serve this), native_invoice.pdf
and native_3page.pdf (byte-identical copies of internal/extraction's fixtures; test_fixtures.py
guards both against drift). table_invoice.pdf/.docx are reached separately (repo_root() /
tests/fixtures/) -- see test_tables.py and test_docx.py.

Runs the real DocumentConverter (EXTR-03-03): assertions target actual fixture content, not
the fixed "STUB" token / docling_version "stub" convert.stub_read returned before this subtask.
"""

from pathlib import Path

import pytest
from conftest import repo_root
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


def test_t04_9_three_page_fixture_returns_pages_in_ascending_order(client):
    raw = (TESTDATA / "native_3page.pdf").read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 200

    numbers = [page["number"] for page in resp.json()["pages"]]
    assert numbers == [1, 2, 3], f"got page order {numbers}, want [1, 2, 3]"


# T-04-17's corpus: every PDF/DOCX fixture this story's tests carry, local plus the
# repo-mounted table_invoice.pdf (single source of truth, fxBuildTable). table_invoice.docx
# contributes zero boxes by design (T-04-16) -- it stays in the corpus anyway so the floor
# reflects the real end-to-end mix, not a PDF-only subset.
def _t04_17_corpus() -> list[tuple[str, bytes]]:
    docs = [
        ("native_invoice.pdf", PDF_CONTENT_TYPE),
        ("native_3page.pdf", PDF_CONTENT_TYPE),
        ("scanned_ocr_fixture.pdf", PDF_CONTENT_TYPE),
    ]
    corpus = [(name, (TESTDATA / name).read_bytes(), ct) for name, ct in docs]

    table_pdf = repo_root() / "internal" / "extraction" / "testdata" / "table_invoice.pdf"
    corpus.append(("table_invoice.pdf", table_pdf.read_bytes(), PDF_CONTENT_TYPE))

    docx_path = Path(__file__).parent / "fixtures" / "table_invoice.docx"
    docx_ct = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
    corpus.append(("table_invoice.docx", docx_path.read_bytes(), docx_ct))
    return corpus


def _bbox_normalised(box: dict) -> bool:
    # Mirrors extraction_field_results_bbox_normalised (migrations/20260827100320_...sql):
    # 0 <= x0 <= x1 <= 1 and 0 <= y0 <= y1 <= 1. A box with no key at all (T-04-13/16's
    # omitted case) is the SQL's "bbox_x0 IS NULL" branch and is not checked here.
    return (
        0.0 <= box["x0"] <= box["x1"] <= 1.0
        and 0.0 <= box["y0"] <= box["y1"] <= 1.0
    )


def test_t04_17_every_box_in_the_corpus_is_normalised_and_the_corpus_clears_the_floor(client):
    boxes = []
    for name, raw, content_type in _t04_17_corpus():
        resp = client.post("/v1/read", content=raw, headers={"content-type": content_type})
        assert resp.status_code == 200, f"{name} did not convert: {resp.status_code} {resp.text}"
        body = resp.json()
        for page in body["pages"]:
            for token in page["tokens"]:
                if {"x0", "y0", "x1", "y1"} <= token.keys():
                    boxes.append((name, token))
            for table in page.get("tables", []):
                for cell in table["cells"]:
                    if {"x0", "y0", "x1", "y1"} <= cell.keys():
                        boxes.append((name, cell))

    # The floor: "all boxes satisfy P" is vacuously true over zero boxes, so this is what
    # stops the assertion below from passing on an empty or near-empty corpus (T-01-7,
    # T-09-3's own precedent for a non-empty floor).
    assert len(boxes) >= 60, f"only {len(boxes)} box(es) seen across the corpus, want >=60"

    for name, box in boxes:
        assert _bbox_normalised(box), f"{name} produced an out-of-range box: {box!r}"
