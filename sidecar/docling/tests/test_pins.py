"""T-02-5, T-02-6: requirements.txt is exact-pinned; Dockerfile carries neither
egress trap. T-03-10, T-03-11: the RapidOCR/onnxruntime pins match docs/docling-sidecar.md,
and no surya/olmocr/CUDA torch ever sneaks in. Pure file scans -- no docling import, runs in
the bare local venv.
"""

import re

from conftest import repo_root

# Resolved through conftest.repo_root(), not Path(__file__)-arithmetic: inside
# docling:test this file lives at /app/tests/, which is not the mounted repo, so
# SIDECAR_DIR must come from REPO_ROOT (see conftest.py). Every test below reads at
# least one of these, so a missing repo fails the whole module loudly at collection.
REPO_ROOT = repo_root()
SIDECAR_DIR = REPO_ROOT / "sidecar" / "docling"
REQUIREMENTS = SIDECAR_DIR / "requirements.txt"
DOCKERFILE = SIDECAR_DIR / "Dockerfile"
DOCLING_SIDECAR_DOC = REPO_ROOT / "docs" / "docling-sidecar.md"

# BuildKit's parser-directive regex (moby/buildkit tokenizeDirective) tolerates
# whitespace around '#', the name and '=', and matches the name case-insensitively --
# confirmed against a real `docker buildx build` with #syntax=, # SYNTAX= and
# #  syntax  =  , all three of which trigger a frontend-image resolve. A literal
# "# syntax=" substring check misses all three.
_SYNTAX_DIRECTIVE_RE = re.compile(r"^\s*#\s*syntax\s*=", re.IGNORECASE | re.MULTILINE)

# fastapi, uvicorn (already pinned) + the four §4 pins table entries
# (docling[rapidocr], rapidocr, onnxruntime, torch) subtask 02's Dockerfile
# needs to run the model-prefetch step. Below this, the per-line check below
# would pass over too few lines to mean anything.
MIN_DIRECT_REQUIREMENTS = 6


def _requirement_lines() -> list[str]:
    """Non-blank, non-comment, non pip-option (-- flag) lines."""
    lines = []
    for raw in REQUIREMENTS.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith(("#", "-")):
            continue
        lines.append(line)
    return lines


def test_t02_5_every_direct_requirement_is_exact_pinned():
    lines = _requirement_lines()
    assert len(lines) >= MIN_DIRECT_REQUIREMENTS, (
        f"scanned {len(lines)} requirement line(s) in {REQUIREMENTS}, want at least "
        f"{MIN_DIRECT_REQUIREMENTS} -- a floor this low means a clean pass below proves nothing"
    )
    for line in lines:
        assert ">=" not in line, f"{line!r} carries a >= floor, not an exact pin"
        assert "==" in line, f"{line!r} carries no == pin (a bare requirement)"
        name = line.split("==", 1)[0]
        assert name, f"{line!r} has no package name before =="


def test_t02_6_dockerfile_has_no_syntax_directive_or_cache_mount():
    assert DOCKERFILE.exists(), f"{DOCKERFILE} does not exist yet"
    text = DOCKERFILE.read_text()

    # Control needle: every Dockerfile has a FROM line, so a scan that stopped
    # matching cannot read as a clean pass.
    assert "FROM" in text, f"{DOCKERFILE}: no FROM line found -- the scan reached nothing"

    assert not _SYNTAX_DIRECTIVE_RE.search(text), (
        f"{DOCKERFILE}: a `syntax` parser directive makes BuildKit fetch a frontend image from "
        "Docker Hub before the build, which strands the fleet on a builder egress outage "
        "(Dockerfile:11-14)"
    )
    assert "--mount=type=cache" not in text, (
        f"{DOCKERFILE}: BuildKit cache mounts are banned -- Railway requires each cache-mount "
        "id to embed the building service's own id (docs/add-a-service.md §1)"
    )


def test_t02_6_syntax_directive_check_catches_buildkit_tolerated_formats():
    """BuildKit's directive parser accepts whitespace/case variants a literal
    "# syntax=" substring check would miss -- confirmed against a real
    `docker buildx build` (see the regex comment above). Guards the regex itself,
    independent of the current Dockerfile's content.
    """
    for variant in (
        "#syntax=docker/dockerfile:1\nFROM scratch\n",
        "# SYNTAX=docker/dockerfile:1\nFROM scratch\n",
        "#   syntax   =   docker/dockerfile:1\nFROM scratch\n",
    ):
        assert _SYNTAX_DIRECTIVE_RE.search(variant), (
            f"missed a live BuildKit directive: {variant!r}"
        )


def _requirement_pins() -> dict[str, str]:
    pins = {}
    for line in _requirement_lines():
        name, _, version = line.partition("==")
        pins[name] = version
    return pins


def test_t03_10_rapidocr_stack_pins_match_the_docs():
    # subtask 09 writes docs/docling-sidecar.md; it does not exist yet, so this is a
    # genuine, current failure -- not a stand-in.
    assert DOCLING_SIDECAR_DOC.exists(), (
        f"{DOCLING_SIDECAR_DOC} does not exist yet -- T-03-10 needs it to name the versions "
        "requirements.txt's RapidOCR/onnxruntime pins are checked against"
    )
    doc_text = DOCLING_SIDECAR_DOC.read_text()
    pins = _requirement_pins()

    for name in ("docling[rapidocr]", "rapidocr", "onnxruntime"):
        assert name in pins, f"requirements.txt has no == pin for {name!r}"
        version = pins[name]
        assert f"{name}=={version}" in doc_text, (
            f"docs/docling-sidecar.md does not name {name}=={version} verbatim "
            "(requirements.txt's pinned version)"
        )


def test_t03_11_no_surya_no_olmocr_no_cuda_torch_wheel():
    text = REQUIREMENTS.read_text()
    lines = _requirement_lines()

    # Control needle: torch must be pinned here for the model stack to work at all, so a
    # scan that found no torch line read nothing meaningful.
    torch_lines = [line for line in lines if line.split("==", 1)[0].lower() == "torch"]
    assert torch_lines, f"no torch pin found in {REQUIREMENTS} -- the scan below proves nothing"
    torch_line = torch_lines[0].lower()
    assert "+cpu" in torch_line, f"{torch_lines[0]!r} is not the CPU wheel"
    assert "+cu" not in torch_line, f"{torch_lines[0]!r} looks like a CUDA wheel tag"

    assert "surya" not in text.lower(), f"{REQUIREMENTS} names surya"
    assert "olmocr" not in text.lower(), f"{REQUIREMENTS} names olmocr"
