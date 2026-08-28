"""Docling conversion: real DocumentConverter, RapidOCR (en/onnxruntime), CPU inference.

Token/box mapping here is deliberately coarse (parsed_page.textline_cells, already
top-left origin, no flip needed) -- EXTR-03-04 owns the precise doc.texts/coord_origin/
table mapping in geometry.py. This subtask only needs a working wire-contract response.
"""

import logging
import threading
from io import BytesIO

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
    # lang explicit: RapidOcrOptions() defaults to ["chinese"] (T-03-2).
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
    """The real, expensive DocumentConverter build (torch/onnxruntime imports + model load)."""
    from docling.datamodel.base_models import InputFormat
    from docling.document_converter import DocumentConverter, PdfFormatOption

    return DocumentConverter(
        format_options={InputFormat.PDF: PdfFormatOption(pipeline_options=pipeline_options())}
    )


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
    tokens = []
    for cell in parsed_page.textline_cells:
        box = cell.rect.to_bounding_box()
        tokens.append(
            {
                "text": cell.text,
                "x0": _clamp01(box.l / width),
                "y0": _clamp01(box.t / height),
                "x1": _clamp01(box.r / width),
                "y1": _clamp01(box.b / height),
            }
        )
    return tokens


def _to_wire_contract(result) -> dict:
    """ConversionResult -> the §3 wire shape. tables stay [] here -- EXTR-03-04's job."""
    doc = result.document
    pages = []
    for page in result.pages:
        size = doc.pages[page.page_no].size
        pages.append(
            {
                "number": page.page_no,
                "width_pt": size.width,
                "height_pt": size.height,
                "tokens": _page_tokens(page.parsed_page, size.width, size.height),
                "tables": [],
            }
        )
    pages.sort(key=lambda p: p["number"])
    return {
        "reader": "docling",
        "version": "v1",
        "docling_version": doc.version,
        "pages": pages,
    }


def _stream_name(content_type: str) -> str:
    """DocumentStream needs a filename for format detection. DOCX arrives in EXTR-03-04;
    this subtask only wires the PDF pipeline, so every request is treated as a PDF.
    """
    return "document.pdf"


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
