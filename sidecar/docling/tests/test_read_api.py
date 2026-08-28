"""T-01-4..T-01-7: /v1/read's size cap, its 400/413 error shape, and the §3 response contract.

The size-cap and empty-body tests are content-independent and collect in a bare venv; the
rest need docling importable (EXTR-03-03's real converter) -- run via the Docker `test` stage.
"""

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

import convert
from app import MAX_DOCUMENT_BYTES, app

PDF_CONTENT_TYPE = "application/pdf"

# T-01-7 and its neighbours below need a body Docling can actually convert (EXTR-03-03
# replaced the always-succeeds stub) -- reuses the fixture test_convert.py already commits.
TESTDATA = Path(__file__).parent / "testdata"


@pytest.fixture
def client():
    return TestClient(app)


@pytest.fixture
def real_pdf_body() -> bytes:
    return (TESTDATA / "native_invoice.pdf").read_bytes()


def test_t01_4_over_cap_body_is_refused_with_413(client):
    body = b"0" * (MAX_DOCUMENT_BYTES + 1)
    resp = client.post("/v1/read", content=body, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 413
    assert "error" in resp.json()


def test_t01_5_exactly_cap_body_is_not_refused(client):
    body = b"0" * MAX_DOCUMENT_BYTES
    resp = client.post("/v1/read", content=body, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code != 413  # the cap bounds over, not at


def test_body_one_byte_under_cap_is_not_refused(client):
    # Third boundary point: T-01-4 covers over, T-01-5 covers at.
    body = b"0" * (MAX_DOCUMENT_BYTES - 1)
    resp = client.post("/v1/read", content=body, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code != 413


def test_t01_6_empty_body_is_400(client):
    resp = client.post("/v1/read", content=b"", headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 400


def test_t01_7_stub_response_validates_against_the_wire_contract(client, real_pdf_body):
    resp = client.post(
        "/v1/read", content=real_pdf_body, headers={"content-type": PDF_CONTENT_TYPE}
    )
    assert resp.status_code == 200
    body = resp.json()

    pages = body["pages"]
    assert len(pages) > 0  # non-empty, so the loop below can't vacuously pass

    for page in pages:
        assert page["number"] >= 1
        for token in page["tokens"]:
            for coord in (token["x0"], token["y0"], token["x1"], token["y1"]):
                assert 0.0 <= coord <= 1.0
        for table in page.get("tables", []):
            for cell in table["cells"]:
                for coord in (cell["x0"], cell["y0"], cell["x1"], cell["y1"]):
                    assert 0.0 <= coord <= 1.0


def test_docling_version_field_is_present(client, real_pdf_body):
    # sec. 3 requires "docling_version" on every response; no existing test checks it.
    resp = client.post(
        "/v1/read", content=real_pdf_body, headers={"content-type": PDF_CONTENT_TYPE}
    )
    assert "docling_version" in resp.json()


def test_missing_content_type_is_accepted(client, real_pdf_body):
    resp = client.post("/v1/read", content=real_pdf_body)
    assert resp.status_code == 200


def test_unsupported_content_type_is_accepted(client, real_pdf_body):
    # /v1/read has no reader that branches on Content-Type; it's forwarded, not validated.
    resp = client.post(
        "/v1/read", content=real_pdf_body, headers={"content-type": "application/x-bogus"}
    )
    assert resp.status_code == 200


def test_t03_13_truncated_pdf_is_422_not_400_or_500(client):
    # Valid header, body cut mid-object -- no endobj, no xref, no %%EOF. docling-parse raises
    # ConversionError on exactly this shape (confirmed against the pinned stack); a document
    # Docling opened but could not convert is 422, never 400 (that's reserved for empty body)
    # and never a bare 500.
    truncated = b"%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R"
    resp = client.post("/v1/read", content=truncated, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 422, f"got {resp.status_code}, want 422"
    assert "error" in resp.json()


def test_unexpected_error_is_500_not_swallowed_into_422(client, monkeypatch, caplog):
    # 422 is reserved for DocumentUnreadable (docling opened the document but couldn't
    # convert it) -- a genuine bug must stay 500 and land in the log, not get relabeled
    # "bad document" and hidden.
    def boom(body, content_type):
        raise ValueError("boom")

    monkeypatch.setattr(convert, "stub_read", boom)
    resp = client.post(
        "/v1/read", content=b"%PDF-1.4\nx", headers={"content-type": PDF_CONTENT_TYPE}
    )
    assert resp.status_code == 500
    assert resp.json() == {"error": "internal error"}
    assert "unexpected /v1/read failure" in caplog.text
