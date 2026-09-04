# Docling sidecar

> **This service is deployed.** Railway service `docling`
> (`2fd6a6f2-8ba2-488d-a686-3a4b73f2046d`) exists, `dev-env.yml`'s `deploy-context` matrix
> ships it, and `expected_json` names it — so a missing or renamed `docling` fails the
> Watch-Paths assertion like any other service.
>
> `DOCLING_URL` is set on `submission` (the only caller, and only under
> `EXTRACTOR=docling`) and on `gateway`, which probes it for `/healthz/fleet`. The
> fleet-visibility decision recorded as D-10/D-16 is taken: probed, not routed — the
> gateway publishes no `/api/docling/*` route.

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

## Licences, and why not Surya or olmOCR

Docling: MIT. The RapidOCR/PaddleOCR checkpoints baked into the image (PP-OCRv6 det/rec,
PP-OCRv4 cls): Apache-2.0. Neither Surya nor olmOCR appears anywhere in this service's
dependency tree.

Both stay excluded, but **on different grounds than the ones this project inherited**. The
earlier wordings — "Surya is GPL-3.0" and "olmOCR is research-only" — are both wrong, and a
wrong reason is worse than none: it survives into the next decision unexamined.

- **Surya**: the *code* is Apache-2.0. The exclusion is the **model weights**, released
  under a modified AI Pubs Open Rail-M licence that requires a paid agreement above a
  $5M funding/revenue threshold.
- **olmOCR**: Apache-2.0, commercial use permitted. The exclusion is hardware — it is a 7B
  VLM needing an NVIDIA GPU with >=12 GB VRAM, and Railway has none.

## Why RapidOCR, not EasyOCR

RapidOCR runs the PaddleOCR checkpoints through onnxruntime: no torch at OCR time, CPU-only,
and every checkpoint is small enough to bake into the image (see the model-bake section
below), so the container needs no network at run time. EasyOCR pulls its own torch-backed
stack and downloads weights on first use, which is precisely the boot-time network
dependency the offline canary job exists to forbid.

## Running the test suite

The `test` stage image carries only `sidecar/docling/`, not the whole repo, so the
repo-scanning specs (`test_pins.py`, `test_fixtures.py`) need the repo mounted
read-only at a path named by `REPO_ROOT` (see `sidecar/docling/tests/conftest.py`). A
missing/unset `REPO_ROOT` fails those specs loudly, it does not skip them. One command,
run from the repo root, works locally and in CI (`$GITHUB_WORKSPACE` in place of
`$PWD`):

```sh
docker build --target test -f sidecar/docling/Dockerfile -t docling:test .
docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test
```

The `test` stage runs as the same non-root `appuser` (uid 1000) the production `run`
stage uses, not root — root bypasses `chmod`-based permission checks, which would
silently defeat `test_health.py`'s unreadable-build-file spec.

## Regenerating the Go golden

`internal/extraction/testdata/native_invoice.docling.json` is the service's real `/v1/read`
response for `native_invoice.pdf`, replayed through an `httptest.Server` because `go test`
cannot run a Python service. `go test -update` does not touch it — Go has no generator for it.
It is regenerated from a running container instead:

```sh
docker build -f sidecar/docling/Dockerfile -t docling:canary .
docker run -d --name docling-canary --network none docling:canary
scripts/ci/docling-canary.sh golden dev --update    # `dev` = the build.txt stamp in the image
docker rm -f docling-canary
```

`corpus_inline_labels.docling.json` is the second golden and takes explicit paths:

```sh
scripts/ci/docling-canary.sh golden dev \
  internal/extraction/testdata/corpus_inline_labels.pdf \
  internal/extraction/testdata/corpus_inline_labels.docling.json --update
```

Without `--update` the same command compares and prints a diff; that is what CI runs.

The `<sha>` argument is a freshness gate, not decoration: the script reads `/healthz` and
refuses to generate unless `build` matches. A `docling:canary` tag left over from an earlier
build serves that build's `/v1/read` and yields a golden that looks plausible and is wrong.

## The coordinate space

Every box on the wire is normalised to `[0,1]` with a **top-left** origin, which is exactly
what `extraction.Region` and the `extraction_field_results_bbox_normalised` CHECK already
require — so a token becomes a `Field` with no conversion.

Getting there means handling two origins in the same object, which is the one genuinely
surprising thing about Docling's geometry:

- **Element provenance** (`prov[].bbox`) arrives **BOTTOM-LEFT** — `readingorder_model` has
  already flipped it.
- **A table's own cells** (`TableData.table_cells[].bbox`) arrive **TOP-LEFT** — they are
  passed through unconverted.

Both appear inside one `TableItem`. `geometry.py` normalises each box by its **own**
`coord_origin`: `page_height - t` / `page_height - b` for bottom-left, pass-through for
top-left. It never guesses — an unrecognised origin raises. Boxes past the page edge clamp
and log; a zero-height page is skipped rather than dividing by zero.

A box that does not exist is **omitted**, not zeroed: an empty table cell carries no
`x0`/`y0`/`x1`/`y1` keys at all, because a zero box is a legal box. Every DOCX token is
boxless this way — `msword_backend` constructs no provenance — which is why the DOCX path
reports `width_pt`/`height_pt` of 0.

## The model bake

`DOCLING_ARTIFACTS_PATH` points at `/opt/docling-models`, populated at **build** time so the
container needs no network at run time — proven, not assumed, by the `docling-canary` CI job,
which boots the image with `--network none` and converts a fixture inside it.

RapidOCR's checkpoints are fetched by `fetch_rapidocr_models.py` rather than by Docling's own
prefetch, which hardcodes ModelScope; that host returned zero bytes for 60+ seconds during
development and there is no configuration knob to redirect it. The script tries the official
source first, falls back to a mirror, and verifies SHA-256 on both paths.

## Backend, memory, throughput

**Backend: `DoclingParseV4DocumentBackend`, named explicitly.** This is the single most
important line in `convert.py`. `PdfFormatOption`'s default is
`ThreadedDoclingParseDocumentBackend` — the v1 parser — and on a scanned page it hands the
OCR stage no bitmap region at all. The service then returns a document with **zero tokens**
for a scan RapidOCR reads perfectly when called directly, and `DoclingExtractor` reports it
`ReasonUnreadable`. `sidecar/docling/tests/test_backend.py` locks the pin and asserts a
scanned page reaches the wire with text.

Measured here, one fresh container per backend, same pipeline options:

| fixture | backend | text cells | median | peak RSS |
|---|---|---|---|---|
| `dense_invoice.pdf` (scan) | default v1 | **0** | 1.12 s | 1626 MiB |
| `dense_invoice.pdf` (scan) | **v4 (shipped)** | 55 | 4.61 s | 1832 MiB |
| `dense_invoice.pdf` (scan) | pypdfium2 | 55 | 4.72 s | 1966 MiB |
| `table_invoice.pdf` (native) | default v1 | 13 | 45.56 s | 2168 MiB |
| `table_invoice.pdf` (native) | **v4 (shipped)** | 13 | **1.48 s** | 1484 MiB |

The default's apparent speed on the scan is an artifact of doing no OCR work. On the native
table it is 31x slower than v4 for identical output. v4 ships over pypdfium2 because it is
the maintained docling-parse line and measured marginally cheaper on both axes.

**The published figures did not reproduce.** The numbers this project inherited — ~6.2 GB
peak RSS for docling-parse against ~2.5 GB for pypdfium2, with OCR disabled — do not match
what this image does. All three backends sit between 1.5 and 2.0 GiB here, and pypdfium2 is
the *highest* rather than the lowest. Treat the published pair as not applicable to this
configuration.

### p95

20 warm runs against the built image, `dense_invoice.pdf`, one page, 55 tokens:

| | seconds |
|---|---|
| cold start (first request, converter build + model load) | 6.19 |
| p50 | 4.76 |
| **p95** | **4.86** |
| max | 4.92 |

Peak RSS across that run: **2.21 GiB**. Memory, not CPU, is the sizing constraint.

**What this number is and is not.** It is a p95 on a *synthetic* fixture, measured in Docker
on an Apple-Silicon laptop — not on real client documents, and not on Railway hardware. The
decision log's open item stands: a corpus of real documents was requested and never supplied,
so nothing here is evidence about real invoices.

The comparison figure is worth the same caution. The only published per-page timings are
~1.7 s/page on a Xeon at 4 threads **with OCR off**, from the 2024 technical report, against
a Docling two major model generations old. The epic's ~16 s/page is not from that table and
has no traced source. Measured p95 is 4.86 s/page — comfortably inside the ~16 s the queue
and timeout decisions assumed, so no finding is owed against EXTR-01 on latency.

Reproduce it with:

```sh
docker run -d --name docling-bench -v "$PWD:/repo:ro" docling:canary
scripts/ci/docling-bench.sh internal/extraction/testdata/dense_invoice.pdf 20
docker rm -f docling-bench
```

The harness refuses to report a latency for a document that yielded no tokens — timing the
failure path is not a throughput measurement.
