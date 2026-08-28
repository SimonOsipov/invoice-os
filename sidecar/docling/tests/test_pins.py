"""T-02-5, T-02-6: requirements.txt is exact-pinned; Dockerfile carries neither
egress trap.
"""

from pathlib import Path

SIDECAR_DIR = Path(__file__).parent.parent
REQUIREMENTS = SIDECAR_DIR / "requirements.txt"
DOCKERFILE = SIDECAR_DIR / "Dockerfile"

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
        if not line or line.startswith("#") or line.startswith("-"):
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

    assert "# syntax=" not in text, (
        f"{DOCKERFILE}: a `# syntax=` line makes BuildKit fetch a frontend image from Docker "
        "Hub before the build, which strands the fleet on a builder egress outage "
        "(Dockerfile:11-14)"
    )
    assert "--mount=type=cache" not in text, (
        f"{DOCKERFILE}: BuildKit cache mounts are banned -- Railway requires each cache-mount "
        "id to embed the building service's own id (docs/add-a-service.md §1)"
    )
