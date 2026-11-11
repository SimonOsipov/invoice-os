"""/v1/read refuses a body that is not a PDF or a DOCX.

Needs docling importable -- run only via the Docker `test` stage.
"""

import io
import zipfile

import pytest
from fastapi.testclient import TestClient

import convert
from app import app

PDF = "application/pdf"
DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


@pytest.fixture
def client():
    return TestClient(app)


@pytest.fixture
def converter_calls(monkeypatch):
    calls = []

    def spy():
        calls.append(1)
        raise RuntimeError("converter must not be reached")

    monkeypatch.setattr(convert, "get_converter", spy)
    return calls


def _image(fmt):
    from PIL import Image

    buf = io.BytesIO()
    Image.new("RGB", (8, 8), "white").save(buf, fmt)
    return buf.getvalue()


def _zip(members):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for name, data in members.items():
            z.writestr(name, data)
    return buf.getvalue()


def _pptx():
    return _zip(
        {
            "[Content_Types].xml": (
                '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
                '<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-'
                'officedocument.presentationml.presentation.main+xml"/></Types>'
            ),
            "_rels/.rels": (
                '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
                '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/'
                'officeDocument" Target="ppt/presentation.xml"/></Relationships>'
            ),
            "ppt/presentation.xml": '<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>',
        }
    )


def test_converter_allows_only_pdf_and_docx():
    from docling.datamodel.base_models import InputFormat

    conv = convert.get_converter()
    assert conv.allowed_formats == [InputFormat.PDF, InputFormat.DOCX]
    assert set(conv.format_to_options) == {InputFormat.PDF, InputFormat.DOCX}


@pytest.mark.parametrize("fmt", ["PNG", "TIFF"])
def test_an_image_labelled_pdf_is_refused_before_the_converter(client, converter_calls, fmt):
    resp = client.post("/v1/read", content=_image(fmt), headers={"content-type": PDF})
    assert resp.status_code == 422
    assert resp.json()["error"] == "unsupported format"
    assert converter_calls == []


@pytest.mark.parametrize("headers", [{"content-type": "text/html"}, {}])
def test_html_is_refused_before_the_converter(client, converter_calls, headers):
    resp = client.post("/v1/read", content=b"<!DOCTYPE html><html><body>x</body></html>", headers=headers)
    assert resp.status_code == 422
    assert converter_calls == []


def test_a_pdf_header_reaches_the_converter(client, converter_calls):
    client.post("/v1/read", content=b"%PDF-", headers={"content-type": PDF})
    assert converter_calls == [1]


def test_a_short_pdf_marker_is_refused(client, converter_calls):
    resp = client.post("/v1/read", content=b"%PDF", headers={"content-type": PDF})
    assert resp.status_code == 422
    assert converter_calls == []


def test_a_pptx_labelled_docx_is_refused_by_the_allow_list(client, monkeypatch):
    from docling.backend.mspowerpoint_backend import MsPowerpointDocumentBackend
    from docling.backend.msword_backend import MsWordDocumentBackend

    built = []
    for cls in (MsWordDocumentBackend, MsPowerpointDocumentBackend):
        monkeypatch.setattr(cls, "__init__", lambda self, *a, _n=cls.__name__, **k: built.append(_n))
    resp = client.post("/v1/read", content=_pptx(), headers={"content-type": DOCX})
    assert resp.status_code == 422
    assert built == []


def test_an_ole_body_labelled_docx_is_refused_before_the_converter(client, converter_calls):
    body = bytes.fromhex("D0CF11E0A1B11AE1") + b"junk" * 16
    resp = client.post("/v1/read", content=body, headers={"content-type": DOCX})
    assert resp.status_code == 422
    assert converter_calls == []


@pytest.mark.parametrize("content_type", [DOCX, PDF])
@pytest.mark.parametrize("body", [_zip({"evidence.txt": "hello"}), b"PK\x03\x04" + b"\x01" * 64], ids=["zip", "junk"])
def test_a_zip_that_is_not_ooxml_is_unreadable(client, sentry_capture, body, content_type):
    resp = client.post("/v1/read", content=body, headers={"content-type": content_type})
    assert resp.status_code == 422
    assert "error" in resp.json()
    assert sentry_capture.events() == []
