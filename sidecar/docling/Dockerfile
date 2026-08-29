# Multi-stage; build context is the repo root (needs internal/platform/buildsha.txt).
# No BuildKit syntax-pragma line: root Dockerfile:11-14 -- it would re-arm the Docker
# Hub egress trap on the largest image in the fleet. No BuildKit cache mounts either
# (docs/add-a-service.md §1).
#
# Python 3.12: onnxruntime==1.29.0 floors at >=3.11, docling at >=3.10. Pulled
# through mirror.gcr.io for the same egress reason as the root Dockerfile.

# ---- deps: pinned installs + model prefetch (EXTR-03 story sec. 5) ----
FROM mirror.gcr.io/library/python:3.12-slim AS deps
RUN python -m venv /opt/venv
ENV PATH="/opt/venv/bin:${PATH}"
WORKDIR /build
COPY sidecar/docling/requirements.txt .
# rapidocr==3.9.2 hard-requires opencv_python (the GUI build) with no marker to swap
# it for -headless, so `pip install -r requirements.txt` alone always pulls it in.
# -headless serves the identical cv2 API rapidocr/docling_ibm_models call here
# (imgproc/imgcodecs only, no highgui) with no X11/GL libs. The uninstall must sit
# strictly between the two installs: resolving both in one command lets pip's
# internal install order silently decide whose cv2/ files land on disk last, and the
# losing order reproduces this story's ImportError (confirmed empirically).
RUN pip install --no-cache-dir -r requirements.txt \
    && pip uninstall -y opencv-python \
    && pip install --no-cache-dir --no-deps opencv-python-headless==5.0.0.93

# Layout + TableFormer (torch) only here -- skips code/formula (~640 MB) and the
# picture classifier (~16 MB), neither of which an invoice pipeline touches. Baked
# here, not fetched at boot: with DOCLING_ARTIFACTS_PATH set below, Docling refuses
# to fetch anything at runtime, so a mismatched selector here fails loudly on the
# first scan instead of quietly re-downloading.
RUN python3 -c "from pathlib import Path; from docling.utils.model_downloader import download_models; download_models(output_dir=Path('/opt/docling-models'), with_code_formula=False, with_picture_classifier=False, with_rapidocr=False)"

# RapidOCR English (onnxruntime backend) fetched separately -- see the script's own
# docstring: official host first (short timeout, one retry), pinned HF mirror on
# failure, hash-verified either way.
COPY sidecar/docling/fetch_rapidocr_models.py .
RUN python3 fetch_rapidocr_models.py

# ---- test: deps + dev deps (pytest/httpx/ruff) + app + tests, the real pinned stack ----
# `docker build --target test -f sidecar/docling/Dockerfile -t docling:test .` then
# `docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test` -- the one command
# CI and a local dev both use to run the Test-Spec suite (T-03-1..9,13,14 need docling
# importable; nothing here skips them). The mount is read by test_pins.py/test_fixtures.py
# (see tests/conftest.py) for the repo-scanning specs; the run stage's own build context
# needs nothing mounted, only this stage.
FROM deps AS test
COPY sidecar/docling/requirements-dev.txt .
# requirements-dev.txt re-includes `-r requirements.txt` (so a local `pip install -r
# requirements-dev.txt` is self-sufficient) -- that re-resolve sees rapidocr's declared
# "opencv-python" as unsatisfied (this stage only has -headless, a different distribution
# name) and reinstalls the GUI build, undoing deps' swap above. Redo the swap after.
# --force-reinstall is required here: -headless' dist-info already exists from the deps
# stage (unaffected by the uninstall above, which only removes opencv-python's files), so
# a plain `pip install` sees it as "already satisfied" and skips writing cv2/ back --
# leaving the package uninstalled in all but name. Confirmed empirically (T-03-cv2 guard).
RUN pip install --no-cache-dir -r requirements-dev.txt \
    && pip uninstall -y opencv-python \
    && pip install --no-cache-dir --force-reinstall --no-deps opencv-python-headless==5.0.0.93
ENV DOCLING_ARTIFACTS_PATH=/opt/docling-models
WORKDIR /app
COPY sidecar/docling/app.py sidecar/docling/buildinfo.py sidecar/docling/convert.py sidecar/docling/geometry.py sidecar/docling/pyproject.toml ./
COPY sidecar/docling/tests ./tests
# Same non-root user the run stage uses below, not just for parity: running as root
# makes chmod 000 a no-op (root bypasses the DAC check), which was silently defeating
# test_healthz_reports_dev_when_build_file_is_unreadable. appuser needs write access to
# /app for pytest's cache dir; /opt/venv and /opt/docling-models stay root-owned but
# world-readable from the deps stage's RUN steps, which is all this stage reads from them.
RUN useradd --no-create-home --uid 1000 --shell /usr/sbin/nologin appuser \
    && chown -R appuser:appuser /app
USER appuser
CMD ["pytest", "-v"]

# ---- run: venv + models + app, non-root ----
FROM mirror.gcr.io/library/python:3.12-slim AS run
RUN useradd --no-create-home --uid 1000 --shell /usr/sbin/nologin appuser
COPY --from=deps /opt/venv /opt/venv
COPY --from=deps /opt/docling-models /opt/docling-models
ENV PATH="/opt/venv/bin:${PATH}" \
    DOCLING_ARTIFACTS_PATH=/opt/docling-models
WORKDIR /app
COPY sidecar/docling/app.py sidecar/docling/buildinfo.py sidecar/docling/convert.py sidecar/docling/geometry.py ./
# Overwrites the sidecar/docling/build.txt placeholder ("dev") with the sha
# scripts/ci/stamp-build-sha.sh wrote -- same mechanism the four SPA Dockerfiles
# use for /build.txt.
COPY internal/platform/buildsha.txt ./build.txt
USER appuser
EXPOSE 8080
CMD ["sh", "-c", "exec uvicorn app:app --host 0.0.0.0 --port ${PORT:-8080}"]
