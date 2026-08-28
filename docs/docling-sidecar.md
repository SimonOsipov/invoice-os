# Docling sidecar

`sidecar/docling/` is a CPU-only FastAPI service wrapping Docling's `DocumentConverter`.
RapidOCR (English, onnxruntime backend) handles scans; TableFormer (ACCURATE mode) handles
tables. See `EXTR-03-finalized.md` sec. 4 for the full pipeline configuration and why each
axis is set explicitly rather than left at its library default.

## Pins

- `docling[rapidocr]==2.123.1` — the `[rapidocr]` extra is mandatory; bare `docling` installs
  `rapidocr` without `onnxruntime`, so `RapidOcrOptions()` fails at ONNX session creation on
  the first scan rather than at install.
- `rapidocr==3.9.2`
- `onnxruntime==1.29.0`
- `torch==2.9.1+cpu` — CPU wheel index (`--extra-index-url https://download.pytorch.org/whl/cpu`).
  Torch is not avoidable (layout and TableFormer need it); this only drops the ~2 GB CUDA payload.

## Licences

Docling: MIT. The RapidOCR/PaddleOCR checkpoints baked into the image (PP-OCRv6 det/rec,
PP-OCRv4 cls): Apache-2.0. Neither Surya nor olmOCR appears anywhere in this service's
dependency tree.

## Running the test suite

The `test` stage image carries only `sidecar/docling/`, not the whole repo, so the
repo-scanning specs (`test_pins.py`, `test_fixtures.py`) need the repo mounted
read-only at a path named by `REPO_ROOT` (see `sidecar/docling/tests/conftest.py`). A
missing/unset `REPO_ROOT` fails those specs loudly, it does not skip them. One command,
run from the repo root, works locally and in CI (`$GITHUB_WORKSPACE` in place of
`$PWD`):

```
docker build --target test -f sidecar/docling/Dockerfile -t docling:test .
docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test
```

The `test` stage runs as the same non-root `appuser` (uid 1000) the production `run`
stage uses, not root — root bypasses `chmod`-based permission checks, which would
silently defeat `test_health.py`'s unreadable-build-file spec.

## Backend, memory, throughput

Not yet measured. EXTR-03-09 records peak RSS, p95 conversion latency, and the
docling-parse-vs-pypdfium2 backend decision (the published figures put docling-parse at
~6.2 GB peak RSS with OCR disabled, ~2.5 GB for pypdfium2 at a table-quality cost) once a
dense fixture and a measurement harness exist.
