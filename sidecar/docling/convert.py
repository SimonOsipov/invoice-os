"""Docling conversion: real DocumentConverter, RapidOCR (en/onnxruntime), CPU inference.

Page tokens stay sourced from parsed_page.textline_cells (already top-left, no flip needed).
Tables and the DOCX path go through geometry.py, which owns all coord_origin arithmetic.
"""

import logging
import threading
from io import BytesIO

import sentry_sdk

import geometry

logger = logging.getLogger(__name__)

# Pinned per story sec. 4: AcceleratorOptions.num_threads defaults to 4 (auto-detected core
# count), and a shared Railway CPU reports far more cores than it grants. 2 matches the
# MaxWorkers: 2 concurrency bound already imposed upstream (cmd/submission/main.go:213) --
# two in-flight /v1/read calls at 2 threads each is 4 OS threads, sane on a modest shared vCPU
# allocation. Revisit once the service is actually provisioned and its real limit is known.
DOCLING_NUM_THREADS = 2


class DocumentUnreadable(Exception):
    """Docling opened the document but could not convert it -- maps to 422 (T-03-13)."""


_construction_count = 0
_construction_lock = threading.Lock()
_converter = None  # cached DocumentConverter; built once behind _construction_lock


def construction_count() -> int:
    """How many times a real DocumentConverter has been built."""
    return _construction_count


def pipeline_options():
    """The pinned PdfPipelineOptions: RapidOCR/en/onnxruntime, cpu/DOCLING_NUM_THREADS
    threads, table structure ACCURATE, code/formula/picture enrichment off (story sec. 4).

    docling is imported lazily (inside this function, not at module load) so `import convert`
    stays valid in a venv without docling installed -- only the Docker `test` stage, which has
    the pinned stack, exercises this.
    """
    from docling.datamodel.accelerator_options import AcceleratorDevice, AcceleratorOptions
    from docling.datamodel.pipeline_options import (
        PdfPipelineOptions,
        RapidOcrOptions,
        TableFormerMode,
    )

    opts = PdfPipelineOptions()
    opts.do_ocr = True
    # lang explicit: RapidOcrOptions() defaults to ["ch"] (T-03-2).
    opts.ocr_options = RapidOcrOptions(lang=["en"], backend="onnxruntime")
    opts.accelerator_options = AcceleratorOptions(
        device=AcceleratorDevice.CPU, num_threads=DOCLING_NUM_THREADS
    )
    opts.do_table_structure = True
    opts.table_structure_options.mode = TableFormerMode.ACCURATE
    opts.do_code_enrichment = False
    opts.do_formula_enrichment = False
    opts.do_picture_classification = False
    # parsed_page.textline_cells is this subtask's token source (see _page_tokens) --
    # normally discarded after document assembly.
    opts.generate_parsed_pages = True
    return opts


def _construct_converter():
    """The real, expensive DocumentConverter build (torch/onnxruntime imports + model load).

    docling-parse text, pypdfium2 page images: docling-parse paints a /RunLengthDecode scan
    blank (test_backend.py).
    """
    from docling.datamodel.backend_options import ThreadedDoclingParseBackendOptions
    from docling.datamodel.base_models import InputFormat
    from docling.document_converter import DocumentConverter, PdfFormatOption

    return DocumentConverter(
        format_options={
            InputFormat.PDF: PdfFormatOption(
                pipeline_options=pipeline_options(),
                backend=_pdfium_image_backend(),
                backend_options=ThreadedDoclingParseBackendOptions(
                    render_pages=False, parser_threads=DOCLING_NUM_THREADS
                ),
            )
        }
    )


def _pdfium_image_backend():
    """docling-parse text, pypdfium2 page images (docling-parse paints a /RunLengthDecode
    scan blank)."""
    import pypdfium2 as pdfium
    from docling.backend.docling_parse_backend import (
        ThreadedDoclingParseDocumentBackend,
        ThreadedDoclingParsePageBackend,
    )
    from docling.backend.pypdfium2_backend import PyPdfiumPageBackend
    from docling.exceptions import DocumentLoadError
    from docling.utils.locks import pypdfium2_lock

    class PdfiumImagePage(ThreadedDoclingParsePageBackend):
        # Sizes stay docling-parse's (token coordinates); the image is docling's own pdfium render.
        def __init__(self, result, pdfium_page):
            super().__init__(result)
            self._pdfium_page = pdfium_page

        def get_page_image(self, scale=1, cropbox=None):
            return self._pdfium_page.get_page_image(scale=scale, cropbox=cropbox)

        def unload(self):
            self._pdfium_page.unload()

    class PdfiumImageBackend(ThreadedDoclingParseDocumentBackend):
        def __init__(self, in_doc, path_or_stream, options=None):
            # Own bytes: pypdfium2 must not share the stream cursor docling-parse reads.
            has_bytes = hasattr(path_or_stream, "getvalue")
            src = path_or_stream.getvalue() if has_bytes else path_or_stream
            password = options.password.get_secret_value() if options and options.password else None
            try:
                with pypdfium2_lock:
                    self._pdoc = pdfium.PdfDocument(src, password=password)
            except pdfium.PdfiumError as e:
                raise DocumentLoadError(
                    f"pypdfium could not load document with hash {in_doc.document_hash}"
                ) from e
            try:
                super().__init__(in_doc, path_or_stream, options)
            except Exception:
                with pypdfium2_lock:
                    self._pdoc.close()
                raise

        def iter_pages(self):
            for page in super().iter_pages():
                with pypdfium2_lock:
                    pdfium_page = PyPdfiumPageBackend(self._pdoc, self.document_hash, page.page_no - 1)
                yield PdfiumImagePage(page._result, pdfium_page)

        def unload(self):
            super().unload()
            with pypdfium2_lock:
                if self._pdoc is not None:
                    self._pdoc.close()
                    self._pdoc = None

    return PdfiumImageBackend


def get_converter():
    """Lazy, lock-guarded construction. D-17: the lock BLOCKS and there is no 503 -- a
    caller arriving mid-build waits and is then served. Built once, cached thereafter.
    """
    global _converter, _construction_count
    with _construction_lock:
        if _converter is None:
            _converter = _construct_converter()
            _construction_count += 1
    return _converter


def warm_up() -> None:
    """Run from a background thread at boot (app.py) so the first real request usually
    finds the converter already built. A failure here leaves _converter unset, so the
    next get_converter() call (from a real request) simply retries construction.
    """
    try:
        get_converter()
    except Exception:
        logger.exception("docling warm-up failed; construction will retry on first /v1/read")
        sentry_sdk.capture_exception()


_warmup_thread: threading.Thread | None = None


def start_warm_up() -> threading.Thread:
    """Starts the background warm-up thread once and returns it. Kept here (not started
    inline in app.py) so tests can `convert.start_warm_up().join()` before touching
    _converter/_construction_count -- otherwise the reset races the boot-time warm-up
    non-deterministically (only masked by test_convert.py happening to sort first).
    """
    global _warmup_thread
    if _warmup_thread is None:
        _warmup_thread = threading.Thread(target=warm_up, daemon=True, name="docling-warmup")
        _warmup_thread.start()
    return _warmup_thread


def _clamp01(value: float) -> float:
    return max(0.0, min(1.0, value))


def _page_tokens(parsed_page, width: float, height: float) -> list[dict]:
    """One token per OCR/text line (parsed_page.textline_cells). Its rect is already
    top-left origin -- matches EXTR-02's Region convention directly, no flip needed.
    """
    if parsed_page is None or not width or not height:
        return []
    words = [(w.text, w.rect.to_bounding_box()) for w in parsed_page.word_cells if not w.from_ocr]
    tokens = []
    for cell in parsed_page.textline_cells:
        for text, box in _split_line(cell, words):
            tokens.append(
                {
                    "text": text,
                    "x0": _clamp01(box.l / width),
                    "y0": _clamp01(box.t / height),
                    "x1": _clamp01(box.r / width),
                    "y1": _clamp01(box.b / height),
                }
            )
    return tokens


# A word gap wider than this many line heights starts a new token (docling-parse 7.20 behaviour).
# ceiling: measured window (0.9, 1.26); re-measure when a golden splits or merges.
SPLIT_GAP_HEIGHTS = 1.0


def _split_line(cell, words):
    from docling_core.types.doc import BoundingBox

    box = cell.rect.to_bounding_box()
    # OCR lines carry their own text; page words belong to the native layer.
    if cell.from_ocr:
        return [(cell.text, box)]
    top, bottom = min(box.t, box.b), max(box.t, box.b)
    inside = sorted(
        (
            (t, b)
            for t, b in words
            if t.strip()
            and b.l >= box.l - 0.01 and b.r <= box.r + 0.01
            and min(b.t, b.b) >= top - 0.01 and max(b.t, b.b) <= bottom + 0.01
        ),
        key=lambda wb: wb[1].l,
    )
    # Unsplit unless the words reproduce the line's text: none may be dropped or reordered.
    if "".join("".join(t.split()) for t, _ in inside) != "".join(cell.text.split()):
        return [(cell.text, box)]
    groups = []
    for t, b in inside:
        if groups and b.l - groups[-1][-1][1].r <= SPLIT_GAP_HEIGHTS * (bottom - top):
            groups[-1].append((t, b))
        else:
            groups.append([(t, b)])
    if len(groups) < 2:
        return [(cell.text, box)]
    # Token text is sliced from the line, so a split inside a word never adds a space.
    offsets = [i for i, c in enumerate(cell.text) if not c.isspace()]
    out, n = [], 0
    for g in groups:
        k = sum(len("".join(t.split())) for t, _ in g)
        out.append((cell.text[offsets[n] : offsets[n + k - 1] + 1], BoundingBox.enclosing_bbox([b for _, b in g])))
        n += k
    return out


def _pdf_wire_pages(result, doc) -> list[dict]:
    """The per-page path: one wire page per result.pages entry, tokens from parsed_page
    (unchanged, already top-left -- see _page_tokens), tables from doc.tables mapped by
    geometry.map_table and grouped by their own page_no.
    """
    page_sizes = {page_no: page_item.size for page_no, page_item in doc.pages.items()}
    tables_by_page: dict[int, list[dict]] = {}
    for table_item in getattr(doc, "tables", []):
        page_no = table_item.prov[0].page_no
        tables_by_page.setdefault(page_no, []).append(geometry.map_table(table_item, page_sizes))

    pages = []
    for page in result.pages:
        size = doc.pages[page.page_no].size
        pages.append(
            {
                "number": page.page_no,
                "width_pt": size.width,
                "height_pt": size.height,
                "tokens": _page_tokens(page.parsed_page, size.width, size.height),
                "tables": tables_by_page.get(page.page_no, []),
            }
        )
    return pages


def _docx_wire_pages(doc) -> list[dict]:
    """DOCX has no per-page structure at all (doc.pages == {}, result.pages == []) --
    msword_backend.py constructs zero ProvenanceItems, so there is no page to loop over and
    no box to compute (T-04-15/16). Text and tables are read straight off the document; one
    synthesised wire "page" carries everything, matching DOCX's structured-view rendering
    (Q10) rather than a page image.
    """
    tokens = [{"text": item.text} for item in doc.texts]
    tables = [geometry.map_table(table_item, {}) for table_item in getattr(doc, "tables", [])]
    return [
        {
            "number": 1,
            "width_pt": 0.0,
            "height_pt": 0.0,
            "tokens": tokens,
            "tables": tables,
        }
    ]


def _to_wire_contract(result) -> dict:
    """ConversionResult -> the §3 wire shape. A DOCX conversion reports result.pages == [] --
    that's the discriminator between the two wire-building paths (measured, not a
    Content-Type check: Content-Type is forwarded, not validated, per T-01's tests).
    """
    doc = result.document
    pages = _pdf_wire_pages(result, doc) if result.pages else _docx_wire_pages(doc)
    pages.sort(key=lambda p: p["number"])
    return {
        "reader": "docling",
        "version": "v1",
        "docling_version": doc.version,
        "pages": pages,
    }


_CONTENT_TYPE_EXTENSIONS = {
    "application/pdf": "pdf",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
}


def _stream_name(content_type: str) -> str:
    """DocumentStream needs a filename for format detection. Docling sniffs the true format
    from content regardless, so a missing/unrecognised Content-Type (T-01's own tests post
    both) safely falls back to .pdf rather than rejecting the request.
    """
    ext = _CONTENT_TYPE_EXTENSIONS.get(content_type.split(";")[0].strip().lower(), "pdf")
    return f"document.{ext}"


def stub_read(body: bytes, content_type: str) -> dict:
    """The real conversion entry point (name kept: T-03-14 monkeypatches convert.stub_read
    by this exact name to prove /v1/read runs off the event loop). Runs the whole convert
    off-thread -- app.py dispatches this via asyncio.to_thread.

    Raises DocumentUnreadable when Docling opens the document but can't convert it (422).
    """
    from docling.datamodel.base_models import DocumentStream
    from docling.exceptions import ConversionError

    converter = get_converter()
    stream = DocumentStream(name=_stream_name(content_type), stream=BytesIO(body))
    try:
        result = converter.convert(stream)
    except ConversionError as exc:
        raise DocumentUnreadable(str(exc)) from exc
    return _to_wire_contract(result)
