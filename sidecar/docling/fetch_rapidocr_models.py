"""Fetch RapidOCR's onnxruntime:en checkpoints: official host first, mirror as fallback.

Both are hash-verified against rapidocr==3.9.2's own registry (rapidocr/default_models.yaml,
keys multi_PP-OCRv6_det_small / multi_PP-OCRv6_rec_small / ch_ppocr_mobile_v2.0_cls_mobile) --
the hash is the contract, not the host. modelscope.cn hung past 60s with zero bytes in the
EXTR-03-02 build (and on this network), so it gets a short timeout and one retry before
falling back to a pinned HuggingFace commit that re-publishes the same bytes.
"""

import hashlib
from pathlib import Path

import requests

_MODELSCOPE_BASE = "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve"
_MODELSCOPE_TIMEOUT = (10, 10)  # (connect, read) -- fail fast, don't repeat the 60s hang
_MODELSCOPE_MAX_ATTEMPTS = 2  # one retry

_MIRROR_COMMIT = "04e88d0483f0bfcdd0f29429594244c10bfc2867"
_MIRROR_BASE = f"https://huggingface.co/DjB314/RapidOCR/resolve/{_MIRROR_COMMIT}"
_MIRROR_TIMEOUT = (10, 60)

# (repo-relative path, sha256) for backend=onnxruntime, lang=en -- det/rec from the
# PP-OCRv6 registry, cls always resolves to the PP-OCRv4 model (rapid_ocr_model.py).
# Both hosts serve this same relative path under their own base URL.
_FILES: list[tuple[str, str]] = [
    (
        "v3.9.2/onnx/PP-OCRv6/det/PP-OCRv6_det_small.onnx",
        "090f04abcd9d9a7498bc4ebf677e4cb9bdce1fe4197ddb7e529f1ef44e1ff94f",
    ),
    (
        "v3.9.2/onnx/PP-OCRv6/rec/PP-OCRv6_rec_small.onnx",
        "6f327246b50388f3c176ae304bd95767ea6dc0c9ae92153ef8cbe210b3c14884",
    ),
    (
        "v3.9.2/onnx/PP-OCRv4/cls/ch_ppocr_mobile_v2.0_cls_mobile.onnx",
        "e47acedf663230f8863ff1ab0e64dd2d82b838fceb5957146dab185a89d6215c",
    ),
]

# Matches RapidOcrModel._model_repo_folder -- Docling looks for the checkpoints here.
_TARGET_DIR = Path("/opt/docling-models/RapidOcr")


def _get(url: str, timeout: tuple[int, int]) -> bytes:
    response = requests.get(url, timeout=timeout)
    response.raise_for_status()
    return response.content


def _fetch(rel_path: str, *, try_modelscope: bool) -> tuple[bytes, str]:
    """Return (bytes, source label). try_modelscope=False skips straight to the mirror --
    set once ModelScope has already exhausted its retries earlier in this run."""
    if not try_modelscope:
        print(
            f"[fetch_rapidocr_models] modelscope already failed this run, "
            f"using mirror for {rel_path}"
        )
        return _get(f"{_MIRROR_BASE}/{rel_path}", _MIRROR_TIMEOUT), "huggingface-mirror"

    last_error: Exception | None = None
    for attempt in range(1, _MODELSCOPE_MAX_ATTEMPTS + 1):
        try:
            return _get(f"{_MODELSCOPE_BASE}/{rel_path}", _MODELSCOPE_TIMEOUT), "modelscope"
        except (requests.RequestException, OSError) as err:
            last_error = err
            print(
                f"[fetch_rapidocr_models] modelscope attempt {attempt}/"
                f"{_MODELSCOPE_MAX_ATTEMPTS} failed for {rel_path}: {err}"
            )
    print(
        f"[fetch_rapidocr_models] modelscope exhausted for {rel_path} ({last_error}); "
        "falling back to huggingface-mirror, and skipping modelscope for the rest of this run"
    )
    return _get(f"{_MIRROR_BASE}/{rel_path}", _MIRROR_TIMEOUT), "huggingface-mirror"


def main() -> None:
    _TARGET_DIR.mkdir(parents=True, exist_ok=True)
    modelscope_still_viable = True
    for rel_path, want_sha256 in _FILES:
        dest = _TARGET_DIR / Path(rel_path).name
        content, source = _fetch(rel_path, try_modelscope=modelscope_still_viable)
        if source != "modelscope":
            modelscope_still_viable = False
        dest.write_bytes(content)
        got_sha256 = hashlib.sha256(dest.read_bytes()).hexdigest()
        if got_sha256 != want_sha256:
            raise SystemExit(
                f"{dest}: sha256 {got_sha256} != expected {want_sha256} (source={source})"
            )
        print(f"[fetch_rapidocr_models] {dest.name}: sha256 OK, source={source}")


if __name__ == "__main__":
    main()
