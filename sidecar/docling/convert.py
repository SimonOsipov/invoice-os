"""Docling conversion. Stub for EXTR-03-01; EXTR-03-03 replaces stub_read with the real DocumentConverter."""

import threading

# Pinned per story sec. 4: AcceleratorOptions.num_threads defaults to 4 (auto-detected core
# count), and a shared Railway CPU reports far more cores than it grants. 2 matches the
# MaxWorkers: 2 concurrency bound already imposed upstream (cmd/submission/main.go:213) --
# two in-flight /v1/read calls at 2 threads each is 4 OS threads, sane on a modest shared vCPU
# allocation. Revisit once the service is actually provisioned and its real limit is known.
DOCLING_NUM_THREADS = 2

_construction_count = 0
_construction_lock = threading.Lock()
_converter = None  # cached DocumentConverter; built once behind _construction_lock


def construction_count() -> int:
    """How many times a real DocumentConverter has been built. Stays 0 until EXTR-03-03."""
    return _construction_count


def pipeline_options():
    """The pinned PdfPipelineOptions: RapidOCR/en/onnxruntime, cpu/DOCLING_NUM_THREADS
    threads, table structure ACCURATE, code/formula/picture enrichment off (story sec. 4).

    EXTR-03-03 stub: not built yet. docling is imported lazily (inside this function, not at
    module load) so `import convert` stays valid in a venv without docling installed --
    only the Docker `test` stage, which has the pinned stack, exercises this.
    """
    raise NotImplementedError("EXTR-03-03: pinned PdfPipelineOptions not yet built")


def _construct_converter():
    """The real, expensive DocumentConverter build. EXTR-03-03 stub -- get_converter()'s
    lock/counter wiring below is real; what it constructs is not, yet.
    """
    raise NotImplementedError("EXTR-03-03: DocumentConverter construction not yet built")


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


def stub_read(body: bytes, content_type: str) -> dict:
    """Fixed, contract-shaped response — see EXTR-03 story sec. 3.

    docling_version is "stub": no real DoclingDocument exists yet (EXTR-03-03), and a
    real-looking value here would misrepresent what produced this response.
    """
    return {
        "reader": "docling",
        "version": "v1",
        "docling_version": "stub",
        "pages": [
            {
                "number": 1,
                "width_pt": 612.0,
                "height_pt": 792.0,
                "tokens": [
                    {"text": "STUB", "x0": 0.1, "y0": 0.1, "x1": 0.2, "y1": 0.15},
                ],
                "tables": [],
            }
        ],
    }
