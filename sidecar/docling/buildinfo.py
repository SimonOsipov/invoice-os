"""Build sha baked into the image at internal/platform/buildsha.txt, copied here as build.txt."""

from pathlib import Path

BUILD_FILE = Path(__file__).parent / "build.txt"


def read_build_sha(path: Path) -> str:
    """Stripped file contents, or "dev" if the path is absent or unreadable.

    An unreadable file must degrade the same as a missing one -- /healthz is a
    liveness probe and must never 500 (carried over from EXTR-03-01 QA).
    """
    try:
        return path.read_text().strip()
    except OSError:
        return "dev"
