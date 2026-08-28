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
RUN pip install --no-cache-dir -r requirements.txt

# rapidocr pulls opencv_python (the GUI build, not -headless); its `import cv2` needs
# these X11/GL shared libs even though nothing here renders anything.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgl1 libglib2.0-0 libsm6 libxext6 libxrender1 \
    && rm -rf /var/lib/apt/lists/*

# Layout + TableFormer (torch) + RapidOCR English (onnxruntime backend) only --
# skips code/formula (~640 MB) and the picture classifier (~16 MB), neither of
# which an invoice pipeline touches. Baked here, not fetched at boot: with
# DOCLING_ARTIFACTS_PATH set below, Docling refuses to fetch anything at runtime,
# so a mismatched selector here fails loudly on the first scan instead of quietly
# re-downloading.
RUN python3 -c "from pathlib import Path; from docling.utils.model_downloader import download_models; download_models(output_dir=Path('/opt/docling-models'), with_code_formula=False, with_picture_classifier=False, rapidocr_models=['onnxruntime:en'])"

# ---- run: venv + models + app, non-root ----
FROM mirror.gcr.io/library/python:3.12-slim AS run
RUN useradd --no-create-home --uid 1000 --shell /usr/sbin/nologin appuser
COPY --from=deps /opt/venv /opt/venv
COPY --from=deps /opt/docling-models /opt/docling-models
ENV PATH="/opt/venv/bin:${PATH}" \
    DOCLING_ARTIFACTS_PATH=/opt/docling-models
WORKDIR /app
COPY sidecar/docling/app.py sidecar/docling/buildinfo.py sidecar/docling/convert.py ./
# Overwrites the sidecar/docling/build.txt placeholder ("dev") with the sha
# scripts/ci/stamp-build-sha.sh wrote -- same mechanism the four SPA Dockerfiles
# use for /build.txt.
COPY internal/platform/buildsha.txt ./build.txt
USER appuser
EXPOSE 8080
CMD ["sh", "-c", "exec uvicorn app:app --host 0.0.0.0 --port ${PORT:-8080}"]
