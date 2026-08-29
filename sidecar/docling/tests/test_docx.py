"""T-04-15, T-04-16: the DOCX path -- read natively (no temp PDF), tables structured,
boxes omitted entirely (measured: msword_backend.py constructs zero ProvenanceItems and
doc.pages/result.pages come back empty for a DOCX -- see story sec. 4).

Needs docling importable -- run only via the Docker `test` stage.
"""

import tempfile
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app import app

FIXTURE = Path(__file__).parent / "fixtures" / "table_invoice.docx"
DOCX_CONTENT_TYPE = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


@pytest.fixture
def client():
    return TestClient(app)


def test_t04_15_docx_returns_structured_tables_and_writes_no_temp_pdf(client, monkeypatch):
    calls = []
    orig_ntf = tempfile.NamedTemporaryFile

    def spy_ntf(*args, **kwargs):
        calls.append((args, kwargs))
        return orig_ntf(*args, **kwargs)

    monkeypatch.setattr(tempfile, "NamedTemporaryFile", spy_ntf)

    raw = FIXTURE.read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": DOCX_CONTENT_TYPE})
    assert resp.status_code == 200

    tables = [t for page in resp.json()["pages"] for t in page.get("tables", [])]
    assert len(tables) >= 1, "table_invoice.docx carries one table; none came back structured"
    assert tables[0]["rows"] >= 1
    assert tables[0]["cols"] >= 1
    assert len(tables[0]["cells"]) >= 1

    assert calls == [], f"a temporary file was created on the DOCX path: {calls!r}"


def test_t04_16_docx_tokens_and_cells_never_carry_a_box(client):
    raw = FIXTURE.read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": DOCX_CONTENT_TYPE})
    assert resp.status_code == 200
    body = resp.json()

    boxable = []
    for page in body["pages"]:
        boxable.extend(page["tokens"])
        for table in page.get("tables", []):
            boxable.extend(table["cells"])

    # Floor: the fixture carries an "INVOICE" paragraph and 12 table cells, so a box-omission
    # check over zero items would prove nothing.
    assert len(boxable) >= 4, f"only {len(boxable)} item(s) to check, want >=4"
    for item in boxable:
        assert not ({"x0", "y0", "x1", "y1"} & item.keys()), f"DOCX item carries a box: {item!r}"
