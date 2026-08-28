"""T-01-4..T-01-7: /v1/read's size cap, its 400/413 error shape, and the §3 response contract."""
import pytest
from fastapi.testclient import TestClient

from app import MAX_DOCUMENT_BYTES, app

PDF_CONTENT_TYPE = "application/pdf"


@pytest.fixture
def client():
    return TestClient(app)


def test_t01_4_over_cap_body_is_refused_with_413(client):
    body = b"0" * (MAX_DOCUMENT_BYTES + 1)
    resp = client.post("/v1/read", content=body, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 413
    assert "error" in resp.json()


def test_t01_5_exactly_cap_body_is_not_refused(client):
    body = b"0" * MAX_DOCUMENT_BYTES
    resp = client.post("/v1/read", content=body, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code != 413  # the cap bounds over, not at


def test_t01_6_empty_body_is_400(client):
    resp = client.post("/v1/read", content=b"", headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 400


def test_t01_7_stub_response_validates_against_the_wire_contract(client):
    resp = client.post(
        "/v1/read", content=b"%PDF-1.4\nstub", headers={"content-type": PDF_CONTENT_TYPE}
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
