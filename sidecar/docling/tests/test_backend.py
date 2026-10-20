"""The PDF backend is a pinned choice, not a default.

docling-parse paints a /RunLengthDecode image blank, so a scan reaches the OCR stage as an
empty page and /v1/read returns ZERO tokens for a scan RapidOCR reads fine directly.
PdfiumImageBackend keeps docling-parse text and renders page images through pypdfium2.

Needs docling importable -- run only via the Docker `test` stage.
"""

import io

import pypdfium2
import pytest
from conftest import repo_root
from docling.backend.docling_parse_backend import ThreadedDoclingParseDocumentBackend
from docling.datamodel.backend_options import ThreadedDoclingParseBackendOptions
from docling.datamodel.base_models import InputFormat
from docling.datamodel.document import InputDocument, get_input_rejection_cause
from docling.exceptions import DocumentLoadError
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
    assert opt.backend.__name__ == "PdfiumImageBackend", (
        f"the PDF backend is {opt.backend}, want PdfiumImageBackend -- "
        "docling-parse's own renderer paints a /RunLengthDecode scan blank"
    )
    assert issubclass(opt.backend, ThreadedDoclingParseDocumentBackend)


def _page_images_by_number(monkeypatch=None, order=None):
    pdf = repo_root().joinpath("internal", "extraction", "testdata", "native_3page.pdf")
    if order:
        orig = ThreadedDoclingParseDocumentBackend.iter_pages

        def shuffled(self):
            pages = list(orig(self))
            for i in order:
                yield pages[i]

        monkeypatch.setattr(ThreadedDoclingParseDocumentBackend, "iter_pages", shuffled)
    in_doc = InputDocument(
        path_or_stream=pdf,
        format=InputFormat.PDF,
        backend=convert._pdfium_image_backend(),
        backend_options=ThreadedDoclingParseBackendOptions(render_pages=False),
    )
    ref = pypdfium2.PdfDocument(pdf)
    seen, images = [], {}
    for page in in_doc._backend.iter_pages():
        got = page.get_page_image(scale=1)
        size = page.get_size()
        # The 2.123.1 algorithm: render at scale * 1.5, resize to the page size.
        want = ref[page.page_no - 1].render(scale=1.5).to_pil().resize(
            (round(size.width), round(size.height))
        )
        assert got.tobytes() == want.tobytes(), f"page {page.page_no} is not pdfium page index"
        seen.append(page.page_no)
        images[page.page_no] = got.tobytes()
    in_doc._backend.unload()
    return seen, images


def test_each_page_image_is_its_own_pdfium_page():
    _, images = _page_images_by_number()
    assert sorted(images) == [1, 2, 3]
    assert len(set(images.values())) == 3, "the three page images must differ pairwise"


def test_each_page_image_is_its_own_pdfium_page_when_pages_arrive_out_of_order(monkeypatch):
    seen, images = _page_images_by_number(monkeypatch, order=[2, 0, 1])
    assert seen == [3, 1, 2]
    assert len(set(images.values())) == 3, "the three page images must differ pairwise"


def test_the_parser_runs_on_the_pinned_thread_count():
    opt = convert._construct_converter().format_to_options[InputFormat.PDF]
    assert opt.backend_options.parser_threads == convert.DOCLING_NUM_THREADS


def _rotated_pdf(tmp_path, degrees):
    src = pypdfium2.PdfDocument(
        repo_root().joinpath("internal", "extraction", "testdata", "native_invoice.pdf")
    )
    src[0].set_rotation(degrees)
    path = tmp_path / "rotated.pdf"
    src.save(path)
    return path


def _first_page_backend(path):
    in_doc = InputDocument(
        path_or_stream=path,
        format=InputFormat.PDF,
        backend=convert._pdfium_image_backend(),
        backend_options=ThreadedDoclingParseBackendOptions(render_pages=False),
    )
    return in_doc, next(iter(in_doc._backend.iter_pages()))


def test_a_rotated_page_image_has_the_rotated_dimensions(tmp_path):
    in_doc, page = _first_page_backend(_rotated_pdf(tmp_path, 90))
    image = page.get_page_image(scale=1)
    size = page.get_size()
    in_doc._backend.unload()
    assert (round(size.width), round(size.height)) == (792, 612)
    assert image.size == (792, 612)


def test_unloading_a_page_closes_its_pdfium_page(tmp_path):
    in_doc, page = _first_page_backend(_rotated_pdf(tmp_path, 0))
    ppage = page._pdfium_page._ppage
    page.unload()
    assert page._pdfium_page._ppage is None
    assert ppage.raw is None
    in_doc._backend.unload()


def test_a_pdfium_open_failure_is_a_backend_failure(monkeypatch):
    def boom(*args, **kwargs):
        raise pypdfium2.PdfiumError("forced")

    monkeypatch.setattr(pypdfium2, "PdfDocument", boom)
    pdf = repo_root().joinpath("internal", "extraction", "testdata", "native_invoice.pdf")
    in_doc = InputDocument(
        path_or_stream=pdf, format=InputFormat.PDF, backend=convert._pdfium_image_backend()
    )
    assert not in_doc.valid
    assert isinstance(get_input_rejection_cause(in_doc), DocumentLoadError)


def test_a_pdfium_open_failure_is_unreadable(client, monkeypatch):
    opened = []

    def boom(*args, **kwargs):
        opened.append(True)
        raise pypdfium2.PdfiumError("forced")

    monkeypatch.setattr(pypdfium2, "PdfDocument", boom)
    fixture = repo_root().joinpath("internal", "extraction", "testdata", "native_invoice.pdf")
    resp = client.post("/v1/read", content=fixture.read_bytes(), headers={"content-type": PDF_CONTENT_TYPE})
    assert opened, "the backend never opened the document through pypdfium2"
    assert resp.status_code == 422


def test_a_failed_docling_parse_open_closes_the_pdfium_document(monkeypatch):
    closed = []

    class FakePdfDocument:
        def __init__(self, src, password=None):
            pass

        def close(self):
            closed.append(True)

    def boom(self, *args, **kwargs):
        raise RuntimeError("docling-parse refused")

    monkeypatch.setattr(pypdfium2, "PdfDocument", FakePdfDocument)
    monkeypatch.setattr(ThreadedDoclingParseDocumentBackend, "__init__", boom)
    backend = convert._pdfium_image_backend()
    with pytest.raises(RuntimeError, match="refused"):
        backend(None, io.BytesIO(b"%PDF"))
    assert closed == [True]


def test_the_document_password_reaches_pdfium(monkeypatch):
    passwords = []

    class FakePdfDocument:
        def __init__(self, src, password=None):
            passwords.append(password)

        def close(self):
            pass

    def boom(self, *args, **kwargs):
        raise RuntimeError("docling-parse refused")

    monkeypatch.setattr(pypdfium2, "PdfDocument", FakePdfDocument)
    monkeypatch.setattr(ThreadedDoclingParseDocumentBackend, "__init__", boom)
    options = ThreadedDoclingParseBackendOptions(password="s3cret")
    with pytest.raises(RuntimeError, match="refused"):
        convert._pdfium_image_backend()(None, io.BytesIO(b"%PDF"), options)
    assert passwords == ["s3cret"]


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
